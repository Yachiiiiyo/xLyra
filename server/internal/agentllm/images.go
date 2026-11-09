package agentllm

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"xlyra/server/internal/agentproxy"
	"xlyra/server/internal/auth"
	"xlyra/server/internal/gateway"
	"xlyra/server/internal/httpx"
)

type agentImageTracker struct {
	mu   sync.Mutex
	runs map[string]*agentImageRun
}

func newAgentImageTracker() *agentImageTracker {
	return &agentImageTracker{runs: make(map[string]*agentImageRun)}
}

type agentImageRun struct {
	mu        sync.Mutex
	calls     int
	expiresAt time.Time
}

type imageGenerationInput struct {
	Prompt  string `json:"prompt"`
	Size    string `json:"size"`
	Quality string `json:"quality"`
}

func (h *Handler) SetImageModelsSource(source func() []agentproxy.ImageModelSelection) {
	h.imageModelsSource = source
}

func (h *Handler) SetImageGenerationOptions(maxCalls int, totalTimeout time.Duration) {
	h.imageMaxCalls = maxCalls
	h.imageTotalTimeout = totalTimeout
}

// clearImageRun 在 run 结束时清掉该 run 的生图与快速任务计数。
func (h *Handler) clearImageRun(sessionID, runID string) {
	for _, tracker := range []*agentImageTracker{h.imageTracker, h.fastTaskTracker} {
		if tracker == nil {
			continue
		}
		tracker.mu.Lock()
		delete(tracker.runs, sessionID+"\x00"+runID)
		tracker.mu.Unlock()
	}
}

func (h *Handler) Images(w http.ResponseWriter, r *http.Request) {
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
	var input imageGenerationInput
	if err := decode(r, &input); err != nil {
		h.fail(w, r, http.StatusBadRequest, "invalid_request", "invalid image generation request")
		return
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	if input.Prompt == "" || utf8.RuneCountInString(input.Prompt) > 4000 {
		h.fail(w, r, http.StatusBadRequest, "invalid_request", "prompt must contain between 1 and 4000 characters")
		return
	}
	if !imageEnumAllowed(input.Size, "", "1024x1024", "1536x1024", "1024x1536", "auto") {
		h.fail(w, r, http.StatusBadRequest, "invalid_request", "invalid image size")
		return
	}
	if !imageEnumAllowed(input.Quality, "", "low", "medium", "high", "auto") {
		h.fail(w, r, http.StatusBadRequest, "invalid_request", "invalid image quality")
		return
	}
	var models []agentproxy.ImageModelSelection
	if h.imageModelsSource != nil {
		models = h.imageModelsSource()
	}
	if len(models) == 0 {
		h.fail(w, r, http.StatusConflict, "image_model_not_configured", "no Agent image generation model is configured")
		return
	}
	apiKey, err := h.repo.EnsureInternalAPIKey(r.Context())
	if err != nil {
		h.fail(w, r, http.StatusServiceUnavailable, "gateway_unavailable", "internal agent gateway is not available")
		return
	}
	maxCalls := h.imageMaxCalls
	if maxCalls < 1 {
		maxCalls = 3
	}
	key := item.SessionID + "\x00" + item.RunID
	now := h.now()
	tracker := h.imageTracker
	if tracker == nil {
		h.fail(w, r, http.StatusServiceUnavailable, "gateway_unavailable", "Agent image generation is not available")
		return
	}
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
	tracker.mu.Unlock()

	// 同 run 串行计数与执行，避免并发请求绕过配额。
	state.mu.Lock()
	defer state.mu.Unlock()
	h.imageTracker.mu.Lock()
	if state.calls >= maxCalls {
		h.imageTracker.mu.Unlock()
		h.fail(w, r, http.StatusTooManyRequests, "image_quota_exceeded", "Agent image generation quota for this run is exhausted")
		return
	}
	state.calls++
	h.imageTracker.mu.Unlock()

	ctx := auth.WithAPIKey(r.Context(), apiKey)
	ctx = gateway.WithInternalRequestMetadata(ctx, map[string]any{
		"agent_instance_id": item.AgentInstanceID,
		"agent_session_id":  item.SessionID,
		"agent_run_id":      item.RunID,
		"agent_transport":   "xlyra-agent",
		"kind":              "image",
	})
	totalTimeout := h.imageTotalTimeout
	if totalTimeout <= 0 {
		totalTimeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, totalTimeout)
	defer cancel()
	requestID := middleware.GetReqID(r.Context())
	if requestID == "" {
		requestID = "agent-image-" + item.RunID
	}
	generate := h.imageGenerate
	if generate == nil {
		generate = h.gateway.GenerateImage
	}
	gatewayModels := make([]gateway.ImageGenerationModel, 0, len(models))
	for _, selection := range models {
		model := gateway.ImageGenerationModel{Model: selection.Model, SiteIDs: make([]uuid.UUID, 0, len(selection.SiteIDs))}
		for _, rawSiteID := range selection.SiteIDs {
			if siteID, err := uuid.Parse(rawSiteID); err == nil {
				model.SiteIDs = append(model.SiteIDs, siteID)
			}
		}
		gatewayModels = append(gatewayModels, model)
	}
	result := generate(ctx, gateway.ImageGenerationRequest{
		RequestID: requestID,
		APIKeyID:  apiKey.ID,
		Models:    gatewayModels,
		Prompt:    input.Prompt,
		Size:      input.Size,
		Quality:   input.Quality,
	})
	if !result.OK {
		h.fail(w, r, http.StatusBadGateway, "upstream_failed", result.ErrorMessage)
		return
	}
	type attempt struct {
		Model string `json:"model"`
		Site  string `json:"site"`
		Error string `json:"error"`
	}
	attempts := make([]attempt, 0, len(result.Attempts))
	for _, item := range result.Attempts {
		attempts = append(attempts, attempt{Model: item.Model, Site: item.SiteName, Error: item.Error})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"b64_json":       result.B64,
		"revised_prompt": result.RevisedPrompt,
		"model":          result.Model,
		"site":           result.SiteName,
		"attempts":       attempts,
	}})
}

func imageEnumAllowed(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
