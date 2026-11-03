package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"xlyra/server/internal/agent/translate"
)

func TestLimitDefaults(t *testing.T) {
	env := newTestEnv(t, "basic")
	if env.runner.cfg.MaxOutputBytes != 64<<20 {
		t.Errorf("MaxOutputBytes = %d", env.runner.cfg.MaxOutputBytes)
	}
	if env.runner.cfg.SessionQuotaBytes != 512<<20 {
		t.Errorf("SessionQuotaBytes = %d", env.runner.cfg.SessionQuotaBytes)
	}
	if env.runner.cfg.QueueTimeout != 2*time.Minute {
		t.Errorf("QueueTimeout = %s", env.runner.cfg.QueueTimeout)
	}
	if env.runner.cfg.QueueMax != 32 {
		t.Errorf("QueueMax = %d", env.runner.cfg.QueueMax)
	}
}

func TestOutputLimit(t *testing.T) {
	env := newTestEnv(t, "basic", func(cfg *Config, args *[]string) {
		cfg.MaxOutputBytes = 2048
		*args = append(*args, "--fake-flood=1")
	})
	result := sendModel(t, env, "", "output-limit")
	events := collectRun(t, env.runs, result.SessionID).wait(t)
	if countTerminals(events) != 1 || events[len(events)-1].Type != "agent_error" {
		t.Fatalf("终态不符: %v", eventTypes(events))
	}
	if message := events[len(events)-1].Data["message"]; message != "输出超过上限,已中止" {
		t.Fatalf("错误消息 = %v", message)
	}
	for _, event := range events {
		if event.Type == "agent_done" {
			t.Fatal("超过输出上限不应有 agent_done")
		}
	}
	if !hasCommand(recordedCommands(t, env.record), "abort") {
		t.Fatal("超过输出上限后未中止 Pi")
	}
	waitForLog(t, env.logs, "reason=output_limit")
}

func TestSessionQuotaBeforeStart(t *testing.T) {
	env := newTestEnv(t, "basic", func(cfg *Config, _ *[]string) {
		cfg.SessionQuotaBytes = 1024
	})
	meta, err := env.store.Create("mock-1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.store.WorkDir(meta.ID), "large.bin"), make([]byte, 2048), 0o600); err != nil {
		t.Fatal(err)
	}
	result := sendModel(t, env, meta.ID, "quota-before")
	events := collectRun(t, env.runs, result.SessionID).wait(t)
	if countTerminals(events) != 1 || events[len(events)-1].Type != "agent_error" {
		t.Fatalf("终态不符: %v", eventTypes(events))
	}
	if message := events[len(events)-1].Data["message"]; message != "会话磁盘占用超过配额,请清理或新建会话" {
		t.Fatalf("错误消息 = %v", message)
	}
	if _, err := os.Stat(env.record); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("预检查超配额时不应启动 Pi: %v", err)
	}
	if env.issuer.issueCount() != 0 || env.issuer.endedCount() != 0 {
		t.Fatalf("预检查超配额时不应签发令牌: issued=%d ended=%d", env.issuer.issueCount(), env.issuer.endedCount())
	}
	waitForLog(t, env.logs, "reason=disk_quota")
}

func TestSessionQuotaDuringRun(t *testing.T) {
	oldInterval := quotaCheckInterval
	quotaCheckInterval = 15 * time.Millisecond
	t.Cleanup(func() { quotaCheckInterval = oldInterval })
	env := newTestEnv(t, "basic", withDelay(30), func(cfg *Config, args *[]string) {
		cfg.SessionQuotaBytes = 4096
		*args = append(*args, "--fake-workspace-bytes", "16384")
	})
	result := sendModel(t, env, "", "quota-during")
	events := collectRun(t, env.runs, result.SessionID).wait(t)
	if countTerminals(events) != 1 || events[len(events)-1].Type != "agent_error" {
		t.Fatalf("终态不符: %v", eventTypes(events))
	}
	if message := events[len(events)-1].Data["message"]; message != "工作区超过磁盘配额,已中止" {
		t.Fatalf("错误消息 = %v", message)
	}
	if !hasCommand(recordedCommands(t, env.record), "abort") {
		t.Fatal("运行期间超配额后未中止 Pi")
	}
	waitForLog(t, env.logs, "reason=disk_quota")
}

func TestCompactSkipsSessionQuota(t *testing.T) {
	env := newTestEnv(t, "basic", func(cfg *Config, _ *[]string) {
		cfg.SessionQuotaBytes = 1
	})
	meta, err := env.store.Create("mock-1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.store.WorkDir(meta.ID), "large.bin"), make([]byte, 128), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := env.runner.Compact(context.Background(), meta.ID, "mock-1"); err != nil {
		t.Fatalf("Compact 不应检查会话配额: %v", err)
	}
}

func TestDirSize(t *testing.T) {
	t.Run("嵌套目录与符号链接", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "nested"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "a"), []byte("123"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "nested", "b"), []byte("12345"), 0o600); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "large"), make([]byte, 128), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "large"), filepath.Join(root, "file-link")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "dir-link")); err != nil {
			t.Fatal(err)
		}
		size, err := dirSize(1024, root)
		if err != nil || size != 8 {
			t.Fatalf("dirSize = %d, %v; want 8, nil", size, err)
		}
	})

	t.Run("超限提前返回", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "a-first"), []byte("1234"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "z-last"), []byte("12345678"), 0o600); err != nil {
			t.Fatal(err)
		}
		size, err := dirSize(1, root)
		if err != nil || size != 4 {
			t.Fatalf("提前返回时 dirSize = %d, %v; want 4, nil", size, err)
		}
	})

	t.Run("不可读项与缺失目录跳过", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root 可以绕过目录读权限")
		}
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "visible"), []byte("123"), 0o600); err != nil {
			t.Fatal(err)
		}
		locked := filepath.Join(root, "locked")
		if err := os.Mkdir(locked, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(locked, "hidden"), []byte("12345"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
		size, err := dirSize(1024, root, filepath.Join(root, "missing"))
		if err != nil || size != 3 {
			t.Fatalf("跳过不可读项后 dirSize = %d, %v; want 3, nil", size, err)
		}
	})
}

func TestQueueFIFOAndNotices(t *testing.T) {
	env := newTestEnv(t, "basic", withDelay(30), withMaxConcurrent(1))
	first := sendModel(t, env, "", "queue-a")
	firstEvents := collectRun(t, env.runs, first.SessionID)
	waitForEvent(t, firstEvents, "text_delta")
	second := sendModel(t, env, "", "queue-b")
	secondEvents := collectRun(t, env.runs, second.SessionID)
	third := sendModel(t, env, "", "queue-c")
	thirdEvents := collectRun(t, env.runs, third.SessionID)

	notice := waitForEvent(t, secondEvents, "agent_notice")
	if got := notice.Data["notice"]; got != "排队中,前面还有 1 个任务" {
		t.Errorf("B 的排队提示 = %v", got)
	}
	notice = waitForEvent(t, thirdEvents, "agent_notice")
	if got := notice.Data["notice"]; got != "排队中,前面还有 2 个任务" {
		t.Errorf("C 的排队提示 = %v", got)
	}
	firstEvents.wait(t)
	secondEvents.wait(t)
	thirdEvents.wait(t)
	if got, want := startModels(t, env.record), []string{"queue-a", "queue-b", "queue-c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Pi 启动顺序 = %v, want FIFO %v", got, want)
	}
}

func TestQueueTimeout(t *testing.T) {
	env := newTestEnv(t, "basic", withDelay(100), withMaxConcurrent(1), withQueueTimeout(60*time.Millisecond))
	first := sendModel(t, env, "", "queue-timeout-a")
	firstEvents := collectRun(t, env.runs, first.SessionID)
	waitForEvent(t, firstEvents, "text_delta")
	second := sendModel(t, env, "", "queue-timeout-b")
	events := collectRun(t, env.runs, second.SessionID).wait(t)
	if events[len(events)-1].Type != "agent_error" || events[len(events)-1].Data["message"] != "当前 Agent 繁忙,请稍后重试" {
		t.Fatalf("排队超时终态不符: %+v", events[len(events)-1])
	}
	stopAndWait(t, env, first.SessionID, firstEvents)
	if got := startModels(t, env.record); !slices.Equal(got, []string{"queue-timeout-a"}) {
		t.Fatalf("排队超时任务不应启动 Pi: %v", got)
	}
	if env.issuer.issueCount() != 1 {
		t.Fatalf("排队超时任务不应签发令牌,签发数 = %d", env.issuer.issueCount())
	}
}

func TestQueueStop(t *testing.T) {
	env := newTestEnv(t, "basic", withDelay(50), withMaxConcurrent(1))
	first := sendModel(t, env, "", "queue-stop-a")
	firstEvents := collectRun(t, env.runs, first.SessionID)
	waitForEvent(t, firstEvents, "text_delta")
	second := sendModel(t, env, "", "queue-stop-b")
	secondEvents := collectRun(t, env.runs, second.SessionID)
	waitForEvent(t, secondEvents, "agent_notice")
	if err := env.runner.Stop(context.Background(), second.SessionID); err != nil {
		t.Fatal(err)
	}
	events := secondEvents.wait(t)
	if countTerminals(events) != 1 || events[len(events)-1].Type != "agent_cancelled" {
		t.Fatalf("排队中 Stop 终态不符: %v", eventTypes(events))
	}
	stopAndWait(t, env, first.SessionID, firstEvents)
	if got := startModels(t, env.record); !slices.Equal(got, []string{"queue-stop-a"}) {
		t.Fatalf("排队中 Stop 不应启动 Pi: %v", got)
	}
	if env.issuer.issueCount() != 1 {
		t.Fatalf("排队中 Stop 不应签发令牌,签发数 = %d", env.issuer.issueCount())
	}
}

func TestQueueShutdown(t *testing.T) {
	env := newTestEnv(t, "basic", withDelay(50), withMaxConcurrent(1))
	first := sendModel(t, env, "", "queue-shutdown-a")
	firstEvents := collectRun(t, env.runs, first.SessionID)
	waitForEvent(t, firstEvents, "text_delta")
	second := sendModel(t, env, "", "queue-shutdown-b")
	secondEvents := collectRun(t, env.runs, second.SessionID)
	third := sendModel(t, env, "", "queue-shutdown-c")
	thirdEvents := collectRun(t, env.runs, third.SessionID)
	waitForEvent(t, secondEvents, "agent_notice")
	waitForEvent(t, thirdEvents, "agent_notice")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := env.runner.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	for _, events := range [][]translate.Event{firstEvents.wait(t), secondEvents.wait(t), thirdEvents.wait(t)} {
		if events[len(events)-1].Type != "agent_cancelled" {
			t.Errorf("Shutdown 终态 = %v", eventTypes(events))
		}
	}
	if got := startModels(t, env.record); !slices.Equal(got, []string{"queue-shutdown-a"}) {
		t.Fatalf("Shutdown 不应启动排队任务: %v", got)
	}
	if env.runs.Running(first.SessionID) || env.runs.Running(second.SessionID) || env.runs.Running(third.SessionID) {
		t.Fatal("Shutdown 后仍有 runlog 未收尾")
	}
}

func TestQueueFullAndSessionBusy(t *testing.T) {
	env := newTestEnv(t, "basic", withDelay(50), withMaxConcurrent(1), withQueueMax(1))
	first := sendModel(t, env, "", "queue-full-a")
	firstEvents := collectRun(t, env.runs, first.SessionID)
	waitForEvent(t, firstEvents, "text_delta")
	second := sendModel(t, env, "", "queue-full-b")
	secondEvents := collectRun(t, env.runs, second.SessionID)
	waitForEvent(t, secondEvents, "agent_notice")
	thirdMeta, err := env.store.Create("queue-full-c", "ask")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.runner.Send(context.Background(), SendInput{SessionID: thirdMeta.ID, Content: "满", Model: "queue-full-c"}); !errors.Is(err, ErrCapacity) {
		t.Fatalf("队列满错误 = %v", err)
	}
	if env.runs.Current(thirdMeta.ID) != nil {
		t.Fatal("队列满时不应创建 runlog")
	}
	if _, err := env.runner.Send(context.Background(), SendInput{SessionID: second.SessionID, Content: "重发", Model: "queue-full-b"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("同会话排队中 Send 错误 = %v", err)
	}
	env.runner.mu.Lock()
	active, queued := len(env.runner.active), len(env.runner.queue)
	env.runner.mu.Unlock()
	if active != 2 || queued != 1 {
		t.Fatalf("队列满后有残留状态: active=%d queued=%d", active, queued)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := env.runner.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestQueueDisabled(t *testing.T) {
	env := newTestEnv(t, "basic", withDelay(50), withMaxConcurrent(1), withQueueMax(-1))
	first := sendModel(t, env, "", "queue-disabled-a")
	firstEvents := collectRun(t, env.runs, first.SessionID)
	waitForEvent(t, firstEvents, "text_delta")
	meta, err := env.store.Create("queue-disabled-b", "ask")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.runner.Send(context.Background(), SendInput{SessionID: meta.ID, Content: "不排队", Model: "queue-disabled-b"}); !errors.Is(err, ErrCapacity) {
		t.Fatalf("QueueMax=-1 容量错误 = %v", err)
	}
	if env.runs.Current(meta.ID) != nil {
		t.Fatal("直接拒绝时不应创建 runlog")
	}
}

func TestCompactQueue(t *testing.T) {
	t.Run("等待名额后执行", func(t *testing.T) {
		env := newTestEnv(t, "basic", withDelay(100), withMaxConcurrent(1))
		first := sendModel(t, env, "", "queue-compact-a")
		firstEvents := collectRun(t, env.runs, first.SessionID)
		waitForEvent(t, firstEvents, "text_delta")
		meta, err := env.store.Create("queue-compact-b", "ask")
		if err != nil {
			t.Fatal(err)
		}
		type compactResponse struct {
			result CompactResult
			err    error
		}
		response := make(chan compactResponse, 1)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		go func() {
			result, err := env.runner.Compact(ctx, meta.ID, "queue-compact-b")
			response <- compactResponse{result: result, err: err}
		}()
		waitQueueLength(t, env.runner, 1)
		select {
		case got := <-response:
			if got.err != nil || got.result.Summary == "" {
				t.Fatalf("排队 Compact = %+v, %v", got.result, got.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("排队 Compact 未结束")
		}
		firstEvents.wait(t)
		if got, want := startModels(t, env.record), []string{"queue-compact-a", "queue-compact-b"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("Compact 启动顺序 = %v, want %v", got, want)
		}
		if env.runs.Current(meta.ID) != nil {
			t.Fatal("Compact 不应创建 runlog")
		}
	})

	t.Run("排队超时", func(t *testing.T) {
		env := newTestEnv(t, "basic", withDelay(100), withMaxConcurrent(1), withQueueTimeout(50*time.Millisecond))
		first := sendModel(t, env, "", "compact-timeout-a")
		firstEvents := collectRun(t, env.runs, first.SessionID)
		waitForEvent(t, firstEvents, "text_delta")
		meta, err := env.store.Create("compact-timeout-b", "ask")
		if err != nil {
			t.Fatal(err)
		}
		_, err = env.runner.Compact(context.Background(), meta.ID, "compact-timeout-b")
		if !errors.Is(err, ErrCapacity) || !strings.Contains(err.Error(), "Agent 繁忙") {
			t.Fatalf("Compact 排队超时错误 = %v", err)
		}
		stopAndWait(t, env, first.SessionID, firstEvents)
		if got := startModels(t, env.record); !slices.Equal(got, []string{"compact-timeout-a"}) {
			t.Fatalf("超时 Compact 不应启动 Pi: %v", got)
		}
	})

	t.Run("调用方取消", func(t *testing.T) {
		env := newTestEnv(t, "basic", withDelay(100), withMaxConcurrent(1), withQueueTimeout(2*time.Second))
		first := sendModel(t, env, "", "compact-cancel-a")
		firstEvents := collectRun(t, env.runs, first.SessionID)
		waitForEvent(t, firstEvents, "text_delta")
		meta, err := env.store.Create("compact-cancel-b", "ask")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()
		if _, err := env.runner.Compact(ctx, meta.ID, "compact-cancel-b"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Compact 调用方取消错误 = %v", err)
		}
		stopAndWait(t, env, first.SessionID, firstEvents)
	})
}

func TestNodeFlags(t *testing.T) {
	env := newTestEnv(t, "basic", func(cfg *Config, _ *[]string) {
		cfg.NodeFlags = []string{"--max-old-space-size=128", "--trace-warnings"}
	})
	result := sendModel(t, env, "", "node-flags")
	collectRun(t, env.runs, result.SessionID).wait(t)
	records := readRecords(t, env.record)
	if len(records) == 0 {
		t.Fatal("没有启动记录")
	}
	args := recordArgs(t, records[0])
	fakeIndex := slices.Index(args, "--fake-pi")
	if fakeIndex != 3 || !slices.Equal(args[1:fakeIndex], []string{"--max-old-space-size=128", "--trace-warnings"}) {
		t.Fatalf("NodeFlags 不在 Args[0] 之前: %v", args)
	}
}

func sendModel(t *testing.T, env *testEnv, sessionID, model string) SendResult {
	t.Helper()
	result, err := env.runner.Send(context.Background(), SendInput{SessionID: sessionID, Content: "测试", Model: model})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	return result
}

func countTerminals(events []translate.Event) int {
	count := 0
	for _, event := range events {
		if event.Type == "agent_done" || event.Type == "agent_error" || event.Type == "agent_cancelled" {
			count++
		}
	}
	return count
}

func hasCommand(commands []map[string]any, typ string) bool {
	for _, command := range commands {
		if command["type"] == typ {
			return true
		}
	}
	return false
}

func startModels(t *testing.T, path string) []string {
	t.Helper()
	var models []string
	for _, record := range readRecords(t, path) {
		if record["kind"] == "start" {
			models = append(models, flagArg(recordArgs(t, record), "--model"))
		}
	}
	return models
}

func waitQueueLength(t *testing.T, runner *Runner, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runner.mu.Lock()
		length := len(runner.queue)
		runner.mu.Unlock()
		if length == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待队列长度 %d 超时", want)
}

func waitForLog(t *testing.T, logs *syncBuffer, substring string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logs.String(), substring) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("日志缺少 %q: %s", substring, logs.String())
}

func stopAndWait(t *testing.T, env *testEnv, sessionID string, events *collector) {
	t.Helper()
	if err := env.runner.Stop(context.Background(), sessionID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	events.wait(t)
}
