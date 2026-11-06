package runner

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"xlyra/server/internal/agent/runlog"
	"xlyra/server/internal/agent/session"
)

func newTitleTestRunner(t *testing.T, complete func(context.Context, string, string, string, string, int, string) (string, error), fastModel func() string) (*Runner, *session.Store, *runlog.Registry) {
	t.Helper()
	store := session.NewStore(filepath.Join(t.TempDir(), "sessions"))
	runs := runlog.NewRegistry()
	r, err := New(Config{
		Store: store, Runs: runs, Tokens: &fakeIssuer{},
		Protocol: func(context.Context, string) (string, error) {
			return "openai-chat-completions", nil
		},
		GatewayBaseURL: "http://127.0.0.1:18080", Command: "node",
		CompleteText: complete, FastModel: fastModel,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := r.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	return r, store, runs
}

func newTitleTestSession(t *testing.T, store *session.Store, content string) (session.Meta, string) {
	t.Helper()
	meta, err := store.Create("main-model", "full")
	if err != nil {
		t.Fatal(err)
	}
	placeholder := session.TitleFrom(content)
	if _, err := store.Update(meta.ID, func(m *session.Meta) error {
		m.Title = placeholder
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return meta, placeholder
}

func waitTitleTasks(t *testing.T, r *Runner) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("等待标题任务结束超时")
	}
}

func assertNoSessionTitleEvent(t *testing.T, run *runlog.Run) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for event := range run.Subscribe(ctx) {
		if event.Type == "session_title" {
			t.Fatalf("不应发布标题事件: %+v", event)
		}
	}
}

func TestGenerateTitleWritesAndPublishes(t *testing.T) {
	content := strings.Repeat("问", 600)
	var gotSessionID, gotModel, gotSystem, gotUser, gotKind string
	var gotMaxTokens, calls int
	var hasDeadline bool
	r, store, runs := newTitleTestRunner(t, func(ctx context.Context, sessionID, model, system, user string, maxTokens int, kind string) (string, error) {
		calls++
		gotSessionID, gotModel, gotSystem, gotUser, gotKind = sessionID, model, system, user, kind
		gotMaxTokens = maxTokens
		deadline, ok := ctx.Deadline()
		hasDeadline = ok && time.Until(deadline) > 0 && time.Until(deadline) <= titleTimeout
		return "《整理长消息》。\n第二行不会采用", nil
	}, func() string { return " fast-model " })
	meta, _ := newTitleTestSession(t, store, content)
	run, err := runs.Begin(meta.ID, "title-run-success")
	if err != nil {
		t.Fatal(err)
	}
	defer run.Finish()
	ar := &activeRun{sessionID: meta.ID, runID: run.ID(), model: "main-model", run: run}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := run.Subscribe(ctx)
	r.maybeGenerateTitle(ar, content)
	waitTitleTasks(t, r)

	updated, err := store.Get(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "整理长消息" {
		t.Fatalf("标题 = %q", updated.Title)
	}
	select {
	case event := <-events:
		if event.Type != "session_title" || event.Data["title"] != "整理长消息" {
			t.Fatalf("标题事件 = %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("未收到 session_title 事件")
	}
	if calls != 1 || gotSessionID != meta.ID || gotModel != "fast-model" || gotSystem != titleSystemPrompt || gotKind != "session_title" || gotMaxTokens != 100 || !hasDeadline {
		t.Fatalf("补全参数: calls=%d session=%q model=%q kind=%q maxTokens=%d deadline=%v", calls, gotSessionID, gotModel, gotKind, gotMaxTokens, hasDeadline)
	}
	if gotUser != strings.Repeat("问", 500) || utf8.RuneCountInString(gotUser) != 500 {
		t.Fatalf("用户提示未按 rune 截到 500: %d", utf8.RuneCountInString(gotUser))
	}
}

func TestGenerateTitleDoesNotOverwriteChangedPlaceholder(t *testing.T) {
	content := "帮我分析这个仓库"
	started := make(chan struct{})
	release := make(chan struct{})
	r, store, runs := newTitleTestRunner(t, func(context.Context, string, string, string, string, int, string) (string, error) {
		close(started)
		<-release
		return "模型生成标题", nil
	}, nil)
	meta, _ := newTitleTestSession(t, store, content)
	run, err := runs.Begin(meta.ID, "title-run-changed")
	if err != nil {
		t.Fatal(err)
	}
	ar := &activeRun{sessionID: meta.ID, runID: run.ID(), model: "main-model", run: run}
	r.maybeGenerateTitle(ar, content)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("标题补全未开始")
	}
	if _, err := store.Update(meta.ID, func(m *session.Meta) error {
		m.Title = "用户已修改"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitTitleTasks(t, r)
	updated, err := store.Get(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "用户已修改" {
		t.Fatalf("占位标题变更后被覆盖: %q", updated.Title)
	}
	run.Finish()
	assertNoSessionTitleEvent(t, run)
}

func TestGenerateTitleFailureKeepsOriginal(t *testing.T) {
	content := "原始首条消息"
	var calls int
	r, store, runs := newTitleTestRunner(t, func(context.Context, string, string, string, string, int, string) (string, error) {
		calls++
		return "", errors.New("上游不可用")
	}, nil)
	meta, placeholder := newTitleTestSession(t, store, content)
	run, err := runs.Begin(meta.ID, "title-run-failure")
	if err != nil {
		t.Fatal(err)
	}
	r.maybeGenerateTitle(&activeRun{sessionID: meta.ID, runID: run.ID(), model: "main-model", run: run}, content)
	waitTitleTasks(t, r)
	updated, err := store.Get(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != placeholder || calls != 1 {
		t.Fatalf("失败后标题 = %q, 调用次数 = %d", updated.Title, calls)
	}
	run.Finish()
	assertNoSessionTitleEvent(t, run)
}

func TestGenerateTitleAfterRunFinishedOnlyPersists(t *testing.T) {
	content := "run 很快就结束"
	r, store, runs := newTitleTestRunner(t, func(context.Context, string, string, string, string, int, string) (string, error) {
		return "延迟标题", nil
	}, nil)
	meta, _ := newTitleTestSession(t, store, content)
	run, err := runs.Begin(meta.ID, "title-run-finished")
	if err != nil {
		t.Fatal(err)
	}
	run.Finish()
	r.maybeGenerateTitle(&activeRun{sessionID: meta.ID, runID: run.ID(), model: "main-model", run: run}, content)
	waitTitleTasks(t, r)
	updated, err := store.Get(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "延迟标题" {
		t.Fatalf("run 结束后未落盘: %q", updated.Title)
	}
	assertNoSessionTitleEvent(t, run)
}

func TestCleanTitle(t *testing.T) {
	long := strings.Repeat("标", 45)
	for _, tt := range []struct {
		name, input, want string
	}{
		{name: "取首行并去引号和句读", input: "  \"修复登录超时。\"  \n第二行", want: "修复登录超时"},
		{name: "书名号", input: "《整理项目依赖》", want: "整理项目依赖"},
		{name: "反引号与连续句读", input: "`汇总日志错误`。!", want: "汇总日志错误"},
		{name: "中文引号", input: "「分析配置」", want: "分析配置"},
		{name: "清洗后为空", input: "《》。", want: ""},
		{name: "只有空白", input: " \n\t ", want: ""},
		{name: "按 rune 截断", input: long, want: strings.Repeat("标", 40)},
		{name: "保留成对括号", input: "修复 foo()。", want: "修复 foo()"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := cleanTitle(tt.input); got != tt.want {
				t.Fatalf("cleanTitle(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSendNonFirstDoesNotGenerateTitle(t *testing.T) {
	var calls atomic.Int64
	env := newTestEnv(t, "basic", func(cfg *Config, _ *[]string) {
		cfg.CompleteText = func(context.Context, string, string, string, string, int, string) (string, error) {
			calls.Add(1)
			return "不应生成", nil
		}
	})
	meta, err := env.store.Create("mock-1", "full")
	if err != nil {
		t.Fatal(err)
	}
	content := "第二条消息"
	if _, err := env.store.Update(meta.ID, func(m *session.Meta) error {
		m.Title = session.TitleFrom(content)
		m.Messages = []session.UserMessage{{MessageID: "first-message", Text: "第一条消息", CreatedAt: time.Now().UTC()}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result, err := env.runner.Send(context.Background(), SendInput{SessionID: meta.ID, Content: content, Model: "mock-1", PermissionMode: "full"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	events := collectRun(t, env.runs, result.SessionID).wait(t)
	if len(events) == 0 || events[len(events)-1].Type != "agent_done" {
		t.Fatalf("run 未正常结束: %v", eventTypes(events))
	}
	if calls.Load() != 0 {
		t.Fatalf("非首条消息调用了标题补全 %d 次", calls.Load())
	}
	updated, err := env.store.Get(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != session.TitleFrom(content) {
		t.Fatalf("非首条消息标题被修改: %q", updated.Title)
	}
}

func TestGenerateTitleCancellation(t *testing.T) {
	for _, mode := range []string{"stop", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			started := make(chan struct{})
			finished := make(chan error, 1)
			r, store, _ := newTitleTestRunner(t, func(ctx context.Context, _ string, _ string, _ string, _ string, _ int, _ string) (string, error) {
				close(started)
				<-ctx.Done()
				finished <- ctx.Err()
				return "", ctx.Err()
			}, nil)
			content := "等待取消的标题"
			meta, placeholder := newTitleTestSession(t, store, content)
			ar := &activeRun{sessionID: meta.ID, model: "main-model", cancel: func() {}}
			r.maybeGenerateTitle(ar, content)
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("标题补全未开始")
			}
			if mode == "stop" {
				r.stopRun(ar)
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := r.Shutdown(ctx); err != nil {
					t.Fatalf("Shutdown: %v", err)
				}
			}
			select {
			case err := <-finished:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("取消原因 = %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("标题补全未随停止取消")
			}
			waitTitleTasks(t, r)
			updated, err := store.Get(meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Title != placeholder {
				t.Fatalf("取消后标题被修改: %q", updated.Title)
			}
		})
	}
}
