// P6h:fork 路由由内置 Pi 引擎注册。
package app

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentRoutesForkBuiltinOnly(t *testing.T) {
	t.Parallel()
	forkPath := "/api/v1/agent/sessions/00000000-0000-4000-8000-00000000dead/fork"

	// builtin 引擎:路由到 handler(会话不存在 → 404 会话不存在)
	cfg := testConfig()
	cfg.AgentDataDir = t.TempDir()
	builtin := newBuiltinAgent(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, fakeBuiltinTokenIssuer{}, nil)
	router := newAgentOnlyRouter(cfg, builtin)
	req := httptest.NewRequest(http.MethodPost, forkPath, strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("builtin fork status = %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "会话不存在") {
		t.Fatalf("builtin fork 应到达 handler: %s", rec.Body.String())
	}
}
