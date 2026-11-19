package agent

import (
	"net/http"

	"xlyra/server/internal/httpx"
)

// Models GET /models → {"data":{"models":[...],"default_model":"..."}}。
// 可用模型为空时仍返回 200 与空数组;默认模型不在列表中时省略。
func (h *Handler) Models(w http.ResponseWriter, r *http.Request) {
	if h.models == nil {
		httpx.Error(w, r, http.StatusServiceUnavailable, "agent_unavailable", "可用模型不可用")
		return
	}
	ctx := r.Context()
	sites, err := h.models.Sites(ctx)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	modelsBySite, err := h.models.SiteModels(ctx)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	canonical, err := h.models.CanonicalModels(ctx)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	scope, err := h.models.Scope(ctx)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defaultModel, err := h.models.DefaultModel(ctx)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	models := buildAgentModels(sites, modelsBySite, canonical, scope)
	data := agentModelsResponse{Models: models}
	if data.Models == nil {
		data.Models = []agentModel{}
	}
	for _, model := range models {
		if model.ID == defaultModel {
			data.DefaultModel = defaultModel
			break
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": data})
}
