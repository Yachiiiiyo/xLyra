package runner

import (
	"context"
	"errors"
	"testing"
)

// 恢复备份期间必须拦住 Agent 运行:有进行中的任务时拒绝进入恢复态,
// 恢复态下新 run 一律被拒绝,EndRestore 后恢复正常。
func TestRestoreGate(t *testing.T) {
	env := newTestEnv(t, "basic", withMaxConcurrent(1))

	if err := env.runner.BeginRestore(); err != nil {
		t.Fatalf("空闲时 BeginRestore 应成功: %v", err)
	}
	if _, err := env.runner.Send(context.Background(), SendInput{Content: "恢复中", Model: "mock-1"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("恢复态下 Send 应返回 ErrUnavailable, got %v", err)
	}
	env.runner.EndRestore()

	result := sendBasic(t, env)
	collectRun(t, env.runs, result.SessionID).wait(t)

	// 进行中的 run 存在时拒绝恢复(用 admit 直接占位模拟)。
	env.runner.mu.Lock()
	env.runner.active["busy-session"] = &activeRun{sessionID: "busy-session"}
	env.runner.mu.Unlock()
	if err := env.runner.BeginRestore(); !errors.Is(err, ErrBusy) {
		t.Fatalf("有进行中的 run 时 BeginRestore 应返回 ErrBusy, got %v", err)
	}
	env.runner.mu.Lock()
	delete(env.runner.active, "busy-session")
	env.runner.mu.Unlock()
	if err := env.runner.BeginRestore(); err != nil {
		t.Fatalf("run 结束后 BeginRestore 应成功: %v", err)
	}
	env.runner.EndRestore()
}
