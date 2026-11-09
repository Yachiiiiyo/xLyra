package agentllm

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"xlyra/server/internal/httpx"
)

const (
	fastTaskMaxCalls          = 40
	fastTaskMaxInstruction    = 2000
	fastTaskMaxContentBytes   = 300000
	fastTaskMaxTokens         = 1500
	fastTaskTimeout           = 90 * time.Second
	fastTaskKind              = "fast_task"
	FastTaskMaxRequestBytes   = 2 << 20 // 内容按 JSON 转义后最多膨胀数倍,单独给 2 MiB 上限
	fastTaskDefaultPathString = "(未提供)"
)

// fastTaskSystemPrompt 是文件摘要等快速任务的系统提示。
const fastTaskSystemPrompt = `你是文件内容提炼助手。
- 只依据给定的文件内容回答,不要编造内容里没有的信息。
- 按用户关注点提炼要点,尽量标出所在位置(章节、函数名或行号附近)。
- 如果说明里提到内容被截断,要在回答中指出结论只覆盖已提供的部分。
- 输出简洁,不要复述原文,不要寒暄。`

type fastTaskInput struct {
	Model       string `json:"model"`
	Instruction string `json:"instruction"`
	Content     string `json:"content"`
	Path        string `json:"path"`
}

// FastTask 用快速模型处理一段体量大、价值低的内容(目前是文件摘要),只把精炼
// 结果返回给扩展;鉴权与 Images 一致,按 run 计数限次。
func (h *Handler) FastTask(w http.ResponseWriter, r *http.Request) {
	token := bearer(r.Header.Get("Authorization"))
	if token == "" {
		h.fail(w, r, http.StatusUnauthorized, "unauthorized", "temporary agent credential is required")
		return
	}
	item, err := h.repo.FindUsableToken(r.Context(), token, h.now())
	if err != nil {
		h.fail(w, r, http.StatusUnauthorized, "unauthorized", "temporary agent credential is invalid")
		return
	}
	var input fastTaskInput
	if err := decode(r, &input); err != nil {
		h.fail(w, r, http.StatusBadRequest, "invalid_request", "invalid fast task request")
		return
	}
	input.Model = strings.TrimSpace(input.Model)
	if input.Model == "" {
		h.fail(w, r, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	input.Instruction = strings.TrimSpace(input.Instruction)
	if utf8.RuneCountInString(input.Instruction) > fastTaskMaxInstruction {
		h.fail(w, r, http.StatusBadRequest, "invalid_request", "instruction must not exceed 2000 characters")
		return
	}
	if len(input.Content) > fastTaskMaxContentBytes {
		h.fail(w, r, http.StatusBadRequest, "invalid_request", "content must not exceed 300000 bytes")
		return
	}
	tracker := h.fastTaskTracker
	if tracker == nil {
		h.fail(w, r, http.StatusServiceUnavailable, "gateway_unavailable", "Agent fast task is not available")
		return
	}
	// 计数仿 Images,但不串行执行:同 run 的多次摘要可以并行。
	key := item.SessionID + "\x00" + item.RunID
	now := h.now()
	tracker.mu.Lock()
	for id, state := range tracker.runs {
		if !state.expiresAt.After(now) {
			delete(tracker.runs, id)
		}
	}
	state := tracker.runs[key]
	if state == nil {
		state = &agentImageRun{}
		tracker.runs[key] = state
	}
	state.expiresAt = item.ExpiresAt
	if state.calls >= fastTaskMaxCalls {
		tracker.mu.Unlock()
		h.fail(w, r, http.StatusTooManyRequests, "fast_task_quota_exceeded", "Agent fast task quota for this run is exhausted")
		return
	}
	state.calls++
	tracker.mu.Unlock()

	path := strings.TrimSpace(input.Path)
	if path == "" {
		path = fastTaskDefaultPathString
	}
	user := "文件:" + path + "\n" + input.Instruction + "\n\n<file>\n" + input.Content + "\n</file>"
	ctx, cancel := context.WithTimeout(r.Context(), fastTaskTimeout)
	defer cancel()
	started := time.Now()
	text, err := h.CompleteTextKind(ctx, item.SessionID, input.Model, fastTaskSystemPrompt, user, fastTaskMaxTokens, fastTaskKind)
	if err != nil {
		if h.logger != nil {
			h.logger.Debug("快速任务失败", "session_id", item.SessionID, "run_id", item.RunID, "model", input.Model, "duration", time.Since(started), "error", err)
		}
		var statusErr *CompletionStatusError
		if errors.As(err, &statusErr) && statusErr.Status == http.StatusTooManyRequests {
			h.fail(w, r, http.StatusTooManyRequests, "upstream_rate_limited", "fast model is rate limited")
			return
		}
		h.fail(w, r, http.StatusBadGateway, "upstream_failed", "fast model request failed")
		return
	}
	if h.logger != nil {
		h.logger.Debug("快速任务完成", "session_id", item.SessionID, "run_id", item.RunID, "model", input.Model, "duration", time.Since(started), "bytes", len(input.Content))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": map[string]any{"text": text, "model": input.Model}})
}
