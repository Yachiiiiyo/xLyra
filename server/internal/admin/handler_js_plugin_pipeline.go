package admin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"xlyra/server/internal/httpx"
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/textpipeline"
)

// jsPluginPipelineBody is what the binding form of a gateway pipeline sends.
type jsPluginPipelineBody struct {
	Name        string         `json:"name"`
	Inputs      map[string]any `json:"inputs"`
	HostOptions map[string]any `json:"host_options"`
}

func (b jsPluginPipelineBody) input() jsplugin.PipelineBindingInput {
	return jsplugin.PipelineBindingInput{Name: b.Name, Inputs: b.Inputs, HostOptions: b.HostOptions}
}

func (h Handler) writePipelineBindingError(w http.ResponseWriter, r *http.Request, action, pluginID string, meta map[string]any, err error) {
	var inputErr *jsplugin.AutomationInputError
	switch {
	case errors.As(err, &inputErr):
		h.recordJSPluginAudit(r, action, pluginID, false, "js_plugin_binding_invalid", meta)
		h.writeError(w, r, http.StatusBadRequest, "js_plugin_binding_invalid", inputErr.Message)
	case errors.Is(err, gorm.ErrRecordNotFound):
		h.recordJSPluginAudit(r, action, pluginID, false, "not_found", meta)
		h.writeError(w, r, http.StatusNotFound, "not_found", "binding was not found")
	default:
		h.recordJSPluginAudit(r, action, pluginID, false, "js_plugin_binding_failed", meta)
		h.writeError(w, r, http.StatusInternalServerError, "js_plugin_binding_failed", "failed to save the binding")
	}
}

// ListJSPluginBindings returns the gateway pipeline bindings of a plugin and
// the host options a binding of the enabled version can set.
func (h Handler) ListJSPluginBindings(w http.ResponseWriter, r *http.Request) {
	if h.jsPlugins == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "js_plugin_unavailable", "js plugin manager is not available")
		return
	}
	pluginID := strings.TrimSpace(chi.URLParam(r, "id"))
	items, err := h.jsPlugins.ListPipelineBindings(r.Context(), pluginID)
	if err != nil {
		if abandonIfClientGone(w, r) {
			return
		}
		h.writeError(w, r, http.StatusInternalServerError, "js_plugin_binding_list_failed", "failed to list bindings")
		return
	}
	hostOptions := []jsplugin.PipelineHostOptionSpec{}
	if plugin, ok := h.jsPlugins.Catalog().Registry().ByPluginID(pluginID); ok && plugin.Manifest.DeclaresGatewayPipeline() {
		hostOptions = jsplugin.PipelineHostOptionSpecs(*plugin.Manifest.GatewayPipeline)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "host_options": hostOptions})
}

// CreateJSPluginBinding binds an enabled gateway pipeline plugin to API keys.
func (h Handler) CreateJSPluginBinding(w http.ResponseWriter, r *http.Request) {
	if h.jsPlugins == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "js_plugin_unavailable", "js plugin manager is not available")
		return
	}
	pluginID := strings.TrimSpace(chi.URLParam(r, "id"))
	var body jsPluginPipelineBody
	if err := httpx.DecodeJSONBody(r, &body); err != nil {
		h.writeError(w, r, http.StatusBadRequest, "invalid_json", "invalid request body")
		return
	}
	meta := map[string]any{"scope": "gateway_pipeline", "inputs": len(body.Inputs)}
	binding, err := h.jsPlugins.CreatePipelineBinding(r.Context(), pluginID, body.input())
	if err != nil {
		h.writePipelineBindingError(w, r, "js_plugin.bind_pipeline", pluginID, meta, err)
		return
	}
	meta["binding_id"] = binding.ID.String()
	h.recordJSPluginAudit(r, "js_plugin.bind_pipeline", pluginID, true, "", meta)
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true, "id": binding.ID.String()})
}

// UpdateJSPluginBinding replaces what a gateway pipeline binding holds.
func (h Handler) UpdateJSPluginBinding(w http.ResponseWriter, r *http.Request) {
	if h.jsPlugins == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "js_plugin_unavailable", "js plugin manager is not available")
		return
	}
	pluginID := strings.TrimSpace(chi.URLParam(r, "id"))
	bindingID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "binding_id")))
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, "invalid_binding_id", "binding_id must be a uuid")
		return
	}
	var body jsPluginPipelineBody
	if err := httpx.DecodeJSONBody(r, &body); err != nil {
		h.writeError(w, r, http.StatusBadRequest, "invalid_json", "invalid request body")
		return
	}
	meta := map[string]any{"scope": "gateway_pipeline", "binding_id": bindingID.String(), "inputs": len(body.Inputs)}
	if err := h.jsPlugins.UpdatePipelineBinding(r.Context(), pluginID, bindingID, body.input()); err != nil {
		h.writePipelineBindingError(w, r, "js_plugin.update_pipeline", pluginID, meta, err)
		return
	}
	h.recordJSPluginAudit(r, "js_plugin.update_pipeline", pluginID, true, "", meta)
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

// DeleteJSPluginBinding removes a gateway pipeline binding.
func (h Handler) DeleteJSPluginBinding(w http.ResponseWriter, r *http.Request) {
	if h.jsPlugins == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "js_plugin_unavailable", "js plugin manager is not available")
		return
	}
	pluginID := strings.TrimSpace(chi.URLParam(r, "id"))
	bindingID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "binding_id")))
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, "invalid_binding_id", "binding_id must be a uuid")
		return
	}
	meta := map[string]any{"scope": "gateway_pipeline", "binding_id": bindingID.String()}
	if err := h.jsPlugins.DeletePipelineBinding(r.Context(), pluginID, bindingID); err != nil {
		h.writePipelineBindingError(w, r, "js_plugin.unbind_pipeline", pluginID, meta, err)
		return
	}
	h.recordJSPluginAudit(r, "js_plugin.unbind_pipeline", pluginID, true, "", meta)
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ListJSPluginPipelineInputOptions lists the objects an admin may pick for one
// input of a gateway pipeline. binding_id is the binding being edited.
func (h Handler) ListJSPluginPipelineInputOptions(w http.ResponseWriter, r *http.Request) {
	if h.jsPlugins == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "js_plugin_unavailable", "js plugin manager is not available")
		return
	}
	var bindingID uuid.UUID
	if raw := strings.TrimSpace(r.URL.Query().Get("binding_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			h.writeError(w, r, http.StatusBadRequest, "invalid_binding_id", "binding_id must be a uuid")
			return
		}
		bindingID = parsed
	}
	items, err := h.jsPlugins.PipelineInputOptions(r.Context(), strings.TrimSpace(chi.URLParam(r, "id")), strings.TrimSpace(chi.URLParam(r, "name")), bindingID)
	if err != nil {
		if abandonIfClientGone(w, r) {
			return
		}
		var inputErr *jsplugin.AutomationInputError
		if errors.As(err, &inputErr) {
			h.writeError(w, r, http.StatusBadRequest, "js_plugin_binding_invalid", inputErr.Message)
			return
		}
		h.writeError(w, r, http.StatusInternalServerError, "js_plugin_binding_failed", "failed to list the options")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// pipelineBindingPayload is the gateway pipeline binding an API key is in, or nil.
func (h Handler) pipelineBindingPayload(apiKeyID uuid.UUID) any {
	if h.jsPlugins == nil {
		return nil
	}
	summary, ok := h.jsPlugins.Catalog().Current().PipelineSummaryForAPIKey(apiKeyID)
	if !ok {
		return nil
	}
	return summary
}

// writePipelineInUse answers a delete that fail_closed bindings still block.
func (h Handler) writePipelineInUse(w http.ResponseWriter, r *http.Request, action, pluginID string, meta map[string]any, err error) bool {
	var inUse *jsplugin.PipelineBindingInUseError
	if !errors.As(err, &inUse) {
		return false
	}
	if meta == nil {
		meta = map[string]any{}
	}
	meta["scope"] = "gateway_pipeline"
	meta["count"] = inUse.Count
	h.recordJSPluginAudit(r, action, pluginID, false, "js_plugin_in_use", meta)
	h.writeErrorFields(w, r, http.StatusConflict, "js_plugin_in_use", err.Error(), map[string]any{"scope": "gateway_pipeline", "count": inUse.Count})
	return true
}

type jsPluginPipelineDryRunBody struct {
	Text     string         `json:"text"`
	Endpoint string         `json:"endpoint"`
	Model    string         `json:"model"`
	Payload  map[string]any `json:"payload"`
}

type jsPluginPipelineDryRunFinding struct {
	SegmentID   string  `json:"segment_id"`
	Role        string  `json:"role,omitempty"`
	Field       string  `json:"field"`
	Start       int     `json:"start"`
	End         int     `json:"end"`
	Type        string  `json:"type"`
	Source      string  `json:"source,omitempty"`
	Score       float64 `json:"score,omitempty"`
	Placeholder string  `json:"placeholder,omitempty"`
	Applied     bool    `json:"applied"`
}

// DryRunJSPluginBinding runs a gateway pipeline binding locally on pasted text
// or a request JSON payload without sending anything upstream.
func (h Handler) DryRunJSPluginBinding(w http.ResponseWriter, r *http.Request) {
	if h.jsPlugins == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "js_plugin_unavailable", "js plugin manager is not available")
		return
	}
	pluginID := strings.TrimSpace(chi.URLParam(r, "id"))
	bindingID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "binding_id")))
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, "invalid_binding_id", "binding_id must be a uuid")
		return
	}
	var body jsPluginPipelineDryRunBody
	if err := httpx.DecodeJSONBody(r, &body); err != nil {
		h.writeError(w, r, http.StatusBadRequest, "invalid_json", "invalid request body")
		return
	}
	resolved, ok := h.jsPlugins.Catalog().Current().PipelineBinding(bindingID)
	if !ok || resolved.PluginID != pluginID {
		h.writeError(w, r, http.StatusNotFound, "not_found", "binding was not found")
		return
	}
	endpoint := strings.TrimSpace(body.Endpoint)
	model := strings.TrimSpace(body.Model)
	if model == "" {
		model = "dry-run"
	}
	plainTextMode := len(body.Payload) == 0
	payload := body.Payload
	if plainTextMode {
		if endpoint == "" {
			endpoint = "/v1/chat/completions"
		}
		payload = map[string]any{
			"model": model,
			"messages": []any{
				map[string]any{"role": "user", "content": body.Text},
			},
		}
	} else if endpoint == "" {
		endpoint = "/v1/chat/completions"
	}
	result := textpipeline.Run(r.Context(), resolved, textpipeline.Request{
		Endpoint: endpoint,
		Model:    model,
		Payload:  payload,
	}, textpipeline.Options{
		NoCache: true,
	})
	findings := make([]jsPluginPipelineDryRunFinding, 0)
	for i, seg := range result.Segments {
		appliedBySpan := map[[3]any]string{}
		if i < len(result.Applied) {
			for _, ap := range result.Applied[i] {
				appliedBySpan[[3]any{ap.Start, ap.End, ap.Type}] = ap.Placeholder
			}
		}
		if i < len(result.Findings) {
			for _, f := range result.Findings[i] {
				ph, wasApplied := appliedBySpan[[3]any{f.Start, f.End, f.Type}]
				findings = append(findings, jsPluginPipelineDryRunFinding{
					SegmentID:   seg.ID,
					Role:        seg.Role,
					Field:       seg.Field,
					Start:       f.Start,
					End:         f.End,
					Type:        f.Type,
					Source:      f.Source,
					Score:       f.Score,
					Placeholder: ph,
					Applied:     wasApplied,
				})
			}
		}
		// Include propagated replacements that were not original findings.
		if i < len(result.Applied) {
			for _, ap := range result.Applied[i] {
				if ap.Propagated {
					findings = append(findings, jsPluginPipelineDryRunFinding{
						SegmentID:   seg.ID,
						Role:        seg.Role,
						Field:       seg.Field,
						Start:       ap.Start,
						End:         ap.End,
						Type:        ap.Type,
						Source:      "propagated",
						Placeholder: ap.Placeholder,
						Applied:     true,
					})
				}
			}
		}
	}
	outPayload := payload
	if result.Changed && result.Payload != nil {
		outPayload = result.Payload
	}
	resp := map[string]any{
		"outcome":             result.Metrics.Outcome,
		"changed":             result.Changed,
		"bypassed":            result.Bypassed,
		"error_kind":          result.ErrorKind,
		"metrics":             result.Metrics,
		"findings":            findings,
		"transformed_payload": outPayload,
	}
	if result.Failure != nil {
		resp["failure"] = map[string]any{
			"status":  result.Failure.Status,
			"code":    result.Failure.Code,
			"message": result.Failure.Message,
		}
	}
	if plainTextMode {
		transformedText := body.Text
		if msgs, ok := outPayload["messages"].([]any); ok {
			for _, rawMsg := range msgs {
				if m, ok := rawMsg.(map[string]any); ok && m["role"] == "user" {
					if content, ok := m["content"].(string); ok {
						transformedText = content
					}
				}
			}
		}
		resp["transformed_text"] = transformedText
	}
	httpx.JSON(w, http.StatusOK, resp)
}
