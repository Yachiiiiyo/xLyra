package notify

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agent/translate"
	"xlyra/server/internal/cloudclient"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(at time.Time) *fakeClock { return &fakeClock{now: at} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *fakeClock) Set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = at
}

type fakeSessions struct {
	mu    sync.Mutex
	metas map[string]SessionMeta
	total int
	gets  int
}

func (f *fakeSessions) Get(id string) (SessionMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	meta, ok := f.metas[id]
	if !ok {
		return SessionMeta{}, errors.New("missing")
	}
	return meta, nil
}

func (f *fakeSessions) PendingTotal() int { return f.total }

func (f *fakeSessions) set(id string, meta SessionMeta) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.metas == nil {
		f.metas = map[string]SessionMeta{}
	}
	f.metas[id] = meta
}

type fakeDevices struct {
	mu      sync.Mutex
	devices []Device
	err     error
	calls   int
	// panicOnce 为真时下一次列出设备会 panic，用来验证通知旁路不会带崩进程。
	panicOnce bool
}

func (f *fakeDevices) ListPushDevices(context.Context) ([]Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.panicOnce {
		f.panicOnce = false
		panic("device store exploded")
	}
	if f.err != nil {
		return nil, f.err
	}
	out := make([]Device, len(f.devices))
	copy(out, f.devices)
	return out, nil
}

type sentPush struct {
	req     cloudclient.PushRequest
	at      time.Time
	attempt int
}

type fakeSender struct {
	mu       sync.Mutex
	results  []cloudclient.PushResult
	errs     []error
	sent     []sentPush
	byDevice map[string][]cloudclient.PushResult
	errBy    map[string][]error
	clock    *fakeClock
	block    chan struct{}
}

func (f *fakeSender) SendPush(ctx context.Context, req cloudclient.PushRequest) (cloudclient.PushResult, error) {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return cloudclient.PushNotReady, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	at := time.Time{}
	if f.clock != nil {
		at = f.clock.Now()
	}
	f.sent = append(f.sent, sentPush{req: req, at: at})
	if queue := f.byDevice[req.DeviceID]; len(queue) > 0 {
		result := queue[0]
		f.byDevice[req.DeviceID] = queue[1:]
		var err error
		if errs := f.errBy[req.DeviceID]; len(errs) > 0 {
			err = errs[0]
			f.errBy[req.DeviceID] = errs[1:]
		}
		return result, err
	}
	var result cloudclient.PushResult
	if len(f.results) > 0 {
		result = f.results[0]
		f.results = f.results[1:]
	}
	var err error
	if len(f.errs) > 0 {
		err = f.errs[0]
		f.errs = f.errs[1:]
	}
	return result, err
}

func (f *fakeSender) snapshot() []sentPush {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]sentPush, len(f.sent))
	copy(out, f.sent)
	return out
}

type harness struct {
	n        *Notifier
	clock    *fakeClock
	sessions *fakeSessions
	devices  *fakeDevices
	sender   *fakeSender
	logs     *bytes.Buffer
	cancel   context.CancelFunc
}

func newHarness(t *testing.T, doneMin time.Duration) *harness {
	t.Helper()
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	sessions := &fakeSessions{metas: map[string]SessionMeta{
		"s1": {Title: "会话一", PermissionMode: "ask"},
	}, total: 3}
	devices := &fakeDevices{devices: []Device{{ID: "dev-1"}}}
	sender := &fakeSender{clock: clock}
	buf := &bytes.Buffer{}
	n := New(Deps{
		Sessions: sessions,
		Devices:  devices,
		Logger:   slog.New(slog.NewTextHandler(buf, nil)),
	}, Options{DoneMinDuration: doneMin, Now: clock.Now, QueueSize: 8})
	n.SetSender(sender)
	ctx, cancel := context.WithCancel(context.Background())
	n.Start(ctx)
	t.Cleanup(func() {
		cancel()
		select {
		case <-n.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("notifier 未退出")
		}
	})
	return &harness{n: n, clock: clock, sessions: sessions, devices: devices, sender: sender, logs: buf, cancel: cancel}
}

func (h *harness) waitSent(t *testing.T, want int) []sentPush {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got := h.sender.snapshot()
		if len(got) >= want {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待 %d 条发送,实际 %d", want, len(h.sender.snapshot()))
	return nil
}

func (h *harness) waitQuiet(t *testing.T) {
	t.Helper()
	time.Sleep(40 * time.Millisecond)
	if got := h.sender.snapshot(); len(got) != 0 {
		t.Fatalf("不应发送,实际 %#v", got)
	}
}

func escalationEvent(id, risk string) translate.Event {
	escalation := map[string]any{"escalation_id": id}
	if risk != "" {
		escalation["risk"] = risk
		escalation["requested_command"] = []string{"rm", "-rf", "/"}
	}
	return translate.Event{Type: "escalation_request", Data: map[string]any{"escalation": escalation}}
}

func questionEvent(id string) translate.Event {
	return translate.Event{Type: "question_request", Data: map[string]any{"question": map[string]any{"question_id": id, "question": "密钥是多少"}}}
}

func (h *harness) publish(started time.Time, ev translate.Event) {
	h.n.Observe("s1", "run-1", started, ev)
}

func TestMapping(t *testing.T) {
	h := newHarness(t, 20*time.Second)
	start := h.clock.Now().Add(-time.Minute)
	h.publish(start, escalationEvent("esc-1", ""))
	h.publish(start, escalationEvent("esc-high", "high"))
	h.publish(start, questionEvent("q-1"))
	h.publish(start, translate.Event{Type: "agent_error", Data: map[string]any{"message": "boom /secret"}})
	h.publish(start, translate.Event{Type: "agent_done"})

	sent := h.waitSent(t, 5)
	want := []struct {
		kind, body, collapse string
		priority             int
		high                 bool
		badge                bool
	}{
		{kindApproval, "需要你审批", "esc-1", 10, false, true},
		{kindApproval, "有高风险操作等你确认", "esc-high", 10, true, true},
		{kindQuestion, "有问题等你回答", "q-1", 10, false, true},
		{kindError, "运行出错", "run-1", 5, false, false},
		{kindDone, "对话已完成", "run-1", 5, false, false},
	}
	// 两个 worker 并发消费队列，发送顺序不保证，按 kind+RefID 对号入座。
	byKey := map[string]sentPush{}
	for _, push := range sent {
		byKey[push.req.Kind+"/"+push.req.RefID] = push
	}
	for i, item := range want {
		push, ok := byKey[item.kind+"/"+item.collapse]
		if !ok {
			t.Fatalf("#%d 缺少 %s/%s，实际 %+v", i, item.kind, item.collapse, sent)
		}
		req := push.req
		if req.Kind != item.kind || req.Body != item.body || req.Title != "会话一" || req.CollapseID != item.collapse || req.Priority != item.priority || req.HighRisk != item.high {
			t.Fatalf("#%d = %+v, want %+v", i, req, item)
		}
		if req.SessionID != "s1" || req.RefID != item.collapse || req.DeviceID != "dev-1" {
			t.Fatalf("#%d 标识错误: %+v", i, req)
		}
		if item.badge {
			// 夹具里待处理总数是 3，且 meta 里还没登记这些请求，所以角标补上当前这条 = 4。
			if req.Badge == nil || *req.Badge != 4 {
				t.Fatalf("#%d badge = %v, want 4", i, req.Badge)
			}
		} else if req.Badge != nil {
			t.Fatalf("#%d 不应带 badge", i)
		}
		if strings.Contains(req.Body, "rm") || strings.Contains(req.Body, "密钥") || strings.Contains(req.Title, "secret") {
			t.Fatalf("#%d 正文泄漏了事件细节: %+v", i, req)
		}
	}
}

func TestAutoModeSkipsApproval(t *testing.T) {
	h := newHarness(t, 20*time.Second)
	h.sessions.set("s1", SessionMeta{Title: "会话一", PermissionMode: "auto"})
	h.publish(h.clock.Now(), escalationEvent("esc-1", "high"))
	h.publish(h.clock.Now(), questionEvent("q-1"))
	sent := h.waitSent(t, 1)
	if sent[0].req.Kind != kindQuestion {
		t.Fatalf("auto 模式仍发出了 %+v", sent[0].req)
	}
}

func TestIgnoredEvents(t *testing.T) {
	h := newHarness(t, 0)
	h.publish(h.clock.Now(), translate.Event{Type: "agent_cancelled", Data: map[string]any{"aborted": true}})
	h.publish(h.clock.Now(), translate.Event{Type: "tool_call"})
	h.waitQuiet(t)
}

func TestTitleEmptyAndTruncated(t *testing.T) {
	h := newHarness(t, 0)
	h.sessions.set("s1", SessionMeta{Title: "  \t "})
	h.publish(h.clock.Now().Add(-time.Minute), translate.Event{Type: "agent_done"})
	first := h.waitSent(t, 1)
	if first[0].req.Title != "新对话" {
		t.Fatalf("空标题 = %q", first[0].req.Title)
	}
	long := strings.Repeat("标题", 40)
	h.sessions.set("s1", SessionMeta{Title: " " + long + " "})
	h.publish(h.clock.Now().Add(-time.Minute), translate.Event{Type: "agent_error"})
	sent := h.waitSent(t, 2)
	if got := []rune(sent[1].req.Title); len(got) != 60 || string(got) != string([]rune(long)[:60]) {
		t.Fatalf("截断标题长度 %d,内容 %q", len(got), sent[1].req.Title)
	}
}

func TestDoneMinDuration(t *testing.T) {
	h := newHarness(t, 20*time.Second)
	now := h.clock.Now()
	h.publish(now.Add(-19*time.Second), translate.Event{Type: "agent_done"})
	h.waitQuiet(t)
	h.publish(now.Add(-20*time.Second), translate.Event{Type: "agent_done"})
	sent := h.waitSent(t, 1)
	if sent[0].req.Kind != kindDone {
		t.Fatalf("边界应发送,got %+v", sent[0].req)
	}

	zero := newHarness(t, 0)
	zero.publish(zero.clock.Now(), translate.Event{Type: "agent_done"})
	if got := zero.waitSent(t, 1); got[0].req.Kind != kindDone {
		t.Fatalf("门槛 0 应总是发送")
	}
}

func TestResolvedItemsAreDropped(t *testing.T) {
	h := newHarness(t, 0)
	granted := true
	answered := h.clock.Now()
	h.sessions.set("s1", SessionMeta{
		Title: "会话一",
		Escalations: []session.Escalation{{
			EscalationID: "esc-1", Granted: &granted,
		}},
		Questions: []session.Question{{QuestionID: "q-1", AnsweredAt: &answered}},
	})
	h.publish(h.clock.Now(), escalationEvent("esc-1", ""))
	h.publish(h.clock.Now(), questionEvent("q-1"))
	h.waitQuiet(t)
}

func TestDedupeWindow(t *testing.T) {
	h := newHarness(t, 0)
	h.publish(h.clock.Now(), questionEvent("q-1"))
	h.waitSent(t, 1)
	h.publish(h.clock.Now().Add(time.Minute), questionEvent("q-1"))
	time.Sleep(40 * time.Millisecond)
	if got := h.sender.snapshot(); len(got) != 1 {
		t.Fatalf("10 分钟内重复事件应丢弃,实际 %d", len(got))
	}
	h.clock.Advance(dedupeWindow + time.Second)
	h.publish(h.clock.Now(), questionEvent("q-1"))
	if got := h.waitSent(t, 2); got[1].req.RefID != "q-1" {
		t.Fatalf("窗口过后应再发,got %+v", got[1].req)
	}
}

func TestPreferenceFilter(t *testing.T) {
	h := newHarness(t, 0)
	h.devices.devices = []Device{
		{ID: "off", Prefs: map[string]bool{prefQuestion: false, prefApproval: true}},
		{ID: "on", Prefs: map[string]bool{prefQuestion: true}},
		{ID: "default", Prefs: nil},
	}
	h.publish(h.clock.Now(), questionEvent("q-1"))
	sent := h.waitSent(t, 2)
	got := map[string]bool{}
	for _, item := range sent {
		got[item.req.DeviceID] = true
	}
	if got["off"] || !got["on"] || !got["default"] {
		t.Fatalf("prefs 过滤结果 = %#v", got)
	}
}

func TestNoDevicesDrops(t *testing.T) {
	h := newHarness(t, 0)
	h.devices.devices = nil
	h.publish(h.clock.Now(), questionEvent("q-1"))
	h.waitQuiet(t)
}

func TestSenderMissingDropsQuietly(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	n := New(Deps{
		Sessions: &fakeSessions{metas: map[string]SessionMeta{"s1": {Title: "t"}}},
		Devices:  &fakeDevices{devices: []Device{{ID: "dev-1"}}},
		Logger:   slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	}, Options{Now: clock.Now})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n.Start(ctx)
	n.Observe("s1", "run-1", clock.Now(), questionEvent("q-1"))
	time.Sleep(40 * time.Millisecond)
	cancel()
	select {
	case <-n.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("未退出")
	}
}

func TestRetryBackoffAndGiveUp(t *testing.T) {
	h := newHarness(t, 0)
	h.sender.results = []cloudclient.PushResult{
		cloudclient.PushRetryLater, cloudclient.PushRetryLater, cloudclient.PushOK,
	}
	h.publish(h.clock.Now(), questionEvent("q-1"))
	first := h.waitSent(t, 1)
	if first[0].req.Kind != kindQuestion {
		t.Fatal(first[0].req.Kind)
	}
	h.clock.Advance(2 * time.Second)
	h.n.kick()
	second := h.waitSent(t, 2)
	if gap := second[1].at.Sub(second[0].at); gap != 2*time.Second {
		t.Fatalf("第一次退避 = %s, want 2s", gap)
	}
	h.clock.Advance(4 * time.Second)
	h.n.kick()
	third := h.waitSent(t, 3)
	if gap := third[2].at.Sub(third[1].at); gap != 4*time.Second {
		t.Fatalf("第二次退避 = %s, want 4s", gap)
	}
	time.Sleep(30 * time.Millisecond)
	if got := h.sender.snapshot(); len(got) != 3 {
		t.Fatalf("成功后不应再发,实际 %d", len(got))
	}
}

func TestRetryAfterOverridesBackoff(t *testing.T) {
	h := newHarness(t, 0)
	h.sender.results = []cloudclient.PushResult{cloudclient.PushRetryLater, cloudclient.PushOK}
	h.sender.errs = []error{&cloudclient.RetryAfterError{Seconds: 30}}
	h.publish(h.clock.Now(), translate.Event{Type: "agent_error"})
	h.waitSent(t, 1)
	h.clock.Advance(2 * time.Second)
	h.n.kick()
	time.Sleep(30 * time.Millisecond)
	if got := h.sender.snapshot(); len(got) != 1 {
		t.Fatalf("未到 Retry-After 不应重试,实际 %d", len(got))
	}
	h.clock.Advance(28 * time.Second)
	h.n.kick()
	sent := h.waitSent(t, 2)
	if gap := sent[1].at.Sub(sent[0].at); gap != 30*time.Second {
		t.Fatalf("Retry-After 间隔 = %s, want 30s", gap)
	}
}

func TestExpiryDropsRetry(t *testing.T) {
	h := newHarness(t, 0)
	h.sender.results = []cloudclient.PushResult{cloudclient.PushRetryLater, cloudclient.PushRetryLater}
	h.publish(h.clock.Now(), translate.Event{Type: "agent_done"})
	h.waitSent(t, 1)
	h.clock.Advance(terminalTTL)
	h.n.kick()
	time.Sleep(40 * time.Millisecond)
	if got := h.sender.snapshot(); len(got) != 1 {
		t.Fatalf("过期后不应再发,实际 %d", len(got))
	}
	if !strings.Contains(h.logs.String(), "expired") {
		t.Fatalf("日志应记录过期丢弃: %s", h.logs.String())
	}
}

func TestTerminalResultsDoNotRetry(t *testing.T) {
	for _, result := range []cloudclient.PushResult{cloudclient.PushUnregistered, cloudclient.PushRejected, cloudclient.PushNotReady, cloudclient.PushOK} {
		h := newHarness(t, 0)
		h.sender.results = []cloudclient.PushResult{result}
		h.publish(h.clock.Now(), questionEvent("q-"+resultName(result)))
		h.waitSent(t, 1)
		h.clock.Advance(retryCap)
		h.n.kick()
		time.Sleep(30 * time.Millisecond)
		if got := h.sender.snapshot(); len(got) != 1 {
			t.Fatalf("%s 不应重试,实际 %d", resultName(result), len(got))
		}
	}
}

func TestDevicesRetryIndependently(t *testing.T) {
	h := newHarness(t, 0)
	h.devices.devices = []Device{{ID: "slow"}, {ID: "fast"}}
	h.sender.byDevice = map[string][]cloudclient.PushResult{
		"slow": {cloudclient.PushRetryLater, cloudclient.PushOK},
		"fast": {cloudclient.PushOK},
	}
	h.publish(h.clock.Now(), questionEvent("q-1"))
	h.waitSent(t, 2)
	h.clock.Advance(2 * time.Second)
	h.n.kick()
	sent := h.waitSent(t, 3)
	var third cloudclient.PushRequest
	for _, item := range sent {
		if item.at.Equal(h.clock.Now()) {
			third = item.req
		}
	}
	if third.DeviceID != "slow" {
		t.Fatalf("只有 slow 应重试,got %+v", third)
	}
}

func TestResolvedBeforeRetryDrops(t *testing.T) {
	h := newHarness(t, 0)
	h.sender.results = []cloudclient.PushResult{cloudclient.PushRetryLater, cloudclient.PushOK}
	h.publish(h.clock.Now(), questionEvent("q-1"))
	h.waitSent(t, 1)
	answered := h.clock.Now()
	h.sessions.set("s1", SessionMeta{
		Title:     "会话一",
		Questions: []session.Question{{QuestionID: "q-1", AnsweredAt: &answered}},
	})
	h.clock.Advance(2 * time.Second)
	h.n.kick()
	time.Sleep(40 * time.Millisecond)
	if got := h.sender.snapshot(); len(got) != 1 {
		t.Fatalf("已回答的提问不应重试,实际 %d", len(got))
	}
}

func TestObserveDropsWhenQueueFull(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	n := New(Deps{
		Sessions: &fakeSessions{metas: map[string]SessionMeta{"s1": {Title: "t"}}},
		Devices:  &fakeDevices{devices: []Device{{ID: "dev-1"}}},
		Logger:   slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	}, Options{Now: clock.Now, QueueSize: 1})
	done := make(chan struct{})
	go func() {
		n.Observe("s1", "run-1", clock.Now(), questionEvent("q-1"))
		n.Observe("s1", "run-1", clock.Now(), questionEvent("q-2"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("队列满时 Observe 阻塞了")
	}
}

func TestLogsOmitSensitiveFields(t *testing.T) {
	h := newHarness(t, 0)
	title := "机密标题-TOKEN"
	bodyMarker := "需要你审批"
	h.sessions.set("s1", SessionMeta{Title: title})
	h.sender.results = []cloudclient.PushResult{cloudclient.PushRetryLater, cloudclient.PushOK}
	h.publish(h.clock.Now(), escalationEvent("esc-1", ""))
	h.waitSent(t, 1)
	h.clock.Advance(2 * time.Second)
	h.n.kick()
	h.waitSent(t, 2)
	logs := h.logs.String()
	for _, secret := range []string{title, bodyMarker, "s1", "apns-token", "esc-1"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("日志泄漏 %q: %s", secret, logs)
		}
	}
	if !strings.Contains(logs, "kind="+kindApproval) || !strings.Contains(logs, "device=dev-1") || !strings.Contains(logs, "retries=0") || !strings.Contains(logs, "retries=1") {
		t.Fatalf("日志缺少允许的字段: %s", logs)
	}
}

func TestBadgeDoesNotDoubleCountRecordedRequest(t *testing.T) {
	h := newHarness(t, 20*time.Second)
	h.sessions.metas["s1"] = SessionMeta{
		Title:          "会话一",
		PermissionMode: "ask",
		Escalations:    []session.Escalation{{EscalationID: "esc-1"}},
	}
	h.publish(h.clock.Now().Add(-time.Minute), escalationEvent("esc-1", ""))

	sent := h.waitSent(t, 1)
	if got := sent[0].req.Badge; got == nil || *got != 3 {
		t.Fatalf("已登记的请求已计入总数，角标应为 3，得到 %v", got)
	}
}

func TestWorkerSurvivesPanicAndKeepsDelivering(t *testing.T) {
	h := newHarness(t, 20*time.Second)
	h.devices.mu.Lock()
	h.devices.panicOnce = true
	h.devices.mu.Unlock()
	start := h.clock.Now().Add(-time.Minute)

	h.publish(start, escalationEvent("esc-boom", ""))
	h.publish(start, questionEvent("q-after"))

	// 两个 worker 并发，先处理到的那条会触发 panic，哪条不固定；只断言之后仍能继续投递。
	h.waitSent(t, 1)
	h.publish(start, questionEvent("q-final"))
	sent := h.waitSent(t, 2)
	if len(sent) != 2 {
		t.Fatalf("panic 之后的通知没有发出: %+v", sent)
	}
	if strings.Contains(h.logs.String(), "esc-boom") || strings.Contains(h.logs.String(), "会话一") {
		t.Fatalf("panic 日志泄漏了通知内容: %s", h.logs.String())
	}
}
