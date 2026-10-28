package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"xlyra/server/internal/httpx"
	"xlyra/server/internal/cloudclient"
)

func RegisterCloudBinding(r chi.Router, h Handler, client *cloudclient.Client) {
	r.Get("/cloud-binding", func(w http.ResponseWriter, req *http.Request) {
		h.GetCloudBinding(client, w, req)
	})
	r.Post("/cloud-binding/pair", func(w http.ResponseWriter, req *http.Request) {
		h.StartCloudBindingPair(client, w, req)
	})
	r.Delete("/cloud-binding/pair", func(w http.ResponseWriter, req *http.Request) {
		h.CancelCloudBindingPair(client, w, req)
	})
	r.Delete("/cloud-binding", func(w http.ResponseWriter, req *http.Request) {
		h.DeleteCloudBinding(client, w, req)
	})
	r.Get("/cloud-binding/report-preview", func(w http.ResponseWriter, req *http.Request) {
		h.GetCloudBindingReportPreview(client, w, req)
	})
	r.Get("/cloud-binding/developer", func(w http.ResponseWriter, req *http.Request) {
		h.GetCloudDeveloper(client, w, req)
	})
	r.Post("/cloud-binding/submissions", func(w http.ResponseWriter, req *http.Request) {
		h.CreateCloudSubmission(client, w, req)
	})
	r.Get("/cloud-binding/submissions", func(w http.ResponseWriter, req *http.Request) {
		h.ListCloudSubmissions(client, w, req)
	})
}

func (h Handler) GetCloudBinding(client *cloudclient.Client, w http.ResponseWriter, r *http.Request) {
	if client == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "cloud_binding_unavailable", "xLyra Cloud binding is not available")
		return
	}
	status, err := client.Status(r.Context())
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, "invalid_request", "读取连接状态失败")
		return
	}
	httpx.JSON(w, http.StatusOK, status)
}

func (h Handler) StartCloudBindingPair(client *cloudclient.Client, w http.ResponseWriter, r *http.Request) {
	if client == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "cloud_binding_unavailable", "xLyra Cloud binding is not available")
		return
	}
	name, err := readCloudPairName(r)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "request body must be valid JSON")
		return
	}
	status, err := client.StartPairing(r.Context(), name)
	if err != nil {
		h.writeCloudClientError(w, r, err)
		return
	}
	h.recordAudit(r, currentAdminActor(r), "cloud_binding.pair_started", "cloud_binding", "", true, "", map[string]any{
		"name": status.InstanceName,
	})
	httpx.JSON(w, http.StatusOK, status)
}

func (h Handler) CancelCloudBindingPair(client *cloudclient.Client, w http.ResponseWriter, r *http.Request) {
	if client == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "cloud_binding_unavailable", "xLyra Cloud binding is not available")
		return
	}
	if err := client.CancelPairing(r.Context()); err != nil {
		h.writeError(w, r, http.StatusInternalServerError, "invalid_request", "取消配对失败")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) DeleteCloudBinding(client *cloudclient.Client, w http.ResponseWriter, r *http.Request) {
	if client == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "cloud_binding_unavailable", "xLyra Cloud binding is not available")
		return
	}
	current, _ := client.Status(r.Context())
	if err := client.Unbind(r.Context()); err != nil {
		h.recordAudit(r, currentAdminActor(r), "cloud_binding.unbound", "cloud_binding", current.InstanceName, false, "invalid_request", map[string]any{
			"instanceName": current.InstanceName,
		})
		h.writeError(w, r, http.StatusInternalServerError, "invalid_request", "断开连接失败")
		return
	}
	h.recordAudit(r, currentAdminActor(r), "cloud_binding.unbound", "cloud_binding", "", true, "", map[string]any{
		"instanceName": current.InstanceName,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) GetCloudBindingReportPreview(client *cloudclient.Client, w http.ResponseWriter, r *http.Request) {
	if client == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "cloud_binding_unavailable", "xLyra Cloud binding is not available")
		return
	}
	report, err := client.Report(r.Context())
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, "invalid_request", "读取上报预览失败")
		return
	}
	httpx.JSON(w, http.StatusOK, report)
}

func (h Handler) writeCloudClientError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, cloudclient.ErrAlreadyBound) {
		h.writeError(w, r, http.StatusConflict, "invalid_request", "实例已连接，请先断开")
		return
	}
	// xLyra Cloud 返回的状态码不能原样透传：前端把 401/403 当作"管理员登录失效"，
	// 一次 Cloud 侧的错误会把管理员踢下线。Cloud 的任何失败都按 502 返回，具体原因放在消息里。
	var cloudErr *cloudclient.CloudError
	if errors.As(err, &cloudErr) {
		message := cloudErr.Message
		if message == "" {
			message = "xLyra Cloud 请求失败"
		}
		h.writeError(w, r, http.StatusBadGateway, "cloud_error", message)
		return
	}
	h.writeError(w, r, http.StatusBadGateway, "cloud_unavailable", err.Error())
}

func readCloudPairName(r *http.Request) (string, error) {
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
