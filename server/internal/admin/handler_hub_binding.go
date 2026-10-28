package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"xlyra/server/internal/httpx"
	"xlyra/server/internal/hubclient"
)

func RegisterHubBinding(r chi.Router, h Handler, client *hubclient.Client) {
	r.Get("/hub-binding", func(w http.ResponseWriter, req *http.Request) {
		h.GetHubBinding(client, w, req)
	})
	r.Post("/hub-binding/pair", func(w http.ResponseWriter, req *http.Request) {
		h.StartHubBindingPair(client, w, req)
	})
	r.Delete("/hub-binding/pair", func(w http.ResponseWriter, req *http.Request) {
		h.CancelHubBindingPair(client, w, req)
	})
	r.Delete("/hub-binding", func(w http.ResponseWriter, req *http.Request) {
		h.DeleteHubBinding(client, w, req)
	})
	r.Get("/hub-binding/report-preview", func(w http.ResponseWriter, req *http.Request) {
		h.GetHubBindingReportPreview(client, w, req)
	})
	r.Get("/hub-binding/developer", func(w http.ResponseWriter, req *http.Request) {
		h.GetHubDeveloper(client, w, req)
	})
	r.Post("/hub-binding/submissions", func(w http.ResponseWriter, req *http.Request) {
		h.CreateHubSubmission(client, w, req)
	})
	r.Get("/hub-binding/submissions", func(w http.ResponseWriter, req *http.Request) {
		h.ListHubSubmissions(client, w, req)
	})
}

func (h Handler) GetHubBinding(client *hubclient.Client, w http.ResponseWriter, r *http.Request) {
	if client == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "hub_binding_unavailable", "hub binding is not available")
		return
	}
	status, err := client.Status(r.Context())
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, "invalid_request", "读取连接状态失败")
		return
	}
	httpx.JSON(w, http.StatusOK, status)
}

func (h Handler) StartHubBindingPair(client *hubclient.Client, w http.ResponseWriter, r *http.Request) {
	if client == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "hub_binding_unavailable", "hub binding is not available")
		return
	}
	name, err := readHubPairName(r)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "request body must be valid JSON")
		return
	}
	status, err := client.StartPairing(r.Context(), name)
	if err != nil {
		h.writeHubClientError(w, r, err)
		return
	}
	h.recordAudit(r, currentAdminActor(r), "hub_binding.pair_started", "hub_binding", "", true, "", map[string]any{
		"name": status.InstanceName,
	})
	httpx.JSON(w, http.StatusOK, status)
}

func (h Handler) CancelHubBindingPair(client *hubclient.Client, w http.ResponseWriter, r *http.Request) {
	if client == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "hub_binding_unavailable", "hub binding is not available")
		return
	}
	if err := client.CancelPairing(r.Context()); err != nil {
		h.writeError(w, r, http.StatusInternalServerError, "invalid_request", "取消配对失败")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) DeleteHubBinding(client *hubclient.Client, w http.ResponseWriter, r *http.Request) {
	if client == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "hub_binding_unavailable", "hub binding is not available")
		return
	}
	current, _ := client.Status(r.Context())
	if err := client.Unbind(r.Context()); err != nil {
		h.recordAudit(r, currentAdminActor(r), "hub_binding.unbound", "hub_binding", current.InstanceName, false, "invalid_request", map[string]any{
			"instanceName": current.InstanceName,
		})
		h.writeError(w, r, http.StatusInternalServerError, "invalid_request", "断开连接失败")
		return
	}
	h.recordAudit(r, currentAdminActor(r), "hub_binding.unbound", "hub_binding", "", true, "", map[string]any{
		"instanceName": current.InstanceName,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) GetHubBindingReportPreview(client *hubclient.Client, w http.ResponseWriter, r *http.Request) {
	if client == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "hub_binding_unavailable", "hub binding is not available")
		return
	}
	report, err := client.Report(r.Context())
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, "invalid_request", "读取上报预览失败")
		return
	}
	httpx.JSON(w, http.StatusOK, report)
}

func (h Handler) writeHubClientError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, hubclient.ErrAlreadyBound) {
		h.writeError(w, r, http.StatusConflict, "invalid_request", "实例已连接，请先断开")
		return
	}
	// Hub 返回的状态码不能原样透传：前端把 401/403 当作"管理员登录失效"，
	// 一次 Hub 侧的错误会把管理员踢下线。Hub 的任何失败都按 502 返回，具体原因放在消息里。
	var hubErr *hubclient.HubError
	if errors.As(err, &hubErr) {
		message := hubErr.Message
		if message == "" {
			message = "Hub 请求失败"
		}
		h.writeError(w, r, http.StatusBadGateway, "hub_error", message)
		return
	}
	h.writeError(w, r, http.StatusBadGateway, "hub_unavailable", err.Error())
}

func readHubPairName(r *http.Request) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(raw)) == "" {
		return "", nil
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", err
	}
	return body.Name, nil
}
