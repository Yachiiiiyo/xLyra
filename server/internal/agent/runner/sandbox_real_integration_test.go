package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xlyra/server/internal/agent/isolation"
	"xlyra/server/internal/agent/runlog"
	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agent/translate"
)

func TestRunnerRealPiSandboxBash(t *testing.T) {
	requireSandboxIntegration(t)
	node, entry, extension := os.Getenv("AGENT_REAL_NODE"), os.Getenv("AGENT_REAL_PI_ENTRY"), os.Getenv("AGENT_REAL_EXTENSION")
	if node == "" || entry == "" || extension == "" {
		t.Skip("需要 AGENT_REAL_NODE、AGENT_REAL_PI_ENTRY、AGENT_REAL_EXTENSION")
	}
	root := shortTempDir(t)
	makeTraversable(t, root)
	makeTraversable(t, filepath.Dir(os.Args[0]))
	dataDir := filepath.Join(root, "agent")
	if err := isolation.PrepareDataDir(dataDir); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "secret")
	const secretText = "REAL-PI-SANDBOX-SECRET"
	if err := os.WriteFile(secret, []byte(secretText), 0o644); err != nil {
		t.Fatal(err)
	}
	commands := []string{"echo hi-sandbox; id -u", "cat " + secret}
	var requestIndex atomic.Int32
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/internal/agent-llm/v1/chat/completions" {
			http.NotFound(w, req)
			return
		}
		var body struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(req.Body).Decode(&body)
		// 会话续接时历史里已有上一轮的 tool 消息,只看最后一条才能区分"该发工具调用"与"该收尾"
		hasTool := len(body.Messages) > 0 && body.Messages[len(body.Messages)-1].Role == "tool"
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		writeChunk := func(chunk map[string]any) {
			data, _ := json.Marshal(chunk)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
		base := func() map[string]any {
			return map[string]any{"id": "chatcmpl-sandbox", "object": "chat.completion.chunk", "created": 1, "model": "mock-1"}
		}
		if !hasTool {
			index := int(requestIndex.Add(1)) - 1
			command := commands[index%len(commands)]
			chunk := base()
			chunk["choices"] = []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
				"index": 0, "id": "call_sandbox", "type": "function",
				"function": map[string]any{"name": "bash", "arguments": string(mustJSON(map[string]string{"command": command}))},
			}}}, "finish_reason": "tool_calls"}}
			writeChunk(chunk)
		} else {
			chunk := base()
			chunk["choices"] = []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "完成"}, "finish_reason": "stop"}}
			writeChunk(chunk)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer mock.Close()
	store := session.NewStore(filepath.Join(dataDir, "sessions"))
	meta, err := store.Create("mock-1", "full")
	if err != nil {
		t.Fatal(err)
	}
	runs := runlog.NewRegistry()
	r, err := New(Config{
		Store: store, Runs: runs, Tokens: &fakeIssuer{},
		Protocol:       func(context.Context, string) (string, error) { return "openai-chat-completions", nil },
		GatewayBaseURL: mock.URL, Command: node, Args: []string{entry}, ExtensionPath: extension,
		PathEnv: os.Getenv("PATH"), Isolation: &IsolationConfig{},
		Sandbox:    &SandboxConfig{Exe: os.Args[0], PiLandlock: true, Bash: true},
		RunTimeout: 2 * time.Minute, AbortGrace: 2 * time.Second,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Shutdown(context.Background())
	identity := func() isolation.Identity {
		meta, err := store.Get(meta.ID)
		if err != nil {
			t.Fatal(err)
		}
		id, err := isolation.IdentityFor(isolation.Config{}, meta.Slot)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	for i := 0; i < 2; i++ {
		// 终态事件发布后 run 还在收尾(关执行器、清理 uid),等它释放会话
		var sendErr error
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			_, sendErr = r.Send(context.Background(), SendInput{SessionID: meta.ID, Content: "执行命令", Model: "mock-1", PermissionMode: "full"})
			if !errors.Is(sendErr, ErrBusy) {
				break
			}
		}
		if sendErr != nil {
			t.Fatal(sendErr)
		}
		events := collectRun(t, runs, meta.ID).wait(t)
		requireSandboxTerminal(t, events, "agent_done")
		output := realToolOutput(events)
		if i == 0 {
			if !strings.Contains(output, "hi-sandbox") || !strings.Contains(output, strconv.Itoa(int(identity().ExecUID))) {
				t.Fatalf("命令输出 = %q", output)
			}
		} else if strings.Contains(output, secretText) {
			t.Fatalf("秘密文件被读出: %q", output)
		}
		assertSandboxUIDsGone(t, identity())
	}
	audit, err := os.ReadFile(filepath.Join(store.Dir(meta.ID), "exec-audit.jsonl"))
	if err != nil || !strings.Contains(string(audit), "hi-sandbox") || !strings.Contains(string(audit), "cat ") {
		t.Fatalf("审计 = %s, %v", audit, err)
	}
}

func mustJSON(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}

func realToolOutput(events []translate.Event) string {
	var output strings.Builder
	for _, event := range events {
		if event.Type != "tool_result" {
			continue
		}
		result, _ := event.Data["tool_result"].(map[string]any)
		text, _ := result["output"].(string)
		output.WriteString(text)
	}
	return output.String()
}
