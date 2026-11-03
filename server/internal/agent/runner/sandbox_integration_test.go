package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"xlyra/server/internal/agent/isolation"
	"xlyra/server/internal/agent/runlog"
	"xlyra/server/internal/agent/sandbox"
	"xlyra/server/internal/agent/session"
)

func requireSandboxIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("AGENT_SANDBOX_INTEGRATION") != "1" || runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("需要 Linux + root + AGENT_SANDBOX_INTEGRATION=1")
	}
}

type sandboxEnv struct {
	runner    *Runner
	store     *session.Store
	runs      *runlog.Registry
	secret    string
	outside   string
	result    string
	gateway   net.Listener
	other     net.Listener
	sessionID string
}

func newSandboxEnv(t *testing.T, command string) *sandboxEnv {
	t.Helper()
	root := shortTempDir(t)
	makeTraversable(t, root)
	makeTraversable(t, filepath.Dir(os.Args[0]))
	dataDir := filepath.Join(root, "agent")
	if err := isolation.PrepareDataDir(dataDir); err != nil {
		t.Fatal(err)
	}
	store := session.NewStore(filepath.Join(dataDir, "sessions"))
	meta, err := store.Create("mock-1", "full")
	if err != nil {
		t.Fatal(err)
	}
	extension := filepath.Join(root, "extension/xlyra.ts")
	if err := os.MkdirAll(filepath.Dir(extension), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extension, []byte("// fake\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "secret")
	if err := os.WriteFile(secret, []byte("TOP-SECRET-SANDBOX"), 0o644); err != nil {
		t.Fatal(err)
	}
	gateway, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	other, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close(); _ = other.Close() })
	result := filepath.Join(store.WorkDir(meta.ID), "sandbox-result.json")
	args := []string{
		"--fake-pi", "--fake-record", "record.jsonl", "--fake-sandbox-result", result,
		"--fake-sandbox-secret", secret, "--fake-sandbox-outside", filepath.Join(root, "outside"),
		"--fake-sandbox-gateway", gateway.Addr().String(), "--fake-sandbox-other", other.Addr().String(),
		"--fake-sandbox-command", command,
	}
	runs := runlog.NewRegistry()
	r, err := New(Config{
		Store: store, Runs: runs, Tokens: &fakeIssuer{},
		Protocol:       func(context.Context, string) (string, error) { return "openai-chat-completions", nil },
		GatewayBaseURL: "http://" + gateway.Addr().String(), Command: os.Args[0], Args: args,
		PathEnv: os.Getenv("PATH"), ExtensionPath: extension,
		RunTimeout: 30 * time.Second, AbortGrace: time.Second,
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, nil)),
		Isolation: &IsolationConfig{},
		Sandbox:   &SandboxConfig{Exe: os.Args[0], PiLandlock: true, Bash: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	env := &sandboxEnv{runner: r, store: store, runs: runs, secret: secret, outside: filepath.Join(root, "outside"), result: result, gateway: gateway, other: other, sessionID: meta.ID}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = r.Shutdown(ctx)
	})
	return env
}

func (env *sandboxEnv) send(t *testing.T) *collector {
	t.Helper()
	if _, err := env.runner.Send(context.Background(), SendInput{SessionID: env.sessionID, Content: "测试", Model: "mock-1", PermissionMode: "full"}); err != nil {
		t.Fatal(err)
	}
	return collectRun(t, env.runs, env.sessionID)
}

func readSandboxResult(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertSandboxUIDsGone(t *testing.T, identity isolation.Identity) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		pi, exec := sandbox.ProcsOfUID(int(identity.PiUID)), sandbox.ProcsOfUID(int(identity.ExecUID))
		if len(pi) == 0 && len(exec) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("沙箱进程残留: pi=%v exec=%v", pi, exec)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRunnerSandboxBashIntegration(t *testing.T) {
	requireSandboxIntegration(t)
	env := newSandboxEnv(t, "echo hi; id -u")
	events := env.send(t).wait(t)
	if events[len(events)-1].Type != "agent_done" {
		t.Fatalf("终态: %v", eventTypes(events))
	}
	result := readSandboxResult(t, env.result)
	for _, key := range []string{"secret_denied", "outside_denied", "workspace_write", "workspace_read", "parent_environ_denied", "gateway_ok", "other_denied"} {
		if ok, _ := result[key].(bool); !ok {
			t.Errorf("探针 %s 失败: %+v", key, result)
		}
	}
	if stdout, _ := result["exec_stdout"].(string); !strings.Contains(stdout, "hi") {
		t.Fatalf("执行器输出: %+v", result)
	}
	identity := identityOfSession(t, env.store, env.sessionID)
	if uid, _ := result["exec_uid"].(float64); int(uid) != int(identity.ExecUID) {
		t.Fatalf("命令 uid = %v, want %d", result["exec_uid"], identity.ExecUID)
	}
	if _, err := os.Stat(filepath.Join(env.store.Dir(env.sessionID), "exec/exec.sock")); !os.IsNotExist(err) {
		t.Fatalf("套接字未清理: %v", err)
	}
	audit, err := os.ReadFile(filepath.Join(env.store.Dir(env.sessionID), "exec-audit.jsonl"))
	if err != nil || !strings.Contains(string(audit), "echo hi; id -u") {
		t.Fatalf("审计: %s, %v", audit, err)
	}
	assertSandboxUIDsGone(t, identity)
}

func TestRunnerSandboxToolsAndCompact(t *testing.T) {
	requireSandboxIntegration(t)
	env := newSandboxEnv(t, "echo hi")
	if events := env.send(t).wait(t); events[len(events)-1].Type != "agent_done" {
		t.Fatal(eventTypes(events))
	}
	if _, err := env.runner.Compact(context.Background(), env.sessionID, "mock-1"); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(env.store.Dir(env.sessionID), "pi-agent/record.jsonl")
	var starts []map[string]any
	for _, rec := range readRecords(t, record) {
		if rec["kind"] == "start" {
			starts = append(starts, rec)
		}
	}
	if len(starts) != 2 {
		t.Fatalf("启动记录数 = %d", len(starts))
	}
	for i, tc := range []struct{ hasBash bool }{{true}, {false}} {
		args, _ := starts[i]["args"].([]any)
		var values []string
		for _, arg := range args {
			values = append(values, fmt.Sprint(arg))
		}
		tools := flagArg(values, "--tools")
		if strings.Contains(","+tools+",", ",bash,") != tc.hasBash {
			t.Errorf("第 %d 次 --tools = %q", i+1, tools)
		}
	}
}

func TestRunnerSandboxCancelIntegration(t *testing.T) {
	requireSandboxIntegration(t)
	env := newSandboxEnv(t, "sleep 300")
	started := filepath.Join(env.store.WorkDir(env.sessionID), "exec-started")
	env.runner.cfg.Args = append(env.runner.cfg.Args, "--fake-sandbox-started", started)
	collected := env.send(t)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("命令未开始")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := env.runner.Stop(context.Background(), env.sessionID); err != nil {
		t.Fatal(err)
	}
	if events := collected.wait(t); events[len(events)-1].Type != "agent_cancelled" {
		t.Fatal(eventTypes(events))
	}
	assertSandboxUIDsGone(t, identityOfSession(t, env.store, env.sessionID))
}

func TestRunnerSandboxExecutorStartFailure(t *testing.T) {
	requireSandboxIntegration(t)
	root := shortTempDir(t)
	longRoot := filepath.Join(root, strings.Repeat("long-session-path-", 8))
	makeTraversable(t, root)
	if err := isolation.PrepareDataDir(longRoot); err != nil {
		t.Fatal(err)
	}
	store := session.NewStore(filepath.Join(longRoot, "sessions"))
	meta, err := store.Create("mock-1", "full")
	if err != nil {
		t.Fatal(err)
	}
	runs := runlog.NewRegistry()
	r, err := New(Config{
		Store: store, Runs: runs, Tokens: &fakeIssuer{},
		Protocol:       func(context.Context, string) (string, error) { return "openai-chat-completions", nil },
		GatewayBaseURL: "http://127.0.0.1:18080", Command: os.Args[0], Args: []string{"--fake-pi"},
		ExtensionPath: os.Args[0], Isolation: &IsolationConfig{},
		Sandbox: &SandboxConfig{Exe: os.Args[0], PiLandlock: true, Bash: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Shutdown(context.Background())
	if _, err := r.Send(context.Background(), SendInput{SessionID: meta.ID, Content: "x", Model: "mock-1"}); err != nil {
		t.Fatal(err)
	}
	if events := collectRun(t, runs, meta.ID).wait(t); events[len(events)-1].Type != "agent_error" {
		t.Fatal(eventTypes(events))
	}
	if _, err := os.Stat(filepath.Join(store.Dir(meta.ID), "pi-agent/record.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("Pi 不应启动: %v", err)
	}
	assertSandboxUIDsGone(t, identityOfSession(t, store, meta.ID))
}

// shortTempDir 返回路径很短的临时目录:执行器套接字受 sun_path 108 字节限制,
// t.TempDir 带完整测试名,放到 sessions/<uuid>/exec/ 之下会超长。
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
