package agentllm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"xlyra/server/internal/auth"
	"xlyra/server/internal/gateway"
	"xlyra/server/internal/store"
)

func TestCompleteTextRequestAndResponse(t *testing.T) {
	keyID := uuid.New()
	repo := &fakeAgentRepository{internalAPIKey: store.APIKey{ID: keyID, Name: "internal"}}
	handler := newTestHandler(repo)
	var requestBody struct {
		Model     string `json:"model"`
		Stream    bool   `json:"stream"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	var metadata map[string]any
	handler.chatCompletions = func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		key, ok := auth.APIKeyFromContext(r.Context())
		if !ok || key.ID != keyID {
			t.Errorf("internal API key = %+v, %v", key, ok)
		}
		metadata = gateway.InternalRequestMetadataFromContext(r.Context())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"继续"}}]}`))
	}

	text, err := handler.CompleteText(context.Background(), "session-1", "gpt-test", "系统提示", "用户提示", 64)
	if err != nil {
		t.Fatalf("CompleteText: %v", err)
	}
	if text != "继续" {
		t.Fatalf("text = %q", text)
	}
	if requestBody.Model != "gpt-test" || requestBody.Stream || requestBody.MaxTokens != 64 {
		t.Fatalf("request body = %+v", requestBody)
	}
	wantMessages := []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{{Role: "system", Content: "系统提示"}, {Role: "user", Content: "用户提示"}}
	if !reflect.DeepEqual(requestBody.Messages, wantMessages) {
		t.Fatalf("messages = %#v, want %#v", requestBody.Messages, wantMessages)
	}
	wantMetadata := map[string]any{"agent_session_id": "session-1", "agent_transport": "xlyra-agent", "kind": "prompt_suggestion"}
	if !reflect.DeepEqual(metadata, wantMetadata) {
		t.Fatalf("metadata = %#v, want %#v", metadata, wantMetadata)
	}
}

func TestCompleteTextFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "非2xx", status: http.StatusBadGateway, body: `{"error":"upstream"}`},
		{name: "非法JSON", status: http.StatusOK, body: `not-json`},
		{name: "缺少choices", status: http.StatusOK, body: `{"choices":[]}`},
		{name: "空内容", status: http.StatusOK, body: `{"choices":[{"message":{"content":"  "}}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := newTestHandler(&fakeAgentRepository{})
			handler.chatCompletions = func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}
			if text, err := handler.CompleteText(context.Background(), "session-1", "gpt-test", "system", "user", 64); err == nil || text != "" {
				t.Fatalf("CompleteText = %q, %v, want error", text, err)
			}
		})
	}
}

func TestCompleteTextInternalKeyFailureSkipsGateway(t *testing.T) {
	repo := &fakeAgentRepository{internalKeyErr: errors.New("key unavailable")}
	handler := newTestHandler(repo)
	called := false
	handler.chatCompletions = func(http.ResponseWriter, *http.Request) { called = true }
	if _, err := handler.CompleteText(context.Background(), "session-1", "gpt-test", "system", "user", 64); err == nil {
		t.Fatal("内部 API key 获取失败应返回 error")
	}
	if called {
		t.Fatal("内部 API key 获取失败时不应调用网关")
	}
}
