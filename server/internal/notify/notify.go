// Package notify 订阅内置 Agent 的运行事件,生成最小化通知,
// 经 Hub 发给已登记推送的手机。多副本部署下每个副本只处理自己进程内的事件。
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agent/translate"
	"xlyra/server/internal/cloudclient"
)

const (
	defaultQueueSize = 256
	defaultDoneMin   = 20 * time.Second
	workerCount      = 2
	dedupeWindow     = 10 * time.Minute
	retryStart       = 2 * time.Second
	retryCap         = 2 * time.Minute
	pendingTTL       = 10 * time.Minute
	terminalTTL      = 2 * time.Minute
	titleMaxRunes    = 60
	defaultTitle     = "新对话"
	dropWarnInterval = 30 * time.Second
	kindApproval     = "approval"
	kindQuestion     = "question"
	kindDone         = "done"
	kindError        = "error"
	priorityAction   = 10
	priorityInfo     = 5
	prefApproval     = "approval"
	prefQuestion     = "question"
	prefDone         = "done"
	prefError        = "error"
)

// SessionMeta 是发通知需要的会话元数据。
type SessionMeta struct {
	Title          string
	PermissionMode string
	Escalations    []session.Escalation
	Questions      []session.Question
}

// Sessions 读取会话标题、权限模式与未决项。
type Sessions interface {
	Get(id string) (SessionMeta, error)
	// PendingTotal 是所有会话的待审批数与待回答数之和。
	PendingTotal() int
}

// Device 是一台可接收推送的手机。Prefs 缺省视为全开。
type Device struct {
	ID    string
	Prefs map[string]bool
}

// Devices 列出可推送设备:活动、ios、有 token、push_state 不是 invalid。
type Devices interface {
	ListPushDevices(ctx context.Context) ([]Device, error)
}

// Sender 经 Hub 发送一条通知。*cloudclient.Client 满足该接口。
type Sender interface {
	SendPush(ctx context.Context, req cloudclient.PushRequest) (cloudclient.PushResult, error)
}

// Deps 是 Notifier 的依赖,都用小接口便于测试。
type Deps struct {
	Sessions Sessions
	Devices  Devices
	Logger   *slog.Logger
}

// Options 配置时长门槛、队列与时钟。
type Options struct {
	DoneMinDuration time.Duration
	QueueSize       int
	Now             func() time.Time
}

// Notifier 把运行事件变成通知。Sender 晚绑定:未设置时通知静默丢弃。
type Notifier struct {
	sessions Sessions
	devices  Devices
	logger   *slog.Logger
	doneMin  time.Duration
	now      func() time.Time

	queue chan observation

	mu       sync.Mutex
	sender   Sender
	dedupe   map[string]time.Time
	pending  []delivery
	wake     chan struct{}
	dropped  int
	lastWarn time.Time

	startOnce sync.Once
	done      chan struct{}
}

type observation struct {
	sessionID string
	runID     string
	startedAt time.Time
	ev        translate.Event
}

type notice struct {
	kind       string
	title      string
	body       string
	sessionID  string
	refID      string
	highRisk   bool
	badge      *int
	collapseID string
	priority   int
	createdAt  time.Time
	deadline   time.Time
}

type delivery struct {
	deviceID string
	notice   notice
	attempts int
	nextAt   time.Time
	backoff  time.Duration
}

// New 建立通知分发器。工作协程由 Start 启动。
func New(deps Deps, opts Options) *Notifier {
	if opts.DoneMinDuration < 0 {
		opts.DoneMinDuration = defaultDoneMin
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = defaultQueueSize
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Notifier{
		sessions: deps.Sessions,
		devices:  deps.Devices,
		logger:   logger,
		doneMin:  opts.DoneMinDuration,
		now:      opts.Now,
		queue:    make(chan observation, opts.QueueSize),
		dedupe:   map[string]time.Time{},
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
}

// SetSender 绑定发送端。可在 Start 之后调用;未设置时通知被静默丢弃。
func (n *Notifier) SetSender(sender Sender) {
	n.mu.Lock()
	n.sender = sender
	n.mu.Unlock()
}

// Observe 给 runlog 观察者用。它在持有 Run.mu 时被调用,只做白名单过滤和非阻塞入队,
// 队列满则丢弃并计数,不得阻塞。
func (n *Notifier) Observe(sessionID, runID string, startedAt time.Time, ev translate.Event) {
	if n == nil || !interesting(ev.Type) {
		return
	}
	obs := observation{sessionID: sessionID, runID: runID, startedAt: startedAt, ev: ev}
	select {
	case n.queue <- obs:
	default:
		n.noteDrop()
	}
}

func interesting(eventType string) bool {
	switch eventType {
	case "escalation_request", "question_request", "agent_error", "agent_done":
		return true
	default:
		return false
	}
}

// Start 启动两个工作协程,ctx 结束时退出并关闭 done。重复调用无效。
func (n *Notifier) Start(ctx context.Context) {
	n.startOnce.Do(func() {
		var wg sync.WaitGroup
		wg.Add(workerCount)
		for i := 0; i < workerCount; i++ {
			go func() {
				defer wg.Done()
				n.loop(ctx)
			}()
		}
		go func() {
			wg.Wait()
			close(n.done)
		}()
	})
}

// Done 在工作协程全部退出后关闭。
func (n *Notifier) Done() <-chan struct{} { return n.done }

func (n *Notifier) loop(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	stopTimer(timer)
	for {
		delay := n.nextDelay()
		resetTimer(timer, delay)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return
		case obs := <-n.queue:
			n.safely("dispatch", func() { n.dispatch(ctx, obs) })
		case <-n.wake:
		case <-timer.C:
			n.safely("flush", func() { n.flushDue(ctx) })
		}
	}
}

// safely 保证通知这条旁路无论出什么错都不会带崩实例：panic 只记一行，不含任何通知内容。
func (n *Notifier) safely(stage string, fn func()) {
	defer func() {
		if rec := recover(); rec != nil {
			n.logger.Error("通知分发 panic，已忽略", "stage", stage)
		}
	}()
	fn()
}

func (n *Notifier) nextDelay() time.Duration {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.pending) == 0 {
		return time.Hour
	}
	now := n.now()
	soonest := n.pending[0].nextAt
	for _, item := range n.pending[1:] {
		if item.nextAt.Before(soonest) {
			soonest = item.nextAt
		}
	}
	if !soonest.After(now) {
		return time.Millisecond
	}
	return soonest.Sub(now)
}

func (n *Notifier) dispatch(ctx context.Context, obs observation) {
	item, ok := n.build(obs)
	if !ok {
		return
	}
	if n.dropResolved(item) || n.duplicate(item) {
		return
	}
	devices, err := n.devices.ListPushDevices(ctx)
	if err != nil {
		n.logger.Warn("推送设备列表读取失败", "kind", item.kind, "error", err.Error())
		return
	}
	targets := filterDevices(devices, item.kind)
	if len(targets) == 0 {
		return
	}
	n.remember(item)
	for _, device := range targets {
		n.send(ctx, delivery{deviceID: device.ID, notice: item})
	}
}

func (n *Notifier) flushDue(ctx context.Context) {
	now := n.now()
	n.mu.Lock()
	rest := n.pending[:0]
	due := make([]delivery, 0)
	for _, item := range n.pending {
		if item.nextAt.After(now) {
			rest = append(rest, item)
			continue
		}
		due = append(due, item)
	}
	n.pending = rest
	n.mu.Unlock()
	for _, item := range due {
		n.retry(ctx, item)
	}
}

func (n *Notifier) retry(ctx context.Context, item delivery) {
	if n.expired(item.notice) || n.dropResolved(item.notice) {
		n.logger.Info("推送已丢弃", "kind", item.notice.kind, "device", item.deviceID, "result", "expired", "retries", item.attempts)
		return
	}
	n.send(ctx, item)
}

// send 发出一条。需要稍后重试时放回共享队列,各设备互不阻塞。
func (n *Notifier) send(ctx context.Context, item delivery) {
	sender := n.currentSender()
	if sender == nil {
		return
	}
	result, err := sender.SendPush(ctx, cloudclient.PushRequest{
		DeviceID:   item.deviceID,
		Kind:       item.notice.kind,
		Title:      item.notice.title,
		Body:       item.notice.body,
		SessionID:  item.notice.sessionID,
		RefID:      item.notice.refID,
		HighRisk:   item.notice.highRisk,
		Badge:      item.notice.badge,
		CollapseID: item.notice.collapseID,
		Priority:   item.notice.priority,
	})
	n.logger.Info("推送结果", "kind", item.notice.kind, "device", item.deviceID, "result", resultName(result), "retries", item.attempts)
	if result != cloudclient.PushRetryLater {
		return
	}
	wait := n.backoff(item)
	var later *cloudclient.RetryAfterError
	if errors.As(err, &later) && later != nil && later.Seconds > 0 {
		hint := time.Duration(later.Seconds) * time.Second
		if hint > wait {
			wait = hint
		}
	}
	item.attempts++
	item.backoff = wait
	item.nextAt = n.now().Add(wait)
	if !item.nextAt.Before(item.notice.deadline) {
		n.logger.Info("推送已丢弃", "kind", item.notice.kind, "device", item.deviceID, "result", "expired", "retries", item.attempts)
		return
	}
	n.enqueueRetry(item)
}

func (n *Notifier) backoff(item delivery) time.Duration {
	if item.backoff <= 0 {
		return retryStart
	}
	next := item.backoff * 2
	if next > retryCap || next <= 0 {
		return retryCap
	}
	return next
}

// kick 唤醒工作协程重算重试时间。测试推进假时钟后调用。
func (n *Notifier) kick() {
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

func (n *Notifier) enqueueRetry(item delivery) {
	n.mu.Lock()
	n.pending = append(n.pending, item)
	n.mu.Unlock()
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

func (n *Notifier) currentSender() Sender {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.sender
}

func (n *Notifier) build(obs observation) (notice, bool) {
	meta, err := n.sessions.Get(obs.sessionID)
	if err != nil {
		return notice{}, false
	}
	item := notice{
		title:     clipTitle(meta.Title),
		sessionID: obs.sessionID,
		createdAt: n.now(),
	}
	switch obs.ev.Type {
	case "escalation_request":
		if meta.PermissionMode == "auto" {
			return notice{}, false
		}
		refID, highRisk := escalationFields(obs.ev)
		if refID == "" {
			return notice{}, false
		}
		item.kind = kindApproval
		item.refID = refID
		item.highRisk = highRisk
		item.body = "需要你审批"
		if highRisk {
			item.body = "有高风险操作等你确认"
		}
		item.collapseID = refID
		item.priority = priorityAction
		item.deadline = item.createdAt.Add(pendingTTL)
		item.badge = n.badge(!hasEscalation(meta.Escalations, refID))
	case "question_request":
		refID := questionID(obs.ev)
		if refID == "" {
			return notice{}, false
		}
		item.kind = kindQuestion
		item.refID = refID
		item.body = "有问题等你回答"
		item.collapseID = refID
		item.priority = priorityAction
		item.deadline = item.createdAt.Add(pendingTTL)
		item.badge = n.badge(!hasQuestion(meta.Questions, refID))
	case "agent_error":
		item.kind = kindError
		item.refID = obs.runID
		item.body = "运行出错"
		item.collapseID = obs.runID
		item.priority = priorityInfo
		item.deadline = item.createdAt.Add(terminalTTL)
	case "agent_done":
		if n.doneMin > 0 && n.now().Sub(obs.startedAt) < n.doneMin {
			return notice{}, false
		}
		item.kind = kindDone
		item.refID = obs.runID
		item.body = "对话已完成"
		item.collapseID = obs.runID
		item.priority = priorityInfo
		item.deadline = item.createdAt.Add(terminalTTL)
	default:
		return notice{}, false
	}
	return item, true
}

// badge 是待处理总数。审批请求先发布、后登记，通知构造时当前这条可能还没算进总数，
// 这时（unrecorded）补上 1，保证角标含当前这条。
func (n *Notifier) badge(unrecorded bool) *int {
	total := n.sessions.PendingTotal()
	if unrecorded {
		total++
	}
	return &total
}

func hasEscalation(items []session.Escalation, id string) bool {
	for _, item := range items {
		if item.EscalationID == id {
			return true
		}
	}
	return false
}

func hasQuestion(items []session.Question, id string) bool {
	for _, item := range items {
		if item.QuestionID == id {
			return true
		}
	}
	return false
}

func (n *Notifier) dropResolved(item notice) bool {
	if item.kind != kindApproval && item.kind != kindQuestion {
		return false
	}
	meta, err := n.sessions.Get(item.sessionID)
	if err != nil {
		return true
	}
	if item.kind == kindApproval {
		return !escalationPending(meta.Escalations, item.refID)
	}
	return !questionPending(meta.Questions, item.refID)
}

func (n *Notifier) expired(item notice) bool {
	return !n.now().Before(item.deadline)
}

func (n *Notifier) duplicate(item notice) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.pruneDedupeLocked(n.now())
	_, seen := n.dedupe[item.kind+"\x00"+item.refID]
	return seen
}

func (n *Notifier) remember(item notice) {
	n.mu.Lock()
	n.dedupe[item.kind+"\x00"+item.refID] = n.now().Add(dedupeWindow)
	n.mu.Unlock()
}

func (n *Notifier) pruneDedupeLocked(now time.Time) {
	for key, until := range n.dedupe {
		if !until.After(now) {
			delete(n.dedupe, key)
		}
	}
}

func (n *Notifier) noteDrop() {
	n.mu.Lock()
	n.dropped++
	dropped := n.dropped
	now := n.now()
	warn := n.lastWarn.IsZero() || now.Sub(n.lastWarn) >= dropWarnInterval
	if warn {
		n.lastWarn = now
	}
	n.mu.Unlock()
	if warn {
		n.logger.Warn("通知队列已满,丢弃事件", "dropped", dropped)
	}
}

// escalationPending 在没有记录,或记录仍未决时为 true。
// 只有 meta 里已经有结果才视为处理完。
func escalationPending(items []session.Escalation, id string) bool {
	for _, item := range items {
		if item.EscalationID == id {
			return item.Granted == nil
		}
	}
	return true
}

func questionPending(items []session.Question, id string) bool {
	for _, item := range items {
		if item.QuestionID == id {
			return item.AnsweredAt == nil
		}
	}
	return true
}

func escalationFields(ev translate.Event) (string, bool) {
	escalation, _ := ev.Data["escalation"].(map[string]any)
	if escalation == nil {
		return "", false
	}
	id, _ := escalation["escalation_id"].(string)
	risk, _ := escalation["risk"].(string)
	return id, risk == "high"
}

func questionID(ev translate.Event) string {
	question, _ := ev.Data["question"].(map[string]any)
	if question == nil {
		return ""
	}
	id, _ := question["question_id"].(string)
	return id
}

func clipTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return defaultTitle
	}
	if utf8.RuneCountInString(title) <= titleMaxRunes {
		return title
	}
	runes := []rune(title)
	return string(runes[:titleMaxRunes])
}

func filterDevices(devices []Device, kind string) []Device {
	out := make([]Device, 0, len(devices))
	for _, device := range devices {
		if deviceAllows(device.Prefs, kind) {
			out = append(out, device)
		}
	}
	return out
}

func deviceAllows(prefs map[string]bool, kind string) bool {
	if prefs == nil {
		return true
	}
	allowed, ok := prefs[kind]
	if !ok {
		return true
	}
	return allowed
}

func resultName(result cloudclient.PushResult) string {
	switch result {
	case cloudclient.PushOK:
		return "ok"
	case cloudclient.PushUnregistered:
		return "unregistered"
	case cloudclient.PushRetryLater:
		return "retry"
	case cloudclient.PushRejected:
		return "rejected"
	case cloudclient.PushNotReady:
		return "not_ready"
	default:
		return "unknown"
	}
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func resetTimer(timer *time.Timer, delay time.Duration) {
	stopTimer(timer)
	timer.Reset(delay)
}

// ParsePrefs 解析设备的 push_prefs。空或非法视为全开(返回 nil)。
func ParsePrefs(raw []byte) map[string]bool {
	if len(raw) == 0 || string(raw) == "{}" || string(raw) == "null" {
		return nil
	}
	var prefs map[string]bool
	if err := json.Unmarshal(raw, &prefs); err != nil {
		return nil
	}
	if len(prefs) == 0 {
		return nil
	}
	return prefs
}
