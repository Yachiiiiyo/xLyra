package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"xlyra/server/internal/admin"
	"xlyra/server/internal/auth"
	"xlyra/server/internal/store"
)

const appDeviceTestToken = "xld_app-test-device-token"

func appDeviceTestTokenHash() string {
	sum := sha256.Sum256([]byte(appDeviceTestToken))
	return hex.EncodeToString(sum[:])
}

// appDeviceRouter 搭一个只含管理面安全路由的轻量路由: requireAdmin 用离线
// gorm 校验设备令牌, 用来验证 DenyDeviceActor 与 /devices/self 的真实接线。
func appDeviceRouter(t *testing.T) http.Handler {
	t.Helper()

	deviceID := uuid.New()
	adminID := uuid.New()
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  "host=localhost user=xlyra dbname=xlyra sslmode=disable",
		PreferSimpleProtocol: true,
	}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open offline gorm db: %v", err)
	}
	if err := db.Callback().Query().Replace("gorm:query", func(tx *gorm.DB) {
		switch dest := tx.Statement.Dest.(type) {
		case *[]store.AdminDevice:
			*dest = []store.AdminDevice{{
				ID:        deviceID,
				AdminID:   adminID,
				Name:      "iPhone 15",
				Platform:  "ios",
				TokenHash: appDeviceTestTokenHash(),
				CreatedAt: time.Now(),
			}}
		case *store.AdminDevice:
			*dest = store.AdminDevice{
				ID:        deviceID,
				AdminID:   adminID,
				Name:      "iPhone 15",
				Platform:  "ios",
				TokenHash: appDeviceTestTokenHash(),
				CreatedAt: time.Now(),
			}
		case *store.Admin:
			*dest = store.Admin{ID: adminID, Username: "owner", Role: "owner", Status: "active", CreatedAt: time.Now()}
		default:
			tx.AddError(errors.New("unexpected device router query destination"))
			return
		}
		tx.Statement.RowsAffected = 1
	}); err != nil {
		t.Fatalf("replace query callback: %v", err)
	}
	if err := db.Callback().Update().Replace("gorm:update", func(tx *gorm.DB) {
		tx.Statement.RowsAffected = 1
	}); err != nil {
		t.Fatalf("replace update callback: %v", err)
	}

	service := auth.NewService(db, "test-master-key")
	handler := admin.NewHandler(nil, service, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := chi.NewRouter()
	router.Use(requireAdmin(service))
	router.Use(requireAdminCSRF(service))
	router.Use(handler.AuditAdminMutation)
	router.Route("/api/v1", func(v1 chi.Router) {
		registerAdminProfileRoutes(v1, handler)
		registerAdminDeviceRoutes(v1, handler)
		// 设备令牌允许访问的对照路由。
		v1.Get("/profile", handler.GetProfile)
		v1.Get("/auth/session", handler.CurrentSession)
		v1.Delete("/profile/sessions", handler.DeleteOtherProfileSessions)
	})
	return router
}

func appDeviceRequest(method string, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+appDeviceTestToken)
	return req
}

func appDecodeErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
	}
	return body.Error.Code
}

func TestDeviceTokenIsDeniedOnWebOnlyAdminRoutes(t *testing.T) {
	t.Parallel()

	router := appDeviceRouter(t)
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPut, "/api/v1/profile/account"},
		{http.MethodPut, "/api/v1/profile/password"},
		{http.MethodGet, "/api/v1/profile/access-token"},
		{http.MethodPost, "/api/v1/profile/access-token"},
		{http.MethodPut, "/api/v1/profile/access-token/enabled"},
		{http.MethodDelete, "/api/v1/profile/access-token"},
		{http.MethodPost, "/api/v1/profile/totp/setup"},
		{http.MethodPost, "/api/v1/profile/totp/enable"},
		{http.MethodDelete, "/api/v1/profile/totp"},
		{http.MethodPost, "/api/v1/devices/pairings"},
		{http.MethodGet, "/api/v1/devices"},
		{http.MethodDelete, "/api/v1/devices/" + uuid.NewString()},
		{http.MethodPost, "/api/v1/devices/" + uuid.NewString() + "/push/test"},
		{http.MethodPost, "/api/v1/devices/revoke-all"},
	} {
		tc := tc
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, appDeviceRequest(tc.method, tc.path))

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
			}
			if code := appDecodeErrorCode(t, rec); code != "forbidden_for_device" {
				t.Fatalf("error code = %q, want forbidden_for_device", code)
			}
		})
	}
}

func TestDeviceTokenReachesAdminSurface(t *testing.T) {
	t.Parallel()

	router := appDeviceRouter(t)

	// GET /api/v1/auth/session 必须识别设备令牌并返回 auth_type=device。
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, appDeviceRequest(http.MethodGet, "/api/v1/auth/session"))
	if rec.Code != http.StatusOK {
		t.Fatalf("auth session status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var session struct {
		AuthType string `json:"auth_type"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&session); err != nil {
		t.Fatalf("decode auth session: %v", err)
	}
	if session.AuthType != "device" {
		t.Fatalf("auth type = %q, want device", session.AuthType)
	}

	// 设备令牌自己的接口可用（App 自助查看/退出）。
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, appDeviceRequest(http.MethodGet, "/api/v1/devices/self"))
	if rec.Code != http.StatusOK {
		t.Fatalf("device self status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// 同权的普通管理接口对设备令牌放行。
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, appDeviceRequest(http.MethodGet, "/api/v1/profile"))
	if rec.Code != http.StatusOK {
		t.Fatalf("profile status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// 设备令牌是非 Cookie 凭证: 写请求不需要 CSRF token, 不会被 CSRF 中间件拦下。
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, appDeviceRequest(http.MethodDelete, "/api/v1/profile/sessions"))
	if code := appDecodeErrorCode(t, rec); code == "csrf_token_required" || code == "csrf_token_invalid" {
		t.Fatalf("device write request was rejected by CSRF: %s", rec.Body.String())
	}
}

func TestDevicePairAndManagementRoutesAreRegistered(t *testing.T) {
	t.Parallel()

	router := NewRouter(testConfig(), slog.Default(), nil, nil, "test-master-key")
	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "public pair", method: http.MethodPost, path: "/api/v1/devices/pair"},
		{name: "pairings", method: http.MethodPost, path: "/api/v1/devices/pairings"},
		{name: "devices list", method: http.MethodGet, path: "/api/v1/devices"},
		{name: "device revoke", method: http.MethodDelete, path: "/api/v1/devices/11111111-1111-1111-1111-111111111111"},
		{name: "revoke all", method: http.MethodPost, path: "/api/v1/devices/revoke-all"},
		{name: "device self", method: http.MethodGet, path: "/api/v1/devices/self"},
		{name: "device self delete", method: http.MethodDelete, path: "/api/v1/devices/self"},
		{name: "device push get", method: http.MethodGet, path: "/api/v1/devices/self/push"},
		{name: "device push put", method: http.MethodPut, path: "/api/v1/devices/self/push"},
		{name: "device push delete", method: http.MethodDelete, path: "/api/v1/devices/self/push"},
		{name: "device push prefs", method: http.MethodPut, path: "/api/v1/devices/self/push/prefs"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			// 认证服务不可用（503）而不是 404, 说明路由已注册且在鉴权之后。
			assertAuthUnavailable(t, rec)
		})
	}
}

// webOnlyAdminRoutes 与对照路由（同方法、无 DenyDeviceActor）相比, 中间件数应恰好多一。
func TestWebOnlyAdminRoutesCarryDenyDeviceActorMiddleware(t *testing.T) {
	t.Parallel()

	router := NewRouter(testConfig(), slog.Default(), nil, nil, "test-master-key")
	routes, ok := router.(chi.Routes)
	if !ok {
		t.Fatal("router does not expose chi routes")
	}
	middlewareCounts := map[string]int{}
	if err := chi.Walk(routes, func(method string, route string, _ http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		middlewareCounts[method+" "+route] = len(middlewares)
		return nil
	}); err != nil {
		t.Fatalf("walk routes: %v", err)
	}

	pairs := []struct {
		webOnly string
		plain   string
	}{
		{webOnly: "PUT /api/v1/profile/account", plain: "PUT /api/v1/api-keys/order"},
		{webOnly: "PUT /api/v1/profile/password", plain: "PUT /api/v1/api-keys/order"},
		{webOnly: "GET /api/v1/profile/access-token", plain: "GET /api/v1/profile"},
		{webOnly: "POST /api/v1/profile/access-token", plain: "POST /api/v1/sites"},
		{webOnly: "PUT /api/v1/profile/access-token/enabled", plain: "PUT /api/v1/api-keys/order"},
		{webOnly: "DELETE /api/v1/profile/access-token", plain: "DELETE /api/v1/profile/sessions"},
		{webOnly: "POST /api/v1/profile/totp/setup", plain: "POST /api/v1/sites"},
		{webOnly: "POST /api/v1/profile/totp/enable", plain: "POST /api/v1/sites"},
		{webOnly: "DELETE /api/v1/profile/totp", plain: "DELETE /api/v1/profile/sessions"},
		{webOnly: "POST /api/v1/devices/pairings", plain: "POST /api/v1/sites"},
		{webOnly: "GET /api/v1/devices", plain: "GET /api/v1/profile"},
		{webOnly: "DELETE /api/v1/devices/{id}", plain: "DELETE /api/v1/profile/sessions"},
		{webOnly: "POST /api/v1/devices/revoke-all", plain: "POST /api/v1/sites"},
	}
	for _, pair := range pairs {
		webOnly, ok := middlewareCounts[pair.webOnly]
		if !ok {
			t.Fatalf("route %q is not registered", pair.webOnly)
		}
		plain, ok := middlewareCounts[pair.plain]
		if !ok {
			t.Fatalf("reference route %q is not registered", pair.plain)
		}
		if webOnly != plain+1 {
			t.Fatalf("middleware count for %q = %d, %q = %d, want exactly one more (DenyDeviceActor)", pair.webOnly, webOnly, pair.plain, plain)
		}
	}

	// /devices/self 与推送自用接口不加 DenyDeviceActor: 它们本来就是设备令牌专属。
	for _, route := range []string{
		"GET /api/v1/devices/self",
		"GET /api/v1/devices/self/push",
		"PUT /api/v1/devices/self/push",
		"DELETE /api/v1/devices/self/push",
		"PUT /api/v1/devices/self/push/prefs",
	} {
		if got := middlewareCounts[route]; got != middlewareCounts["GET /api/v1/profile"] {
			t.Fatalf("%s middleware count = %d, want same as plain protected route (%d)", route, got, middlewareCounts["GET /api/v1/profile"])
		}
	}
}
