package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xlyra/server/internal/agent/runlog"
	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agent/translate"
)

// realQuestionEnv 用真实 Pi 1.4.0 + 真实 xlyra 扩展,上游用 mock:第一次请求让模型调用
// question 工具,看到 tool 消息后收尾。无沙箱,不要求 Linux/root。
func realQuestionEnv(t *testing.T, questionTimeout time.Duration) (*Runner, *session.Store, *runlog.Registry, string, *atomic.Value) {
	t.Helper()
	node, entry, extension := os.Getenv("AGENT_REAL_NODE"), os.Getenv("AGENT_REAL_PI_ENTRY"), os.Getenv("AGENT_REAL_EXTENSION")
	if node == "" || entry == "" || extension == "" {
		t.Skip("需要 AGENT_REAL_NODE、AGENT_REAL_PI_ENTRY、AGENT_REAL_EXTENSION")
	}
	var toolText atomic.Value
	args := map[string]any{"questions": []any{
		map[string]any{"header": "方案", "question": "选哪个?", "multiSelect": false, "options": []any{
			map[string]any{"label": "A 方案"}, map[string]any{"label": "B 方案", "description": "更快"}}},
		map[string]any{"header": "语言", "question": "用哪些?", "multiSelect": true, "options": []any{
			map[string]any{"label": "中文"}, map[string]any{"label": "英文"}}},
	}}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/internal/agent-llm/v1/chat/completions" {
			http.NotFound(w, req)
			return
		}
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(req.Body).Decode(&body)
		last := body.Messages[len(body.Messages)-1]
		hasTool := last.Role == "tool"
		if hasTool {
			toolText.Store(fmt.Sprint(last.Content))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(delta map[string]any, finish string) {
			chunk := map[string]any{"id": "c", "object": "chat.completion.chunk", "created": 1, "model": "mock-1",
				"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
			data, _ := json.Marshal(chunk)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
		if !hasTool {
			write(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
				"index": 0, "id": "call_q", "type": "function",
				"function": map[string]any{"name": "question", "arguments": string(mustJSON(args))},
			}}}, "tool_calls")
		} else {
			write(map[string]any{"role": "assistant", "content": "收到"}, "stop")
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	t.Cleanup(mock.Close)
	dir := t.TempDir()
	store := session.NewStore(filepath.Join(dir, "sessions"))
	meta, err := store.Create("mock-1", "full")
	if err != nil {
		t.Fatal(err)
	}
	runs := runlog.NewRegistry()
	r, err := New(Config{
		Store: store, Runs: runs, Tokens: &fakeIssuer{},
		Protocol:       func(context.Context, string) (string, error) { return "openai-chat-completions", nil },
		GatewayBaseURL: mock.URL, Command: node, Args: []string{entry}, ExtensionPath: extension,
		PathEnv: os.Getenv("PATH"), RunTimeout: time.Minute, AbortGrace: 2 * time.Second,
		QuestionTimeout: questionTimeout,
		Logger:          slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Shutdown(context.Background()) })
	return r, store, runs, meta.ID, &toolText
}

func realSend(t *testing.T, r *Runner, id string) {
	t.Helper()
	if _, err := r.Send(context.Background(), SendInput{SessionID: id, Content: "帮我选", Model: "mock-1", PermissionMode: "full"}); err != nil {
		t.Fatal(err)
	}
}

func TestRealPiQuestionAnswer(t *testing.T) {
	r, store, runs, id, toolText := realQuestionEnv(t, 0)
	realSend(t, r, id)
	c := collectRun(t, runs, id)
	req := waitForEvent(t, c, "question_request")
	qid := req.Data["question"].(map[string]any)["question_id"].(string)
	if err := r.AnswerQuestion(context.Background(), AnswerQuestionInput{SessionID: id, QuestionID: qid, Answers: []session.QuestionAnswer{
		{Selected: []string{"B 方案"}}, {Selected: []string{"中文", session.OtherOptionMarker}, Other: "还要日文"},
	}}); err != nil {
		t.Fatal(err)
	}
	events := c.wait(t)
	if events[len(events)-1].Type != "agent_done" {
		t.Fatalf("终态: %v", eventTypes(events))
	}
	text, _ := toolText.Load().(string)
	t.Logf("模型收到的工具结果: %s", text)
	if !strings.Contains(text, "B 方案") || !strings.Contains(text, "还要日文") {
		t.Fatalf("工具结果不含回答: %q", text)
	}
	meta, _ := store.Get(id)
	if len(meta.Questions) != 1 || meta.Questions[0].AnsweredAt == nil {
		t.Fatalf("meta.Questions = %+v", meta.Questions)
	}
}

func TestRealPiQuestionStopAndTimeout(t *testing.T) {
	for _, tc := range []string{"stop", "timeout"} {
		t.Run(tc, func(t *testing.T) {
			timeout := time.Duration(0)
			if tc == "timeout" {
				timeout = 1500 * time.Millisecond
			}
			r, store, runs, id, toolText := realQuestionEnv(t, timeout)
			realSend(t, r, id)
			c := collectRun(t, runs, id)
			waitForEvent(t, c, "question_request")
			if tc == "stop" {
				if err := r.Stop(context.Background(), id); err != nil {
					t.Fatal(err)
				}
			}
			events := c.wait(t)
			answered := eventsOfType(events, "question_answered")
			if len(answered) != 1 {
				t.Fatalf("事件: %v", eventTypes(events))
			}
			meta, _ := store.Get(id)
			q := meta.Questions[0]
			if tc == "stop" && !q.Cancelled {
				t.Fatalf("应取消: %+v", q)
			}
			if tc == "timeout" {
				text, _ := toolText.Load().(string)
				t.Logf("模型收到的工具结果: %s", text)
				if !q.Skipped || !q.Auto || events[len(events)-1].Type != "agent_done" {
					t.Fatalf("应自动跳过并完成: %+v %v", q, eventTypes(events))
				}
			}
		})
	}
}

var _ = translate.Event{}
