package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xlyra/server/internal/agent"
	"xlyra/server/internal/agent/capabilities"
	"xlyra/server/internal/agent/isolation"
	"xlyra/server/internal/agent/runlog"
	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/config"
)

// 布置 <root>/agent-runtime/{node_modules/@earendil-works/pi-coding-agent/dist/
// bundle/cli.js, extension/xlyra.ts} 的仓库布局,返回入口与扩展路径。
func makeAgentRuntime(t *testing.T, root string, withExtension bool) (entry, extension string) {
	t.Helper()
	entry = filepath.Join(root, "agent-runtime", "node_modules", "@earendil-works",
		"pi-coding-agent", "dist", "bundle", "cli.js")
	if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte("// pi entry\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	extension = filepath.Join(root, "agent-runtime", "extension", "xlyra.ts")
	if err := os.MkdirAll(filepath.Dir(extension), 0o755); err != nil {
		t.Fatal(err)
	}
	if withExtension {
		if err := os.WriteFile(extension, []byte("// xlyra extension\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return entry, extension
}

// TestAgentExtensionPathDerivation:显式配置优先;未配置时按 Pi 入口向上找
// node_modules 祖先推导;推导不到或文件不存在返回空(交给 Health 报不可用)。
func TestAgentExtensionPathDerivation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	entry, extension := makeAgentRuntime(t, root, true)

	if got := agentExtensionPath("", entry); got != extension {
		t.Errorf("agentExtensionPath(未配置) = %q, want %q", got, extension)
	}
	if got := agentExtensionPath("  ", entry); got != extension {
		t.Errorf("agentExtensionPath(空白配置) = %q, want %q(应回退推导)", got, extension)
	}
	if got := agentExtensionPath("/opt/xlyra/xlyra.ts", entry); got != "/opt/xlyra/xlyra.ts" {
		t.Errorf("agentExtensionPath(显式配置) = %q, want 原样返回", got)
	}
	// 显式路径不做存在性校验,由 runner.Health 统一把关。
	if got := agentExtensionPath("/missing/xlyra.ts", entry); got != "/missing/xlyra.ts" {
		t.Errorf("agentExtensionPath(配置缺失文件) = %q, want 原样返回", got)
	}

	// 推导目标不存在 → 空。
	if got := agentExtensionPath("", filepath.Join(root, "other.js")); got != "" {
		t.Errorf("无 node_modules 祖先时 = %q, want 空", got)
	}
	bare, _ := makeAgentRuntime(t, t.TempDir(), false)
	if got := agentExtensionPath("", bare); got != "" {
		t.Errorf("扩展文件不存在时 = %q, want 空", got)
	}
	if got := agentExtensionPath("", ""); got != "" {
		t.Errorf("空入口 = %q, want 空", got)
	}
}

// TestNewBuiltinRunnerWiresExtension:装配时从 Pi 入口推导扩展;扩展缺失时
// runner 的 Health 因写类工具 fail-closed 而不可用。
func TestNewBuiltinRunnerWiresExtension(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	entry, extension := makeAgentRuntime(t, root, true)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	build := func() (agent.Runner, func(context.Context)) {
		cfg := testConfig()
		cfg.AgentEngine = "pi"
		cfg.AgentNodeBin = os.Args[0]
		cfg.AgentPiEntry = entry
		store := session.NewStore(filepath.Join(root, "sessions"))
		// 决策为 on 的形态(工具全开 + 隔离配置),不实际探测
		decision := decideIsolation("off", false, isolation.Capability{}, nil)
		bins := resolveAgentBins(cfg)
		run, shutdown := newBuiltinRunner(cfg, logger, nil, nil, store, runlog.NewRegistry(), fakeBuiltinTokenIssuer{}, capabilities.NewStore(filepath.Join(root, "caps")), decision, bins)
		return run, shutdown
	}

	run, shutdown := build()
	defer shutdown(context.Background())
	health := run.Health()
	if !health.Available {
		t.Fatalf("推导到扩展时 Health 应可用: %+v", health)
	}

	// 移除扩展后重新装配:推导为空,Health 报缺少扩展。
	if err := os.Remove(extension); err != nil {
		t.Fatal(err)
	}
	broken, brokenShutdown := build()
	defer brokenShutdown(context.Background())
	brokenHealth := broken.Health()
	if brokenHealth.Available || !strings.Contains(brokenHealth.Reason, "扩展") {
		t.Fatalf("扩展缺失时 Health = %+v, want 不可用并说明缺扩展", brokenHealth)
	}
}

// TestBuiltinAgentWiresCapabilities:pi 引擎装配后 skills/workspace/config
// 路由接到真实能力存储,目录体系为 <AgentDataDir>/{skills,AGENTS.md,config.json}。
func TestBuiltinAgentWiresCapabilities(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.AgentEngine = "pi"
	cfg.AgentDataDir = t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	builtin := newBuiltinAgent(cfg, logger, nil, nil, fakeBuiltinTokenIssuer{}, nil)
	router := newAgentOnlyRouter(cfg, builtin)

	do := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(method, path, reader))
		return rec
	}

	// PUT /skills 落盘 <AgentDataDir>/skills/<name>/SKILL.md。
	skill := "---\nname: demo\ndescription: 演示\n---\n内容"
	content, _ := json.Marshal(map[string]string{"content": skill})
	rec := do(http.MethodPut, "/api/v1/agent/skills/demo", string(content))
	if rec.Code != http.StatusOK {
		t.Fatalf("put skill status = %d, body %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(cfg.AgentDataDir, "skills", "demo", "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md 未落盘: %v", err)
	}
	rec = do(http.MethodGet, "/api/v1/agent/skills", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "demo") {
		t.Fatalf("list skills = %d %s", rec.Code, rec.Body.String())
	}

	// AGENTS.md 走 <AgentDataDir>/AGENTS.md。
	rec = do(http.MethodPut, "/api/v1/agent/workspace/file", `{"path":"AGENTS.md","content":"# 人格"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put agents md status = %d, body %s", rec.Code, rec.Body.String())
	}
	if data, err := os.ReadFile(filepath.Join(cfg.AgentDataDir, "AGENTS.md")); err != nil || string(data) != "# 人格" {
		t.Fatalf("AGENTS.md 落盘异常: %q %v", string(data), err)
	}

	// config 合并写入 <AgentDataDir>/config.json。
	rec = do(http.MethodPut, "/api/v1/agent/config", `{"agent":{"compact_trigger_ratio":0.8}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put config status = %d, body %s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(cfg.AgentDataDir, "config.json"))
	if err != nil {
		t.Fatalf("config.json 未落盘: %v", err)
	}
	var stored capabilities.Config
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("解析 config.json: %v", err)
	}
	if stored.CompactTriggerRatio != 0.8 || !stored.EnableSkills {
		t.Fatalf("config.json = %+v", stored)
	}
	rec = do(http.MethodGet, "/api/v1/agent/config", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "0.8") {
		t.Fatalf("get config = %d %s", rec.Code, rec.Body.String())
	}
}

// TestDecideIsolation:模式 × 探测 × 自检的全组合决策表(fail-closed)。
func TestDecideIsolation(t *testing.T) {
	t.Parallel()

	detectOK := isolation.Capability{OK: true}
	detectBad := isolation.Capability{Reason: "需要 root 运行(当前 euid=501)"}
	checkOK := &isolation.Report{OK: true, Checks: []isolation.Check{{Name: "自有目录可读写", Passed: true}}}
	checkBad := &isolation.Report{Checks: []isolation.Check{
		{Name: "敏感路径不可读", Passed: false, Detail: "/workdir/conf: 预期读不到但成功"},
		{Name: "跨会话隔离", Passed: false, Detail: "…"},
	}}

	cases := []struct {
		name          string
		mode          string
		sandboxOpen   bool
		detect        isolation.Capability
		check         *isolation.Report
		wantMode      string
		wantTools     []string
		wantIso       bool
		reasonParts   []string
	}{
		{"off 忽略探测与自检", "off", true, detectBad, nil, "off", fullAgentTools, false, nil},
		{"off 即使探测通过", "off", true, detectOK, checkBad, "off", fullAgentTools, false, nil},
		{"auto 沙箱未开直接不隔离", "auto", false, detectBad, nil, "off", fullAgentTools, false, []string{"沙箱未启用", "直接在本机执行"}},
		{"auto 沙箱未开即使探测通过", "auto", false, detectOK, checkOK, "off", fullAgentTools, false, nil},
		{"required 忽略沙箱开关", "required", false, detectBad, nil, "failed", nil, false, []string{"需要 root"}},
		{"auto 探测失败降级只读", "auto", true, detectBad, nil, "degraded", readOnlyAgentTools, false, []string{"需要 root"}},
		{"auto 探测失败不看自检", "auto", true, detectBad, checkOK, "degraded", readOnlyAgentTools, false, []string{"需要 root"}},
		{"required 探测失败直接关闭", "required", true, detectBad, nil, "failed", nil, false, []string{"需要 root"}},
		{"auto 探测通过自检未执行 fail-closed", "auto", true, detectOK, nil, "failed", nil, false, []string{"自检未执行"}},
		{"required 探测通过自检未执行 fail-closed", "required", true, detectOK, nil, "failed", nil, false, []string{"自检未执行"}},
		{"auto 探测通过自检通过", "auto", true, detectOK, checkOK, "on", fullAgentTools, true, nil},
		{"required 探测通过自检通过", "required", true, detectOK, checkOK, "on", fullAgentTools, true, nil},
		{"auto 自检失败绝不降级运行", "auto", true, detectOK, checkBad, "failed", nil, false, []string{"敏感路径不可读", "跨会话隔离"}},
		{"required 自检失败直接关闭", "required", true, detectOK, checkBad, "failed", nil, false, []string{"敏感路径不可读", "跨会话隔离"}},
		{"未知模式按 required 处理且忽略开关", "force", false, detectBad, nil, "failed", nil, false, []string{"需要 root"}},
		{"空模式按 required 处理", "", true, detectOK, nil, "failed", nil, false, []string{"自检未执行"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideIsolation(tc.mode, tc.sandboxOpen, tc.detect, tc.check)
			if got.Mode != tc.wantMode {
				t.Fatalf("Mode = %q, want %q", got.Mode, tc.wantMode)
			}
			if (got.Isolation != nil) != tc.wantIso {
				t.Fatalf("Isolation = %v, want 非 nil: %v", got.Isolation, tc.wantIso)
			}
			if fmt.Sprint(got.Tools) != fmt.Sprint(tc.wantTools) {
				t.Fatalf("Tools = %v, want %v", got.Tools, tc.wantTools)
			}
			for _, part := range tc.reasonParts {
				if !strings.Contains(got.Reason, part) {
					t.Fatalf("Reason %q 应包含 %q", got.Reason, part)
				}
			}
			// status 输出与决策一致
			st := got.status()
			if st.Mode != got.Mode || st.Reason != got.Reason {
				t.Fatalf("status 与决策不一致: %+v vs %+v", st, got)
			}
			write := false
			for _, tool := range got.Tools {
				if tool == "write" || tool == "edit" {
					write = true
				}
			}
			if st.WriteToolsEnabled != write {
				t.Fatalf("WriteToolsEnabled = %v, want %v", st.WriteToolsEnabled, write)
			}
		})
	}
}

// TestPrepareIsolationSkipsWhenPiNotReady:Pi 入口/扩展未就绪时不做探测与
// 权限变更,返回未评估的空决策。
func TestPrepareIsolationSkipsWhenPiNotReady(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dataDir := filepath.Join(t.TempDir(), "agent")
	decision := prepareIsolation(testConfig(), logger, dataDir, capabilities.NewStore(dataDir), agentBins{})
	if decision.Mode != "" || decision.Isolation != nil || decision.Tools != nil {
		t.Fatalf("Pi 未就绪时应返回空决策,得到 %+v", decision)
	}
	if _, err := os.Lstat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("Pi 未就绪时不应创建数据目录: %v", err)
	}
}

// agentHealth 通过 HTTP 路由拉取 /health 并解码 body。
func agentHealth(t *testing.T, cfg config.Config) (int, map[string]any) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	builtin := newBuiltinAgent(cfg, logger, nil, nil, fakeBuiltinTokenIssuer{}, nil)
	router := newAgentOnlyRouter(cfg, builtin)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/agent/health", nil))
	body := map[string]any{}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode health body: %v", err)
	}
	return rec.Code, body
}

// readyAgentConfig 返回 bins 就绪(node=测试二进制、Pi 入口与扩展在临时
// 目录)的 pi 引擎配置;WORKDIR 指向临时目录,避免探测误碰真实仓库。
func readyAgentConfig(t *testing.T, isolationMode string) config.Config {
	t.Helper()
	workdir := t.TempDir()
	t.Setenv("WORKDIR", workdir)
	root := t.TempDir()
	entry, _ := makeAgentRuntime(t, root, true)
	cfg := testConfig()
	cfg.AgentEngine = "pi"
	cfg.AgentNodeBin = os.Args[0]
	cfg.AgentPiEntry = entry
	cfg.AgentDataDir = filepath.Join(workdir, "agent")
	cfg.AgentIsolation = isolationMode
	return cfg
}

// TestNewBuiltinAgentIsolationOff:AGENT_ISOLATION=off 时不隔离、工具全开,
// health 报 isolation=off 且写工具可用(任何环境都成立:不做探测)。
func TestNewBuiltinAgentIsolationOff(t *testing.T) {
	// readyAgentConfig 用 t.Setenv 固定 WORKDIR,不能并行
	cfg := readyAgentConfig(t, "off")
	code, body := agentHealth(t, cfg)
	if code != http.StatusOK {
		t.Fatalf("off 模式 health = %d, body %v", code, body)
	}
	if body["isolation"] != "off" || body["write_tools_enabled"] != true || body["sandbox"] != "off" || body["bash_enabled"] != true {
		t.Fatalf("off 模式隔离状态 = %v", body)
	}
	if _, has := body["reason"]; has {
		t.Fatalf("runner 可用时不应有 reason: %v", body)
	}
}

// TestNewBuiltinAgentSandboxDisabled:默认沙箱未开启时 auto 直接不隔离,工具全开
// 且 bash 开放(命令在本机执行);AGENT_BASH=off 时 bash 关闭。
func TestNewBuiltinAgentSandboxDisabled(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 环境下 uid 隔离路径由集成测试覆盖")
	}
	for name, bashMode := range map[string]string{"": "", "bash off": "off"} {
		cfg := readyAgentConfig(t, "auto")
		cfg.AgentBash = bashMode
		code, body := agentHealth(t, cfg)
		if code != http.StatusOK || body["status"] != "healthy" {
			t.Fatalf("%s: health = %d %v", name, code, body)
		}
		if body["isolation"] != "off" || body["write_tools_enabled"] != true || body["sandbox"] != "off" {
			t.Fatalf("%s: 隔离状态 = %v", name, body)
		}
		wantBash := bashMode != "off"
		if body["bash_enabled"] != wantBash {
			t.Fatalf("%s: bash_enabled = %v, want %v", name, body["bash_enabled"], wantBash)
		}
		if reason, _ := body["isolation_reason"].(string); !strings.Contains(reason, "沙箱未启用") {
			t.Fatalf("%s: isolation_reason = %q", name, reason)
		}
	}
}

// TestNewBuiltinAgentDegradedOnNonRoot:非 root(本地开发)下 auto 降级为
// 只读工具,runner 本身仍可用。
func TestNewBuiltinAgentDegradedOnNonRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("本用例验证非 root 行为,root 环境跳过(容器内由集成测试覆盖)")
	}
	// readyAgentConfig 用 t.Setenv 固定 WORKDIR,不能并行
	cfg := readyAgentConfig(t, "auto")
	// auto 降级用例需先在 Agent 设置里开启沙箱，否则默认不隔离。
	if _, err := capabilities.NewStore(cfg.AgentDataDir).PutConfig(capabilities.Config{
		EnableAgentsMD: true, EnableSkills: true, DisabledSkills: []string{},
		CompactTriggerRatio: 0.9, SandboxEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	code, body := agentHealth(t, cfg)
	if code != http.StatusOK {
		t.Fatalf("degraded 模式 health = %d, body %v", code, body)
	}
	if body["status"] != "healthy" {
		t.Fatalf("degraded 仍应 healthy: %v", body)
	}
	if body["isolation"] != "degraded" {
		t.Fatalf("isolation = %v, want degraded", body["isolation"])
	}
	if body["write_tools_enabled"] != false {
		t.Fatalf("degraded 应关闭写工具: %v", body)
	}
	if body["sandbox"] != "off" || body["bash_enabled"] != false {
		t.Fatalf("degraded 应关闭沙箱与 bash: %v", body)
	}
	if reason, _ := body["isolation_reason"].(string); !strings.Contains(reason, "root") {
		t.Fatalf("isolation_reason = %v, 应说明非 root", body["isolation_reason"])
	}
}

// TestNewBuiltinAgentRequiredFailsOnNonRoot:非 root 下 required 直接关闭
// Agent,health 不可用并带失败原因。
func TestNewBuiltinAgentRequiredFailsOnNonRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("本用例验证非 root 行为,root 环境跳过(容器内由集成测试覆盖)")
	}
	// readyAgentConfig 用 t.Setenv 固定 WORKDIR,不能并行
	cfg := readyAgentConfig(t, "required")
	code, body := agentHealth(t, cfg)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("required + 非 root 应不可用,health = %d, body %v", code, body)
	}
	if body["status"] != "unhealthy" {
		t.Fatalf("status = %v", body["status"])
	}
	if body["isolation"] != "failed" {
		t.Fatalf("isolation = %v, want failed", body["isolation"])
	}
	if body["write_tools_enabled"] != false {
		t.Fatalf("failed 应无写工具: %v", body)
	}
	if reason, _ := body["reason"].(string); !strings.Contains(reason, "root") {
		t.Fatalf("reason = %v, 应说明根因", body["reason"])
	}
}
