package agentllm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"xlyra/server/internal/auth"
	"xlyra/server/internal/gateway"
)

// CompleteText 在进程内调用网关完成一次非流式文本补全,供输入预测使用;
// 鉴权与请求记录仍走内部 Agent API key,审计 kind 保持 prompt_suggestion。
func (h Handler) CompleteText(ctx context.Context, sessionID, model, system, user string, maxTokens int) (string, error) {
	return h.CompleteTextKind(ctx, sessionID, model, system, user, maxTokens, "prompt_suggestion")
}

// CompleteTextKind 与 CompleteText 相同,但允许内部调用方指定请求审计 kind。
func (h Handler) CompleteTextKind(ctx context.Context, sessionID, model, system, user string, maxTokens int, kind string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"model":      model,
		"stream":     false,
		"max_tokens": maxTokens,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	})
	if err != nil {
		return "", fmt.Errorf("agentllm: encode completion request: %w", err)
	}
	apiKey, err := h.repo.EnsureInternalAPIKey(ctx)
	if err != nil {
		return "", fmt.Errorf("agentllm: ensure internal API key: %w", err)
	}
	ctx = auth.WithAPIKey(ctx, apiKey)
	ctx = gateway.WithInternalRequestMetadata(ctx, map[string]any{
		"agent_session_id": sessionID,
		"agent_transport":  "xlyra-agent",
		"kind":             kind,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("agentllm: create completion request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	next := h.chatCompletions
	if next == nil {
		next = h.gateway.ChatCompletions
	}
	next(rec, req)
	if rec.Code < http.StatusOK || rec.Code >= http.StatusMultipleChoices {
		return "", fmt.Errorf("agentllm: chat completions returned status %d", rec.Code)
	}
	var payload struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		return "", fmt.Errorf("agentllm: decode completion response: %w", err)
	}
	if len(payload.Choices) == 0 {
		return "", errors.New("agentllm: completion response has no choices")
	}
	content := payload.Choices[0].Message.Content
	if strings.TrimSpace(content) == "" {
		return "", errors.New("agentllm: completion response content is empty")
	}
	return content, nil
}
