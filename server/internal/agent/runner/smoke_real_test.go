package runner

// TestRealPiSmoke 用真实 Pi 与真实上游网关做端到端冒烟(P2.6),覆盖假 Pi 验证
// 不到的环节:CLI 参数、models.json 写法、authHeader、真实事件形态、会话文件
// 格式与 --session-id 续接。默认跳过,需要同时设置:
//   - XLYRA_SMOKE_PI_ENTRY:Pi 入口脚本(如 agent-runtime/node_modules/
//     @earendil-works/pi-coding-agent/dist/bundle/cli.js)
//   - XLYRA_SMOKE_UPSTREAM:上游网关根地址(如 https://xl.yachiyo.im)
//   - XLYRA_SMOKE_TOKEN:上游令牌(只经环境变量进入 Pi 子进程,绝不打印)
//   - XLYRA_SMOKE_MODELS:可选,逗号分隔的模型清单
//
// 本地起一个反向代理,把 /internal/agent-llm 前缀剥掉后转发到上游
// (Authorization 头原样透传),runner 的 GatewayBaseURL 指向该代理;代理只记录
// 方法、路径、状态码与是否带令牌头,请求体另存内存供 P4 冒烟断言、绝不
// 输出。真实运行耗时可能超过默认 -timeout,
// 执行时请自行调大(如 -timeout 60m)。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"xlyra/server/internal/agent/piconfig"
	"xlyra/server/internal/agent/runlog"
	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agent/translate"
)

const (
	smokeEnvEntry    = "XLYRA_SMOKE_PI_ENTRY"
	smokeEnvExt      = "XLYRA_SMOKE_EXTENSION"
	smokeEnvUpstream = "XLYRA_SMOKE_UPSTREAM"
	smokeEnvToken    = "XLYRA_SMOKE_TOKEN"
	smokeEnvModels   = "XLYRA_SMOKE_MODELS"

	smokeDefaultModels = "claude-sonnet-5-5,gpt-6-luna,glm-5.3,deepseek-v4.1-flash,kimi-k3.1-pro"
	smokeMarker        = "xlyra-smoke-42"

	// 与 piconfig.piBaseURL 拼出的路径对应:代理剥掉该前缀后转发上游根地址。
	smokeLLMPrefix = "/internal/agent-llm"

	smokeRunTimeout      = 3 * time.Minute                  // 单个 run 的上限
	smokeCollectTimeout  = smokeRunTimeout + 30*time.Second // 收集略长于 run,让终态先到
	smokeStopTimeout     = 45 * time.Second
	smokeShutdownTimeout = 45 * time.Second
	smokeLogTail         = 16 << 10 // 失败时输出 runner 日志的尾部上限
)

// smokeUpstreamKeywords:失败信息命中任一(或代理见到 5xx)即视为疑似上游
// 故障,对该模型整个子测试重试一次。
var smokeUpstreamKeywords = []string{"upstream_failed", "上游", "超时", "Pi 进程提前退出"}

func TestRealPiSmoke(t *testing.T) {
	env := smokePrepare(t)
	models := smokeModels(t)
	var summaries []*smokeSummary
	for i, model := range models {
		summary := &smokeSummary{model: model, protocol: smokeProtocol(model)}
		summaries = append(summaries, summary)
		t.Run(model, func(t *testing.T) {
			smokeModelSubtest(t, model, summary, func(at smokeAPI) (bool, string) {
				return runSmokeModel(at, t, env, model, i == 0, summary)
			})
		})
	}
	for _, summary := range summaries {
		t.Logf("冒烟汇总 | 模型 %s | 协议 %s | 耗时 %s | 步骤:%s | 代理请求 %d 次,状态码 %s,缺 Authorization %d 次",
			summary.model, summary.protocol, summary.duration.Round(time.Second),
			summary.stepsText(), summary.requests, summary.statuses, summary.noAuth)
	}
	if t.Failed() {
		t.Logf("runner 日志(已脱敏,仅失败时输出):\n%s", smokeSanitize(smokeTail(env.logs.String(), smokeLogTail), env.token))
	}
}

// smokePrepare 校验真实冒烟所需的环境变量、定位 node 与 Pi 入口、解析扩展
// 路径,并启动把 /internal/agent-llm/* 转发到上游的反向代理;P2(TestRealPiSmoke)、
// P3(TestRealPiSmokeP3)与 P4(TestRealPiSmokeP4)共用。任一必需变量缺失时 Skip。
func smokePrepare(t *testing.T) *smokeEnv {
	t.Helper()
	entry := strings.TrimSpace(os.Getenv(smokeEnvEntry))
	upstreamEnv := strings.TrimSpace(os.Getenv(smokeEnvUpstream))
	token := os.Getenv(smokeEnvToken)
	var missing []string
	for _, item := range []struct{ name, value string }{
		{smokeEnvEntry, entry}, {smokeEnvUpstream, upstreamEnv}, {smokeEnvToken, token},
	} {
		if item.value == "" {
			missing = append(missing, item.name)
		}
	}
	if len(missing) > 0 {
		t.Skipf("缺少环境变量 %s;真实 Pi 冒烟需要 %s(Pi 入口脚本)、%s(上游网关根地址)与 %s(上游令牌),未设置时默认跳过",
			strings.Join(missing, "、"), smokeEnvEntry, smokeEnvUpstream, smokeEnvToken)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("找不到 node:%v", err)
	}
	if info, statErr := os.Stat(entry); statErr != nil || info.IsDir() {
		t.Fatalf("%s = %q 不是可用的 Pi 入口脚本:%v", smokeEnvEntry, entry, statErr)
	}
	// 扩展路径优先取 XLYRA_SMOKE_EXTENSION,缺省按 Pi 入口推导(与 app 包
	// agentExtensionPath 同规则):入口在 <root>/agent-runtime/node_modules/…
	// 下时取 <root>/agent-runtime/extension/xlyra.ts;推导不到时保持空,
	// Health 会因写类工具缺少扩展而不可用。
	extension := strings.TrimSpace(os.Getenv(smokeEnvExt))
	if extension == "" {
		extension = smokeDeriveExtension(entry)
	}
	upstream, err := url.Parse(strings.TrimRight(upstreamEnv, "/"))
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		t.Fatalf("%s = %q 不是合法的网关根地址", smokeEnvUpstream, upstreamEnv)
	}

	stats := &proxyStats{codes: map[int]int{}}
	// 请求体只记进内存(P4 冒烟按游标断言「下一个 run 的请求真的变了」),
	// 绝不写日志、不打印;先读再还原,不影响转发与请求形态记录。
	bodies := &smokeBodyLog{}
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = upstream.Scheme
			req.URL.Host = upstream.Host
			req.URL.Path = upstream.Path + strings.TrimPrefix(req.URL.Path, smokeLLMPrefix)
			req.Host = upstream.Host // Host 头指向上游;Authorization 等其余头原样透传
		},
		FlushInterval: -1, // 逐写刷新,流式响应不被代理缓冲
		// 上游返回 4xx/5xx 时,记录响应体前 400 字节(上游的错误说明,不含令牌),便于区分
		// 上游故障与请求形态问题。
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode < 400 || resp.Body == nil {
				return nil
			}
			head, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
			rest, _ := io.ReadAll(resp.Body)
			resp.Body = io.NopCloser(bytes.NewReader(append(head, rest...)))
			t.Logf("上游错误响应 %d:%s", resp.StatusCode, smokeSanitize(string(head), token))
			return nil
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hasAuth := r.Header.Get("Authorization") != ""
		if r.Body != nil && r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			bodies.add(string(body))
		}
		t.Logf("请求形态:%s", smokeRequestShape(r))
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		proxy.ServeHTTP(recorder, r)
		stats.add(recorder.status, hasAuth)
		// 只记方法、路径、状态码与是否带令牌头;绝不记录令牌值与请求/响应体。
		t.Logf("代理请求:%s %s → %d(带 Authorization:%v)", r.Method, r.URL.Path, recorder.status, hasAuth)
	}))
	t.Cleanup(server.Close)

	return &smokeEnv{
		proxyURL: server.URL, piEntry: entry, node: node, token: token, extension: extension,
		stats: stats, bodies: bodies, logs: &syncBuffer{},
	}
}

// smokeModelSubtest 跑一个模型的全部步骤;失败且疑似上游故障时整个模型重试一次。
// attempt 由调用方构造(P2 的 runSmokeModel 或 P3 的 runSmokeP3Model)。
func smokeModelSubtest(t *testing.T, model string, summary *smokeSummary, attempt func(at smokeAPI) (bool, string)) {
	first := captureSmokeAttempt(t, attempt)
	if len(first.errs) == 0 {
		return
	}
	if !first.suspected {
		smokeReport(t, first.errs)
		return
	}
	t.Logf("模型 %s 第 1 次尝试失败且疑似上游故障(%s),整个模型重试一次", model, first.fault)
	second := captureSmokeAttempt(t, attempt)
	if len(second.errs) == 0 {
		t.Logf("模型 %s 重试通过", model)
		return
	}
	smokeReport(t, second.errs)
	fault := second.fault
	if fault == "" {
		fault = first.fault
	}
	t.Errorf("模型 %s 疑似上游故障:%s", model, fault)
}

func smokeReport(t *testing.T, errs []string) {
	for _, message := range errs {
		t.Errorf("%s", message)
	}
}

// runSmokeModel 按顺序执行一个模型的全部冒烟步骤;任一步骤失败立即返回,
// 由代理统计与错误文本推断是否疑似上游故障。返回值:(疑似上游故障, 摘要)。
func runSmokeModel(at smokeAPI, t *testing.T, env *smokeEnv, model string, withCompact bool, summary *smokeSummary) (suspected bool, fault string) {
	at.Helper()
	started := time.Now()
	summary.reset(model)
	defer func() { summary.duration = time.Since(started).Round(time.Millisecond) }()

	store := session.NewStore(filepath.Join(t.TempDir(), "sessions"))
	runs := runlog.NewRegistry()
	agent, err := New(Config{
		Store: store, Runs: runs, Tokens: &smokeIssuer{token: env.token},
		Protocol:       func(context.Context, string) (string, error) { return smokeProtocol(model), nil },
		GatewayBaseURL: env.proxyURL,
		Command:        env.node, Args: []string{env.piEntry},
		ExtensionPath: env.extension,
		PathEnv:       os.Getenv("PATH"),
		RunTimeout:    smokeRunTimeout, AbortGrace: 10 * time.Second,
		Logger: slog.New(slog.NewTextHandler(env.logs, nil)),
	})
	if err != nil {
		at.Fatalf("New:%v", err)
	}
	sessionID := ""
	// 无论成败都收干净 Pi 子进程:Shutdown 会等全部 run 收口(Process.Wait 完成)。
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), smokeShutdownTimeout)
		defer cancel()
		if err := agent.Shutdown(ctx); err != nil {
			at.Errorf("模型 %s Shutdown:%v", model, err)
		}
		if sessionID != "" && runs.Running(sessionID) {
			at.Errorf("模型 %s 收尾后仍有运行中的 run", model)
		}
	}()

	var faults []string
	before := env.stats.snapshot()
	finish := func() (bool, string) {
		delta := env.stats.since(before)
		summary.requests, summary.statuses, summary.noAuth = delta.total, delta.describe(), delta.noAuth
		if delta.noAuth > 0 {
			at.Errorf("模型 %s 有 %d 个上游请求未携带 Authorization 头", model, delta.noAuth)
		}
		return smokeFault(faults, delta)
	}
	step := func(name string, pass bool) bool {
		summary.setStep(name, pass)
		return pass
	}

	// 1) 预置工作目录:Send 新建会话时才建目录,所以先 Create + EnsureDirs 拿到
	//    会话 id,把 hello.txt 写进 WorkDir,再以该 SessionID 首次 Send。
	meta, err := store.Create(model, "full")
	if err != nil {
		at.Fatalf("Store.Create:%v", err)
	}
	if err := store.EnsureDirs(meta.ID); err != nil {
		at.Fatalf("Store.EnsureDirs:%v", err)
	}
	sessionID = meta.ID
	if err := os.WriteFile(filepath.Join(store.WorkDir(sessionID), "hello.txt"), []byte(smokeMarker), 0o644); err != nil {
		at.Fatalf("写入 hello.txt:%v", err)
	}

	send := func(content string) []translate.Event {
		if _, err := agent.Send(context.Background(), SendInput{
			SessionID: sessionID, Content: content, Model: model,
			ReasoningEffort: "low", PermissionMode: "full",
		}); err != nil {
			at.Errorf("Send(%q):%v", content, err)
			return nil
		}
		return smokeRunEvents(at, runs, sessionID, smokeCollectTimeout)
	}

	// 2) 首轮:read 工具读 hello.txt 并原样复述。
	first := send("请用 read 工具读取当前目录下的 hello.txt,然后把里面的内容原样告诉我。")
	firstOK := len(first) > 0
	if firstOK {
		if !smokeHasType(first, "agent_start") {
			at.Errorf("首轮:缺少 agent_start 事件")
			firstOK = false
		}
		if !smokeCheckTerminal(at, "首轮", first, env.token, &faults) {
			firstOK = false
		}
		if !smokeHasReadCall(first) {
			at.Errorf("首轮:缺少 name 含 read 的 tool_call 事件")
			firstOK = false
		}
		if !smokeHasOKToolResult(first) {
			at.Errorf("首轮:缺少 is_error=false 的 tool_result 事件")
			firstOK = false
		}
		if text := smokeText(first); !strings.Contains(text, smokeMarker) {
			at.Errorf("首轮:拼接 text_delta 后未包含 %s(事件数 %d)", smokeMarker, len(first))
			firstOK = false
		}
	}
	if !step("首轮(read 工具)", firstOK) {
		return finish()
	}

	// 3) 转录:Pi 会话文件 + meta 组装出的 transcript 与绑定关系。
	if !step("转录(transcript+meta)", smokeCheckTranscript(at, store, sessionID) && smokeCheckMeta(at, store, sessionID)) {
		return finish()
	}

	// 4) 续聊:同一 SessionID 二次 Send,验证 --session-id 上下文恢复。
	second := send("上一条里文件的内容是什么?只回答内容。")
	secondOK := false
	if second != nil {
		secondOK = smokeCheckTerminal(at, "续聊", second, env.token, &faults) &&
			strings.Contains(smokeText(second), smokeMarker)
	}
	if !step("续聊(--session-id)", secondOK) {
		return finish()
	}

	// 5) 停止:长输出任务收到首个 text_delta 后 Stop,终态应为 agent_cancelled。
	if !step("停止(Stop→agent_cancelled)", smokeStopScenario(at, agent, runs, sessionID, model, env.token, &faults)) {
		return finish()
	}

	// 6) 停止后再发:进程已退出,仍能正常开新 run。
	after := send("停止后再发:请直接回答 1+1 等于几?只回答数字。")
	resendOK := false
	if after != nil {
		resendOK = smokeCheckTerminal(at, "停止后再发", after, env.token, &faults)
	}
	if !step("停止后再发", resendOK) {
		return finish()
	}

	// 7) 压缩:仅对第一个模型做;compact 后再发一次仍成功。
	if withCompact {
		compactOK := false
		compactCtx, cancel := context.WithTimeout(context.Background(), smokeCollectTimeout)
		result, compactErr := agent.Compact(compactCtx, sessionID, model)
		cancel()
		switch {
		case errors.Is(compactErr, ErrInvalid) && strings.Contains(compactErr.Error(), "无需压缩"):
			// 冒烟里的会话只有几轮,Pi 会拒绝压缩;确认返回的是明确提示而不是内部错误即可
			compactOK = true
		case compactErr != nil:
			at.Errorf("Compact:%v", compactErr)
		case result.Summary == "":
			at.Errorf("Compact 返回空 Summary")
		default:
			afterCompact := send("压缩后再问:hello.txt 里的内容是什么?只回答内容。")
			compactOK = afterCompact != nil &&
				smokeCheckTerminal(at, "压缩后再发", afterCompact, env.token, &faults)
		}
		if !step("压缩(Compact)", compactOK) {
			return finish()
		}
	}
	return finish()
}

// smokeStopScenario 发起长输出任务,收到第一个 text_delta 后 Stop,断言终态
// 恰好一个 agent_cancelled、run 收口(Running=false)。Stop 在事件循环内调用,
// 其间暂停读事件是安全的:runlog 的订阅通道会缓冲,Stop 返回后继续排空。
func smokeStopScenario(at smokeAPI, agent *Runner, runs *runlog.Registry, sessionID, model, token string, faults *[]string) bool {
	at.Helper()
	if _, err := agent.Send(context.Background(), SendInput{
		SessionID: sessionID, Content: "请从 1 数到 200,每个数字单独一行,不要省略。",
		Model: model, ReasoningEffort: "low", PermissionMode: "full",
	}); err != nil {
		at.Errorf("Send(数数):%v", err)
		return false
	}
	run := runs.Current(sessionID)
	if run == nil {
		at.Errorf("停止场景:会话 %s 没有当前 run", sessionID)
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), smokeCollectTimeout)
	defer cancel()
	var events []translate.Event
	sawDelta := false
	for event := range run.Subscribe(ctx) {
		events = append(events, event.Event)
		if event.Event.Type == "text_delta" && !sawDelta {
			sawDelta = true
			stopCtx, stopCancel := context.WithTimeout(context.Background(), smokeStopTimeout)
			if err := agent.Stop(stopCtx, sessionID); err != nil {
				at.Errorf("Stop:%v", err)
			}
			stopCancel()
		}
	}
	if !sawDelta {
		at.Errorf("停止场景:未收到任何 text_delta(事件类型:%v)", eventTypes(events))
		return false
	}
	terminals := smokeTerminals(events)
	if len(terminals) != 1 || terminals[0].Type != "agent_cancelled" {
		at.Errorf("停止场景:终态 = %v,want 恰好一个 agent_cancelled", eventTypes(terminals))
		return false
	}
	if runs.Running(sessionID) {
		at.Errorf("停止场景:Stop 后 Runs.Running 仍为 true")
		return false
	}
	return true
}

// captureSmokeAttempt 以可丢弃错误的方式跑一次尝试:错误先收集,疑似上游故障
// 时整模型重试,重试通过则第一次的错误不再上报;Fatalf 记录错误后中止本次尝试。
func captureSmokeAttempt(t *testing.T, fn func(at smokeAPI) (bool, string)) (result smokeAttempt) {
	rec := &smokeRecorder{t: t}
	defer func() {
		if p := recover(); p != nil {
			if _, ok := p.(smokeAbort); !ok {
				panic(p)
			}
		}
		result.errs = rec.snapshot()
	}()
	result.suspected, result.fault = fn(rec)
	return result
}

type smokeAttempt struct {
	errs      []string
	suspected bool
	fault     string
}

// smokeAPI 是步骤断言依赖的最小 testing 接口(*testing.T 天然满足)。
type smokeAPI interface {
	Helper()
	Logf(format string, args ...any)
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

type smokeAbort struct{}

type smokeRecorder struct {
	t    *testing.T
	mu   sync.Mutex
	errs []string
}

func (r *smokeRecorder) Helper() {}

func (r *smokeRecorder) Logf(format string, args ...any) { r.t.Logf(format, args...) }

func (r *smokeRecorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
	r.mu.Unlock()
}

func (r *smokeRecorder) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	panic(smokeAbort{})
}

func (r *smokeRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.errs...)
}

// smokeIssuer 让 IssueRunToken 直接返回真实上游令牌:令牌经 XLYRA_TOKEN 进入
// Pi 子进程,models.json 的 apiKey 引用它,代理再原样透传 Authorization 头。
type smokeIssuer struct {
	token string
}

func (s *smokeIssuer) IssueRunToken(context.Context, string, string, string) (string, time.Time, error) {
	return s.token, time.Now().Add(30 * time.Minute), nil
}

func (s *smokeIssuer) EndRun(context.Context, string, string, string) error { return nil }

// smokeDeriveExtension 与 app 包 agentExtensionPath 的推导规则一致(未配置
// 扩展时从 Pi 入口反推);两边各自实现,避免 runner 依赖 app。
func smokeDeriveExtension(piEntry string) string {
	dir := filepath.Dir(filepath.Clean(piEntry))
	for {
		if filepath.Base(dir) == "node_modules" {
			candidate := filepath.Join(filepath.Dir(dir), "extension", "xlyra.ts")
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// smokeFault 结合代理状态码分布与错误文本,判断是否疑似上游故障。
func smokeFault(faults []string, delta proxyDelta) (bool, string) {
	if fiveXX := delta.fiveXXText(); fiveXX != "" {
		return true, "上游 5xx:" + fiveXX
	}
	for _, text := range faults {
		for _, keyword := range smokeUpstreamKeywords {
			if strings.Contains(text, keyword) {
				return true, "错误信息包含 " + keyword
			}
		}
	}
	return false, ""
}

// smokeRunEvents 订阅会话当前 run 并收集到通道关闭(run 收口)或超时。
func smokeRunEvents(at smokeAPI, runs *runlog.Registry, sessionID string, timeout time.Duration) []translate.Event {
	at.Helper()
	run := runs.Current(sessionID)
	if run == nil {
		at.Fatalf("会话 %s 没有当前 run", sessionID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var events []translate.Event
	for event := range run.Subscribe(ctx) {
		events = append(events, event.Event)
	}
	return events
}

// smokeCheckTerminal 断言终态恰好一个且为 agent_done;agent_error 的 message
// 先做令牌脱敏再上报,并记入 faults 供疑似上游故障判定。
func smokeCheckTerminal(at smokeAPI, label string, events []translate.Event, token string, faults *[]string) bool {
	at.Helper()
	terminals := smokeTerminals(events)
	if len(terminals) != 1 {
		at.Errorf("%s:终态事件数 = %d(全部事件类型:%v)", label, len(terminals), eventTypes(events))
		return false
	}
	if terminals[0].Type == "agent_done" {
		return true
	}
	if terminals[0].Type == "agent_error" {
		message, _ := terminals[0].Data["message"].(string)
		message = smokeSanitize(message, token)
		*faults = append(*faults, message)
		at.Errorf("%s:终态 agent_error:%s", label, message)
		return false
	}
	*faults = append(*faults, terminals[0].Type)
	at.Errorf("%s:终态 = %s,want agent_done", label, terminals[0].Type)
	return false
}

func smokeHasType(events []translate.Event, typ string) bool {
	for _, event := range events {
		if event.Type == typ {
			return true
		}
	}
	return false
}

func smokeTerminals(events []translate.Event) []translate.Event {
	var terminals []translate.Event
	for _, event := range events {
		switch event.Type {
		case "agent_done", "agent_error", "agent_cancelled":
			terminals = append(terminals, event)
		}
	}
	return terminals
}

func smokeText(events []translate.Event) string {
	var builder strings.Builder
	for _, event := range events {
		if event.Type == "text_delta" {
			delta, _ := event.Data["delta"].(string)
			builder.WriteString(delta)
		}
	}
	return builder.String()
}

func smokeHasReadCall(events []translate.Event) bool {
	for _, event := range events {
		if event.Type != "tool_call" {
			continue
		}
		call, _ := event.Data["tool_call"].(map[string]any)
		name, _ := call["name"].(string)
		if strings.Contains(strings.ToLower(name), "read") {
			return true
		}
	}
	return false
}

func smokeHasOKToolResult(events []translate.Event) bool {
	for _, event := range events {
		if event.Type != "tool_result" {
			continue
		}
		result, _ := event.Data["tool_result"].(map[string]any)
		if isError, _ := result["is_error"].(bool); !isError {
			return true
		}
	}
	return false
}

// smokeCheckTranscript 校验首轮后的 transcript:至少 user / assistant(带
// tool_calls)/ tool / assistant(最终回答)四类,且 tool 消息的 tool_call_id
// 能在某个 assistant 的 tool_calls[].id 里对上。
func smokeCheckTranscript(at smokeAPI, store *session.Store, sessionID string) bool {
	at.Helper()
	entries, err := store.BuildTranscript(sessionID)
	if err != nil {
		at.Errorf("BuildTranscript:%v", err)
		return false
	}
	users, assistants, assistantsWithCalls, tools := 0, 0, 0, 0
	callIDs := map[string]bool{}
	var toolIDs []string
	for _, entry := range entries {
		if entry["type"] != "message" {
			continue
		}
		message, _ := entry["message"].(map[string]any)
		switch role, _ := message["role"].(string); role {
		case "user":
			users++
		case "assistant":
			assistants++
			calls := smokeMaps(message["tool_calls"])
			if len(calls) > 0 {
				assistantsWithCalls++
				for _, call := range calls {
					if id := smokeString(call["id"]); id != "" {
						callIDs[id] = true
					}
				}
			}
		case "tool":
			tools++
			toolIDs = append(toolIDs, smokeString(message["tool_call_id"]))
		}
	}
	pass := true
	if users < 1 {
		at.Errorf("transcript:user 消息数 = %d", users)
		pass = false
	}
	if assistantsWithCalls < 1 {
		at.Errorf("transcript:带 tool_calls 的 assistant 消息数 = %d", assistantsWithCalls)
		pass = false
	}
	if tools < 1 {
		at.Errorf("transcript:tool 消息数 = %d", tools)
		pass = false
	}
	if assistants < 2 {
		at.Errorf("transcript:assistant 消息数 = %d,至少应有带工具调用与最终回答两条", assistants)
		pass = false
	}
	for _, id := range toolIDs {
		if !callIDs[id] {
			at.Errorf("transcript:tool 消息的 tool_call_id %q 与任何 assistant 的 tool_calls[].id 对不上", id)
			pass = false
		}
	}
	return pass
}

func smokeCheckMeta(at smokeAPI, store *session.Store, sessionID string) bool {
	at.Helper()
	meta, err := store.Get(sessionID)
	if err != nil {
		at.Errorf("Store.Get:%v", err)
		return false
	}
	pass := true
	if meta.Title == "" {
		at.Errorf("meta.Title 为空")
		pass = false
	}
	if meta.PiSessionID == "" {
		at.Errorf("meta.PiSessionID 为空")
		pass = false
	}
	if len(meta.Messages) == 0 || meta.Messages[0].EntryID == "" {
		at.Errorf("meta.Messages[0].EntryID 为空(消息绑定失败)")
		pass = false
	}
	return pass
}

func smokeModels(t *testing.T) []string {
	t.Helper()
	spec := strings.TrimSpace(os.Getenv(smokeEnvModels))
	if spec == "" {
		spec = smokeDefaultModels
	}
	var models []string
	for _, model := range strings.Split(spec, ",") {
		if model = strings.TrimSpace(model); model != "" {
			models = append(models, model)
		}
	}
	if len(models) == 0 {
		t.Skipf("%s 未提供任何模型", smokeEnvModels)
	}
	return models
}

// smokeProtocol 按模型前缀选下行协议,取值与 piconfig.PiAPIName 支持的三种对应。
func smokeProtocol(model string) string {
	id := strings.ToLower(model)
	switch {
	case strings.HasPrefix(id, "claude-"):
		return piconfig.ProtocolAnthropicMessages
	case strings.HasPrefix(id, "gpt-"), strings.HasPrefix(id, "grok-"):
		return piconfig.ProtocolOpenAIResponses
	default:
		return piconfig.ProtocolOpenAIChatCompletions
	}
}

type smokeEnv struct {
	proxyURL  string
	piEntry   string
	node      string
	token     string
	extension string
	stats     *proxyStats
	bodies    *smokeBodyLog
	logs      *syncBuffer
}

type smokeStep struct {
	name string
	pass bool
}

type smokeSummary struct {
	model    string
	protocol string
	duration time.Duration
	steps    []smokeStep
	requests int
	statuses string
	noAuth   int
}

func (s *smokeSummary) reset(model string) {
	s.model = model
	s.steps = nil
}

func (s *smokeSummary) setStep(name string, pass bool) {
	for i := range s.steps {
		if s.steps[i].name == name {
			s.steps[i].pass = pass
			return
		}
	}
	s.steps = append(s.steps, smokeStep{name: name, pass: pass})
}

func (s *smokeSummary) stepsText() string {
	if len(s.steps) == 0 {
		return "无"
	}
	parts := make([]string, 0, len(s.steps))
	for _, step := range s.steps {
		mark := "通过"
		if !step.pass {
			mark = "失败"
		}
		parts = append(parts, step.name+"="+mark)
	}
	return strings.Join(parts, " ")
}

// proxyStats 统计代理见到的请求:状态码分布与是否携带 Authorization 头。
type proxyStats struct {
	mu     sync.Mutex
	total  int
	noAuth int
	codes  map[int]int
}

type proxySnapshot struct {
	total  int
	noAuth int
	codes  map[int]int
}

type proxyDelta struct {
	total  int
	noAuth int
	codes  map[int]int
}

func (s *proxyStats) add(status int, hasAuth bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.total++
	if !hasAuth {
		s.noAuth++
	}
	s.codes[status]++
}

func (s *proxyStats) snapshot() proxySnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	codes := make(map[int]int, len(s.codes))
	for code, n := range s.codes {
		codes[code] = n
	}
	return proxySnapshot{total: s.total, noAuth: s.noAuth, codes: codes}
}

// since 计算相对 before 的增量,供按模型统计请求数与状态码分布。
func (s *proxyStats) since(before proxySnapshot) proxyDelta {
	after := s.snapshot()
	codes := make(map[int]int, len(after.codes))
	for code, n := range after.codes {
		if d := n - before.codes[code]; d > 0 {
			codes[code] = d
		}
	}
	return proxyDelta{total: after.total - before.total, noAuth: after.noAuth - before.noAuth, codes: codes}
}

func (d proxyDelta) describe() string {
	text := d.format(d.sortedCodes())
	if text == "" {
		return "无"
	}
	return text
}

func (d proxyDelta) fiveXXText() string {
	codes := make([]int, 0, len(d.codes))
	for code := range d.codes {
		if code >= 500 {
			codes = append(codes, code)
		}
	}
	sort.Ints(codes)
	counts := make(map[int]int, len(codes))
	for _, code := range codes {
		counts[code] = d.codes[code]
	}
	return d.format(codes)
}

func (d proxyDelta) sortedCodes() []int {
	codes := make([]int, 0, len(d.codes))
	for code := range d.codes {
		codes = append(codes, code)
	}
	sort.Ints(codes)
	return codes
}

func (d proxyDelta) format(codes []int) string {
	if len(codes) == 0 {
		return ""
	}
	parts := make([]string, 0, len(codes))
	for _, code := range codes {
		parts = append(parts, fmt.Sprintf("%d×%d", code, d.codes[code]))
	}
	return strings.Join(parts, ", ")
}

// statusRecorder 记录响应状态码;Flush 透传,保证流式响应不被代理缓冲。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func smokeSanitize(text, token string) string {
	if token == "" {
		return text
	}
	return strings.ReplaceAll(text, token, "***")
}

func smokeTail(text string, max int) string {
	if len(text) <= max {
		return text
	}
	return text[len(text)-max:]
}

func smokeMaps(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		result := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok {
				result = append(result, m)
			}
		}
		return result
	default:
		return nil
	}
}

func smokeString(value any) string {
	text, _ := value.(string)
	return text
}

// smokeRequestShape 只描述请求的形态(顶层字段名、工具个数、thinking 类型、关键头名),
// 不含任何正文、令牌或头的值。会读取并还原请求体。
func smokeRequestShape(r *http.Request) string {
	var parts []string
	var names []string
	for name := range r.Header {
		lower := strings.ToLower(name)
		if lower == "authorization" || lower == "x-api-key" || lower == "x-goog-api-key" {
			names = append(names, lower+"=<present>")
			continue
		}
		if strings.HasPrefix(lower, "anthropic-") || lower == "x-xlyra-run-id" {
			names = append(names, lower+"="+r.Header.Get(name))
		}
	}
	sort.Strings(names)
	parts = append(parts, "headers["+strings.Join(names, " ")+"]")
	if r.Body != nil && r.Method == http.MethodPost {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var payload map[string]json.RawMessage
		if json.Unmarshal(body, &payload) == nil {
			keys := make([]string, 0, len(payload))
			for k := range payload {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts = append(parts, "body-keys["+strings.Join(keys, ",")+"]", fmt.Sprintf("bytes=%d", len(body)))
			if raw, ok := payload["thinking"]; ok {
				parts = append(parts, "thinking="+string(raw))
			}
			if raw, ok := payload["max_tokens"]; ok {
				parts = append(parts, "max_tokens="+string(raw))
			}
			if raw, ok := payload["tools"]; ok {
				var tools []json.RawMessage
				if json.Unmarshal(raw, &tools) == nil {
					parts = append(parts, fmt.Sprintf("tools=%d", len(tools)))
				}
			}
		}
	}
	return strings.Join(parts, " ")
}
