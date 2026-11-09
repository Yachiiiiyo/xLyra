package agentllm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xlyra/server/internal/gateway"
)

func fastTaskRequest(token string, body any) (*httptest.ResponseRecorder, *http.Request) {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/internal/agent-llm/fast-task", strings.NewReader(string(raw)))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return httptest.NewRecorder(), req
}

func validFastTaskBody() map[string]string {
	return map[string]string{"model": "fast-1", "instruction": "用户关注点:概括", "content": "line1\nline2", "path": "logs/app.log"}
}

func TestFastTaskSuccessAndKind(t *testing.T) {
	h, _ := newImageTestHandler()
	var metadata map[string]any
	var requestBody struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	h.chatCompletions = func(w http.ResponseWriter, r *http.Request) {
		metadata = gateway.InternalRequestMetadataFromContext(r.Context())
		_ = json.NewDecoder(r.Body).Decode(&requestBody)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"摘要正文"}}]}`))
	}
	rec, req := fastTaskRequest("live-token", validFastTaskBody())
	h.FastTask(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data struct {
			Text  string `json:"text"`
			Model string `json:"model"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Text != "摘要正文" || payload.Data.Model != "fast-1" {
		t.Fatalf("response=%+v", payload.Data)
	}
	if metadata["kind"] != "fast_task" || metadata["agent_session_id"] != "sess-1" {
		t.Fatalf("metadata=%v", metadata)
	}
	if requestBody.Model != "fast-1" || requestBody.MaxTokens != 1500 || len(requestBody.Messages) != 2 {
		t.Fatalf("request=%+v", requestBody)
	}
	user := requestBody.Messages[1].Content
	if !strings.Contains(user, "logs/app.log") || !strings.Contains(user, "用户关注点:概括") || !strings.Contains(user, "line1\nline2") {
		t.Fatalf("user prompt=%q", user)
	}
	if requestBody.Messages[0].Content != fastTaskSystemPrompt {
		t.Fatalf("system prompt=%q", requestBody.Messages[0].Content)
	}
}

func TestFastTaskAuthenticationAndValidation(t *testing.T) {
	h, _ := newImageTestHandler()
	called := false
	h.chatCompletions = func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"x"}}]}`))
	}
	for _, token := range []string{"", "bad-token"} {
		rec, req := fastTaskRequest(token, validFastTaskBody())
		h.FastTask(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: status=%d", token, rec.Code)
		}
	}
	mutate := func(key, value string) map[string]string {
		body := validFastTaskBody()
		body[key] = value
		return body
	}
	for name, body := range map[string]map[string]string{
		"空模型":  mutate("model", " "),
		"说明超长": mutate("instruction", strings.Repeat("说", 2001)),
		"内容超限": mutate("content", strings.Repeat("a", 300001)),
	} {
		rec, req := fastTaskRequest("live-token", body)
		h.FastTask(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d body=%s", name, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/agent-llm/fast-task", strings.NewReader("not-json"))
	req.Header.Set("Authorization", "Bearer live-token")
	h.FastTask(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON: status=%d", rec.Code)
	}
	if called {
		t.Fatal("参数或鉴权失败时不应调用网关")
	}
	// 恰好 300000 字节可以通过
	rec, req = fastTaskRequest("live-token", mutate("content", strings.Repeat("a", 300000)))
	h.FastTask(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("边界内容: status=%d", rec.Code)
	}
	// 空内容未在协议中禁止,同样交给快速模型处理
	rec, req = fastTaskRequest("live-token", mutate("content", ""))
	h.FastTask(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("空内容: status=%d", rec.Code)
	}
}

func TestFastTaskUpstreamErrorMapping(t *testing.T) {
	for _, tt := range []struct {
		upstream int
		want     int
	}{{http.StatusTooManyRequests, http.StatusTooManyRequests}, {http.StatusBadGateway, http.StatusBadGateway}, {http.StatusInternalServerError, http.StatusBadGateway}} {
		h, _ := newImageTestHandler()
		h.chatCompletions = func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tt.upstream)
			_, _ = w.Write([]byte(`{"error":{"message":"secret upstream detail"}}`))
		}
		rec, req := fastTaskRequest("live-token", validFastTaskBody())
		h.FastTask(rec, req)
		if rec.Code != tt.want || strings.Contains(rec.Body.String(), "secret upstream detail") {
			t.Fatalf("upstream %d: status=%d body=%s", tt.upstream, rec.Code, rec.Body.String())
		}
	}
}

func TestFastTaskQuotaPerRun(t *testing.T) {
	h, _ := newImageTestHandler()
	calls := 0
	h.chatCompletions = func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}
	for index := 0; index < fastTaskMaxCalls; index++ {
		rec, req := fastTaskRequest("live-token", validFastTaskBody())
		h.FastTask(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: status=%d", index+1, rec.Code)
		}
	}
	rec, req := fastTaskRequest("live-token", validFastTaskBody())
	h.FastTask(rec, req)
	if rec.Code != http.StatusTooManyRequests || calls != fastTaskMaxCalls {
		t.Fatalf("超限: status=%d calls=%d", rec.Code, calls)
	}
	// run 结束后清掉计数
	h.clearImageRun("sess-1", "run-1")
	h.fastTaskTracker.mu.Lock()
	remaining := len(h.fastTaskTracker.runs)
	h.fastTaskTracker.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("clearImageRun 未清理快速任务计数: %d", remaining)
	}
}
