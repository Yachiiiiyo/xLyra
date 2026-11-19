package admin

import (
	"net/http"
	"strconv"
	"strings"

	"xlyra/server/internal/auth"
)

type auditResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *auditResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *auditResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func (w *auditResponseWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *auditResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (h Handler) AuditAdminMutation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.auth == nil || !shouldAuditAdminMutation(r.Method) || skipDeviceAgentMutation(r) {
			next.ServeHTTP(w, r)
			return
		}

		recorder := &auditResponseWriter{ResponseWriter: w}
		next.ServeHTTP(recorder, r)

		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		actor, _ := auth.AdminActorFromContext(r.Context())
		h.recordAudit(r, actor, "admin_api.mutation", "http_request", r.URL.Path, status < http.StatusBadRequest, strconv.Itoa(status), map[string]any{
			"method": r.Method,
			"path":   r.URL.Path,
			"query":  r.URL.RawQuery,
		})
	})
}

// skipDeviceAgentMutation 让 App（设备令牌）在 /api/v1/agent/ 下的写请求不产生
// admin_api.mutation: 这些是日常操作, 逐条记录会把审计表灌满。其它路径的写
// 请求照旧记录, 设备令牌的自我扩权类接口另有 DenyDeviceActor 拦截。
func skipDeviceAgentMutation(r *http.Request) bool {
	actor, ok := auth.AdminActorFromContext(r.Context())
	if !ok || actor.Type != auth.DeviceActorType {
		return false
	}
	return strings.HasPrefix(r.URL.Path, "/api/v1/agent/")
}

func shouldAuditAdminMutation(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}
