// P6h:fork 路由只在 builtin(pi)引擎注册,runner 引擎分支不注册。
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
	cfg.AgentEngine = "pi"
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

	// runner 引擎:不注册 fork,请求不会到达内置 handler(默认 404)
	proxyRouter := newAgentOnlyRouter(testConfig(), nil)
	req = httptest.NewRequest(http.MethodPost, forkPath, strings.NewReader("{}"))
	rec = httptest.NewRecorder()
	proxyRouter.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("runner 引擎 fork status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "会话不存在") {
		t.Fatalf("runner 引擎不应注册 fork: %s", rec.Body.String())
	}
}
