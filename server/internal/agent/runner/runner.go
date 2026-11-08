// Package runner 编排一次 Agent run 的完整生命周期:会话元数据、短期令牌、
// Pi 子进程、事件翻译与内存事件流。同一会话由 active 表互斥,避免多个 Pi
// 进程同时写一份会话文件。
package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"xlyra/server/internal/agent/capabilities"
	"xlyra/server/internal/agent/execd"
	"xlyra/server/internal/agent/isolation"
	"xlyra/server/internal/agent/pirpc"
	"xlyra/server/internal/agent/runlog"
	"xlyra/server/internal/agent/sandbox"
	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agent/translate"
)

var (
	ErrInvalid     = errors.New("runner: 非法请求")
	ErrNotFound    = errors.New("runner: 目标不存在")
	ErrBusy        = errors.New("runner: 会话已有进行中的 run")
	ErrCapacity    = errors.New("runner: 并发已满")
	ErrUnavailable = errors.New("runner: Agent 不可用")
	// ErrNotRunning 表示会话存在但没有进行中的 run(handler 映射 409)。
	ErrNotRunning = errors.New("runner: 会话没有进行中的 run")
	// ErrInvalidAnswer 表示提问回答不合法(handler 映射 400)。
	ErrInvalidAnswer = errors.New("runner: 回答不合法")
)

// TokenIssuer 由 agentllm.Handler 实现。
type TokenIssuer interface {
	IssueRunToken(ctx context.Context, sessionID, runID, model string) (string, time.Time, error)
	EndRun(ctx context.Context, sessionID, runID, model string) error
}

// CapabilitiesSource 是每次 run 取能力快照的最小接口(技能、AGENTS.md、
// 上下文参数),生产实现是 *capabilities.Store。
type CapabilitiesSource interface {
	Snapshot() (capabilities.Snapshot, error)
}

// IsolationConfig 开启进程隔离:Pi 以会话专属身份(槽位→PiUID/GID)启动,
// 会话目录按 isolation 布局收紧。Config 只含身份池参数,零值取默认
// (UIDBase=20000、MaxSlots=4000)。
type IsolationConfig struct {
	Config isolation.Config
}

// SandboxConfig 控制 Pi 自身与命令执行器的 Landlock 沙箱。
type SandboxConfig struct {
	Exe                string
	PiLandlock         bool
	Bash               bool
	ExecDefaultTimeout time.Duration
	ExecMaxTimeout     time.Duration
}

// LocalBashConfig 描述 bash 在本机直接执行的超时限制;非 nil 表示 bash 不经
// 沙箱,与 xLyra 同权限运行。
type LocalBashConfig struct {
	DefaultTimeout time.Duration
	MaxTimeout     time.Duration
}

type Config struct {
	Store          *session.Store
	Runs           *runlog.Registry
	Tokens         TokenIssuer
	Protocol       func(ctx context.Context, model string) (string, error)
	GatewayBaseURL string
	Command        string
	Args           []string
	ExtraArgs      []string
	Tools          []string
	// ImageModels 每次 run 实时返回生图模型数量;nil 或零不注册生图工具。
	ImageModels func() int
	// Classify 调用内部文本补全判断单次审批能否自动放行;nil 时 auto 直接拒绝。
	Classify func(ctx context.Context, sessionID, model, system, user string, maxTokens int) (string, error)
	// ClassifierModel 返回分类器专用模型;为空时使用本次 run 的模型。
	ClassifierModel func() string
	// FastModel 返回快速模型(文件摘要、会话标题);为空时使用本次 run 的模型。
	FastModel func() string
	// FastTasks 为 true 时普通 run 注册 summarize_file 工具(经 fast-task 端点调快速模型)。
	FastTasks bool
	// CompleteText 调用内部文本补全生成会话标题等;nil 时跳过 AI 标题。
	CompleteText func(ctx context.Context, sessionID, model, system, user string, maxTokens int, kind string) (string, error)
	// ExtensionPath 是 xLyra 扩展(xLyra.ts)的绝对路径,经 -e 传给 Pi;
	// Tools 含 write/edit 时必填且文件必须存在(Health fail-closed)。
	ExtensionPath string
	// SystemPrompt 覆盖默认系统提示(prompt.go),为空用默认。
	SystemPrompt string
	// Capabilities 提供每次 run 的能力快照;nil 时技能与 AGENTS.md 全关、
	// 压缩水位线 0.9、窗口 200000。
	Capabilities  CapabilitiesSource
	PathEnv       string
	MaxConcurrent int
	// RunTimeout 是单个 run 的时长上限;令牌已改文件轮换,不再受 TTL 约束。
	RunTimeout time.Duration
	// QuestionTimeout 是提问无人回答时自动跳过的等待时长;0 表示不超时。
	QuestionTimeout time.Duration
	// MaxOutputBytes 是单个 run 内 Pi stdout 事件的总字节上限。
	MaxOutputBytes int64
	// SessionQuotaBytes 是会话 workspace/ 与 pi/ 的总字节配额。
	SessionQuotaBytes int64
	// NodeFlags 插在 Command 与 Args 之间,用于设置 Node 堆等运行时参数。
	NodeFlags []string
	// QueueTimeout 是等待并发名额的最长时间。
	QueueTimeout time.Duration
	// QueueMax 是等待队列长度;-1 关闭排队,恢复并发满直接拒绝。
	QueueMax int
	// TokenRenewInterval 是令牌续期间隔:每到点重新签发并原子重写令牌文件。
	// 必须显著小于 agentllm.DefaultTokenTTL(30m):默认 10m 下令牌在龄期
	// ≤10m 时被替换,可容忍两次连续续期失败。
	TokenRenewInterval time.Duration
	// Isolation 非 nil 时开启进程隔离(需 root + Linux):每次 run 按会话
	// 槽位分配身份、准备目录并以该身份启动 Pi;失败则该 run 以 agent_error
	// 收尾,不降级为无隔离启动。nil 时行为与无隔离时完全一致。
	Isolation *IsolationConfig
	// Sandbox 非 nil 时按配置用 Landlock 启动 Pi,并可为普通 run 启动命令执行器。
	Sandbox *SandboxConfig
	// LocalBash 非 nil 表示 bash 在本机直接执行(不经沙箱,可联网),
	// 与 Sandbox.Bash 互斥。
	LocalBash  *LocalBashConfig
	AbortGrace time.Duration
	Logger     *slog.Logger
}

type Runner struct {
	cfg    Config
	logger *slog.Logger
	tools  string

	mu     sync.Mutex
	closed bool
	// restoring 为真表示正在恢复备份:新 run 一律被拒绝(见 BeginRestore)。
	restoring bool
	active    map[string]*activeRun
	running   int
	queue     []*activeRun
	wg        sync.WaitGroup
	// bgCtx 是不随单个 run 结束的后台任务(会话标题)的父 ctx,Shutdown 时取消。
	bgCtx    context.Context
	bgCancel context.CancelFunc
}

type activeRun struct {
	sessionID string
	runID     string
	model     string
	mode      string
	kind      string
	turnID    string
	run       *runlog.Run // stateMu 保护;admit 后即可被 Grant/清理并发访问
	tr        *translate.Translator
	proc      atomic.Pointer[pirpc.Process]

	// grant 在取得并发名额时关闭;queued/hasSlot 由 Runner.mu 保护。
	grant      chan struct{}
	queued     bool
	hasSlot    bool
	queueAhead int

	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	stopping  atomic.Bool
	abortOnce sync.Once
	cleanOnce sync.Once
	renewWG   sync.WaitGroup // 续期、配额与分类 goroutine,cleanup 里 cancel 后 Wait
	// auto 的自动拒绝计数;分类器放行只清连续值,累计值贯穿本次 run。
	autoDenyStreak atomic.Int64
	autoDenyTotal  atomic.Int64

	stateMu          sync.Mutex
	terminal         string
	singleTerminal   bool
	endReason        string
	pending          map[string]session.Escalation
	autoReviewing    map[string]translate.Event  // auto 决定前的原审批请求;这些审批不等用户,决定时随结果补发
	resolved         map[string]resolveOptions   // 已回写的审批决定:决定先于落盘到达时由 recordEscalation 补写
	pendingQuestions map[string]session.Question // 未回答的提问,id 即 extension_ui_request 的 id
	questionTimers   map[string]*time.Timer      // 提问超时自动跳过的计时器,与 pendingQuestions 同生命周期
	titleCancel      context.CancelFunc          // 首条消息的标题生成;仅显式停止 run 时取消,正常结束仍允许落盘
	waitChanged      chan struct{}               // 未决审批/提问集合变化的信号(容量 1),事件循环据此暂停/恢复运行总时限
	startedAt        time.Time
	lastEntryID      string // 事件循环里最后见到的 Pi 条目 id,作 run 失败记录的锚点
	lastError        string // 最近发布的 agent_error message,cleanup 按最终终态决定是否落盘
}

type runSpec struct {
	model      string
	effort     string
	mode       string
	text       string
	images     []map[string]any
	references []session.Reference
	retry      *retrySpec
}

type retrySpec struct {
	messageID   string
	index       int
	original    session.UserMessage
	content     string
	attachments []session.Attachment
	references  []session.Reference
}

type prepared struct {
	proc        *pirpc.Process
	executor    *execd.Executor
	level       string
	tokenFile   string // 令牌文件路径,续期时原子重写
	tokenIssued bool
	// identity 非 nil 表示本 run 开启隔离:root 写出的文件(含续期重写的
	// 令牌)都要移交给会话身份。
	identity *isolation.Identity

	tokenMu sync.Mutex
	tokens  []string // 本 run 签发过的全部令牌,供 stderr 脱敏(含已被替换的旧令牌)
}

func (p *prepared) addToken(token string) {
	p.tokenMu.Lock()
	p.tokens = append(p.tokens, token)
	p.tokenMu.Unlock()
}

func (p *prepared) tokenList() []string {
	p.tokenMu.Lock()
	defer p.tokenMu.Unlock()
	return append([]string(nil), p.tokens...)
}

type Health struct {
	Available bool
	Reason    string
	PiVersion string
}

type Attachment struct {
	Name     string
	MimeType string
	DataURL  string
}

type SendInput struct {
	SessionID       string
	Content         string
	Model           string
	ReasoningEffort string
	PermissionMode  string
	Attachments     []Attachment
	// References 是本次发送引用的其他会话 id(最多 3 个,去重;不能含当前
	// 会话;每个必须存在),内容由后端渲染后拼进发给 Pi 的提示。
	References []string
}

type SendResult struct {
	SessionID string
	MessageID string
	RunID     string
}

type RetryInput struct {
	SessionID       string
	MessageID       string
	Content         string
	Model           string
	ReasoningEffort string
	PermissionMode  string
	Attachments     []Attachment
	// References 为空时沿用原消息保存的引用(重新渲染取最新内容)。
	References []string
}

type CompactResult struct {
	Summary      string
	TokensBefore int
	TokensAfter  int
	CompactionID string
}

type GrantInput struct {
	EscalationID string
	Granted      bool
	ResolvedPath string
	// Scope 为 "session" 时表示会话内允许:这条审批按允许处理,并把
	// 工具名写进 meta.SessionAllow;其余值(含空)均为仅此一次。
	Scope string
}

func New(cfg Config) (*Runner, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("runner: Store 不能为空")
	case cfg.Runs == nil:
		return nil, errors.New("runner: Runs 不能为空")
	case cfg.Tokens == nil:
		return nil, errors.New("runner: Tokens 不能为空")
	case cfg.Protocol == nil:
		return nil, errors.New("runner: Protocol 不能为空")
	case strings.TrimSpace(cfg.GatewayBaseURL) == "":
		return nil, errors.New("runner: GatewayBaseURL 不能为空")
	case strings.TrimSpace(cfg.Command) == "":
		return nil, errors.New("runner: Command 不能为空")
	case cfg.Sandbox != nil && cfg.Sandbox.Bash && (!cfg.Sandbox.PiLandlock || cfg.Isolation == nil):
		return nil, errors.New("runner: 沙箱 bash 要求同时开启 PiLandlock 与 Isolation")
	case cfg.LocalBash != nil && cfg.Sandbox != nil && cfg.Sandbox.Bash:
		return nil, errors.New("runner: LocalBash 与沙箱 bash 不能同时开启")
	}
	if cfg.Sandbox != nil {
		sandbox := *cfg.Sandbox
		if sandbox.ExecDefaultTimeout <= 0 {
			sandbox.ExecDefaultTimeout = 120 * time.Second
		}
		if sandbox.ExecMaxTimeout <= 0 {
			sandbox.ExecMaxTimeout = 600 * time.Second
		}
		if sandbox.ExecDefaultTimeout > sandbox.ExecMaxTimeout {
			sandbox.ExecDefaultTimeout = sandbox.ExecMaxTimeout
		}
		cfg.Sandbox = &sandbox
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 4
	}
	if cfg.RunTimeout <= 0 {
		cfg.RunTimeout = 60 * time.Minute
	}
	if cfg.MaxOutputBytes <= 0 {
		cfg.MaxOutputBytes = 64 << 20
	}
	if cfg.SessionQuotaBytes <= 0 {
		cfg.SessionQuotaBytes = 512 << 20
	}
	if cfg.QueueTimeout <= 0 {
		cfg.QueueTimeout = 2 * time.Minute
	}
	if cfg.QueueMax <= 0 && cfg.QueueMax != -1 {
		cfg.QueueMax = 32
	}
	if cfg.AbortGrace <= 0 {
		cfg.AbortGrace = 5 * time.Second
	}
	if cfg.TokenRenewInterval <= 0 {
		cfg.TokenRenewInterval = 10 * time.Minute
	}
	if len(cfg.Tools) == 0 {
		cfg.Tools = []string{"read", "grep", "find", "ls", "write", "edit"}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	bgCtx, bgCancel := context.WithCancel(context.Background())
	return &Runner{
		cfg:      cfg,
		logger:   cfg.Logger,
		tools:    strings.Join(cfg.Tools, ","),
		active:   map[string]*activeRun{},
		bgCtx:    bgCtx,
		bgCancel: bgCancel,
	}, nil
}

func (r *Runner) Health() Health {
	command := strings.TrimSpace(r.cfg.Command)
	if command == "" {
		return Health{Reason: "未配置 Pi 命令"}
	}
	if strings.ContainsRune(command, filepath.Separator) {
		info, err := os.Stat(command)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			return Health{Reason: "Pi 命令不存在或不可执行: " + command}
		}
	} else if _, err := exec.LookPath(command); err != nil {
		return Health{Reason: "Pi 命令不可执行: " + command}
	}
	// 部署时 Args[0] 通常是 Pi 的 cli.js;测试或扩展参数以 '-' 开头,不是脚本。
	if len(r.cfg.Args) > 0 && r.cfg.Args[0] != "" && !strings.HasPrefix(r.cfg.Args[0], "-") {
		info, err := os.Stat(r.cfg.Args[0])
		if err != nil || info.IsDir() {
			return Health{Reason: "Pi 入口脚本不存在: " + r.cfg.Args[0]}
		}
	}
	// fail-closed:写类工具的路径约束与审批都由 xLyra 扩展实现,扩展缺失时
	// 不允许自降为无约束地开放 write/edit。
	if r.needsExtension() {
		path := strings.TrimSpace(r.cfg.ExtensionPath)
		if path == "" {
			return Health{Reason: "缺少 xLyra 扩展,写类工具无法受控(未配置扩展路径)"}
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return Health{Reason: "缺少 xLyra 扩展,写类工具无法受控: " + path}
		}
	}
	return Health{Available: true}
}

// needsExtension 报告工具白名单是否含写类工具;含则必须加载 xLyra 扩展。
func (r *Runner) needsExtension() bool {
	for _, tool := range r.cfg.Tools {
		if tool == "write" || tool == "edit" {
			return true
		}
	}
	return false
}

func (r *Runner) Send(ctx context.Context, in SendInput) (SendResult, error) {
	spec, metaAttachments, err := r.validateMessage(in.Content, in.Model, in.PermissionMode, in.ReasoningEffort, in.Attachments)
	if err != nil {
		return SendResult{}, err
	}
	if health := r.Health(); !health.Available {
		return SendResult{}, fmt.Errorf("%w: %s", ErrUnavailable, health.Reason)
	}

	var meta session.Meta
	created := in.SessionID == ""
	if created {
		meta, err = r.cfg.Store.Create(spec.model, spec.mode)
	} else {
		meta, err = r.cfg.Store.Get(in.SessionID)
	}
	if err != nil {
		if errors.Is(err, session.ErrNotFound) || errors.Is(err, session.ErrInvalidID) {
			return SendResult{}, fmt.Errorf("%w: %s", ErrNotFound, in.SessionID)
		}
		return SendResult{}, err
	}
	// 本次调用新建的会话,后面任何一步失败都要删掉,否则被拒绝的请求会留下空会话
	fail := func(err error) (SendResult, error) {
		if created {
			_ = r.cfg.Store.Delete(meta.ID)
		}
		return SendResult{}, err
	}
	references, err := r.resolveReferences(meta.ID, in.References)
	if err != nil {
		return fail(err)
	}
	spec.references = r.withTextReferences(meta.ID, in.Content, references)

	ar, err := r.admit(meta.ID, spec, "send")
	if err != nil {
		return fail(err)
	}
	ar.run, err = r.cfg.Runs.Begin(meta.ID, ar.runID)
	if err != nil {
		r.cleanup(ar, nil)
		if errors.Is(err, runlog.ErrRunActive) {
			return fail(fmt.Errorf("%w: %s", ErrBusy, meta.ID))
		}
		return fail(err)
	}
	ar.setRun(ar.run)
	messageID := uuid.NewString()
	ar.turnID = messageID
	first := len(meta.Messages) == 0
	if err := r.appendUserMessage(meta.ID, messageID, in.Content, spec, metaAttachments, first); err != nil {
		r.cleanup(ar, nil)
		return fail(err)
	}
	// 必须在 start 之前:此时本 run 的 r.wg 计数尚未释放,标题 goroutine 才能安全 Add。
	if first && ar.kind == "send" {
		r.maybeGenerateTitle(ar, in.Content)
	}
	r.start(ar, spec)
	return SendResult{SessionID: meta.ID, MessageID: messageID, RunID: ar.runID}, nil
}

func (r *Runner) Retry(ctx context.Context, in RetryInput) (SendResult, error) {
	if strings.TrimSpace(in.SessionID) == "" || strings.TrimSpace(in.MessageID) == "" {
		return SendResult{}, fmt.Errorf("%w: SessionID 与 MessageID 不能为空", ErrInvalid)
	}
	meta, err := r.cfg.Store.Get(in.SessionID)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) || errors.Is(err, session.ErrInvalidID) {
			return SendResult{}, fmt.Errorf("%w: %s", ErrNotFound, in.SessionID)
		}
		return SendResult{}, err
	}
	index := -1
	for i := range meta.Messages {
		if meta.Messages[i].MessageID == in.MessageID {
			index = i
			break
		}
	}
	if index < 0 {
		return SendResult{}, fmt.Errorf("%w: 用户消息 %s", ErrNotFound, in.MessageID)
	}
	original := meta.Messages[index]
	content := in.Content
	if content == "" {
		content = original.Text
	}
	model := strings.TrimSpace(in.Model)
	if model == "" {
		model = meta.Model
	}
	attachments := in.Attachments
	if attachments == nil {
		attachments = r.originalAttachments(meta, original)
	}
	spec, metaAttachments, err := r.validateMessage(content, model, in.PermissionMode, in.ReasoningEffort, attachments)
	if err != nil {
		return SendResult{}, err
	}
	if health := r.Health(); !health.Available {
		return SendResult{}, fmt.Errorf("%w: %s", ErrUnavailable, health.Reason)
	}
	// 引用列表为空时沿用原消息保存的引用;重发时重新渲染,取被引用会话的最新内容。
	referenceIDs := in.References
	if len(referenceIDs) == 0 {
		for _, ref := range original.References {
			referenceIDs = append(referenceIDs, ref.SessionID)
		}
	}
	references, err := r.resolveReferences(meta.ID, referenceIDs)
	if err != nil {
		return SendResult{}, err
	}
	spec.references = r.withTextReferences(meta.ID, content, references)

	ar, err := r.admit(meta.ID, spec, "retry")
	if err != nil {
		return SendResult{}, err
	}
	ar.run, err = r.cfg.Runs.Begin(meta.ID, ar.runID)
	if err != nil {
		r.cleanup(ar, nil)
		if errors.Is(err, runlog.ErrRunActive) {
			return SendResult{}, fmt.Errorf("%w: %s", ErrBusy, meta.ID)
		}
		return SendResult{}, err
	}
	ar.setRun(ar.run)
	messageID := uuid.NewString()
	ar.turnID = messageID
	// fork 与消息改写必须等取得名额后再做;排队期间不启动 Pi、不签发令牌。
	spec.retry = &retrySpec{
		messageID: messageID, index: index, original: original,
		content: content, attachments: metaAttachments, references: references,
	}
	r.start(ar, spec)
	return SendResult{SessionID: meta.ID, MessageID: messageID, RunID: ar.runID}, nil
}

func (r *Runner) Compact(ctx context.Context, sessionID, model string) (CompactResult, error) {
	if strings.TrimSpace(sessionID) == "" {
		return CompactResult{}, fmt.Errorf("%w: SessionID 不能为空", ErrInvalid)
	}
	meta, err := r.cfg.Store.Get(sessionID)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) || errors.Is(err, session.ErrInvalidID) {
			return CompactResult{}, fmt.Errorf("%w: %s", ErrNotFound, sessionID)
		}
		return CompactResult{}, err
	}
	if model == "" {
		model = meta.Model
	}
	if strings.TrimSpace(model) == "" {
		return CompactResult{}, fmt.Errorf("%w: 模型不能为空", ErrInvalid)
	}
	if health := r.Health(); !health.Available {
		return CompactResult{}, fmt.Errorf("%w: %s", ErrUnavailable, health.Reason)
	}
	spec := runSpec{model: model, mode: meta.PermissionMode}
	ar, err := r.admit(sessionID, spec, "compact")
	if err != nil {
		return CompactResult{}, err
	}
	var prep *prepared
	defer func() { r.cleanup(ar, prep) }()
	if err := r.waitForSlot(ar, ctx, false); err != nil {
		if errors.Is(err, errQueueTimeout) {
			return CompactResult{}, fmt.Errorf("%w: Agent 繁忙", ErrCapacity)
		}
		return CompactResult{}, err
	}

	compactCtx, cancel := context.WithTimeout(ar.ctx, r.cfg.RunTimeout)
	defer cancel()
	stopCaller := context.AfterFunc(ctx, cancel)
	defer stopCaller()

	prep, err = r.prepare(ar, spec, meta.PiSessionID)
	if err != nil {
		return CompactResult{}, err
	}
	// compact 不是普通 run,不写 runlog;仍须持续读 stdout,避免背压卡住进程。
	drained := make(chan struct{})
	go func() {
		for range prep.proc.Events() {
		}
		close(drained)
	}()
	resp, err := prep.proc.Call(compactCtx, map[string]any{"type": "compact"})
	if err != nil {
		return CompactResult{}, fmt.Errorf("runner: compact: %w", err)
	}
	data, err := commandData(resp)
	if err != nil {
		// 会话太短时 Pi 拒绝压缩,属于用户可理解的情况,不是内部错误
		if strings.Contains(err.Error(), "Nothing to compact") {
			return CompactResult{}, fmt.Errorf("%w: 会话内容较少,暂无需压缩", ErrInvalid)
		}
		return CompactResult{}, fmt.Errorf("runner: compact: %w", err)
	}
	var result struct {
		Summary              string `json:"summary"`
		TokensBefore         int    `json:"tokensBefore"`
		EstimatedTokensAfter int    `json:"estimatedTokensAfter"`
		CompactionID         string `json:"compactionId"`
	}
	if err := jsonUnmarshal(data, &result); err != nil {
		return CompactResult{}, fmt.Errorf("runner: compact 应答解析失败: %w", err)
	}
	r.settleMeta(ar, r.currentPiSessionID(prep.proc))
	_ = prep.proc.Close()
	<-drained
	return CompactResult{
		Summary: result.Summary, TokensBefore: result.TokensBefore,
		TokensAfter: result.EstimatedTokensAfter, CompactionID: result.CompactionID,
	}, nil
}

func (r *Runner) Stop(ctx context.Context, sessionID string) error {
	r.mu.Lock()
	ar := r.active[sessionID]
	r.mu.Unlock()
	if ar == nil {
		return nil
	}
	r.stopRun(ar)
	timer := time.NewTimer(r.cfg.AbortGrace + 15*time.Second)
	defer timer.Stop()
	select {
	case <-ar.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("runner: 停止 run 超时")
	}
}

func (r *Runner) Grant(ctx context.Context, sessionID string, in GrantInput) error {
	r.mu.Lock()
	ar := r.active[sessionID]
	r.mu.Unlock()
	if ar == nil {
		return fmt.Errorf("%w: 审批 %s", ErrNotFound, in.EscalationID)
	}
	// 拒绝时 scope 无意义;非法值一律按仅此一次兜底。
	scope := ""
	if in.Granted && in.Scope == "session" {
		scope = "session"
	}
	if !r.resolveEscalation(ar, in.EscalationID, resolveOptions{
		granted: in.Granted, resolvedPath: in.ResolvedPath, scope: scope,
	}) {
		return fmt.Errorf("%w: 审批 %s", ErrNotFound, in.EscalationID)
	}
	return nil
}

func (r *Runner) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	if r.bgCancel != nil {
		r.bgCancel()
	}
	runs := make([]*activeRun, 0, len(r.active))
	for _, ar := range r.active {
		runs = append(runs, ar)
	}
	r.mu.Unlock()
	for _, ar := range runs {
		r.stopRun(ar)
	}
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// BeginRestore 在恢复备份前调用:有进行中或排队的 run 时返回 ErrBusy(恢复会
// 替换会话与工作区文件,不能与运行中的 Agent 并行);否则进入恢复态,新 run
// 一律被拒绝,直到调用 EndRestore。
func (r *Runner) BeginRestore() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	if len(r.active) > 0 || len(r.queue) > 0 {
		return fmt.Errorf("%w: 有进行中的 Agent 任务,请先停止后再恢复备份", ErrBusy)
	}
	r.restoring = true
	return nil
}

// EndRestore 退出恢复态,允许新的 run。
func (r *Runner) EndRestore() {
	r.mu.Lock()
	r.restoring = false
	r.mu.Unlock()
}

var errQueueTimeout = errors.New("runner: 排队超时")

func (r *Runner) admit(sessionID string, spec runSpec, kind string) (*activeRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, fmt.Errorf("%w: 服务正在关闭", ErrUnavailable)
	}
	if r.restoring {
		return nil, fmt.Errorf("%w: 正在恢复备份,请稍后再试", ErrUnavailable)
	}
	if r.active[sessionID] != nil {
		return nil, fmt.Errorf("%w: %s", ErrBusy, sessionID)
	}
	immediate := r.running < r.cfg.MaxConcurrent
	if !immediate {
		if r.cfg.QueueMax == -1 || len(r.queue) >= r.cfg.QueueMax {
			return nil, fmt.Errorf("%w: 上限 %d", ErrCapacity, r.cfg.MaxConcurrent)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	ar := &activeRun{
		sessionID: sessionID, runID: uuid.NewString(), model: spec.model, mode: spec.mode, kind: kind,
		tr: translate.New(), ctx: ctx, cancel: cancel, done: make(chan struct{}), grant: make(chan struct{}),
		pending: map[string]session.Escalation{}, autoReviewing: map[string]translate.Event{}, resolved: map[string]resolveOptions{}, startedAt: time.Now(),
		pendingQuestions: map[string]session.Question{}, questionTimers: map[string]*time.Timer{},
		waitChanged: make(chan struct{}, 1),
	}
	if immediate {
		r.running++
		ar.hasSlot = true
		close(ar.grant)
	} else {
		// 排队提示里的 N 包含正在运行的任务和排在它前面的等待任务。
		ar.queueAhead = r.running + len(r.queue)
		ar.queued = true
		r.queue = append(r.queue, ar)
	}
	r.active[sessionID] = ar
	r.wg.Add(1)
	return ar, nil
}

func (r *Runner) waitForSlot(ar *activeRun, callerCtx context.Context, notify bool) error {
	if notify && ar.queueAhead > 0 {
		r.publish(ar, translate.Event{Type: "agent_notice", Data: map[string]any{
			"notice": fmt.Sprintf("排队中,前面还有 %d 个任务", ar.queueAhead),
		}})
	}
	if err := ar.ctx.Err(); err != nil {
		return err
	}
	var callerDone <-chan struct{}
	if callerCtx != nil {
		if err := callerCtx.Err(); err != nil {
			return err
		}
		callerDone = callerCtx.Done()
	}
	timer := time.NewTimer(r.cfg.QueueTimeout)
	defer timer.Stop()
	select {
	case <-ar.grant:
		if err := ar.ctx.Err(); err != nil {
			return err
		}
		if callerCtx != nil {
			return callerCtx.Err()
		}
		return nil
	case <-ar.ctx.Done():
		return ar.ctx.Err()
	case <-callerDone:
		return callerCtx.Err()
	case <-timer.C:
		return errQueueTimeout
	}
}

func (r *Runner) start(ar *activeRun, spec runSpec) {
	go func() {
		var prep *prepared
		defer func() { r.cleanup(ar, prep) }()
		if err := r.waitForSlot(ar, nil, true); err != nil {
			if errors.Is(err, errQueueTimeout) && !ar.stopping.Load() {
				r.publishFinal(ar, translate.Event{Type: "agent_error", Data: map[string]any{"message": "当前 Agent 繁忙,请稍后重试"}}, "")
			} else {
				r.publishFinal(ar, translate.Event{Type: "agent_cancelled", Data: map[string]any{"aborted": true}}, "")
			}
			return
		}
		ar.startedAt = time.Now()
		exceeded, err := r.sessionQuotaExceeded(ar.sessionID)
		if err == nil && exceeded {
			r.publishFinal(ar, translate.Event{Type: "agent_error", Data: map[string]any{"message": "会话磁盘占用超过配额,请清理或新建会话"}}, "disk_quota")
			return
		}
		if err == nil {
			prep, err = r.prepare(ar, spec, r.sessionPiID(ar.sessionID))
		}
		if err == nil && spec.retry != nil {
			err = r.applyRetry(ar, spec)
		}
		// 引用块在真正发提示前拼入:渲染取被引用会话的最新内容,排队等待
		// 期间的新变化也能被看到。
		if err == nil {
			spec.text = r.withReferences(spec.text, spec.references)
		}
		if err != nil {
			r.logger.Error("Agent 启动失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
			if ar.stopping.Load() {
				r.publish(ar, translate.Event{Type: "agent_cancelled", Data: map[string]any{"aborted": true}})
			} else {
				r.publish(ar, translate.Event{Type: "agent_error", Data: map[string]any{"message": "Agent 启动失败: " + err.Error()}})
			}
			return
		}
		r.beginTurn(ar, prep)
		r.drive(ar, spec, prep)
	}()
}

func (r *Runner) cleanup(ar *activeRun, prep *prepared) {
	ar.cleanOnce.Do(func() {
		defer r.wg.Done()
		// 进程异常退出等路径下仍未回答的提问在关闭进程、结束 run 之前标记取消。
		r.cancelPendingQuestions(ar)
		exitCode := 0
		if prep != nil && prep.proc != nil {
			_ = prep.proc.Close()
			info := prep.proc.Wait()
			exitCode = info.Code
			if info.Err != nil {
				r.logger.Warn("Pi 进程异常退出", "session_id", ar.sessionID, "run_id", ar.runID, "code", info.Code, "signal", info.Signal)
			}
		}
		if prep != nil && prep.executor != nil {
			if err := prep.executor.Close(); err != nil {
				r.logger.Error("关闭命令执行器失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
			}
		}
		if prep != nil && prep.identity != nil && r.cfg.Sandbox != nil && r.cfg.Sandbox.PiLandlock {
			for _, uid := range []uint32{prep.identity.PiUID, prep.identity.ExecUID} {
				if _, _, err := sandbox.KillUID(int(uid)); err != nil {
					r.logger.Error("清理沙箱残留进程失败", "session_id", ar.sessionID, "run_id", ar.runID, "uid", uid, "error", err)
				}
			}
		}
		r.finishTurn(ar, prep)
		if prep != nil && prep.tokenIssued {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := r.cfg.Tokens.EndRun(ctx, ar.sessionID, ar.runID, ar.model); err != nil {
				r.logger.Error("结束 Agent 令牌失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
			}
			cancel()
		}
		r.recordRunError(ar)
		ar.cancel()
		// 续期与配额 goroutine 都盯着 ar.ctx,这里等它们退出,避免收尾泄漏。
		ar.renewWG.Wait()
		r.releaseAdmission(ar)
		ar.stateMu.Lock()
		terminal := ar.terminal
		reason := ar.endReason
		ar.stateMu.Unlock()
		r.logger.Debug("Agent run 结束", "session_id", ar.sessionID, "run_id", ar.runID, "kind", ar.kind,
			"duration", time.Since(ar.startedAt), "terminal", terminal, "exit_code", exitCode, "reason", reason)
		close(ar.done)
	})
}

// releaseAdmission 移除会话互斥与排队状态,并把释放的名额交给队首。
func (r *Runner) releaseAdmission(ar *activeRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Finish 与释放会话互斥要在同一把锁内完成:订阅者见到流结束后立刻重发,
	// 要么还在持锁排队等待,要么已看到互斥被释放,不会撞上"已有进行中的 run"。
	if run := ar.finishedRun(); run != nil {
		run.Finish()
	}
	if r.active[ar.sessionID] == ar {
		delete(r.active, ar.sessionID)
	}
	if ar.queued {
		for i, queued := range r.queue {
			if queued == ar {
				r.queue = append(r.queue[:i], r.queue[i+1:]...)
				break
			}
		}
		ar.queued = false
	}
	if ar.hasSlot {
		ar.hasSlot = false
		r.running--
		for !r.closed && r.running < r.cfg.MaxConcurrent && len(r.queue) > 0 {
			next := r.queue[0]
			r.queue = r.queue[1:]
			next.queued = false
			next.hasSlot = true
			r.running++
			close(next.grant)
		}
	}
}

func (r *Runner) sessionPiID(sessionID string) string {
	meta, err := r.cfg.Store.Get(sessionID)
	if err != nil {
		return ""
	}
	return meta.PiSessionID
}

func (r *Runner) appendUserMessage(sessionID, messageID, content string, spec runSpec, attachments []session.Attachment, first bool) error {
	_, err := r.cfg.Store.Update(sessionID, func(m *session.Meta) error {
		message := session.UserMessage{
			MessageID: messageID, Text: content, CreatedAt: time.Now().UTC(), Attachments: attachments,
		}
		if len(spec.references) > 0 {
			message.References = append([]session.Reference(nil), spec.references...)
		}
		m.Messages = append(m.Messages, message)
		if first && m.Title == "" {
			m.Title = session.TitleFrom(content)
		}
		m.Model, m.PermissionMode = spec.model, spec.mode
		return nil
	})
	return err
}

func (r *Runner) applyRetry(ar *activeRun, spec runSpec) error {
	retry := spec.retry
	entryID := retry.original.EntryID
	if entryID == "" {
		var err error
		entryID, err = r.findForkEntry(ar, retry.original.Text)
		if err != nil {
			return err
		}
	}
	newPiSessionID, err := r.fork(ar, entryID)
	if err != nil {
		return err
	}
	_, err = r.cfg.Store.Update(ar.sessionID, func(m *session.Meta) error {
		m.PiSessionID = newPiSessionID
		m.Messages = append([]session.UserMessage(nil), m.Messages[:retry.index]...)
		message := session.UserMessage{
			MessageID: retry.messageID, Text: retry.content, CreatedAt: time.Now().UTC(), Attachments: retry.attachments,
		}
		if len(retry.references) > 0 {
			message.References = append([]session.Reference(nil), retry.references...)
		}
		m.Messages = append(m.Messages, message)
		m.Model, m.PermissionMode = spec.model, spec.mode
		return nil
	})
	return err
}

func (r *Runner) stopRun(ar *activeRun) {
	ar.stopping.Store(true)
	ar.stateMu.Lock()
	ids := make([]string, 0, len(ar.pending))
	for id := range ar.pending {
		ids = append(ids, id)
	}
	titleCancel := ar.titleCancel
	ar.stateMu.Unlock()
	if titleCancel != nil {
		titleCancel()
	}
	for _, id := range ids {
		r.resolveEscalation(ar, id, resolveOptions{granted: false})
	}
	r.cancelPendingQuestions(ar)
	ar.cancel()
}

func (ar *activeRun) setRun(run *runlog.Run) {
	ar.stateMu.Lock()
	ar.run = run
	ar.stateMu.Unlock()
}

// noteEntryID 记录事件循环里最新见到的 Pi 条目 id,作 run 失败记录的锚点
// (与 recordEscalation 的锚点同源);quotaLoop 等其他 goroutine 会并发读,
// 由 stateMu 保护。
func (ar *activeRun) noteEntryID(id string) {
	ar.stateMu.Lock()
	ar.lastEntryID = id
	ar.stateMu.Unlock()
}

func (ar *activeRun) finishedRun() *runlog.Run {
	ar.stateMu.Lock()
	defer ar.stateMu.Unlock()
	return ar.run
}

func (r *Runner) publish(ar *activeRun, event translate.Event) {
	ar.stateMu.Lock()
	if ar.singleTerminal && ar.terminal != "" {
		ar.stateMu.Unlock()
		return
	}
	switch event.Type {
	case "agent_done", "agent_error", "agent_cancelled":
		ar.terminal = event.Type
		if event.Type == "agent_error" {
			ar.lastError, _ = event.Data["message"].(string)
		}
	}
	run := ar.run
	ar.stateMu.Unlock()
	if run != nil {
		run.Publish(event)
	}
}

// publishFinal 用于限额与排队产生的终态。Abort 后 Pi 还会补发
// agent_cancelled,这里收口为恰好一个终态,并记录结束原因供日志使用。
func (r *Runner) publishFinal(ar *activeRun, event translate.Event, reason string) bool {
	if ar.stopping.Load() && event.Type == "agent_error" {
		event = translate.Event{Type: "agent_cancelled", Data: map[string]any{"aborted": true}}
		reason = ""
	}
	ar.stateMu.Lock()
	if ar.terminal != "" {
		ar.stateMu.Unlock()
		return false
	}
	ar.terminal = event.Type
	ar.singleTerminal = true
	ar.endReason = reason
	if event.Type == "agent_error" {
		ar.lastError, _ = event.Data["message"].(string)
	}
	run := ar.run
	ar.stateMu.Unlock()
	if run != nil {
		run.Publish(event)
	}
	return true
}

// recordRunError 在 cleanup 按 run 的最终终态落盘失败信息:中止流程可能先
// 发 agent_error、再以 agent_cancelled 收口,后者不算错误。锚点优先取事件
// 循环里最后见到的 Pi 条目 id,为空时从 Pi 会话文件末尾兜底(Pi 1.4.0 的
// entry_appended 不覆盖普通消息条目);cleanup 只执行一次,落盘失败只记日志。
func (r *Runner) recordRunError(ar *activeRun) {
	ar.stateMu.Lock()
	if ar.terminal != "agent_error" {
		ar.stateMu.Unlock()
		return
	}
	message := ar.lastError
	anchor := ar.lastEntryID
	ar.stateMu.Unlock()
	if anchor == "" {
		anchor = r.anchorFromSessionFile(ar)
	}
	if _, err := r.cfg.Store.Update(ar.sessionID, func(meta *session.Meta) error {
		meta.AppendRunError(session.RunError{
			RunID: ar.runID, Message: message, AfterEntryID: anchor, At: time.Now().UTC(),
		})
		return nil
	}); err != nil {
		r.logger.Error("记录 run 失败信息失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
	}
}

// notifyWaitChanged 通知事件循环未决审批/提问集合已变化。
func (ar *activeRun) notifyWaitChanged() {
	select {
	case ar.waitChanged <- struct{}{}:
	default:
	}
}

// waiting 报告是否存在等待用户的审批或提问,此时运行总时限暂停计时。
// auto 分类中的审批由模型自动决定,不暂停运行总时限。
func (ar *activeRun) waiting() bool {
	ar.stateMu.Lock()
	defer ar.stateMu.Unlock()
	for id := range ar.pending {
		if _, reviewing := ar.autoReviewing[id]; !reviewing {
			return true
		}
	}
	return len(ar.pendingQuestions) > 0
}
