package app

// 容器集成测试:AGENT_SANDBOX_INTEGRATION=1 且 Linux root 时运行。

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"xlyra/server/internal/agent/execd"
	"xlyra/server/internal/agent/isolation"
	"xlyra/server/internal/agent/sandbox"
	"xlyra/server/internal/agent/session"
)

func requireAgentSandboxIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("AGENT_SANDBOX_INTEGRATION") != "1" || runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("需要 Linux + root + AGENT_SANDBOX_INTEGRATION=1")
	}
}

func startResidualProcess(t *testing.T, id isolation.Identity) int {
	t.Helper()
	cmd, err := sandbox.ChildCommand(os.Args[0], sandbox.Spec{
		UID: int(id.PiUID), GID: int(id.GID), Workspace: "/", Net: sandbox.NetNone, Umask: -1,
	}, []string{"/bin/sleep", "300"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid
}

func requireUIDGone(t *testing.T, uid uint32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		alive := false
		for _, proc := range sandbox.ProcsOfUID(int(uid)) {
			if proc.State != "Z" {
				alive = true
			}
		}
		if !alive {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("uid %d 仍有残留进程: %+v", uid, sandbox.ProcsOfUID(int(uid)))
}

func TestAgentSandboxIntegrationOnAndSweep(t *testing.T) {
	requireAgentSandboxIntegration(t)
	cfg := buildIsolationWorkdir(t, "auto")
	identity, err := isolation.IdentityFor(isolation.Config{UIDBase: cfg.AgentUIDBase, MaxSlots: cfg.AgentMaxSlots}, 1)
	if err != nil {
		t.Fatal(err)
	}
	startResidualProcess(t, identity)
	code, body := fetchIsolationHealth(t, cfg)
	if code != http.StatusOK || body["isolation"] != "on" || body["sandbox"] != "on" || body["bash_enabled"] != true {
		t.Fatalf("health = %d %v", code, body)
	}
	requireUIDGone(t, identity.PiUID)
}

func TestAgentSandboxIntegrationSelfCheckFailure(t *testing.T) {
	requireAgentSandboxIntegration(t)
	original := execSelfCheckFn
	execSelfCheckFn = func(context.Context, execd.CheckInput) isolation.Report {
		return isolation.Report{Checks: []isolation.Check{{Name: "无网络", Detail: "集成测试注入失败"}}}
	}
	t.Cleanup(func() { execSelfCheckFn = original })
	cfg := buildIsolationWorkdir(t, "auto")
	code, body := fetchIsolationHealth(t, cfg)
	if code != http.StatusOK || body["status"] != "healthy" || body["isolation"] != "on" ||
		body["sandbox"] != "failed" || body["bash_enabled"] != false || body["write_tools_enabled"] != true {
		t.Fatalf("health = %d %v", code, body)
	}
	if reason, _ := body["sandbox_reason"].(string); !strings.Contains(reason, "无网络") {
		t.Fatalf("sandbox_reason = %q", reason)
	}
}

func TestAgentSandboxIntegrationBashOff(t *testing.T) {
	requireAgentSandboxIntegration(t)
	cfg := buildIsolationWorkdir(t, "auto")
	cfg.AgentBash = "off"
	code, body := fetchIsolationHealth(t, cfg)
	if code != http.StatusOK || body["sandbox"] != "on" || body["bash_enabled"] != false {
		t.Fatalf("health = %d %v", code, body)
	}
}

func TestAgentSandboxIntegrationSocketPathTooLong(t *testing.T) {
	requireAgentSandboxIntegration(t)
	cfg := buildIsolationWorkdir(t, "auto")
	cfg.AgentDataDir = filepath.Join(cfg.AgentDataDir, strings.Repeat("a", 100))
	code, body := fetchIsolationHealth(t, cfg)
	if code != http.StatusOK || body["sandbox"] != "failed" || body["bash_enabled"] != false {
		t.Fatalf("health = %d %v", code, body)
	}
	if reason, _ := body["sandbox_reason"].(string); !strings.Contains(reason, "路径过长") || !strings.Contains(reason, "AGENT_DATA_DIR") {
		t.Fatalf("sandbox_reason = %q", reason)
	}
}

func TestAgentSandboxIntegrationDeleteSessionKillsProcesses(t *testing.T) {
	requireAgentSandboxIntegration(t)
	cfg := buildIsolationWorkdir(t, "auto")
	code, body := fetchIsolationHealth(t, cfg)
	if code != http.StatusOK || body["sandbox"] != "on" {
		t.Fatalf("health = %d %v", code, body)
	}
	store := session.NewStore(filepath.Join(cfg.AgentDataDir, "sessions"))
	meta, err := store.Create("m", "ask")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(meta.ID, func(m *session.Meta) error {
		m.Slot = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	identity, err := isolation.IdentityFor(isolation.Config{UIDBase: cfg.AgentUIDBase, MaxSlots: cfg.AgentMaxSlots}, 1)
	if err != nil {
		t.Fatal(err)
	}
	startResidualProcess(t, identity)
	// 重新装配只为取得已创建会话使用的同一个 store 与 router。
	builtin := newBuiltinAgent(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, fakeBuiltinTokenIssuer{}, nil)
	router := newAgentOnlyRouter(cfg, builtin)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/agent/sessions/"+meta.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d, body %s", rec.Code, rec.Body.String())
	}
	requireUIDGone(t, identity.PiUID)
}
