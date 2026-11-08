package app

// 容器集成测试:需要 Linux 容器内以 root 运行并显式设置
// AGENT_ISOLATION_INTEGRATION=1,其余环境跳过。覆盖完整启动流程:
// 探测 → 修权限 → 收紧 WORKDIR → 自检 → 决策 on/failed。

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"xlyra/server/internal/agent/isolation"
	"xlyra/server/internal/config"
)

func requireAgentIsolationIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("AGENT_ISOLATION_INTEGRATION") != "1" || runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("需要 Linux + root + AGENT_ISOLATION_INTEGRATION=1")
	}
}

// makeWorkdirTraversable 把 dir 及其到系统临时目录之间的祖先放宽到 0711
// (只给"穿越"位),否则会话身份走不到 WORKDIR,自检的正向对照会失败。
func makeWorkdirTraversable(t *testing.T, dir string) {
	t.Helper()
	stop := filepath.Clean(os.TempDir())
	for cur := filepath.Clean(dir); cur != stop && cur != string(filepath.Separator) && cur != "."; cur = filepath.Dir(cur) {
		if err := os.Chmod(cur, 0o711); err != nil {
			t.Fatal(err)
		}
	}
}

// buildIsolationWorkdir 搭建一个故意权限宽松的 WORKDIR:conf/master.key、
// logs/ 与 README.md 对其他用户可读;Agent 数据目录含一个权限
// 过严的技能(考验 FixPermissions);Pi 入口与扩展放在 WORKDIR/agent-runtime
// 下(考验收紧排除规则)。返回就绪的 pi 引擎配置。
func buildIsolationWorkdir(t *testing.T, isolationMode string) config.Config {
	t.Helper()
	workdir, err := os.MkdirTemp("", "ag")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(workdir) })
	t.Setenv("WORKDIR", workdir)

	// 敏感内容:目录 0777、文件 0666,故意让其他用户可读
	sensitiveDirs := []string{"conf", "logs"}
	for _, name := range sensitiveDirs {
		if err := os.MkdirAll(filepath.Join(workdir, name), 0o777); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workdir, "conf", "master.key"), []byte("key"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "logs", "x.log"), []byte("log"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "README.md"), []byte("readme"), 0o666); err != nil {
		t.Fatal(err)
	}
	for _, p := range sensitiveDirs {
		if err := os.Chmod(filepath.Join(workdir, p), 0o777); err != nil {
			t.Fatal(err)
		}
	}

	// Agent 数据目录与技能(技能权限故意过严,启动时应被修成 0755/0644)
	dataDir := filepath.Join(workdir, "agent")
	skill := filepath.Join(dataDir, "skills", "demo")
	if err := os.MkdirAll(skill, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"),
		[]byte("---\nname: demo\ndescription: 演示\n---\n正文"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dataDir, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Pi 入口与扩展放在 WORKDIR 之下:验证它们不随收紧而锁死
	entry, _ := makeAgentRuntime(t, workdir, true)

	// 会话身份要能从系统临时目录走到 WORKDIR
	makeWorkdirTraversable(t, workdir)

	cfg := testConfig()
	cfg.AgentNodeBin = os.Args[0]
	cfg.AgentPiEntry = entry
	cfg.AgentDataDir = dataDir
	cfg.AgentIsolation = isolationMode
	return cfg
}

// fetchIsolationHealth 装配内置 Agent 并拉取 /health。
func fetchIsolationHealth(t *testing.T, cfg config.Config) (int, map[string]any) {
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
	t.Logf("health = %d %v", rec.Code, body)
	return rec.Code, body
}

func assertPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if fi.Mode().Perm() != want {
		t.Fatalf("%s 权限 = %o, want %o", path, fi.Mode().Perm(), want)
	}
}

// TestAgentIsolationIntegrationOn:完整启动流程后 WORKDIR 敏感子项被收紧、
// 技能权限被修复、数据目录可穿越,决策为 on 且写工具开放。
func TestAgentIsolationIntegrationOn(t *testing.T) {
	requireAgentIsolationIntegration(t)
	cfg := buildIsolationWorkdir(t, "auto")
	workdir := config.ResolveWorkdir()

	code, body := fetchIsolationHealth(t, cfg)
	if code != http.StatusOK {
		t.Fatalf("health = %d, want 200(隔离成立时应可用)", code)
	}
	if body["isolation"] != "on" {
		t.Fatalf("isolation = %v, want on", body["isolation"])
	}
	if body["write_tools_enabled"] != true {
		t.Fatalf("write_tools_enabled = %v, want true", body["write_tools_enabled"])
	}

	// 敏感子项被收紧(目录 0700、文件 0600)
	for _, name := range []string{"conf", "logs"} {
		assertPerm(t, filepath.Join(workdir, name), 0o700)
	}
	// 配置目录里的文件本身也收紧(密钥所在,第二层防御);其他目录只收紧顶层,
	// 目录 0700 已挡住其他用户,不递归 chmod。
	assertPerm(t, filepath.Join(workdir, "conf", "master.key"), 0o600)
	assertPerm(t, filepath.Join(workdir, "README.md"), 0o600)

	// Agent 数据目录整条路径不收紧,且其他用户可穿越(会话身份要能到达)
	dataDir := filepath.Join(workdir, "agent")
	if err := isolation.PrepareDataDir(dataDir); err != nil {
		t.Fatal(err)
	}
	assertPerm(t, dataDir, 0o711)
	assertPerm(t, filepath.Join(dataDir, "sessions"), 0o711)
	wd, err := os.Stat(workdir)
	if err != nil {
		t.Fatal(err)
	}
	if wd.Mode().Perm()&0o001 == 0 {
		t.Fatalf("WORKDIR 缺少其他用户可穿越位: %o", wd.Mode().Perm())
	}

	// 技能目录树被一次性修成 0755/0644(会话身份要能读)
	assertPerm(t, filepath.Join(dataDir, "skills"), 0o755)
	assertPerm(t, filepath.Join(dataDir, "skills", "demo"), 0o755)
	assertPerm(t, filepath.Join(dataDir, "skills", "demo", "SKILL.md"), 0o644)

	// Pi 运行时目录未被收紧(会话身份要能读入口与扩展)
	entry, err := os.Stat(cfg.AgentPiEntry)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Mode().Perm()&0o004 == 0 {
		t.Fatalf("Pi 入口应对其他用户可读: %o", entry.Mode().Perm())
	}
}

// TestAgentIsolationIntegrationSelfCheckFail:自检注入失败时,auto 与
// required 都必须 failed(fail-closed,绝不降级运行 Pi)。
func TestAgentIsolationIntegrationSelfCheckFail(t *testing.T) {
	requireAgentIsolationIntegration(t)

	original := selfCheckFn
	selfCheckFn = func(_ context.Context, _ isolation.Config, _ isolation.SelfCheckInput) isolation.Report {
		return isolation.Report{Checks: []isolation.Check{{
			Name:   "敏感路径不可读",
			Passed: false,
			Detail: "集成测试注入失败",
		}}}
	}
	t.Cleanup(func() { selfCheckFn = original })

	for _, mode := range []string{"auto", "required"} {
		t.Run(mode, func(t *testing.T) {
			cfg := buildIsolationWorkdir(t, mode)
			code, body := fetchIsolationHealth(t, cfg)
			if code != http.StatusServiceUnavailable {
				t.Fatalf("health = %d, want 503", code)
			}
			if body["isolation"] != "failed" {
				t.Fatalf("isolation = %v, want failed(auto 也不得降级)", body["isolation"])
			}
			if body["write_tools_enabled"] != false {
				t.Fatalf("write_tools_enabled = %v, want false", body["write_tools_enabled"])
			}
			reason, _ := body["reason"].(string)
			if !strings.Contains(reason, "隔离") {
				t.Fatalf("reason = %q, 应说明隔离失败", reason)
			}
			if isoReason, _ := body["isolation_reason"].(string); !strings.Contains(isoReason, "敏感路径不可读") {
				t.Fatalf("isolation_reason = %q, 应汇总失败检查项", isoReason)
			}
		})
	}
}
