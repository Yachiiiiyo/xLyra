package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agent/workspace"
)

func newTurnFixture(t *testing.T) (*testEnv, session.Meta, *activeRun) {
	t.Helper()
	env := newTestEnv(t, "basic")
	meta, err := env.store.Create("mock-1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	ar := &activeRun{sessionID: meta.ID, runID: "run-1", kind: "send", turnID: "turn-1"}
	return env, meta, ar
}

func writeTurnFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBeginTurnSnapshot(t *testing.T) {
	env, meta, ar := newTurnFixture(t)
	workDir := env.store.WorkDir(meta.ID)
	writeTurnFile(t, workDir, "a.txt", "before\n")
	env.runner.beginTurn(ar, &prepared{})
	stateDir := env.store.StateDir(meta.ID)
	beforePath, lastPath := turnPaths(stateDir)
	before, err := loadTurnSnapshot(beforePath)
	if err != nil || before == nil || before.TurnID != ar.turnID {
		t.Fatalf("before.json = %#v, %v", before, err)
	}
	entry := before.Manifest.Entries["a.txt"]
	if _, ok := workspace.NewStore(filepath.Join(stateDir, "blobs")).Get(entry.SHA256); !ok {
		t.Fatal("修改前文本未写入 blob")
	}
	if info, err := os.Stat(stateDir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("状态目录权限 = %v, %v", info.Mode(), err)
	}
	if info, err := os.Stat(beforePath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("before.json 权限 = %v, %v", info.Mode(), err)
	}

	// 同轮复用基准;新轮才重新扫描。
	writeTurnFile(t, workDir, "a.txt", "outside\n")
	env.runner.beginTurn(ar, &prepared{})
	again, _ := loadTurnSnapshot(beforePath)
	if again.Manifest.Entries["a.txt"].SHA256 != entry.SHA256 {
		t.Fatal("同 turn 不应重扫")
	}
	ar.turnID = "turn-2"
	env.runner.beginTurn(ar, &prepared{})
	next, _ := loadTurnSnapshot(beforePath)
	if next.Manifest.Entries["a.txt"].SHA256 == entry.SHA256 {
		t.Fatal("新 turn 未更新基准")
	}
	if _, err := workspace.LoadManifest(lastPath); err != nil {
		t.Fatal(err)
	}
}

func TestFinishTurnFileChanges(t *testing.T) {
	env, meta, ar := newTurnFixture(t)
	workDir := env.store.WorkDir(meta.ID)
	writeTurnFile(t, workDir, "modified.txt", "one\ntwo\n")
	writeTurnFile(t, workDir, "deleted.txt", "bye\n")
	env.runner.beginTurn(ar, &prepared{})
	writeTurnFile(t, workDir, "modified.txt", "one\nthree\n")
	writeTurnFile(t, workDir, "added.txt", "new\n")
	if err := os.Remove(filepath.Join(workDir, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	env.runner.finishTurn(ar, &prepared{})
	got, err := env.store.Get(meta.ID)
	if err != nil || len(got.RunChanges) != 1 {
		t.Fatalf("RunChanges = %#v, %v", got.RunChanges, err)
	}
	record := got.RunChanges[0]
	if record.Total != 3 || len(record.Files) != 3 || record.Incomplete {
		t.Fatalf("变更汇总错误: %+v", record)
	}
	want := []session.FileChange{
		{Path: "added.txt", Kind: "added", Size: 4, Additions: 1, HasDiff: true},
		{Path: "deleted.txt", Kind: "deleted", Size: 4, Deletions: 1, HasDiff: true},
		{Path: "modified.txt", Kind: "modified", Size: 10, Additions: 1, Deletions: 1, HasDiff: true},
	}
	for i := range want {
		if got := record.Files[i]; got != want[i] {
			t.Errorf("Files[%d] = %+v, want %+v", i, got, want[i])
		}
	}
}

func TestFinishTurnNoChangesRemovesRecord(t *testing.T) {
	env, meta, ar := newTurnFixture(t)
	writeTurnFile(t, env.store.WorkDir(meta.ID), "a.txt", "same\n")
	if _, err := env.store.Update(meta.ID, func(m *session.Meta) error {
		m.UpsertRunChange(session.RunChange{TurnID: ar.turnID, Total: 1})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	env.runner.beginTurn(ar, &prepared{})
	env.runner.finishTurn(ar, &prepared{})
	got, _ := env.store.Get(meta.ID)
	if len(got.RunChanges) != 0 {
		t.Fatalf("空差集未移除旧记录: %+v", got.RunChanges)
	}
}

func TestFinishTurnIncompleteSources(t *testing.T) {
	t.Run("未开 Landlock 不算不完整", func(t *testing.T) {
		env, meta, ar := newTurnFixture(t)
		env.runner.beginTurn(ar, &prepared{})
		writeTurnFile(t, env.store.WorkDir(meta.ID), "a.txt", "a\n")
		env.runner.finishTurn(ar, &prepared{})
		got, _ := env.store.Get(meta.ID)
		if got.RunChanges[0].Incomplete {
			t.Fatal("未开 Landlock 不应标 Incomplete")
		}
	})
	t.Run("清单截断", func(t *testing.T) {
		env, meta, ar := newTurnFixture(t)
		env.runner.cfg.Sandbox = &SandboxConfig{PiLandlock: true}
		env.runner.beginTurn(ar, &prepared{})
		writeTurnFile(t, env.store.WorkDir(meta.ID), "visible.txt", "a\n")
		writeTurnFile(t, env.store.WorkDir(meta.ID), strings.Repeat("deep/", 13)+"a.txt", "a\n")
		env.runner.finishTurn(ar, &prepared{})
		got, _ := env.store.Get(meta.ID)
		if !got.RunChanges[0].Incomplete {
			t.Fatal("清单截断应标 Incomplete")
		}
	})
	t.Run("超时降级", func(t *testing.T) {
		old := finishTurnTimeout
		finishTurnTimeout = 0
		t.Cleanup(func() { finishTurnTimeout = old })
		env, meta, ar := newTurnFixture(t)
		env.runner.cfg.Sandbox = &SandboxConfig{PiLandlock: true}
		writeTurnFile(t, env.store.WorkDir(meta.ID), "before.txt", "before\n")
		env.runner.beginTurn(ar, &prepared{})
		writeTurnFile(t, env.store.WorkDir(meta.ID), "after.txt", "after\n")
		env.runner.finishTurn(ar, &prepared{})
		got, _ := env.store.Get(meta.ID)
		if len(got.RunChanges) != 1 || !got.RunChanges[0].Incomplete {
			t.Fatalf("超时应保留不完整差集: %+v", got.RunChanges)
		}
		for _, file := range got.RunChanges[0].Files {
			if file.Additions != 0 || file.Deletions != 0 {
				t.Fatalf("超时仍统计了行数: %+v", file)
			}
		}
	})
}

func TestFinishTurnFileLimitAndLargeFile(t *testing.T) {
	t.Run("超过展示上限", func(t *testing.T) {
		env, meta, ar := newTurnFixture(t)
		env.runner.beginTurn(ar, &prepared{})
		for i := 0; i < session.MaxRunChangeFiles+1; i++ {
			writeTurnFile(t, env.store.WorkDir(meta.ID), fmt.Sprintf("%03d.txt", i), "x\n")
		}
		env.runner.finishTurn(ar, &prepared{})
		got, _ := env.store.Get(meta.ID)
		record := got.RunChanges[0]
		if record.Total != session.MaxRunChangeFiles+1 || len(record.Files) != session.MaxRunChangeFiles {
			t.Fatalf("Total/files = %d/%d", record.Total, len(record.Files))
		}
	})
	t.Run("超大文本无 diff", func(t *testing.T) {
		env, meta, ar := newTurnFixture(t)
		env.runner.beginTurn(ar, &prepared{})
		writeTurnFile(t, env.store.WorkDir(meta.ID), "large.txt", strings.Repeat("x", workspace.BlobMaxBytes+1))
		env.runner.finishTurn(ar, &prepared{})
		got, _ := env.store.Get(meta.ID)
		if file := got.RunChanges[0].Files[0]; file.HasDiff || file.Additions != 0 {
			t.Fatalf("超大文本不应有 diff: %+v", file)
		}
	})
}

func TestFinishTurnSameTurnMergesRunIDs(t *testing.T) {
	env, meta, ar := newTurnFixture(t)
	env.runner.beginTurn(ar, &prepared{})
	writeTurnFile(t, env.store.WorkDir(meta.ID), "a.txt", "one\n")
	env.runner.finishTurn(ar, &prepared{})
	ar.runID = "run-2"
	env.runner.beginTurn(ar, &prepared{})
	writeTurnFile(t, env.store.WorkDir(meta.ID), "b.txt", "two\n")
	env.runner.finishTurn(ar, &prepared{})
	got, _ := env.store.Get(meta.ID)
	if len(got.RunChanges) != 1 || fmt.Sprint(got.RunChanges[0].RunIDs) != "[run-1 run-2]" || got.RunChanges[0].Total != 2 {
		t.Fatalf("同轮合并未成功: %+v", got.RunChanges)
	}
}

func withWorkspaceFile(path, content string) envOption {
	return func(_ *Config, args *[]string) {
		*args = append(*args, "--fake-workspace-file", path, "--fake-workspace-content", content)
	}
}

func TestRunChangesIntegration(t *testing.T) {
	env := newTestEnv(t, "basic", withWorkspaceFile("notes.txt", "hello\n"))
	result := sendBasic(t, env)
	events := collectRun(t, env.runs, result.SessionID).wait(t)
	terminal, changes := -1, -1
	for i, event := range events {
		switch event.Type {
		case "agent_done":
			terminal = i
		case "run_changes":
			changes = i
			data := event.Data
			if data["turn_id"] != result.MessageID || data["total"] != 1 {
				t.Fatalf("run_changes 数据错误: %#v", data)
			}
		}
	}
	if terminal < 0 || changes != len(events)-1 || changes <= terminal {
		t.Fatalf("run_changes 时序错误: %v", eventTypes(events))
	}
	meta, _ := env.store.Get(result.SessionID)
	if len(meta.RunChanges) != 1 || meta.RunChanges[0].Files[0].Path != "notes.txt" {
		t.Fatalf("meta 变更错误: %+v", meta.RunChanges)
	}
	entries, err := env.store.BuildTranscript(result.SessionID)
	if err != nil || entries[len(entries)-1]["type"] != "run_changes" {
		t.Fatalf("transcript 缺少 run_changes: %#v, %v", entries, err)
	}
}

func TestRunChangesApprovalCompactAndRetry(t *testing.T) {
	t.Run("审批仍在同一 run", func(t *testing.T) {
		env := newTestEnv(t, "approval-allow", withDelay(2), withWorkspaceFile("approved.txt", "ok\n"))
		result := sendBasic(t, env)
		collected := collectRun(t, env.runs, result.SessionID)
		event := waitForEvent(t, collected, "escalation_request")
		id := event.Data["escalation"].(map[string]any)["escalation_id"].(string)
		if err := env.runner.Grant(context.Background(), result.SessionID, GrantInput{EscalationID: id, Granted: true}); err != nil {
			t.Fatal(err)
		}
		collected.wait(t)
		meta, _ := env.store.Get(result.SessionID)
		if len(meta.RunChanges) != 1 || len(meta.RunChanges[0].RunIDs) != 1 || meta.RunChanges[0].TurnID != result.MessageID {
			t.Fatalf("审批轮次错误: %+v", meta.RunChanges)
		}
	})
	t.Run("Compact 不产生变更", func(t *testing.T) {
		env := newTestEnv(t, "basic")
		result := sendBasic(t, env)
		collectRun(t, env.runs, result.SessionID).wait(t)
		if _, err := env.runner.Compact(context.Background(), result.SessionID, "mock-1"); err != nil {
			t.Fatal(err)
		}
		meta, _ := env.store.Get(result.SessionID)
		if len(meta.RunChanges) != 0 {
			t.Fatalf("Compact 产生了变更: %+v", meta.RunChanges)
		}
	})
	t.Run("Retry 是新一轮", func(t *testing.T) {
		env := newTestEnv(t, "basic", withWorkspaceFile("retry-$PID.txt", "ok\n"))
		first := sendBasic(t, env)
		collectRun(t, env.runs, first.SessionID).wait(t)
		second, err := env.runner.Retry(context.Background(), RetryInput{SessionID: first.SessionID, MessageID: first.MessageID})
		if err != nil {
			t.Fatal(err)
		}
		collectRun(t, env.runs, second.SessionID).wait(t)
		meta, _ := env.store.Get(first.SessionID)
		if len(meta.RunChanges) != 2 || meta.RunChanges[0].TurnID != first.MessageID || meta.RunChanges[1].TurnID != second.MessageID {
			t.Fatalf("Retry 轮次错误: %+v", meta.RunChanges)
		}
	})
}

func TestRunChangesPublishedBeforeFinish(t *testing.T) {
	env := newTestEnv(t, "basic", withWorkspaceFile("event.txt", "ok\n"))
	result := sendBasic(t, env)
	run := env.runs.Current(result.SessionID)
	if run == nil {
		t.Fatal("会话没有 run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seen := false
	for event := range run.Subscribe(ctx) {
		if event.Type == "run_changes" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("流结束前未收到 run_changes")
	}
}
