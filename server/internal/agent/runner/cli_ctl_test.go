package runner

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agent/translate"
	"xlyra/server/internal/agentctl"
	"xlyra/server/internal/config"
)

func TestCLIApproverAskAutoFullModes(t *testing.T) {
	dir := t.TempDir()
	cf, err := config.LoadConfigFile(dir)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	eng := agentctl.NewEngine(agentctl.Deps{ConfFile: cf, Version: "test"})
	store := session.NewStore(filepath.Join(dir, "sessions"))
	meta, err := store.Create("gpt-4o-mini", "ask")
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	r := &Runner{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg: Config{
			Store:     store,
			CtlEngine: eng,
			Classify: func(_ context.Context, _, _, _, user string, _ int) (string, error) {
				if strings.Contains(user, "general.log.level") {
					return `{"decision":"allow","reason":"用户明确调整日志级别"}`, nil
				}
				return `{"decision":"block","reason":"未授权"}`, nil
			},
		},
	}

	ar := &activeRun{
		sessionID:    meta.ID,
		runID:        "run-cli",
		model:        "gpt-4o-mini",
		tr:           translate.New(),
		ctx:          context.Background(),
		pending:      map[string]session.Escalation{},
		cliApprovals: map[string]chan bool{},
		resolved:     map[string]resolveOptions{},
		waitChanged:  make(chan struct{}, 1),
	}

	// 1. full 模式直接放行 Tier 2 与 Tier 3
	fullApp := r.newCLIApprover(ar, nil, "full")
	dec, err := fullApp.Approve(context.Background(), agentctl.ApprovalRequest{
		Resource: "config", Action: "set", Tier: agentctl.Tier3HighRisk,
	})
	if err != nil || !dec.Granted {
		t.Fatalf("full mode should grant: dec=%#v err=%v", dec, err)
	}

	// 2. auto 模式 Tier 2 经分类器放行，未命中指令则拒绝
	autoApp := r.newCLIApprover(ar, nil, "auto")
	dec, err = autoApp.Approve(context.Background(), agentctl.ApprovalRequest{
		Resource: "config", Action: "set", Tier: agentctl.Tier2Managed,
		Diff: []agentctl.DiffItem{{Path: "general.log.level", Before: "info", After: "debug"}},
	})
	if err != nil || !dec.Granted {
		t.Fatalf("auto mode Tier 2 classifier allow failed: dec=%#v err=%v", dec, err)
	}
	dec, err = autoApp.Approve(context.Background(), agentctl.ApprovalRequest{
		Resource: "config", Action: "set", Tier: agentctl.Tier2Managed,
		Diff: []agentctl.DiffItem{{Path: "portal.enabled", Before: false, After: true}},
	})
	if err != nil || dec.Granted || dec.Code != agentctl.ErrCodeClassifierDenied {
		t.Fatalf("auto mode Tier 2 classifier block unexpected: dec=%#v err=%v", dec, err)
	}

	// 3. ask 模式触发交互式 cliApprovals 并通过 resolveEscalation 解锁
	askApp := r.newCLIApprover(ar, nil, "ask")
	done := make(chan agentctl.ApprovalDecision, 1)
	go func() {
		d, _ := askApp.Approve(context.Background(), agentctl.ApprovalRequest{
			Resource: "config", Action: "set", Tier: agentctl.Tier2Managed,
			Summary: "修改 log.level",
			Diff:    []agentctl.DiffItem{{Path: "general.log.level", Before: "info", After: "debug"}},
		})
		done <- d
	}()

	var escID string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ar.stateMu.Lock()
		for id := range ar.cliApprovals {
			escID = id
			break
		}
		ar.stateMu.Unlock()
		if escID != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if escID == "" {
		t.Fatal("ask mode did not register interactive CLI escalation")
	}
	if !r.resolveEscalation(ar, escID, resolveOptions{granted: true}) {
		t.Fatal("resolveEscalation returned false for CLI escalation")
	}
	select {
	case d := <-done:
		if !d.Granted {
			t.Fatalf("expected granted=true, got %#v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for CLI approver unblock")
	}
}
