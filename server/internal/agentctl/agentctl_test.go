package agentctl_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"xlyra/server/internal/agent/capabilities"
	"xlyra/server/internal/agentctl"
	"xlyra/server/internal/config"
)

func testConfFile(t *testing.T) (*config.ConfigFile, string) {
	t.Helper()
	dir := t.TempDir()
	cf, err := config.LoadConfigFile(dir)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	return cf, dir
}

func startTestUDS(t *testing.T, eng *agentctl.Engine, caller agentctl.Caller) (*agentctl.UDSServer, string) {
	t.Helper()
	sockDir, err := os.MkdirTemp("/tmp", "xctl-test-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "ctl.sock")

	srv, err := agentctl.StartUDSServer(eng, agentctl.UDSConfig{
		SocketPath: sockPath,
		Caller:     caller,
	})
	if err != nil {
		t.Fatalf("StartUDSServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close(context.Background()) })
	return srv, sockPath
}

func realCLIBin(t *testing.T) string {
	t.Helper()
	candidates := []string{
		filepath.Join("..", "..", "..", "data", "bin", "xlyra"),
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if info, err := os.Stat(abs); err == nil && !info.IsDir() {
			return abs
		}
	}
	t.Skip("xlyra binary not built at data/bin/xlyra; skipping real binary E2E test")
	return ""
}

func runCLICmd(t *testing.T, bin, sock, nonce string, args ...string) (int, agentctl.Envelope, string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + t.TempDir(),
		"LANG=C.UTF-8",
		"XLYRA_CTL_SOCK=" + sock,
		"XLYRA_CTL_NONCE=" + nonce,
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("exec xlyra %v: %v (stderr=%s)", args, err, stderr.String())
		}
	}
	var env agentctl.Envelope
	raw := strings.TrimSpace(stdout.String())
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &env)
	}
	return exitCode, env, stdout.String() + stderr.String()
}

func TestCapabilityPolicyMatrix(t *testing.T) {
	siteRead, _ := agentctl.LookupOpSpec("site", "list")
	siteEnable, _ := agentctl.LookupOpSpec("site", "enable")
	configReset, _ := agentctl.LookupOpSpec("config", "reset")
	agentsWrite, _ := agentctl.LookupOpSpec("agents-md", "set")

	// Default policy opens non-Tier 0 but ALWAYS rejects Tier 0.
	def := agentctl.DefaultBuiltinAgentPolicy()
	if ok, _ := def.AllowsOp(siteEnable); !ok {
		t.Fatal("default policy should allow site/enable")
	}
	if ok, _ := def.AllowsOp(agentsWrite); ok {
		t.Fatal("default policy must always reject Tier 0 agents-md/set")
	}

	// Read-only policy allows Tier 1 reads, denies writes.
	ro := agentctl.ReadOnlyPolicy()
	if ok, _ := ro.AllowsOp(siteRead); !ok {
		t.Fatal("read_only policy should allow site/list")
	}
	if ok, _ := ro.AllowsOp(siteEnable); ok {
		t.Fatal("read_only policy should reject site/enable")
	}

	// Custom MCP-style capability matrix.
	custom := &agentctl.CapabilityPolicy{
		Mode:                "custom",
		MaxTier:             agentctl.Tier2Managed,
		AllowDomains:        []string{agentctl.DomainSite, agentctl.DomainConfig},
		DenyOps:             []string{"site/enable"},
		AllowConfigPrefixes: []string{"general.log", "portal"},
		DenyConfigPrefixes:  []string{"general.security"},
	}
	if ok, _ := custom.AllowsOp(siteRead); !ok {
		t.Fatal("custom policy should allow site/list")
	}
	if ok, _ := custom.AllowsOp(siteEnable); ok {
		t.Fatal("custom policy deny_ops should reject site/enable")
	}
	if ok, _ := custom.AllowsOp(configReset); ok {
		t.Fatal("custom policy max_tier=2 should reject Tier 3 config/reset")
	}
	if ok, _ := custom.AllowsConfigPath("general.log.level"); !ok {
		t.Fatal("custom policy should allow general.log.level")
	}
	if ok, _ := custom.AllowsConfigPath("general.security.session_lifetime_hours"); ok {
		t.Fatal("custom policy should deny general.security.*")
	}
	if ok, _ := custom.AllowsConfigPath("portal.summary_days"); !ok {
		t.Fatal("custom policy should allow portal.summary_days")
	}
	if ok, _ := custom.AllowsConfigPath("backup.auto.enabled"); ok {
		t.Fatal("custom policy should reject non-allowlisted backup.auto.enabled")
	}
}

func TestRedactionProjectionAndTruncation(t *testing.T) {
	input := map[string]any{
		"name":         "upstream-a",
		"api_key":      "sk-live-secret-123",
		"token_count":  420,
		"input_tokens": 120,
		"proxy_url":    "http://alice:supersecret@127.0.0.1:7890",
		"request_headers": map[string]any{
			"Authorization": "Bearer secret-jwt",
			"X-Trace-Id":    "trace-123",
		},
	}
	redacted := agentctl.RedactValue(input).(map[string]any)
	if redacted["api_key"] != agentctl.RedactedPlaceholder {
		t.Fatalf("api_key = %v, want %s", redacted["api_key"], agentctl.RedactedPlaceholder)
	}
	if redacted["token_count"] != float64(420) || redacted["input_tokens"] != float64(120) {
		t.Fatalf("numeric token counters should be preserved, got %#v", redacted)
	}
	if redacted["proxy_url"] != "http://alice:***@127.0.0.1:7890" {
		t.Fatalf("proxy_url = %v", redacted["proxy_url"])
	}
	headers := redacted["request_headers"].(map[string]any)
	if headers["Authorization"] != agentctl.RedactedPlaceholder {
		t.Fatalf("Authorization header = %v", headers["Authorization"])
	}
	if headers["X-Trace-Id"] != "trace-123" {
		t.Fatalf("safe header X-Trace-Id = %v", headers["X-Trace-Id"])
	}

	projected := agentctl.ProjectFields(map[string]any{
		"items": []any{
			map[string]any{"id": "1", "name": "a", "secret": "x"},
		},
	}, []string{"id", "name"}).(map[string]any)
	items := projected["items"].([]any)
	if len(items[0].(map[string]any)) != 2 {
		t.Fatalf("projected item = %#v, want only id & name", items[0])
	}

	// Oversized slice truncation must trim and mark meta.truncated=true.
	bigItems := make([]any, 0, 500)
	for i := 0; i < 500; i++ {
		bigItems = append(bigItems, map[string]any{
			"id":   i,
			"blob": strings.Repeat("A", 120),
		})
	}
	env := agentctl.Envelope{
		OK:   true,
		Data: map[string]any{"items": bigItems},
		Meta: &agentctl.Meta{Tier: 1},
	}
	agentctl.TruncateEnvelope(&env, 8*1024)
	if !env.Meta.Truncated {
		t.Fatal("expected Meta.Truncated=true for oversized envelope")
	}
}

func TestRealCLIBinaryEndToEnd(t *testing.T) {
	bin := realCLIBin(t)
	cf, dataDir := testConfFile(t)
	caps := capabilities.NewStore(dataDir)
	_, _ = cf.Get(config.GeneralConfigPath)

	var onChangeCalls atomic.Int32
	cf.OnChange(func(map[string]any) {
		onChangeCalls.Add(1)
	})

	var approverCalls atomic.Int32
	var approveNext atomic.Bool
	var lastApproval agentctl.ApprovalRequest

	eng := agentctl.NewEngine(agentctl.Deps{
		Version:  "test-1.0.0",
		ConfFile: cf,
		Caps:     caps,
		Approver: agentctl.ApproverFunc(func(_ context.Context, req agentctl.ApprovalRequest) (agentctl.ApprovalDecision, error) {
			approverCalls.Add(1)
			lastApproval = req
			if !approveNext.Load() {
				return agentctl.ApprovalDecision{
					Granted: false,
					Code:    agentctl.ErrCodeUserDenied,
					Reason:  "denied by test approver",
				}, nil
			}
			return agentctl.ApprovalDecision{Granted: true}, nil
		}),
	})

	srv, sock := startTestUDS(t, eng, agentctl.Caller{
		Channel:        agentctl.ChannelAgentCLI,
		RunID:          "run-e2e-1",
		SessionID:      "sess-e2e-1",
		PermissionMode: "ask",
		Policy:         agentctl.DefaultBuiltinAgentPolicy(),
		Quota:          agentctl.NewRunQuota(20),
	})

	// 1. status & schema read (0 approver calls).
	code, env, raw := runCLICmd(t, bin, sock, srv.Nonce(), "status")
	if code != 0 || !env.OK {
		t.Fatalf("status failed: code=%d raw=%s", code, raw)
	}
	statusData := env.Data.(map[string]any)
	rev, _ := statusData["config_revision"].(string)
	if rev == "" {
		t.Fatalf("missing config_revision in status: %#v", statusData)
	}
	if code, env, raw = runCLICmd(t, bin, sock, srv.Nonce(), "schema"); code != 0 || !env.OK {
		t.Fatalf("schema failed: code=%d raw=%s", code, raw)
	}
	if code, env, raw = runCLICmd(t, bin, sock, srv.Nonce(), "schema", "config"); code != 0 || !env.OK {
		t.Fatalf("schema config failed: code=%d raw=%s", code, raw)
	}

	// 2. config get general
	if code, env, raw = runCLICmd(t, bin, sock, srv.Nonce(), "config", "get", "general"); code != 0 || !env.OK {
		t.Fatalf("config get general failed: code=%d raw=%s", code, raw)
	}
	if approverCalls.Load() != 0 {
		t.Fatalf("reads must not call approver, got %d", approverCalls.Load())
	}

	// 3. --dry-run does not mutate and does not trigger approval.
	code, env, raw = runCLICmd(t, bin, sock, srv.Nonce(), "config", "set", "general.log.level=debug", "--dry-run")
	if code != 0 || !env.OK || env.Meta == nil || !env.Meta.DryRun || len(env.Diff) != 1 {
		t.Fatalf("dry-run unexpected: code=%d env=%#v raw=%s", code, env, raw)
	}
	if approverCalls.Load() != 0 {
		t.Fatalf("dry-run must not call approver, got %d", approverCalls.Load())
	}
	if got := config.ReadGeneralConfig(cf).Log.Level; got != "info" {
		t.Fatalf("dry-run mutated config: got %q", got)
	}

	// 4. --if-match mismatch fails with exit 6 BEFORE approval.
	code, _, raw = runCLICmd(t, bin, sock, srv.Nonce(), "config", "set", "general.log.level=debug", "--if-match", "sha256:stale")
	if code != agentctl.ExitConflict {
		t.Fatalf("stale if-match exit=%d, want %d (raw=%s)", code, agentctl.ExitConflict, raw)
	}
	if approverCalls.Load() != 0 {
		t.Fatalf("if-match conflict must happen before approval")
	}

	// 5. Denied mutation fails with exit 5 and preserves config.
	approveNext.Store(false)
	code, env, raw = runCLICmd(t, bin, sock, srv.Nonce(), "config", "set", "general.log.level=debug", "--if-match", rev)
	if code != agentctl.ExitApprovalDenied || env.Error == nil {
		t.Fatalf("denied write exit=%d want %d (raw=%s)", code, agentctl.ExitApprovalDenied, raw)
	}
	if approverCalls.Load() != 1 {
		t.Fatalf("approver calls=%d, want 1", approverCalls.Load())
	}
	if got := config.ReadGeneralConfig(cf).Log.Level; got != "info" {
		t.Fatalf("denied write mutated config: got %q", got)
	}

	// 6. Approved mutation succeeds, notifies OnChange, persists change.
	approveNext.Store(true)
	code, env, raw = runCLICmd(t, bin, sock, srv.Nonce(), "config", "set", "general.log.level=debug", "--if-match", rev)
	if code != 0 || !env.OK {
		t.Fatalf("approved write failed: code=%d raw=%s", code, raw)
	}
	if approverCalls.Load() != 2 || lastApproval.Tier != agentctl.Tier2Managed || len(lastApproval.Diff) != 1 {
		t.Fatalf("unexpected approval request: calls=%d req=%#v", approverCalls.Load(), lastApproval)
	}
	if got := config.ReadGeneralConfig(cf).Log.Level; got != "debug" {
		t.Fatalf("config not persisted: got %q", got)
	}
	if onChangeCalls.Load() == 0 {
		t.Fatal("expected ConfigFile OnChange callback to fire")
	}

	// 7. Tier 3 escalation on ip_whitelist.
	code, _, raw = runCLICmd(t, bin, sock, srv.Nonce(), "config", "set", "general.ip_whitelist.enabled=true")
	if code != 0 {
		t.Fatalf("ip_whitelist write failed: code=%d raw=%s", code, raw)
	}
	if lastApproval.Tier != agentctl.Tier3HighRisk {
		t.Fatalf("expected Tier 3 escalation for ip_whitelist, got Tier %d", lastApproval.Tier)
	}

	// 8. Tier 0 operations blocked with exit 4 (approver NOT called).
	callsBeforeTier0 := approverCalls.Load()
	tier0Cases := [][]string{
		{"config", "set", "agent.settings.fast_model=evil"},
		{"agents-md", "set", "--text", "hacked"},
		{"skill", "put", "evil"},
		{"backup", "restore", "snap.zip"},
		{"site-key", "create", "11111111-1111-1111-1111-111111111111", "--json", `{"secret":"sk"}`},
		{"proxy", "create", "--json", `{"name":"p1","type":"http","url":"http://user:pw@127.0.0.1:7890"}`},
	}
	for _, tc := range tier0Cases {
		code, _, raw = runCLICmd(t, bin, sock, srv.Nonce(), tc...)
		if code != agentctl.ExitPolicyDenied {
			t.Fatalf("Tier 0 %v exited %d, want %d (raw=%s)", tc, code, agentctl.ExitPolicyDenied, raw)
		}
	}
	// api-key reveal is omitted from CLI subcommands AND blocked at server Engine Tier 0.
	code, _, raw = runCLICmd(t, bin, sock, srv.Nonce(), "api-key", "reveal", "11111111-1111-1111-1111-111111111111")
	if code == 0 {
		t.Fatalf("api-key reveal unexpectedly succeeded: raw=%s", raw)
	}
	serverTier0Reqs := []agentctl.CallRequest{
		{Protocol: 1, Resource: "api-key", Action: "reveal", Target: "11111111-1111-1111-1111-111111111111"},
		{Protocol: 1, Resource: "site-key", Action: "create", Target: "11111111-1111-1111-1111-111111111111"},
		{Protocol: 1, Resource: "agents-md", Action: "set"},
		{Protocol: 1, Resource: "skill", Action: "put"},
		{Protocol: 1, Resource: "backup", Action: "restore"},
		{Protocol: 1, Resource: "cloud", Action: "bind"},
		{Protocol: 1, Resource: "proxy", Action: "create", Payload: map[string]any{"name": "p1", "type": "http", "url": "http://u:pw@127.0.0.1:7890"}},
	}
	for _, r := range serverTier0Reqs {
		st, e := eng.Execute(context.Background(), agentctl.Caller{Channel: agentctl.ChannelAgentCLI, PermissionMode: "full"}, r)
		if st != http.StatusForbidden || e.OK || e.Error == nil || e.Error.Code != agentctl.ErrCodePolicyTier0Denied {
			t.Fatalf("server Tier 0 %s/%s not enforced in full mode: st=%d env=%#v", r.Resource, r.Action, st, e)
		}
	}
	if approverCalls.Load() != callsBeforeTier0 {
		t.Fatal("Tier 0 denials must never invoke approver")
	}

	// 9. Safe proxy create/list without password works.
	code, _, raw = runCLICmd(t, bin, sock, srv.Nonce(), "proxy", "create", "--json", `{"name":"local-socks","type":"socks5","url":"socks5://127.0.0.1:1080"}`)
	if code != 0 {
		t.Fatalf("safe proxy create failed: code=%d raw=%s", code, raw)
	}
	code, env, raw = runCLICmd(t, bin, sock, srv.Nonce(), "proxy", "list")
	if code != 0 || !env.OK {
		t.Fatalf("proxy list failed: code=%d raw=%s", code, raw)
	}

	// 10. Read-only agent capabilities diagnostics work cleanly.
	if code, _, raw = runCLICmd(t, bin, sock, srv.Nonce(), "agents-md", "get"); code != 0 {
		t.Fatalf("agents-md get failed: code=%d raw=%s", code, raw)
	}
	if code, _, raw = runCLICmd(t, bin, sock, srv.Nonce(), "skill", "list"); code != 0 {
		t.Fatalf("skill list failed: code=%d raw=%s", code, raw)
	}

	// 11. Bad nonce is rejected.
	code, _, raw = runCLICmd(t, bin, sock, "wrong-nonce", "status")
	if code != agentctl.ExitPolicyDenied {
		t.Fatalf("wrong nonce exit=%d, want %d (raw=%s)", code, agentctl.ExitPolicyDenied, raw)
	}

	// 12. Unsupported protocol header/body is rejected directly by Engine.
	status, badEnv := eng.Execute(context.Background(), agentctl.Caller{Channel: agentctl.ChannelAgentCLI}, agentctl.CallRequest{
		Protocol: 99, Resource: "status", Action: "get",
	})
	if status != http.StatusBadRequest || badEnv.OK {
		t.Fatalf("bad protocol status=%d env=%#v", status, badEnv)
	}
}
