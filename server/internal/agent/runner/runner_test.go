package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"xlyra/server/internal/agent/runlog"
	"xlyra/server/internal/agent/sandbox"
	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agent/translate"
)

const fakeToken = "runner-test-secret-token"

// TestMain 在测试框架解析参数之前分流:os.Args 里带 --fake-pi 时,本进程就是
// 假 Pi(runner 只传 piconfig.Env 的 8 个环境键,配置只能走命令行)。
func TestMain(m *testing.M) {
	sandbox.MaybeRunHelper()
	for _, arg := range os.Args {
		if arg == "--fake-pi" {
			fakePiMain()
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

type fakePi struct {
	recordPath       string
	fixture          string
	sessionSrc       string
	sessionDir       string
	sessionID        string
	forkID           string
	probePath        string
	delay            time.Duration
	exitAfter        int
	flood            bool
	workspaceBytes   int
	workspacePath    string
	workspaceContent string
	sandboxResult    string
	sandboxStarted   string
	sandboxSecret    string
	sandboxOutside   string
	sandboxGateway   string
	sandboxOther     string
	sandboxCommand   string
	writeMu          sync.Mutex
	grow             bool // 增量写会话文件:session_append 先落盘再继续(模拟真实 Pi 工具执行前持久化 assistant 条目)
	growTurn         int  // 当前轮次(懒计算:文件里已有的 u- 条目数 + 1),仅 stream goroutine 访问
	respMu           sync.Mutex
	respWait         map[string]chan struct{} // grow 模式下等待审批应答的流
	abort            chan struct{}
	abortOnce        sync.Once
}

func fakePiMain() {
	recordPath := flagValue("--fake-record")
	// 相对路径按假 Pi 自己的 HOME 解析,使不同隔离会话各写各的
	// pi-agent/;绝对路径保持既有测试的行为。
	if recordPath != "" && !filepath.IsAbs(recordPath) {
		recordPath = filepath.Join(os.Getenv("HOME"), recordPath)
	}
	pi := &fakePi{
		recordPath: recordPath,
		fixture:    flagValue("--fake-fixture"),
		sessionSrc: flagValue("--fake-session-src"),
		sessionDir: flagValue("--session-dir"),
		sessionID:  "pi-sess-1",
		forkID:     "pi-sess-2",
		probePath:  flagValue("--fake-probe-path"),
		abort:      make(chan struct{}),
		respWait:   map[string]chan struct{}{},
	}
	if value := flagValue("--fake-delay-ms"); value != "" {
		ms, _ := strconv.Atoi(value)
		pi.delay = time.Duration(ms) * time.Millisecond
	}
	pi.exitAfter, _ = strconv.Atoi(flagValue("--fake-exit-after"))
	pi.flood = flagValue("--fake-flood") != ""
	pi.workspaceBytes, _ = strconv.Atoi(flagValue("--fake-workspace-bytes"))
	pi.workspacePath = flagValue("--fake-workspace-file")
	pi.workspaceContent = flagValue("--fake-workspace-content")
	pi.sandboxResult = flagValue("--fake-sandbox-result")
	pi.sandboxStarted = flagValue("--fake-sandbox-started")
	pi.sandboxSecret = flagValue("--fake-sandbox-secret")
	pi.sandboxOutside = flagValue("--fake-sandbox-outside")
	pi.sandboxGateway = flagValue("--fake-sandbox-gateway")
	pi.sandboxOther = flagValue("--fake-sandbox-other")
	pi.sandboxCommand = flagValue("--fake-sandbox-command")
	pi.grow = flagValue("--fake-grow") != ""
	// 启动即记录自身身份(集成测试断言 Pi 真的以会话身份运行)。
	pi.record(map[string]any{"kind": "start", "args": os.Args, "env": os.Environ(), "token": os.Getenv("XLYRA_TOKEN"), "uid": os.Getuid(), "gid": os.Getgid()})
	if pi.probePath != "" {
		entries, err := os.ReadDir(pi.probePath)
		probe := map[string]any{"kind": "probe", "path": pi.probePath, "ok": err == nil}
		if err != nil {
			probe["error"] = err.Error()
			probe["permission_denied"] = errors.Is(err, os.ErrPermission)
		} else {
			probe["entries"] = len(entries)
		}
		pi.record(probe)
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var command map[string]any
			if json.Unmarshal(bytes.TrimSpace(line), &command) == nil {
				pi.handle(command)
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *fakePi) handle(command map[string]any) {
	p.record(map[string]any{"kind": "command", "command": command})
	id, _ := command["id"].(string)
	typ, _ := command["type"].(string)
	respond := func(data map[string]any) {
		response := map[string]any{"type": "response", "id": id, "command": typ, "success": true}
		if data != nil {
			response["data"] = data
		}
		p.write(response)
	}
	switch typ {
	case "get_state":
		respond(map[string]any{"sessionId": p.sessionID, "isStreaming": false})
	case "set_thinking_level":
		respond(nil)
	case "prompt":
		if p.workspaceBytes > 0 {
			p.writeWorkspaceFile()
		}
		if p.workspacePath != "" {
			p.writeNamedWorkspaceFile()
		}
		respond(nil)
		if p.flood {
			go p.floodOutput()
		} else if p.sandboxResult != "" {
			go func() {
				p.runSandboxProbes()
				p.stream()
			}()
		} else {
			go p.stream()
		}
	case "abort":
		respond(nil)
		p.abortOnce.Do(func() { close(p.abort) })
		p.writeSession()
		p.write(map[string]any{"type": "agent_settled", "aborted": true})
	case "compact":
		p.writeSession()
		respond(map[string]any{
			"summary": "测试压缩摘要", "tokensBefore": 1200, "estimatedTokensAfter": 300,
			"compactionId": "compact-1", "firstKeptEntryId": "u102",
		})
		p.write(map[string]any{"type": "compaction_start", "reason": "manual"})
		p.write(map[string]any{"type": "compaction_end", "reason": "manual", "result": map[string]any{
			"summary": "测试压缩摘要", "tokensBefore": 1200, "estimatedTokensAfter": 300,
		}})
	case "get_fork_messages":
		respond(map[string]any{"messages": []map[string]any{
			{"entryId": "u101", "text": "RETRY-A 第一问"},
			{"entryId": "u102", "text": "RETRY-B 改写后的重发"},
		}})
	case "fork":
		p.sessionID = p.forkID
		respond(map[string]any{"text": "RETRY-A 第一问", "cancelled": false})
	case "extension_ui_response":
		// 该记录由上面的 command 日志保存,协议上没有 response。
		p.signalUIResponse(id)
	default:
		respond(nil)
	}
}

func (p *fakePi) stream() {
	if p.fixture == "" {
		p.write(map[string]any{"type": "text_delta", "delta": "完成"})
		p.write(map[string]any{"type": "agent_settled"})
		return
	}
	file, err := os.Open(p.fixture)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	lines := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			select {
			case <-p.abort:
				return
			default:
			}
			if p.delay > 0 {
				time.Sleep(p.delay)
			}
			select {
			case <-p.abort:
				return
			default:
			}
			if eventType(line) == "agent_settled" {
				p.writeSession()
			}
			var event map[string]any
			if json.Unmarshal(line, &event) == nil {
				kind, _ := event["type"].(string)
				if kind == "session_append" {
					// 假 Pi 专用指令:把条目追加进会话文件再继续,不转发给
					// runner;审批请求前面的 session_append 模拟真实 Pi 在
					// 工具执行前落盘 assistant 条目。
					if entry, _ := event["entry"].(map[string]any); entry != nil {
						p.appendSessionEntry(entry)
					}
				} else if !p.grow {
					p.write(event)
				} else if kind == "extension_ui_request" {
					// 真实 Pi 在审批未决时不会执行工具、更不会落盘 toolResult:
					// 先展开轮次占位、登记等待者再输出事件,等到
					// extension_ui_response(或中止)才继续流式输出;登记与输出
					// 的 id 必须都是展开后的值。
					event = substituteTurnTokens(event, p.turnNumber())
					id, _ := event["id"].(string)
					if id != "" {
						p.registerUIResponse(id)
						p.write(event)
						p.waitUIResponse(id)
					} else {
						p.write(event)
					}
				} else {
					p.write(substituteTurnTokens(event, p.turnNumber()))
				}
			}
			lines++
			if p.exitAfter > 0 && lines >= p.exitAfter {
				fmt.Fprintln(os.Stderr, "fake pi crash marker")
				os.Exit(3)
			}
		}
		if readErr != nil {
			return
		}
	}
}

func (p *fakePi) floodOutput() {
	payload := strings.Repeat("x", 1024)
	for {
		select {
		case <-p.abort:
			return
		default:
			p.write(map[string]any{"type": "ignored", "data": payload})
		}
	}
}

// writeWorkspaceFile 把配额填充文件写到 Pi 私有目录(cwd 是 workspace,而
// 共享工作区不计入会话配额;../pi 是会话的 Pi 目录)。
func (p *fakePi) writeWorkspaceFile() {
	data := bytes.Repeat([]byte{'q'}, p.workspaceBytes)
	if err := os.WriteFile(filepath.Join("..", "pi", "quota-fill.bin"), data, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func (p *fakePi) writeNamedWorkspaceFile() {
	path := strings.ReplaceAll(p.workspacePath, "$PID", strconv.Itoa(os.Getpid()))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	if err := os.WriteFile(path, []byte(p.workspaceContent), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func (p *fakePi) write(value map[string]any) {
	encoded, _ := json.Marshal(value)
	p.writeMu.Lock()
	_, _ = os.Stdout.Write(append(encoded, '\n'))
	p.writeMu.Unlock()
}

func (p *fakePi) record(value map[string]any) {
	encoded, _ := json.Marshal(value)
	file, err := os.OpenFile(p.recordPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	_, _ = file.Write(append(encoded, '\n'))
	_ = file.Close()
}

func (p *fakePi) writeSession() {
	// 增量模式下会话文件由 session_append 选条目拼装,不能被整体覆盖。
	if p.grow || p.sessionSrc == "" || p.sessionDir == "" {
		return
	}
	data, err := os.ReadFile(p.sessionSrc)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	name := "2026-01-01T00-00-00-000Z_" + p.sessionID + ".jsonl"
	if err := os.WriteFile(filepath.Join(p.sessionDir, name), data, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

// registerUIResponse 登记 extension_ui_request 的等待者;必须先于事件输出,
// 避免应答先到而错过、流永久阻塞。
func (p *fakePi) registerUIResponse(id string) {
	ch := make(chan struct{})
	p.respMu.Lock()
	p.respWait[id] = ch
	p.respMu.Unlock()
}

// waitUIResponse 阻塞等审批应答或中止;应答由 signalUIResponse 关闭并移除
// 等待者,因此等待中不会错过信号。
func (p *fakePi) waitUIResponse(id string) {
	p.respMu.Lock()
	ch := p.respWait[id]
	p.respMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case <-ch:
	case <-p.abort:
	}
}

// signalUIResponse 唤醒等待该审批应答的流(grow 模式):关闭并移除等待者,
// 迟到的重复应答无副作用。
func (p *fakePi) signalUIResponse(id string) {
	p.respMu.Lock()
	ch := p.respWait[id]
	delete(p.respWait, id)
	p.respMu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// sessionFilePath 返回假 Pi 的会话文件路径(与 writeSession 同一命名)。
func (p *fakePi) sessionFilePath() string {
	return filepath.Join(p.sessionDir, "2026-01-01T00-00-00-000Z_"+p.sessionID+".jsonl")
}

// turnNumber 返回当前轮次:会话文件里已有的 u- 条目数 + 1。每轮 run 一个
// 假 Pi 进程,靠文件本身让下一个进程接着上一轮的编号,使各轮条目 id 不同。
func (p *fakePi) turnNumber() int {
	if p.growTurn == 0 {
		p.growTurn = countSessionTurns(p.sessionFilePath())
	}
	return p.growTurn
}

func countSessionTurns(path string) int {
	file, err := os.Open(path)
	if err != nil {
		return 1
	}
	defer file.Close()
	turns := 0
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var entry struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) == nil && strings.HasPrefix(entry.ID, "u-") {
			turns++
		}
	}
	return turns + 1
}

// appendSessionEntry 把 session_append 的条目模板落盘:文件不存在时先写会话
// 头与根条目(f003);模板里的 $N 替换为当前轮次、$PREV 替换为上一轮
// assistant 条目 id(首轮为根),使各轮条目 id 两两不同且父子链完整。
// 先落盘再回到事件流,保证 runner 收到后续事件时文件已可见。
func (p *fakePi) appendSessionEntry(entry map[string]any) {
	if p.sessionDir == "" {
		return
	}
	path := p.sessionFilePath()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		header := `{"type":"session","version":3,"id":"` + p.sessionID + `","timestamp":"2026-10-29T00-00-00-000Z","cwd":"/w"}` + "\n" +
			`{"type":"model_change","id":"f003","parentId":null,"timestamp":"2026-10-29T00-00-00-001Z","provider":"xlyra","modelId":"mock-1"}` + "\n"
		if err := os.WriteFile(path, []byte(header), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
	}
	encoded, err := json.Marshal(substituteTurnTokens(entry, p.turnNumber()))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	defer file.Close()
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

// substituteTurnTokens 展开事件/条目模板里的轮次占位:$N 为当前轮次,
// $PREV 为上一轮 assistant 条目 id(首轮为根条目 f003)。仅在 grow 模式
// (approval-anchor 等夹具)下使用,普通夹具不做替换。
func substituteTurnTokens(event map[string]any, turn int) map[string]any {
	prev := "f003"
	if turn > 1 {
		prev = "a-" + strconv.Itoa(turn-1)
	}
	var walk func(value any) any
	walk = func(value any) any {
		switch typed := value.(type) {
		case string:
			text := strings.ReplaceAll(typed, "$N", strconv.Itoa(turn))
			return strings.ReplaceAll(text, "$PREV", prev)
		case map[string]any:
			for key, item := range typed {
				typed[key] = walk(item)
			}
			return typed
		case []any:
			for i, item := range typed {
				typed[i] = walk(item)
			}
			return typed
		default:
			return value
		}
	}
	return walk(event).(map[string]any)
}

func flagValue(name string) string {
	for i, arg := range os.Args {
		if arg == name && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
		if value, ok := strings.CutPrefix(arg, name+"="); ok {
			return value
		}
	}
	return ""
}

// fakeIssuer 每次签发都返回新令牌(fakeToken、fakeToken-2、…),供续期测试
// 断言令牌文件确实被轮换。
type fakeIssuer struct {
	mu     sync.Mutex
	issued []string
	ended  []string
}

func (f *fakeIssuer) IssueRunToken(_ context.Context, sessionID, runID, model string) (string, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issued = append(f.issued, fmt.Sprintf("%s-%d", fakeToken, len(f.issued)+1))
	return f.issued[len(f.issued)-1], time.Now().Add(30 * time.Minute), nil
}

func (f *fakeIssuer) EndRun(_ context.Context, sessionID, runID, model string) error {
	f.mu.Lock()
	f.ended = append(f.ended, runID)
	f.mu.Unlock()
	return nil
}

func (f *fakeIssuer) endedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ended)
}

func (f *fakeIssuer) issueCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.issued)
}

func (f *fakeIssuer) issuedTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.issued...)
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type testEnv struct {
	runner    *Runner
	store     *session.Store
	runs      *runlog.Registry
	issuer    *fakeIssuer
	record    string
	logs      *syncBuffer
	extension string // 假 xLyra 扩展文件路径,断言启动参数用
	cleanup   func()
}

type envOption func(*Config, *[]string)

func newTestEnv(t *testing.T, fixture string, options ...envOption) *testEnv {
	t.Helper()
	root := t.TempDir()
	store := session.NewStore(filepath.Join(root, "sessions"))
	runs := runlog.NewRegistry()
	issuer := &fakeIssuer{}
	logs := &syncBuffer{}
	record := filepath.Join(root, "fake-pi-record.jsonl")
	fixturePath, err := filepath.Abs("../testdata/pi-events/" + fixture + ".jsonl")
	if err != nil {
		t.Fatal(err)
	}
	sessionSource, err := filepath.Abs("../session/testdata/fork.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	// 默认工具集含 write/edit,Health 要求扩展文件存在;逃一个占位 .ts。
	extension := filepath.Join(root, "extension", "xlyra.ts")
	if err := os.MkdirAll(filepath.Dir(extension), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extension, []byte("// fake xlyra extension\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{
		"--fake-pi",
		"--fake-record", record, "--fake-fixture", fixturePath, "--fake-session-src", sessionSource,
	}
	cfg := Config{
		Store: store, Runs: runs, Tokens: issuer,
		Protocol: func(context.Context, string) (string, error) {
			return "openai-chat-completions", nil
		},
		GatewayBaseURL: "http://127.0.0.1:18080", Command: os.Args[0], PathEnv: os.Getenv("PATH"),
		ExtensionPath: extension,
		RunTimeout:    10 * time.Second, AbortGrace: 500 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	for _, option := range options {
		option(&cfg, &args)
	}
	cfg.Args = args
	runner, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	env := &testEnv{runner: runner, store: store, runs: runs, issuer: issuer, record: record, logs: logs, extension: extension}
	env.cleanup = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Shutdown(ctx)
	}
	t.Cleanup(env.cleanup)
	return env
}

func withDelay(ms int) envOption {
	return func(_ *Config, args *[]string) {
		*args = append(*args, "--fake-delay-ms", strconv.Itoa(ms))
	}
}

// withLocalFixture 使用 runner/testdata 下的夹具;translate 的回放测试只消费
// agent/testdata/pi-events,runner 专用场景不应挤进它的固定样本集。
func withLocalFixture(name string) envOption {
	return func(_ *Config, args *[]string) {
		path, err := filepath.Abs(filepath.Join("testdata", name))
		if err != nil {
			panic(err)
		}
		for i, arg := range *args {
			if arg == "--fake-fixture" && i+1 < len(*args) {
				(*args)[i+1] = path
				return
			}
		}
		panic("缺少 --fake-fixture 参数")
	}
}

// withGrowSession 让假 Pi 以增量模式写会话文件:session_append 事件先把条目
// 落盘再继续(带 $N/$PREV 轮次占位替换),模拟真实 Pi 在工具执行前持久化
// assistant 条目,供审批/失败记录的锚点测试使用。
func withGrowSession() envOption {
	return func(_ *Config, args *[]string) {
		*args = append(*args, "--fake-grow")
	}
}

func withRunTimeout(timeout time.Duration) envOption {
	return func(cfg *Config, _ *[]string) { cfg.RunTimeout = timeout }
}

func withTokenRenew(interval time.Duration) envOption {
	return func(cfg *Config, _ *[]string) { cfg.TokenRenewInterval = interval }
}

func withMaxConcurrent(max int) envOption {
	return func(cfg *Config, _ *[]string) { cfg.MaxConcurrent = max }
}

func withQueueMax(max int) envOption {
	return func(cfg *Config, _ *[]string) { cfg.QueueMax = max }
}

func withQueueTimeout(timeout time.Duration) envOption {
	return func(cfg *Config, _ *[]string) { cfg.QueueTimeout = timeout }
}

func withExitAfter(lines int) envOption {
	return func(_ *Config, args *[]string) {
		*args = append(*args, "--fake-exit-after", strconv.Itoa(lines))
	}
}

func withIsolation() envOption {
	return func(cfg *Config, _ *[]string) { cfg.Isolation = &IsolationConfig{} }
}

func withoutSessionFile() envOption {
	return func(_ *Config, args *[]string) {
		filtered := (*args)[:0]
		for i := 0; i < len(*args); i++ {
			if (*args)[i] == "--fake-session-src" {
				i++
				continue
			}
			filtered = append(filtered, (*args)[i])
		}
		*args = filtered
	}
}

type collector struct {
	mu     sync.Mutex
	events []translate.Event
	done   chan struct{}
}

func collectRun(t *testing.T, runs *runlog.Registry, sessionID string) *collector {
	t.Helper()
	run := runs.Current(sessionID)
	if run == nil {
		t.Fatalf("会话 %s 没有 run", sessionID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	result := &collector{done: make(chan struct{})}
	events := run.Subscribe(ctx)
	go func() {
		defer close(result.done)
		for event := range events {
			result.mu.Lock()
			result.events = append(result.events, event.Event)
			result.mu.Unlock()
		}
	}()
	return result
}

func (c *collector) snapshot() []translate.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]translate.Event(nil), c.events...)
}

func (c *collector) wait(t *testing.T) []translate.Event {
	t.Helper()
	select {
	case <-c.done:
		return c.snapshot()
	case <-time.After(5 * time.Second):
		t.Fatal("等待 run 收尾超时")
		return nil
	}
}

func waitForEvent(t *testing.T, c *collector, eventType string) translate.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, event := range c.snapshot() {
			if event.Type == eventType {
				return event
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待事件 %s 超时;已有事件=%v", eventType, eventTypes(c.snapshot()))
	return translate.Event{}
}

func eventTypes(events []translate.Event) []string {
	result := make([]string, len(events))
	for i, event := range events {
		result[i] = event.Type
	}
	return result
}

func goldenEvents(t *testing.T, name string) []translate.Event {
	t.Helper()
	data, err := os.ReadFile("../translate/testdata/translated/" + name + ".golden.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var result []translate.Event
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var event translate.Event
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		result = append(result, event)
	}
	return result
}

func sendBasic(t *testing.T, env *testEnv) SendResult {
	t.Helper()
	result, err := env.runner.Send(context.Background(), SendInput{
		Content: "你好", Model: "mock-1", ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	return result
}

func TestSendBasicLifecycle(t *testing.T) {
	env := newTestEnv(t, "basic", withMaxConcurrent(1))
	result := sendBasic(t, env)
	events := collectRun(t, env.runs, result.SessionID).wait(t)
	if want := goldenEvents(t, "basic"); !reflect.DeepEqual(events, want) {
		t.Fatalf("事件与 golden 不符:\n got=%v\nwant=%v", eventTypes(events), eventTypes(want))
	}
	meta, err := env.store.Get(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "你好" || meta.Model != "mock-1" || meta.PermissionMode != "ask" || meta.PiSessionID != "pi-sess-1" {
		t.Errorf("meta 不符: %+v", meta)
	}
	if len(meta.Messages) != 1 || meta.Messages[0].MessageID != result.MessageID || meta.Messages[0].EntryID != "u101" {
		t.Errorf("消息绑定不符: %+v", meta.Messages)
	}
	if env.issuer.endedCount() != 1 {
		t.Errorf("EndRun 调用数 = %d", env.issuer.endedCount())
	}
	entries, err := env.store.BuildTranscript(result.SessionID)
	if err != nil || len(entries) == 0 {
		t.Errorf("transcript 构建失败: entries=%d err=%v", len(entries), err)
	}
	// 名额已在 runlog 关闭前释放,MaxConcurrent=1 仍能启动另一会话。
	second, err := env.runner.Send(context.Background(), SendInput{Content: "第二次", Model: "mock-1"})
	if err != nil {
		t.Fatalf("名额未释放: %v", err)
	}
	collectRun(t, env.runs, second.SessionID).wait(t)
}

func TestToolEchoFixture(t *testing.T) {
	env := newTestEnv(t, "tool-echo")
	result := sendBasic(t, env)
	events := collectRun(t, env.runs, result.SessionID).wait(t)
	if want := goldenEvents(t, "tool-echo"); !reflect.DeepEqual(events, want) {
		t.Fatalf("事件与 golden 不符: got=%v want=%v", eventTypes(events), eventTypes(want))
	}
	terminals := 0
	for _, event := range events {
		if event.Type == "agent_done" || event.Type == "agent_error" || event.Type == "agent_cancelled" {
			terminals++
		}
	}
	if terminals != 1 || events[len(events)-1].Type != "agent_done" {
		t.Errorf("终态不唯一: %v", eventTypes(events))
	}
}

func TestErrorFixture(t *testing.T) {
	env := newTestEnv(t, "err-500")
	result := sendBasic(t, env)
	events := collectRun(t, env.runs, result.SessionID).wait(t)
	if len(events) == 0 || events[len(events)-1].Type != "agent_error" {
		t.Fatalf("终态不是 agent_error: %v", eventTypes(events))
	}
	for _, event := range events {
		if event.Type == "agent_done" {
			t.Fatal("错误 run 不应有 agent_done")
		}
		if event.Type == "agent_error" {
			if message, _ := event.Data["message"].(string); message == "" {
				t.Fatal("agent_error.message 为空")
			}
		}
	}
}

func TestStopThenSendAgain(t *testing.T) {
	env := newTestEnv(t, "basic", withDelay(20))
	result := sendBasic(t, env)
	collected := collectRun(t, env.runs, result.SessionID)
	waitForEvent(t, collected, "text_delta")
	if err := env.runner.Stop(context.Background(), result.SessionID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	events := collected.wait(t)
	if last := events[len(events)-1]; last.Type != "agent_cancelled" {
		t.Fatalf("终态 = %s, want agent_cancelled", last.Type)
	}
	if env.issuer.endedCount() != 1 {
		t.Fatalf("EndRun 调用数 = %d", env.issuer.endedCount())
	}
	again, err := env.runner.Send(context.Background(), SendInput{
		SessionID: result.SessionID, Content: "停止后再发", Model: "mock-1",
	})
	if err != nil {
		t.Fatalf("Stop 后 Send: %v", err)
	}
	collectRun(t, env.runs, again.SessionID).wait(t)
}

func TestApprovalGrant(t *testing.T) {
	for _, granted := range []bool{true, false} {
		name := "approval-allow"
		if !granted {
			name = "approval-deny"
		}
		t.Run(name, func(t *testing.T) {
			env := newTestEnv(t, name, withDelay(5))
			result := sendBasic(t, env)
			collected := collectRun(t, env.runs, result.SessionID)
			escalation := waitForEvent(t, collected, "escalation_request")
			data := escalation.Data["escalation"].(map[string]any)
			escalationID := data["escalation_id"].(string)
			if err := env.runner.Grant(context.Background(), result.SessionID, GrantInput{
				EscalationID: escalationID, Granted: granted, ResolvedPath: "/workspace/a.txt",
			}); err != nil {
				t.Fatalf("Grant: %v", err)
			}
			events := collected.wait(t)
			found := false
			for _, event := range events {
				if event.Type == "permission_result" && event.Data["request_id"] == escalationID && event.Data["allowed"] == granted {
					found = true
				}
			}
			if !found {
				t.Errorf("缺少 permission_result: %v", eventTypes(events))
			}
			meta, _ := env.store.Get(result.SessionID)
			if len(meta.Escalations) != 1 || meta.Escalations[0].Granted == nil || *meta.Escalations[0].Granted != granted {
				t.Fatalf("meta 审批决定不符: %+v", meta.Escalations)
			}
			if meta.Escalations[0].ResolvedPath != "/workspace/a.txt" {
				t.Errorf("ResolvedPath = %q", meta.Escalations[0].ResolvedPath)
			}
			commands := recordedCommands(t, env.record)
			if !hasUIResponse(commands, escalationID, granted) {
				t.Errorf("假 Pi 未收到 extension_ui_response: %v", commands)
			}
			if err := env.runner.Grant(context.Background(), result.SessionID, GrantInput{EscalationID: escalationID, Granted: granted}); !errors.Is(err, ErrNotFound) {
				t.Errorf("重复 Grant 错误 = %v", err)
			}
		})
	}
}

func TestStopRejectsPendingApproval(t *testing.T) {
	env := newTestEnv(t, "approval-abort", withDelay(20))
	result := sendBasic(t, env)
	collected := collectRun(t, env.runs, result.SessionID)
	escalation := waitForEvent(t, collected, "escalation_request")
	escalationID := escalation.Data["escalation"].(map[string]any)["escalation_id"].(string)
	if err := env.runner.Stop(context.Background(), result.SessionID); err != nil {
		t.Fatal(err)
	}
	events := collected.wait(t)
	if events[len(events)-1].Type != "agent_cancelled" {
		t.Fatalf("终态 = %v", eventTypes(events))
	}
	meta, _ := env.store.Get(result.SessionID)
	if len(meta.Escalations) == 0 || meta.Escalations[0].Granted == nil || *meta.Escalations[0].Granted {
		t.Fatalf("未决审批未按拒绝处理: %+v", meta.Escalations)
	}
	if !hasUIResponse(recordedCommands(t, env.record), escalationID, false) {
		t.Error("Stop 未向假 Pi 发送拒绝应答")
	}
}

func TestValidationAndAdmissionErrors(t *testing.T) {
	env := newTestEnv(t, "basic", withDelay(20), withMaxConcurrent(1), withQueueMax(-1))
	if _, err := env.runner.Send(context.Background(), SendInput{Content: "", Model: "mock-1"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("空内容错误 = %v", err)
	}
	if _, err := env.runner.Send(context.Background(), SendInput{Content: "x"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("空模型错误 = %v", err)
	}
	if _, err := env.runner.Send(context.Background(), SendInput{
		Content: "x", Model: "mock-1", Attachments: []Attachment{{
			Name: "a.bin", MimeType: "application/octet-stream", DataURL: "data:application/octet-stream;base64,AA==",
		}},
	}); !errors.Is(err, ErrInvalid) {
		t.Errorf("不支持附件错误 = %v", err)
	}
	if _, err := env.runner.Send(context.Background(), SendInput{
		SessionID: uuid.NewString(), Content: "x", Model: "mock-1",
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在会话错误 = %v", err)
	}

	first := sendBasic(t, env)
	collectRun(t, env.runs, first.SessionID)
	if _, err := env.runner.Send(context.Background(), SendInput{
		SessionID: first.SessionID, Content: "并发", Model: "mock-1",
	}); !errors.Is(err, ErrBusy) {
		t.Errorf("同会话并发错误 = %v", err)
	}
	if _, err := env.runner.Send(context.Background(), SendInput{Content: "另一会话", Model: "mock-1"}); !errors.Is(err, ErrCapacity) {
		t.Errorf("容量错误 = %v", err)
	}
}

func TestUnavailable(t *testing.T) {
	env := newTestEnv(t, "basic")
	broken, err := New(Config{
		Store: env.store, Runs: env.runs, Tokens: env.issuer,
		Protocol:       func(context.Context, string) (string, error) { return "openai-responses", nil },
		GatewayBaseURL: "http://127.0.0.1", Command: filepath.Join(t.TempDir(), "missing-node"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if broken.Health().Available {
		t.Fatal("不存在命令应不可用")
	}
	if _, err := broken.Send(context.Background(), SendInput{Content: "x", Model: "mock-1"}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Send 错误 = %v", err)
	}
}

func TestProcessCrash(t *testing.T) {
	env := newTestEnv(t, "basic", withExitAfter(3), withoutSessionFile())
	result := sendBasic(t, env)
	events := collectRun(t, env.runs, result.SessionID).wait(t)
	var errorsSeen int
	for _, event := range events {
		if event.Type == "agent_error" {
			errorsSeen++
			message, _ := event.Data["message"].(string)
			if !strings.Contains(message, "fake pi crash marker") {
				t.Errorf("错误未包含 stderr 摘要: %q", message)
			}
		}
		if event.Type == "agent_done" || event.Type == "agent_cancelled" {
			t.Errorf("崩溃不应有 %s", event.Type)
		}
	}
	if errorsSeen != 1 {
		t.Fatalf("agent_error 数 = %d; 事件=%v", errorsSeen, eventTypes(events))
	}
	if env.issuer.endedCount() != 1 {
		t.Errorf("崩溃后未 EndRun")
	}
}

func TestRunTimeout(t *testing.T) {
	env := newTestEnv(t, "basic", withDelay(30), withRunTimeout(100*time.Millisecond))
	result := sendBasic(t, env)
	events := collectRun(t, env.runs, result.SessionID).wait(t)
	types := eventTypes(events)
	errorIndex, cancelIndex := -1, -1
	for i, event := range events {
		if event.Type == "agent_error" && event.Data["message"] == "运行超时" {
			errorIndex = i
		}
		if event.Type == "agent_cancelled" {
			cancelIndex = i
		}
	}
	if errorIndex < 0 || cancelIndex < errorIndex {
		t.Fatalf("超时终态不符: %v", types)
	}
	if env.issuer.endedCount() != 1 {
		t.Error("超时后未 EndRun")
	}
}

func TestCompact(t *testing.T) {
	env := newTestEnv(t, "basic", withDelay(20))
	meta, err := env.store.Create("mock-1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	result, err := env.runner.Compact(context.Background(), meta.ID, "mock-1")
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if result.Summary == "" || result.TokensBefore != 1200 || result.TokensAfter != 300 || result.CompactionID != "compact-1" {
		t.Errorf("CompactResult = %+v", result)
	}
	updated, _ := env.store.Get(meta.ID)
	if updated.PiSessionID != "pi-sess-1" {
		t.Errorf("PiSessionID = %q", updated.PiSessionID)
	}
	if env.issuer.endedCount() != 1 {
		t.Error("Compact 未 EndRun")
	}

	running, err := env.runner.Send(context.Background(), SendInput{SessionID: meta.ID, Content: "慢运行", Model: "mock-1"})
	if err != nil {
		t.Fatal(err)
	}
	collectRun(t, env.runs, meta.ID)
	if _, err := env.runner.Compact(context.Background(), meta.ID, "mock-1"); !errors.Is(err, ErrBusy) {
		t.Errorf("运行中 Compact 错误 = %v", err)
	}
	if err := env.runner.Stop(context.Background(), running.SessionID); err != nil {
		t.Fatal(err)
	}
}

func TestRetryFork(t *testing.T) {
	env := newTestEnv(t, "basic")
	first := sendBasic(t, env)
	collectRun(t, env.runs, first.SessionID).wait(t)
	before, _ := env.store.Get(first.SessionID)
	if len(before.Messages) != 1 || before.Messages[0].EntryID != "u101" {
		t.Fatalf("首轮绑定不符: %+v", before.Messages)
	}
	retry, err := env.runner.Retry(context.Background(), RetryInput{
		SessionID: first.SessionID, MessageID: first.MessageID, Content: "重发一下", Model: "mock-1",
	})
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	events := collectRun(t, env.runs, first.SessionID).wait(t)
	if events[len(events)-1].Type != "agent_done" {
		t.Fatalf("重试 run 未正常结束: %v", eventTypes(events))
	}
	after, _ := env.store.Get(first.SessionID)
	if after.PiSessionID != "pi-sess-2" {
		t.Errorf("fork 后 PiSessionID = %q", after.PiSessionID)
	}
	if len(after.Messages) != 1 || after.Messages[0].Text != "重发一下" || after.Messages[0].MessageID != retry.MessageID {
		t.Errorf("重试消息不符: %+v", after.Messages)
	}
	found := false
	for _, record := range readRecords(t, env.record) {
		if record["kind"] != "command" {
			continue
		}
		command := record["command"].(map[string]any)
		if command["type"] == "fork" && command["entryId"] == "u101" {
			found = true
		}
	}
	if !found {
		t.Error("未按绑定的 entryId 调用 fork")
	}
}

func TestEnvironmentAndLogSafety(t *testing.T) {
	env := newTestEnv(t, "basic")
	result := sendBasic(t, env)
	collectRun(t, env.runs, result.SessionID).wait(t)
	// 第二轮携带已保存的 Pi 会话 id,顺带断言恢复参数。
	second, err := env.runner.Send(context.Background(), SendInput{
		SessionID: result.SessionID, Content: "第二轮", Model: "mock-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	collectRun(t, env.runs, second.SessionID).wait(t)
	records := readRecords(t, env.record)
	if len(records) == 0 {
		t.Fatal("没有启动记录")
	}
	start := records[0]
	for _, record := range records {
		if record["kind"] == "start" {
			start = record
		}
	}
	envValues, ok := start["env"].([]any)
	if !ok {
		t.Fatalf("环境记录类型 = %T", start["env"])
	}
	keys := make([]string, 0, len(envValues))
	for _, value := range envValues {
		key, _, _ := strings.Cut(value.(string), "=")
		keys = append(keys, key)
	}
	sort.Strings(keys)
	// 令牌改走文件轮换:环境有 8 个键,不含 XLYRA_TOKEN;运行配置单独注入。
	want := []string{
		"HOME", "PATH", "PI_CODING_AGENT_DIR", "PI_CODING_AGENT_SESSION_DIR",
		"PI_OFFLINE", "PI_SKIP_VERSION_CHECK", "PI_TELEMETRY", "XLYRA_RUN_CONFIG",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("子进程环境键 = %v, want %v", keys, want)
	}
	if token, _ := start["token"].(string); token != "" {
		t.Errorf("令牌不应进入子进程环境: %q", token)
	}
	// 令牌文件:内容为最近一次签发的令牌、权限 0600(同会话第二轮 run 重写)。
	tokenPath := filepath.Join(env.store.Dir(result.SessionID), "pi-agent", "token")
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("读取令牌文件: %v", err)
	}
	tokens := env.issuer.issuedTokens()
	if len(tokens) < 2 || string(data) != tokens[len(tokens)-1] {
		t.Errorf("令牌文件内容 = %q, want 最近签发令牌 %q(共签发 %d 次)", string(data), tokens[len(tokens)-1], len(tokens))
	}
	var runConfig struct {
		Provider struct {
			APIKey string `json:"apiKey"`
		} `json:"provider"`
	}
	if err := json.Unmarshal([]byte(lastRunConfigJSON(t, env.record)), &runConfig); err != nil {
		t.Fatalf("解析运行配置: %v", err)
	}
	if want := "!cat '" + tokenPath + "'"; runConfig.Provider.APIKey != want {
		t.Errorf("provider.apiKey = %q, want %q", runConfig.Provider.APIKey, want)
	}
	if info, err := os.Stat(tokenPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("令牌文件权限异常: %v %v", info.Mode().Perm(), err)
	}
	// 日志与 stderr 摘要里不得出现任何签发过的令牌。
	logText := env.logs.String()
	for _, token := range tokens {
		if strings.Contains(logText, token) {
			t.Fatalf("日志包含令牌 %q", token)
		}
		if strings.Contains(lastRunConfigJSON(t, env.record), token) {
			t.Fatalf("运行配置包含令牌 %q", token)
		}
	}
	args := recordArgs(t, start)
	for _, expected := range []string{"--mode", "--session-dir", "--session-id", "--provider", "--model", "--tools", "-e", "--system-prompt"} {
		if !slices.Contains(args, expected) {
			t.Errorf("启动参数缺少 %s: %v", expected, args)
		}
	}
	if value := flagArg(args, "-e"); value != env.extension {
		t.Errorf("-e 参数 = %q, want %q", value, env.extension)
	}
	prompt := flagArg(args, "--system-prompt")
	if prompt == "" || !strings.Contains(prompt, defaultSystemPrompt) {
		t.Fatalf("--system-prompt 应直接携带系统提示文本: %q", prompt)
	}
	tools := flagArg(args, "--tools")
	for _, expected := range []string{"write", "edit"} {
		if !strings.Contains(","+tools+",", ","+expected+",") {
			t.Errorf("--tools = %q, 缺少 %s", tools, expected)
		}
	}
}

// recordArgs 取出启动记录里的 args([]any → []string)。
func recordArgs(t *testing.T, record map[string]any) []string {
	t.Helper()
	values, ok := record["args"].([]any)
	if !ok {
		t.Fatalf("启动参数记录类型 = %T", record["args"])
	}
	args := make([]string, 0, len(values))
	for _, value := range values {
		text, _ := value.(string)
		args = append(args, text)
	}
	return args
}

// flagArg 返回 args 里紧跟 flag 的值,找不到返回空串。
func flagArg(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// TestTokenRenewal:续期间隔内反复签发并重写令牌文件;run 结束后续期停止。
func TestTokenRenewal(t *testing.T) {
	env := newTestEnv(t, "basic", withDelay(40), withTokenRenew(50*time.Millisecond))
	result := sendBasic(t, env)
	tokenPath := filepath.Join(env.store.Dir(result.SessionID), "pi-agent", "token")

	// run 存活期间:多次续期,且令牌文件内容被换成新令牌。
	deadline := time.Now().Add(5 * time.Second)
	var first string
	for time.Now().Before(deadline) {
		if tokens := env.issuer.issuedTokens(); len(tokens) > 0 {
			first = tokens[0]
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if first == "" {
		t.Fatal("首次签发未发生")
	}
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(tokenPath)
		if err == nil && string(data) != first && env.issuer.issueCount() >= 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := env.issuer.issueCount(); got < 3 {
		t.Fatalf("续期次数不足: 签发 %d 次(含首次)", got)
	}
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == first {
		t.Fatalf("令牌文件未被轮换: %q", string(data))
	}

	collectRun(t, env.runs, result.SessionID).wait(t)
	// runlog 关闭(Finish)先于收尾完成(cancel + renewWG.Wait 在其后):等
	// 签发数稳定且文件内容等于最后一次签发的令牌。
	var tokens []string
	deadline = time.Now().Add(5 * time.Second)
	for {
		now := env.issuer.issuedTokens()
		stable, err := os.ReadFile(tokenPath)
		if err == nil && len(now) > 0 && string(stable) == now[len(now)-1] {
			time.Sleep(150 * time.Millisecond) // ≥3 个续期周期,存活则必再签发
			if after := env.issuer.issuedTokens(); len(after) == len(now) {
				tokens = after
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("收尾未稳定: 已签发 %d 次", env.issuer.issueCount())
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, err = os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != tokens[len(tokens)-1] {
		t.Fatalf("收尾后令牌文件 = %q, want 最后签发令牌 %q", string(data), tokens[len(tokens)-1])
	}
	// run 结束后不再续期(无 goroutine 泄漏)。
	count := len(tokens)
	time.Sleep(200 * time.Millisecond)
	if after := env.issuer.issueCount(); after != count {
		t.Fatalf("run 结束后仍在续期: %d → %d", count, after)
	}
	if strings.Contains(env.logs.String(), tokens[len(tokens)-1]) {
		t.Fatal("日志包含续期后的令牌")
	}
}

// TestXlyraRunConfigEnv:runtime 权限模式取自会话 meta,经环境传递,非 full 一律 ask。
func TestXlyraRunConfigEnv(t *testing.T) {
	env := newTestEnv(t, "basic")
	runtimeJSON := func() string {
		t.Helper()
		var runConfig struct {
			Runtime json.RawMessage `json:"runtime"`
		}
		if err := json.Unmarshal([]byte(lastRunConfigJSON(t, env.record)), &runConfig); err != nil {
			t.Fatal(err)
		}
		return string(runConfig.Runtime)
	}

	full, err := env.runner.Send(context.Background(), SendInput{Content: "full 模式", Model: "mock-1", PermissionMode: "full"})
	if err != nil {
		t.Fatal(err)
	}
	collectRun(t, env.runs, full.SessionID).wait(t)
	if got := runtimeJSON(); got != `{"permissionMode":"full","readRoots":[]}` {
		t.Fatalf("full 模式 runtime = %s", got)
	}

	auto, err := env.runner.Send(context.Background(), SendInput{Content: "auto 模式", Model: "mock-1", PermissionMode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	collectRun(t, env.runs, auto.SessionID).wait(t)
	if got := runtimeJSON(); got != `{"permissionMode":"auto","readRoots":[]}` {
		t.Fatalf("auto 模式 runtime = %s, want auto", got)
	}

	// 默认(未传 PermissionMode)传 ask。
	ask, err := env.runner.Send(context.Background(), SendInput{Content: "默认模式", Model: "mock-1"})
	if err != nil {
		t.Fatal(err)
	}
	collectRun(t, env.runs, ask.SessionID).wait(t)
	if got := runtimeJSON(); got != `{"permissionMode":"ask","readRoots":[]}` {
		t.Fatalf("默认模式 runtime = %s", got)
	}

	// meta 里的非法值也归一到 ask:Compact 直接用 meta.PermissionMode。
	meta, err := env.store.Create("mock-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.Update(meta.ID, func(m *session.Meta) error {
		m.PermissionMode = "bogus"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.runner.Compact(context.Background(), meta.ID, "mock-1"); err != nil {
		t.Fatal(err)
	}
	if got := runtimeJSON(); got != `{"permissionMode":"ask","readRoots":[]}` {
		t.Fatalf("非法模式 runtime = %s", got)
	}

	// 归一化函数的边界:full/auto 原样保留,大小写敏感,其余一律 ask。
	for mode, want := range map[string]string{"": "ask", "full": "full", "auto": "auto", "Full": "ask", "ask": "ask", "bogus": "ask"} {
		if got := permissionModeValue(mode); got != want {
			t.Errorf("permissionModeValue(%q) = %q, want %q", mode, got, want)
		}
	}
}

// TestImageGenerationRunConfig:生图配置逐 run 现取,白名单、提示与令牌路径同步切换。
func TestImageGenerationRunConfig(t *testing.T) {
	modelCount := 0
	env := newTestEnv(t, "basic", func(cfg *Config, _ *[]string) {
		cfg.ImageModels = func() int { return modelCount }
	})
	first, err := env.runner.Send(context.Background(), SendInput{Content: "未配置生图", Model: "mock-1"})
	if err != nil {
		t.Fatal(err)
	}
	collectRun(t, env.runs, first.SessionID).wait(t)
	agentDir := filepath.Join(env.store.Dir(first.SessionID), "pi-agent")
	data := lastRunConfigJSON(t, env.record)
	if strings.Contains(data, "imageGeneration") {
		t.Fatalf("未配置生图时运行配置 = %s", data)
	}
	records := readRecords(t, env.record)
	args := recordArgs(t, records[0])
	if strings.Contains(flagArg(args, "--tools"), "image_generation") {
		t.Fatalf("未配置生图时白名单错误: %v", args)
	}
	if strings.Contains(flagArg(args, "--system-prompt"), "image_generation") {
		t.Fatal("未配置生图时系统提示不应包含工具说明")
	}

	modelCount = 1
	second, err := env.runner.Send(context.Background(), SendInput{SessionID: first.SessionID, Content: "配置后生图", Model: "mock-1"})
	if err != nil {
		t.Fatal(err)
	}
	collectRun(t, env.runs, second.SessionID).wait(t)
	data = lastRunConfigJSON(t, env.record)
	var runConfig struct {
		Runtime struct {
			ImageGeneration struct {
				Enabled        bool   `json:"enabled"`
				Endpoint       string `json:"endpoint"`
				TokenFile      string `json:"tokenFile"`
				MaxBytes       int    `json:"maxBytes"`
				TimeoutSeconds int    `json:"timeoutSeconds"`
			} `json:"imageGeneration"`
		} `json:"runtime"`
	}
	if err := json.Unmarshal([]byte(data), &runConfig); err != nil {
		t.Fatal(err)
	}
	image := runConfig.Runtime.ImageGeneration
	if !image.Enabled || image.Endpoint != "http://127.0.0.1:18080/internal/agent-llm/images" || image.TokenFile != filepath.Join(agentDir, "token") || image.MaxBytes != 33554432 || image.TimeoutSeconds != 660 {
		t.Fatalf("第二个 run 生图配置错误: %+v", image)
	}
	records = readRecords(t, env.record)
	var starts []map[string]any
	for _, record := range records {
		if record["kind"] == "start" {
			starts = append(starts, record)
		}
	}
	args = recordArgs(t, starts[len(starts)-1])
	if !strings.Contains(","+flagArg(args, "--tools")+",", ",image_generation,") {
		t.Fatalf("配置生图后白名单错误: %v", args)
	}
	if !strings.Contains(flagArg(args, "--system-prompt"), "image_generation") {
		t.Fatal("配置生图后系统提示缺少工具说明")
	}
}

// TestHealthRequiresExtensionForWriteTools:工具含 write/edit 时扩展缺失即
// fail-closed;只读工具不受影响。
func TestHealthRequiresExtensionForWriteTools(t *testing.T) {
	env := newTestEnv(t, "basic")
	newCfg := func() Config {
		return Config{
			Store: env.store, Runs: env.runs, Tokens: env.issuer,
			Protocol:       func(context.Context, string) (string, error) { return "openai-responses", nil },
			GatewayBaseURL: "http://127.0.0.1", Command: os.Args[0], Args: []string{"--fake-pi"},
			PathEnv: os.Getenv("PATH"),
		}
	}

	// 默认工具集含 write/edit,未配置扩展 → 不可用。
	missing, err := New(newCfg())
	if err != nil {
		t.Fatal(err)
	}
	if health := missing.Health(); health.Available || !strings.Contains(health.Reason, "扩展") {
		t.Fatalf("缺扩展时 Health = %+v", health)
	}
	if _, err := missing.Send(context.Background(), SendInput{Content: "x", Model: "mock-1"}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("缺扩展 Send 错误 = %v", err)
	}

	// 指向不存在的文件同样不可用。
	broken := newCfg()
	broken.ExtensionPath = filepath.Join(t.TempDir(), "missing.ts")
	missingFile, err := New(broken)
	if err != nil {
		t.Fatal(err)
	}
	if health := missingFile.Health(); health.Available || !strings.Contains(health.Reason, "扩展") {
		t.Fatalf("扩展文件不存在时 Health = %+v", health)
	}

	// 只读工具无写类风险,不强制扩展。
	readOnly := newCfg()
	readOnly.Tools = []string{"read", "ls"}
	clean, err := New(readOnly)
	if err != nil {
		t.Fatal(err)
	}
	if health := clean.Health(); !health.Available {
		t.Fatalf("只读工具不应要求扩展: %+v", health)
	}
}

func TestShutdownAbortsAll(t *testing.T) {
	env := newTestEnv(t, "basic", withDelay(30), withMaxConcurrent(2))
	first := sendBasic(t, env)
	second, err := env.runner.Send(context.Background(), SendInput{Content: "第二个会话", Model: "mock-1"})
	if err != nil {
		t.Fatal(err)
	}
	left := collectRun(t, env.runs, first.SessionID)
	right := collectRun(t, env.runs, second.SessionID)
	waitForEvent(t, left, "text_delta")
	waitForEvent(t, right, "text_delta")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := env.runner.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if events := left.wait(t); events[len(events)-1].Type != "agent_cancelled" {
		t.Errorf("第一个终态 = %v", eventTypes(events))
	}
	if events := right.wait(t); events[len(events)-1].Type != "agent_cancelled" {
		t.Errorf("第二个终态 = %v", eventTypes(events))
	}
	if env.runs.Running(first.SessionID) || env.runs.Running(second.SessionID) {
		t.Error("Shutdown 后仍有运行中的 run")
	}
	if _, err := env.runner.Send(context.Background(), SendInput{Content: "关闭后", Model: "mock-1"}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Shutdown 后 Send 错误 = %v", err)
	}
}

// TestIsolationNotRootFailsClosed:开启隔离但当前非 root 时,
// PrepareSession 返回 ErrNotRoot,run 必须以 agent_error 收尾且 Pi 根本没
// 被启动;槽位已写进 meta(身份分配先于目录准备)。root 环境跳过,由容器
// 集成测试(isolation_integration_test.go)覆盖正向路径。
func TestIsolationNotRootFailsClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 下的正向行为由 RUNNER_ISOLATION_INTEGRATION 集成测试覆盖")
	}
	env := newTestEnv(t, "basic", withIsolation())
	result := sendBasic(t, env)
	events := collectRun(t, env.runs, result.SessionID).wait(t)
	if len(events) == 0 || events[len(events)-1].Type != "agent_error" {
		t.Fatalf("终态应为 agent_error: %v", eventTypes(events))
	}
	message, _ := events[len(events)-1].Data["message"].(string)
	if !strings.Contains(message, "隔离") || !strings.Contains(message, "root") {
		t.Fatalf("错误信息应指出隔离目录准备失败与 root: %q", message)
	}
	// 假 Pi 未被启动:记录文件由假 Pi 创建,不应存在。
	if _, err := os.Stat(env.record); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("隔离失败时不应启动 Pi: %v", err)
	}
	// 隔离准备先于令牌签发:不应签发过令牌,也就无需 EndRun。
	if env.issuer.issueCount() != 0 || env.issuer.endedCount() != 0 {
		t.Fatalf("隔离失败不应签发令牌: issued=%d ended=%d", env.issuer.issueCount(), env.issuer.endedCount())
	}
	meta, err := env.store.Get(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Slot <= 0 {
		t.Fatalf("槽位应已写入 meta(分配先于目录准备): %+v", meta)
	}
}

// TestIsolationDisabledKeepsHome:关闭隔离时 HOME 仍是会话目录(与既有行为
// 完全一致),meta 也不落 slot 字段。
func TestIsolationDisabledKeepsHome(t *testing.T) {
	env := newTestEnv(t, "basic")
	result := sendBasic(t, env)
	collectRun(t, env.runs, result.SessionID).wait(t)
	var start map[string]any
	for _, record := range readRecords(t, env.record) {
		if record["kind"] == "start" {
			start = record
		}
	}
	if start == nil {
		t.Fatal("没有启动记录")
	}
	for _, value := range start["env"].([]any) {
		if strings.HasPrefix(value.(string), "HOME=") {
			if want := "HOME=" + env.store.Dir(result.SessionID); value != want {
				t.Fatalf("HOME = %v, want %v", value, want)
			}
			return
		}
	}
	t.Fatal("启动环境里没有 HOME")
}

func recordedCommands(t *testing.T, path string) []map[string]any {
	t.Helper()
	var result []map[string]any
	for _, record := range readRecords(t, path) {
		if record["kind"] == "command" {
			result = append(result, record["command"].(map[string]any))
		}
	}
	return result
}

func hasUIResponse(commands []map[string]any, id string, granted bool) bool {
	for _, command := range commands {
		if command["type"] == "extension_ui_response" && command["id"] == id && command["confirmed"] == granted {
			return true
		}
	}
	return false
}

func readRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("打开记录: %v", err)
	}
	defer file.Close()
	var result []map[string]any
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 16*1024*1024)
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		result = append(result, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

// ---- P6e:会话内允许 / run 失败记录 ----

// grantSession 对当前未决审批做出「会话内允许」决定。
func grantSession(t *testing.T, env *testEnv, sessionID, escalationID string) {
	t.Helper()
	if err := env.runner.Grant(context.Background(), sessionID, GrantInput{
		EscalationID: escalationID, Granted: true, Scope: "session",
	}); err != nil {
		t.Fatalf("Grant(session): %v", err)
	}
}

// findEscalation 等待并返回指定 id 的审批请求事件。
func findEscalation(t *testing.T, c *collector, id string) translate.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for i, event := range c.snapshot() {
			if event.Type != "escalation_request" {
				continue
			}
			if data, _ := event.Data["escalation"].(map[string]any); data["escalation_id"] == id {
				return c.snapshot()[i]
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待审批 %s 超时;已有事件=%v", id, eventTypes(c.snapshot()))
	return translate.Event{}
}

// decisionEvents 返回指定审批的全部 permission_result 事件。
func decisionEvents(events []translate.Event, id string) []translate.Event {
	var result []translate.Event
	for _, event := range events {
		if event.Type == "permission_result" && event.Data["request_id"] == id {
			result = append(result, event)
		}
	}
	return result
}

// metaEscalation 取 meta 里指定 id 的审批记录。
// metaEscalation 取 meta 里指定 id 的审批记录(重复 id 时取最后一条,
// 假 Pi 重放夹具会用同一 id)。
func metaEscalation(t *testing.T, env *testEnv, sessionID, id string) session.Escalation {
	t.Helper()
	meta, err := env.store.Get(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(meta.Escalations) - 1; i >= 0; i-- {
		if meta.Escalations[i].EscalationID == id {
			return meta.Escalations[i]
		}
	}
	t.Fatalf("meta 里没有审批 %s: %+v", id, meta.Escalations)
	return session.Escalation{}
}

// ① 会话内允许后,同一 run 内同工具的第二次请求被自动允许,事件带 auto,
// 且 permission_result 排在对应 escalation_request 之后。
func TestSessionAllowAutoGrantSameRun(t *testing.T) {
	env := newTestEnv(t, "approval-session", withLocalFixture("approval-session.jsonl"), withDelay(40))
	result := startSession(t, env, "你好")
	collected := collectRun(t, env.runs, result.SessionID)
	findEscalation(t, collected, "esc-s1")
	grantSession(t, env, result.SessionID, "esc-s1")
	events := collected.wait(t)

	decisions := decisionEvents(events, "esc-s2")
	if len(decisions) != 1 || decisions[0].Data["allowed"] != true || decisions[0].Data["auto"] != true {
		t.Fatalf("esc-s2 应有一条自动允许的 permission_result: %+v", decisions)
	}
	// 事件顺序:escalation_request 先于自动 permission_result
	reqIndex, resIndex := -1, -1
	for i, event := range events {
		if event.Type == "escalation_request" {
			if data, _ := event.Data["escalation"].(map[string]any); data["escalation_id"] == "esc-s2" {
				reqIndex = i
			}
		}
		if event.Type == "permission_result" && event.Data["request_id"] == "esc-s2" {
			resIndex = i
		}
	}
	if reqIndex < 0 || resIndex < reqIndex {
		t.Fatalf("escalation_request(%d) 应先于 permission_result(%d)", reqIndex, resIndex)
	}
	// 用户点击的那条没有 auto
	first := decisionEvents(events, "esc-s1")
	if len(first) != 1 || first[0].Data["auto"] != nil {
		t.Fatalf("esc-s1 的 permission_result 不应带 auto: %+v", first)
	}
	esc2 := metaEscalation(t, env, result.SessionID, "esc-s2")
	if esc2.Granted == nil || !*esc2.Granted || esc2.Scope != "session" || !esc2.Auto {
		t.Fatalf("esc-s2 记录不符: %+v", esc2)
	}
	esc1 := metaEscalation(t, env, result.SessionID, "esc-s1")
	if esc1.Granted == nil || !*esc1.Granted || esc1.Scope != "session" || esc1.Auto {
		t.Fatalf("esc-s1 记录不符: %+v", esc1)
	}
	meta, _ := env.store.Get(result.SessionID)
	if !reflect.DeepEqual(meta.SessionAllow, []string{"write"}) {
		t.Fatalf("SessionAllow = %v, want [write]", meta.SessionAllow)
	}
	// 自动放行也向 Pi 回写了允许
	commands := recordedCommands(t, env.record)
	if !hasUIResponse(commands, "esc-s2", true) {
		t.Errorf("假 Pi 未收到 esc-s2 的自动允许应答: %v", commands)
	}
}

// startSession 先建会话并写入 SessionAllow,再发送消息(避免与事件流竞争)。
func startSession(t *testing.T, env *testEnv, content string, tools ...string) SendResult {
	t.Helper()
	meta, err := env.store.Create("mock-1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) > 0 {
		if _, err := env.store.Update(meta.ID, func(m *session.Meta) error {
			m.SessionAllow = append(m.SessionAllow, tools...)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := env.runner.Send(context.Background(), SendInput{
		SessionID: meta.ID, Content: content, Model: "mock-1",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	return result
}

// ② 跨 run:预先写入的会话内允许对本 run 的同工具请求生效。
func TestSessionAllowAutoGrantNextRun(t *testing.T) {
	env := newTestEnv(t, "approval-session", withLocalFixture("approval-session.jsonl"))
	result := startSession(t, env, "你好", "write")
	collected := collectRun(t, env.runs, result.SessionID)
	findEscalation(t, collected, "esc-s1")
	events := collected.wait(t)
	decisions := decisionEvents(events, "esc-s1")
	if len(decisions) != 1 || decisions[0].Data["allowed"] != true || decisions[0].Data["auto"] != true {
		t.Fatalf("esc-s1 应被自动允许: %+v", decisions)
	}
	esc1 := metaEscalation(t, env, result.SessionID, "esc-s1")
	if esc1.Granted == nil || !*esc1.Granted || !esc1.Auto {
		t.Fatalf("esc-s1 记录不符: %+v", esc1)
	}
	if !hasUIResponse(recordedCommands(t, env.record), "esc-s1", true) {
		t.Error("假 Pi 未收到自动允许应答")
	}
}

// ③ 不同工具不受会话内允许影响:仍等用户。
func TestSessionAllowOtherToolUnaffected(t *testing.T) {
	env := newTestEnv(t, "approval-session", withLocalFixture("approval-session.jsonl"))
	result := startSession(t, env, "你好", "bash")
	events := collectRun(t, env.runs, result.SessionID).wait(t)
	if decisions := decisionEvents(events, "esc-s1"); len(decisions) != 0 {
		t.Fatalf("write 不应被 bash 的会话内允许放行: %+v", decisions)
	}
	esc1 := metaEscalation(t, env, result.SessionID, "esc-s1")
	if esc1.Granted != nil {
		t.Fatalf("esc-s1 应保持未决: %+v", esc1)
	}
}

// ④ bash 高危命令(note 含「命中规则」)不自动放行。
func TestSessionAllowBashHighRiskNotAutoGranted(t *testing.T) {
	env := newTestEnv(t, "approval-bash-highrisk", withLocalFixture("approval-bash-highrisk.jsonl"))
	result := startSession(t, env, "你好", "bash")
	events := collectRun(t, env.runs, result.SessionID).wait(t)
	if decisions := decisionEvents(events, "esc-hr"); len(decisions) != 0 {
		t.Fatalf("高危 bash 不应被自动放行: %+v", decisions)
	}
	esc := metaEscalation(t, env, result.SessionID, "esc-hr")
	if esc.Granted != nil || esc.Auto {
		t.Fatalf("esc-hr 应保持未决且非自动: %+v", esc)
	}
	commands := recordedCommands(t, env.record)
	for _, command := range commands {
		if command["type"] == "extension_ui_response" && command["id"] == "esc-hr" {
			t.Fatal("不应向 Pi 回写 esc-hr 的自动应答")
		}
	}
}

// ⑤ revoke(清空 SessionAllow)后恢复询问。
func TestSessionAllowRevokeRestoresAsking(t *testing.T) {
	env := newTestEnv(t, "approval-session", withLocalFixture("approval-session.jsonl"))
	result := startSession(t, env, "第一轮", "write")
	collectRun(t, env.runs, result.SessionID).wait(t)

	// 模拟 revoke-access:清空允许列表
	if _, err := env.store.Update(result.SessionID, func(m *session.Meta) error {
		m.SessionAllow = []string{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	second, err := env.runner.Send(context.Background(), SendInput{
		SessionID: result.SessionID, Content: "第二轮", Model: "mock-1",
	})
	if err != nil {
		t.Fatalf("第二轮 Send: %v", err)
	}
	events := collectRun(t, env.runs, second.SessionID).wait(t)
	if decisions := decisionEvents(events, "esc-s1"); len(decisions) != 0 {
		t.Fatalf("清空后不应自动放行: %+v", decisions)
	}
	// 假 Pi 重放同一夹具,id 相同:第二轮新记录的审批应保持未决。
	meta, _ := env.store.Get(result.SessionID)
	var pending int
	for _, esc := range meta.Escalations {
		if esc.EscalationID == "esc-s1" && esc.Granted == nil {
			pending++
		}
	}
	if pending == 0 {
		t.Fatalf("清空后新请求应保持未决: %+v", meta.Escalations)
	}
}

// ⑦ granted=false + scope=session 当作普通拒绝,不写入 SessionAllow。
func TestGrantDenyWithSessionScope(t *testing.T) {
	env := newTestEnv(t, "approval-session", withLocalFixture("approval-session.jsonl"), withDelay(40))
	result := sendBasic(t, env)
	collected := collectRun(t, env.runs, result.SessionID)
	findEscalation(t, collected, "esc-s1")
	if err := env.runner.Grant(context.Background(), result.SessionID, GrantInput{
		EscalationID: "esc-s1", Granted: false, Scope: "session",
	}); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	collected.wait(t)
	meta, _ := env.store.Get(result.SessionID)
	if len(meta.SessionAllow) != 0 {
		t.Fatalf("拒绝不应写入 SessionAllow: %v", meta.SessionAllow)
	}
	esc1 := metaEscalation(t, env, result.SessionID, "esc-s1")
	if esc1.Granted == nil || *esc1.Granted || esc1.Scope != "" {
		t.Fatalf("拒绝记录不应带 scope: %+v", esc1)
	}
}

// run 以 agent_error 收尾时把失败信息写进 meta.RunErrors(锚点取事件
// 循环里最后见到的 entry_appended id,err-500 夹具第 3 次重试为 67b1e11d)。
func TestRunErrorRecordedOnError(t *testing.T) {
	// withoutSessionFile:err-500 事件夹具的 entry_appended 锚点不在假 Pi的
	// 会话文件里(两者来自不同录制),没有会话文件时走“回放全部 run 失败”
	// 路径,夹具才自洽。
	env := newTestEnv(t, "err-500", withoutSessionFile())
	result := sendBasic(t, env)
	collectRun(t, env.runs, result.SessionID).wait(t)
	meta, err := env.store.Get(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.RunErrors) != 1 {
		t.Fatalf("RunErrors = %d 条, want 1: %+v", len(meta.RunErrors), meta.RunErrors)
	}
	runErr := meta.RunErrors[0]
	if runErr.RunID != result.RunID {
		t.Fatalf("RunID = %s, want %s", runErr.RunID, result.RunID)
	}
	if !strings.Contains(runErr.Message, "500") {
		t.Fatalf("Message = %q, 应包含上游错误", runErr.Message)
	}
	if runErr.AfterEntryID != "67b1e11d" {
		t.Fatalf("AfterEntryID = %q, want 67b1e11d", runErr.AfterEntryID)
	}
	// transcript 能回放出 run_error 条目
	entries, err := env.store.BuildTranscript(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry["type"] == "run_error" && strings.Contains(entry["message"].(string), "500") {
			found = true
		}
	}
	if !found {
		t.Fatalf("transcript 缺少 run_error 条目: %+v", entries)
	}
}

// agent_cancelled(用户停止)不是错误,不写入 RunErrors。
func TestRunErrorNotRecordedOnCancel(t *testing.T) {
	env := newTestEnv(t, "basic", withDelay(20))
	result := sendBasic(t, env)
	collected := collectRun(t, env.runs, result.SessionID)
	waitForEvent(t, collected, "text_delta")
	if err := env.runner.Stop(context.Background(), result.SessionID); err != nil {
		t.Fatal(err)
	}
	events := collected.wait(t)
	if events[len(events)-1].Type != "agent_cancelled" {
		t.Fatalf("终态 = %s, want agent_cancelled", events[len(events)-1].Type)
	}
	meta, _ := env.store.Get(result.SessionID)
	if len(meta.RunErrors) != 0 {
		t.Fatalf("取消不应记录 RunErrors: %+v", meta.RunErrors)
	}
}

// 工具中止等流程可能先发布 agent_error、再以 agent_cancelled 收口;
// 记录必须看最终终态,不能把中间错误当成失败 run。
func TestRunErrorSkippedWhenCancelFollowsError(t *testing.T) {
	env := newTestEnv(t, "basic")
	meta, err := env.store.Create("mock-1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	ar := &activeRun{sessionID: meta.ID, runID: "run-abort"}
	env.runner.publish(ar, translate.Event{Type: "agent_error", Data: map[string]any{"message": "工具被中止"}})
	env.runner.publish(ar, translate.Event{Type: "agent_cancelled", Data: map[string]any{"aborted": true}})
	env.runner.recordRunError(ar)
	meta, err = env.store.Get(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.RunErrors) != 0 {
		t.Fatalf("最终取消不应记录 RunErrors: %+v", meta.RunErrors)
	}
}

// ---- P6g:审批/失败记录锚点兜底(从 Pi 会话文件末尾取) ----

// 一个会话连续两轮各发一次审批:夹具没有 entry_appended 事件,锚点应兜底
// 取自会话文件末尾,两条审批的 AfterEntryID 都非空且分别等于各自轮次
// assistant 条目的 id(审批请求到达时该条目已先落盘)。
func TestEscalationAnchorFromSessionFile(t *testing.T) {
	env := newTestEnv(t, "approval-anchor", withLocalFixture("approval-anchor.jsonl"), withGrowSession())
	result := sendBasic(t, env)
	first := collectRun(t, env.runs, result.SessionID)
	findEscalation(t, first, "esc-1")
	if err := env.runner.Grant(context.Background(), result.SessionID, GrantInput{EscalationID: "esc-1", Granted: true}); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	first.wait(t)

	// run 名额的释放与 runlog 关闭存在小窗口,Busy 时轮询重试。
	var (
		second SendResult
		err    error
	)
	deadline := time.Now().Add(5 * time.Second)
	for {
		second, err = env.runner.Send(context.Background(), SendInput{
			SessionID: result.SessionID, Content: "第二轮", Model: "mock-1",
		})
		if err == nil || !errors.Is(err, ErrBusy) || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("第二轮 Send: %v", err)
	}
	if second.SessionID != result.SessionID || second.RunID == result.RunID {
		t.Fatalf("第二轮应复用会话并新开 run: %+v", second)
	}
	collected := collectRun(t, env.runs, result.SessionID)
	findEscalation(t, collected, "esc-2")
	if err := env.runner.Grant(context.Background(), result.SessionID, GrantInput{EscalationID: "esc-2", Granted: true}); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	collected.wait(t)

	meta, err := env.store.Get(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Escalations) != 2 {
		t.Fatalf("审批数 = %d, want 2: %+v", len(meta.Escalations), meta.Escalations)
	}
	anchors := map[string]string{}
	for _, esc := range meta.Escalations {
		if esc.AfterEntryID == "" {
			t.Fatalf("审批 %s 的锚点为空: %+v", esc.EscalationID, esc)
		}
		anchors[esc.EscalationID] = esc.AfterEntryID
	}
	if anchors["esc-1"] != "a-1" {
		t.Fatalf("esc-1 锚点 = %q, want 第一轮 assistant 条目 a-1", anchors["esc-1"])
	}
	if anchors["esc-2"] != "a-2" {
		t.Fatalf("esc-2 锚点 = %q, want 第二轮 assistant 条目 a-2", anchors["esc-2"])
	}

	// 端到端:transcript 里每条审批各自出现在自己那一轮的 assistant 条目之后
	entries, err := env.store.BuildTranscript(result.SessionID)
	if err != nil {
		t.Fatalf("BuildTranscript: %v", err)
	}
	var seq []string
	for _, entry := range entries {
		switch entry["type"] {
		case "message":
			message, _ := entry["message"].(map[string]any)
			seq = append(seq, "msg:"+fmt.Sprint(message["content"]))
		case "escalation":
			seq = append(seq, "esc:"+entry["escalation_id"].(string))
		}
	}
	// 用户消息展示取 meta 原文(P6h:优先绑定文本,引用/附件不进气泡),
	// 因此第一轮显示发送的「你好」而非夹具里的「第 1 轮请求」。
	want := []string{
		"msg:你好", "msg:第 1 轮动手", "esc:esc-1",
		"msg:第二轮", "msg:第 2 轮动手", "esc:esc-2", "msg:已写入",
	}
	if !reflect.DeepEqual(seq, want) {
		t.Fatalf("transcript 序列 = %q, want %q", seq, want)
	}
}

// 失败 run 的 RunErrors 锚点同理兜底:夹具没有 entry_appended,取文件末尾
// 最后落盘的条目(出错轮次的 assistant 条目 a-1)。
func TestRunErrorAnchorFromSessionFile(t *testing.T) {
	env := newTestEnv(t, "error-anchor", withLocalFixture("error-anchor.jsonl"), withGrowSession())
	result := sendBasic(t, env)
	events := collectRun(t, env.runs, result.SessionID).wait(t)
	if last := events[len(events)-1]; last.Type != "agent_error" {
		t.Fatalf("终态 = %s, want agent_error: %v", last.Type, eventTypes(events))
	}
	meta, err := env.store.Get(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.RunErrors) != 1 {
		t.Fatalf("RunErrors = %d 条, want 1: %+v", len(meta.RunErrors), meta.RunErrors)
	}
	runErr := meta.RunErrors[0]
	if runErr.RunID != result.RunID || !strings.Contains(runErr.Message, "500") {
		t.Fatalf("RunError = %+v", runErr)
	}
	if runErr.AfterEntryID != "a-1" {
		t.Fatalf("AfterEntryID = %q, want a-1(文件末尾的 assistant 条目)", runErr.AfterEntryID)
	}
}
