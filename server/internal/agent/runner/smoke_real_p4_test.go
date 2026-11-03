package runner

// TestRealPiSmokeP4 在真实 Pi + 真实上游上验证 P4 的能力快照接线:设置
// (Skills / AGENTS.md / 上下文参数)修改后,下一个 run 发给上游模型的请求
// 真的变了。断言主力是「该 run 的上游请求体是否包含独特标记」:三种协议
// 的 system 文本位置不同(anthropic 在 system、responses 在 instructions、
// chat completions 在 messages[0]),但标记只可能随 system 出现,对整个请求体
// 做 strings.Contains 即可,无需解析协议结构。
// 环境变量与 TestRealPiSmoke 相同(缺任一即跳过),另有可选的
// XLYRA_SMOKE_P4_MODELS(逗号分隔,默认两个代表性模型)。每个模型一个子
// 测试、独立 Store 与 capabilities.Store,场景各自新建会话;上游偶发 5xx 时
// 沿用整模型重试一次的策略。真实运行耗时可能超过默认 -timeout,执行时请
// 自行调大(如 -timeout 60m)。

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xlyra/server/internal/agent/capabilities"
	"xlyra/server/internal/agent/piconfig"
	"xlyra/server/internal/agent/runlog"
	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agent/translate"
)

const (
	smokeEnvP4Models = "XLYRA_SMOKE_P4_MODELS"

	smokeP4DefaultModels = "glm-5.3,claude-sonnet-5-5"

	smokeP4SkillName   = "xlyra-smoke-skill"
	smokeP4SkillMark   = "XLYRA-SKILL-MARK-7731" // 技能 description 里的独特标记,随系统提示进请求体
	smokeP4SecretCode  = "XL-P4-4821"            // 技能正文里的暗号,验证技能可被真正读取
	smokeP4AgentsMarkA = "XL-AGENTS-5521"        // AGENTS.md 第一版标记
	smokeP4AgentsMarkB = "XL-AGENTS-9907"        // AGENTS.md 更新后的标记
)

// smokeP4SkillMD:description 里的独特标记用于「技能出现在系统提示」的请求体
// 断言;正文暗号用于「技能可被真正使用」。
const smokeP4SkillMD = `---
name: xlyra-smoke-skill
description: P4 冒烟技能;XLYRA-SKILL-MARK-7731:当用户询问冒烟暗号时使用
---

# 冒烟暗号

冒烟暗号是 XL-P4-4821。用户询问冒烟暗号时,原样给出这个暗号,不要额外解释。
`

// smokeBodyLog 把每个上游请求的请求体完整留在内存里(绝不写日志、不打印):
// Send 前记 count() 作游标,run 收口后取 since(cursor) 即该 run 的全部请求体,
// 供 P4 冒烟做标记断言;请求体可能含会话正文,只做 Contains、绝不输出内容。
// 代理的 handler 与测试 goroutine 并发访问,由互斥保护。
type smokeBodyLog struct {
	mu     sync.Mutex
	bodies []string
}

func (b *smokeBodyLog) add(body string) {
	b.mu.Lock()
	b.bodies = append(b.bodies, body)
	b.mu.Unlock()
}

func (b *smokeBodyLog) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.bodies)
}

// since 返回第 n 次捕获之后(不含第 n 次)的全部请求体副本。
func (b *smokeBodyLog) since(n int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.bodies[n:]...)
}

func TestRealPiSmokeP4(t *testing.T) {
	env := smokePrepare(t)
	models := smokeP4Models(t)
	var summaries []*smokeSummary
	for i, model := range models {
		summary := &smokeSummary{model: model, protocol: smokeProtocol(model)}
		summaries = append(summaries, summary)
		t.Run(model, func(t *testing.T) {
			smokeModelSubtest(t, model, summary, func(at smokeAPI) (bool, string) {
				return runSmokeP4Model(at, t, env, model, i == 0, summary)
			})
		})
	}
	for _, summary := range summaries {
		t.Logf("P4 冒烟汇总 | 模型 %s | 协议 %s | 耗时 %s | 场景:%s | 代理请求 %d 次,状态码 %s,缺 Authorization %d 次",
			summary.model, summary.protocol, summary.duration.Round(time.Second),
			summary.stepsText(), summary.requests, summary.statuses, summary.noAuth)
	}
	if t.Failed() {
		t.Logf("runner 日志(已脱敏,仅失败时输出):\n%s", smokeSanitize(smokeTail(env.logs.String(), smokeLogTail), env.token))
	}
}

// smokeP4Models 读取 P4 专属模型清单,默认两个代表性模型。
func smokeP4Models(t *testing.T) []string {
	t.Helper()
	spec := strings.TrimSpace(os.Getenv(smokeEnvP4Models))
	if spec == "" {
		spec = smokeP4DefaultModels
	}
	var models []string
	for _, model := range strings.Split(spec, ",") {
		if model = strings.TrimSpace(model); model != "" {
			models = append(models, model)
		}
	}
	if len(models) == 0 {
		t.Skipf("%s 未提供任何模型", smokeEnvP4Models)
	}
	return models
}

// smokeP4Harness 聚合单个模型的 P4 场景运行依赖;每个场景各自新建会话。
type smokeP4Harness struct {
	at       smokeAPI
	env      *smokeEnv
	model    string
	agent    *Runner
	store    *session.Store
	caps     *capabilities.Store
	runs     *runlog.Registry
	faults   *[]string
	sessions []string
}

// runSmokeP4Model 按顺序执行一个模型的 P4 场景;任一场景失败立即返回,由代理
// 统计与错误文本推断是否疑似上游故障。first 表示是否为模型清单里的第一个
// (「技能可被真正使用」只对它执行)。返回值:(疑似上游故障, 摘要)。
func runSmokeP4Model(at smokeAPI, t *testing.T, env *smokeEnv, model string, first bool, summary *smokeSummary) (suspected bool, fault string) {
	at.Helper()
	started := time.Now()
	summary.reset(model)
	defer func() { summary.duration = time.Since(started).Round(time.Millisecond) }()

	store := session.NewStore(filepath.Join(t.TempDir(), "sessions"))
	caps := capabilities.NewStore(t.TempDir())
	runs := runlog.NewRegistry()
	var faults []string
	// Tools 留空用默认白名单;Capabilities 接真实能力存储,场景里就地改设置。
	agent, err := New(Config{
		Store: store, Runs: runs, Tokens: &smokeIssuer{token: env.token},
		Protocol:       func(context.Context, string) (string, error) { return smokeProtocol(model), nil },
		GatewayBaseURL: env.proxyURL,
		Command:        env.node, Args: []string{env.piEntry},
		ExtensionPath: env.extension,
		Capabilities:  caps,
		PathEnv:       os.Getenv("PATH"),
		RunTimeout:    smokeRunTimeout, AbortGrace: 10 * time.Second,
		Logger: slog.New(slog.NewTextHandler(env.logs, nil)),
	})
	if err != nil {
		at.Fatalf("New:%v", err)
	}
	h := &smokeP4Harness{
		at: at, env: env, model: model, agent: agent,
		store: store, caps: caps, runs: runs, faults: &faults,
	}
	// 无论成败都收干净 Pi 子进程:Shutdown 会等全部 run 收口(Process.Wait 完成)。
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), smokeShutdownTimeout)
		defer cancel()
		if err := agent.Shutdown(ctx); err != nil {
			at.Errorf("模型 %s Shutdown:%v", model, err)
		}
		for _, id := range h.sessions {
			if runs.Running(id) {
				at.Errorf("模型 %s 收尾后会话 %s 仍有运行中的 run", model, id)
			}
		}
	}()

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

	type stepSpec struct {
		name string
		run  func() bool
	}
	steps := []stepSpec{
		{"技能出现在系统提示", h.scenarioSkillInPrompt},
	}
	if first {
		// 模型是否真读技能有偶发性且耗时,只对第一个模型执行
		steps = append(steps, stepSpec{"技能可被真正使用", h.scenarioSkillUsed})
	}
	steps = append(steps,
		stepSpec{"停用技能后不再出现", h.scenarioSkillDisable},
		stepSpec{"AGENTS.md 生效与更新", h.scenarioAgentsMD},
		stepSpec{"上下文设置落盘并生效", h.scenarioContextSettings},
	)
	for _, item := range steps {
		if !step(item.name, item.run()) {
			return finish()
		}
	}
	return finish()
}

// newSession 为一个场景开新会话并登记,供收尾检查与文件定位。
func (h *smokeP4Harness) newSession() session.Meta {
	meta, err := h.store.Create(h.model, "full")
	if err != nil {
		h.at.Fatalf("Store.Create:%v", err)
	}
	h.sessions = append(h.sessions, meta.ID)
	return meta
}

// defaultP4Config 返回 P4 冒烟的基准配置:技能与 AGENTS.md 全开、默认水位线。
func defaultP4Config() capabilities.Config {
	return capabilities.Config{
		EnableSkills: true, EnableAgentsMD: true,
		DisabledSkills: []string{}, CompactTriggerRatio: 0.9,
	}
}

// putConfig 整体覆盖能力配置;每处显式写全部字段,避免上一场景的开关残留。
func (h *smokeP4Harness) putConfig(label string, cfg capabilities.Config) {
	h.at.Helper()
	if _, err := h.caps.PutConfig(cfg); err != nil {
		h.at.Fatalf("%s:PutConfig:%v", label, err)
	}
}

// send 发一条消息并收全事件(等 run 收口);失败时返回 nil。
func (h *smokeP4Harness) send(sessionID, content string) []translate.Event {
	if _, err := h.agent.Send(context.Background(), SendInput{
		SessionID: sessionID, Content: content, Model: h.model,
		ReasoningEffort: "low", PermissionMode: "full",
	}); err != nil {
		h.at.Errorf("Send(%q):%v", content, err)
		return nil
	}
	return smokeRunEvents(h.at, h.runs, sessionID, smokeCollectTimeout)
}

// sendChecked 在 send 之上断言终态 agent_done;第二返回值为 false 表示本次
// run 未正常收口(错误已报)。
func (h *smokeP4Harness) sendChecked(sessionID, content, label string) ([]translate.Event, bool) {
	h.at.Helper()
	events := h.send(sessionID, content)
	if events == nil {
		return nil, false
	}
	return events, smokeCheckTerminal(h.at, label, events, h.env.token, h.faults)
}

// runFreshSession 开新会话发一条消息并校验终态,返回该 run 的请求体游标。
// 「不含旧标记」类断言必须开新会话:同会话里助手旧回答会带着旧标记进
// 历史,污染后续请求体。
func (h *smokeP4Harness) runFreshSession(prompt, label string) (int, bool) {
	h.at.Helper()
	meta := h.newSession()
	cursor := h.env.bodies.count()
	if _, ok := h.sendChecked(meta.ID, prompt, label); !ok {
		return cursor, false
	}
	return cursor, true
}

// checkBodyContains 硬断言:该 run 的上游请求体中至少一个包含标记。
// 失败信息只报标记与请求数,绝不贴请求体内容。
func (h *smokeP4Harness) checkBodyContains(label string, cursor int, mark string) bool {
	h.at.Helper()
	bodies := h.env.bodies.since(cursor)
	if len(bodies) == 0 {
		h.at.Errorf("%s:该 run 未捕获任何上游请求体,无法断言标记 %s 出现", label, mark)
		return false
	}
	for _, body := range bodies {
		if strings.Contains(body, mark) {
			return true
		}
	}
	h.at.Errorf("%s:该 run 共 %d 个上游请求体,均不含标记 %s", label, len(bodies), mark)
	return false
}

// checkBodyAbsent 硬断言:该 run 的全部上游请求体都不含任一标记。
func (h *smokeP4Harness) checkBodyAbsent(label string, cursor int, marks ...string) bool {
	h.at.Helper()
	bodies := h.env.bodies.since(cursor)
	if len(bodies) == 0 {
		h.at.Errorf("%s:该 run 未捕获任何上游请求体,无法断言标记 %v 消失", label, marks)
		return false
	}
	pass := true
	for _, mark := range marks {
		for _, body := range bodies {
			if strings.Contains(body, mark) {
				h.at.Errorf("%s:该 run 仍有上游请求体包含标记 %s", label, mark)
				pass = false
				break
			}
		}
	}
	return pass
}

// scenarioSkillInPrompt:启用技能后,description 里的独特标记随系统提示进入
// 该 run 的上游请求体——证明「设置 → 下一个 run 的请求」链路真实生效。
func (h *smokeP4Harness) scenarioSkillInPrompt() bool {
	const label = "技能出现在系统提示"
	if _, err := h.caps.PutSkill(smokeP4SkillName, smokeP4SkillMD); err != nil {
		h.at.Fatalf("%s:PutSkill:%v", label, err)
	}
	h.putConfig(label, defaultP4Config())
	cursor, ok := h.runFreshSession("你好,请用一句话回答。", label)
	if !ok {
		return false
	}
	return h.checkBodyContains(label, cursor, smokeP4SkillMark)
}

// scenarioSkillUsed:模型应读 SKILL.md 后原样回答暗号。终态异常是硬失败;
// 最终文本不含暗号只警告不判失败(模型是否调用技能有偶发性,前一个场景的
// 请求体断言才是硬断言)。
func (h *smokeP4Harness) scenarioSkillUsed() bool {
	const label = "技能可被真正使用"
	meta := h.newSession()
	events, ok := h.sendChecked(meta.ID, "冒烟暗号是什么?只回答暗号。", label)
	if !ok {
		return false
	}
	if text := smokeText(events); !strings.Contains(text, smokeP4SecretCode) {
		h.at.Logf("%s:模型未使用技能(可能是模型偶发),最终文本前 200 字符:%s",
			label, smokeP3TextHead(text, h.env.token))
	}
	return true
}

// scenarioSkillDisable:同一会话里依次「单独停用 → 总开关关闭 → 恢复启用」,
// 每次设置变更后的下一个 run 请求体都要跟着变:停用时标记消失,恢复时
// 重新出现(可逆)。
func (h *smokeP4Harness) scenarioSkillDisable() bool {
	const label = "停用技能后不再出现"
	meta := h.newSession()

	cfg := defaultP4Config()
	cfg.DisabledSkills = []string{smokeP4SkillName}
	h.putConfig(label+"·DisabledSkills", cfg)
	cursor := h.env.bodies.count()
	if _, ok := h.sendChecked(meta.ID, "请直接回答 1+1 等于几?只回答数字。", label+"·DisabledSkills"); !ok {
		return false
	}
	if !h.checkBodyAbsent(label+"·DisabledSkills", cursor, smokeP4SkillMark) {
		return false
	}

	cfg = defaultP4Config()
	cfg.EnableSkills = false // DisabledSkills 清空:关的是总开关而不是单个技能
	h.putConfig(label+"·EnableSkills=false", cfg)
	cursor = h.env.bodies.count()
	if _, ok := h.sendChecked(meta.ID, "请直接回答 2+2 等于几?只回答数字。", label+"·EnableSkills=false"); !ok {
		return false
	}
	if !h.checkBodyAbsent(label+"·EnableSkills=false", cursor, smokeP4SkillMark) {
		return false
	}

	h.putConfig(label+"·恢复", defaultP4Config())
	cursor = h.env.bodies.count()
	if _, ok := h.sendChecked(meta.ID, "请直接回答 3+3 等于几?只回答数字。", label+"·恢复"); !ok {
		return false
	}
	return h.checkBodyContains(label+"·恢复", cursor, smokeP4SkillMark)
}

// scenarioAgentsMD:AGENTS.md 的规则标记要进下一个 run 的请求体;改内容、
// 删除、关总开关都要即时反映。「不含旧标记」的断言都用新会话,避免助手
// 旧回答里的暗号留在历史里造成误判。
func (h *smokeP4Harness) scenarioAgentsMD() bool {
	const label = "AGENTS.md 生效与更新"
	agentsMD := func(mark string) string {
		return "# 规则\n回答任何问题时都要在句末加上暗号 " + mark + "。\n"
	}

	if err := h.caps.PutAgentsMD(agentsMD(smokeP4AgentsMarkA)); err != nil {
		h.at.Fatalf("%s:PutAgentsMD:%v", label, err)
	}
	cursor, ok := h.runFreshSession("1+1 等于几?", label+"·写入")
	if !ok || !h.checkBodyContains(label+"·写入", cursor, smokeP4AgentsMarkA) {
		return false
	}

	if err := h.caps.PutAgentsMD(agentsMD(smokeP4AgentsMarkB)); err != nil {
		h.at.Fatalf("%s:PutAgentsMD:%v", label, err)
	}
	cursor, ok = h.runFreshSession("2+2 等于几?", label+"·更新")
	if !ok || !h.checkBodyContains(label+"·更新", cursor, smokeP4AgentsMarkB) ||
		!h.checkBodyAbsent(label+"·更新", cursor, smokeP4AgentsMarkA) {
		return false
	}

	if err := h.caps.DeleteAgentsMD(); err != nil {
		h.at.Fatalf("%s:DeleteAgentsMD:%v", label, err)
	}
	cursor, ok = h.runFreshSession("3+3 等于几?", label+"·删除")
	if !ok || !h.checkBodyAbsent(label+"·删除", cursor, smokeP4AgentsMarkA, smokeP4AgentsMarkB) {
		return false
	}

	// 总开关关闭:磁盘上有 AGENTS.md 也不加载
	if err := h.caps.PutAgentsMD(agentsMD(smokeP4AgentsMarkA)); err != nil {
		h.at.Fatalf("%s:PutAgentsMD:%v", label, err)
	}
	cfg := defaultP4Config()
	cfg.EnableAgentsMD = false
	h.putConfig(label+"·EnableAgentsMD=false", cfg)
	cursor, ok = h.runFreshSession("4+4 等于几?", label+"·EnableAgentsMD=false")
	if !ok || !h.checkBodyAbsent(label+"·EnableAgentsMD=false", cursor, smokeP4AgentsMarkA, smokeP4AgentsMarkB) {
		return false
	}
	// 收尾恢复总开关,不影响后续场景
	h.putConfig(label, defaultP4Config())
	return true
}

// scenarioContextSettings:水位线与窗口换算落进 <pi-agent>/settings.json 的
// compaction.reserveTokens 与 models.json 的 contextWindow,改设置后的下一
// 个 run 立即换成新值(不需要真实触发压缩)。任务示例的第二组 0.9/200000
// 与第一组 0.8/100000 算出的 reserve 同为 20000,故按任务备注换成 0.5 开头
// 的取值,并让 reserve 与 window 都变化:0.8/100000 → 20000/100000,
// 0.5/200000 → 100000/200000。
func (h *smokeP4Harness) scenarioContextSettings() bool {
	const label = "上下文设置落盘并生效"
	check := func(stepLabel, sessionID string, wantReserve, wantWindow int) bool {
		h.at.Helper()
		agentDir := filepath.Join(h.store.Dir(sessionID), "pi-agent")
		settings, err := os.ReadFile(filepath.Join(agentDir, "settings.json"))
		if err != nil {
			h.at.Errorf("%s:读取 settings.json:%v", stepLabel, err)
			return false
		}
		var settingsFile struct {
			Compaction struct {
				Enabled       bool `json:"enabled"`
				ReserveTokens int  `json:"reserveTokens"`
			} `json:"compaction"`
		}
		if err := json.Unmarshal(settings, &settingsFile); err != nil {
			h.at.Errorf("%s:解析 settings.json:%v", stepLabel, err)
			return false
		}
		models, err := os.ReadFile(filepath.Join(agentDir, "models.json"))
		if err != nil {
			h.at.Errorf("%s:读取 models.json:%v", stepLabel, err)
			return false
		}
		var modelsFile struct {
			Providers map[string]struct {
				Models []struct {
					ContextWindow int `json:"contextWindow"`
				} `json:"models"`
			} `json:"providers"`
		}
		if err := json.Unmarshal(models, &modelsFile); err != nil {
			h.at.Errorf("%s:解析 models.json:%v", stepLabel, err)
			return false
		}
		provider, ok := modelsFile.Providers[piconfig.ProviderName]
		if !ok || len(provider.Models) == 0 {
			h.at.Errorf("%s:models.json 缺少 provider %s 的模型条目", stepLabel, piconfig.ProviderName)
			return false
		}
		pass := true
		if !settingsFile.Compaction.Enabled || settingsFile.Compaction.ReserveTokens != wantReserve {
			h.at.Errorf("%s:compaction = enabled(%v) + reserveTokens %d,want enabled + %d",
				stepLabel, settingsFile.Compaction.Enabled, settingsFile.Compaction.ReserveTokens, wantReserve)
			pass = false
		}
		if window := provider.Models[0].ContextWindow; window != wantWindow {
			h.at.Errorf("%s:contextWindow = %d,want %d", stepLabel, window, wantWindow)
			pass = false
		}
		return pass
	}

	meta := h.newSession()
	cfg := defaultP4Config()
	cfg.CompactTriggerRatio, cfg.ContextWindow = 0.8, 100000
	h.putConfig(label+"·第一组", cfg)
	if _, ok := h.sendChecked(meta.ID, "请直接回答 5+5 等于几?只回答数字。", label+"·第一组"); !ok {
		return false
	}
	if !check(label+"·第一组", meta.ID, 20000, 100000) {
		return false
	}

	cfg = defaultP4Config()
	cfg.CompactTriggerRatio, cfg.ContextWindow = 0.5, 200000
	h.putConfig(label+"·第二组", cfg)
	if _, ok := h.sendChecked(meta.ID, "请直接回答 6+6 等于几?只回答数字。", label+"·第二组"); !ok {
		return false
	}
	return check(label+"·第二组", meta.ID, 100000, 200000)
}
