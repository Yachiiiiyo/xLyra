package admin

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"xlyra/server/internal/auth"
	"xlyra/server/internal/crypto"
	"xlyra/server/internal/hubclient"
	"xlyra/server/internal/store"
)

func TestHubBindingRoutesRequireAdminSession(t *testing.T) {
	authService := auth.NewService(nil, adminTestMasterKey)
	router := hubBindingRouter(Handler{}, nil, authService)
	routes := []struct {
		method string
		target string
	}{
		{http.MethodGet, "/api/v1/hub-binding"},
		{http.MethodPost, "/api/v1/hub-binding/pair"},
		{http.MethodDelete, "/api/v1/hub-binding/pair"},
		{http.MethodDelete, "/api/v1/hub-binding"},
		{http.MethodGet, "/api/v1/hub-binding/report-preview"},
		{http.MethodGet, "/api/v1/hub-binding/developer"},
		{http.MethodPost, "/api/v1/hub-binding/submissions"},
		{http.MethodGet, "/api/v1/hub-binding/submissions?pluginId=acme.demo"},
	}
	for _, route := range routes {
		req := httptest.NewRequest(route.method, route.target, strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assertAuditPluginError(t, rec, http.StatusUnauthorized, "unauthorized")
	}
}

func TestHubBindingMissingHubAndPreview(t *testing.T) {
	st := openAdminJSPluginStore(t)
	ctx := context.Background()
	authService := auth.NewService(st.DB(), adminTestMasterKey)
	session, err := authService.BootstrapAdmin(ctx, "owner", "StrongPass123!", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	client := hubclient.New(
		store.NewHubBindingRepository(st.DB()),
		store.NewJSPluginRepository(st.DB()),
		adminTestMasterKey,
		func(context.Context) (string, error) { return "", errMissingHub },
		hubclient.Options{},
	)
	router := hubBindingRouter(Handler{auth: authService}, client, authService)
	rec := hubAuthed(t, router, session.Token, authService.CSRFTokenForSession(session.Token), http.MethodGet, "/api/v1/hub-binding", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "未缓存已验签的市场 root") {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	preview := hubAuthed(t, router, session.Token, "", http.MethodGet, "/api/v1/hub-binding/report-preview", nil)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview = %d %s", preview.Code, preview.Body.String())
	}
	var report map[string]json.RawMessage
	if err := json.Unmarshal(preview.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"xlyraVersion", "platform", "plugins", "counts", "sentAt"} {
		if _, ok := report[key]; !ok {
			t.Fatalf("preview missing %s: %s", key, preview.Body.String())
		}
	}
	for _, forbidden := range []string{"apiKey", "secret", "password", "baseUrl", "pollToken", "privateKey"} {
		if strings.Contains(preview.Body.String(), forbidden) {
			t.Fatalf("preview contains %s", forbidden)
		}
	}
}

func TestHubBindingPairCancelUnbindAndAudit(t *testing.T) {
	st := openAdminJSPluginStore(t)
	ctx := context.Background()
	authService := auth.NewService(st.DB(), adminTestMasterKey)
	session, err := authService.BootstrapAdmin(ctx, "owner", "StrongPass123!", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	hub := newAdminFakeHub()
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)
	client := hubclient.New(
		store.NewHubBindingRepository(st.DB()),
		store.NewJSPluginRepository(st.DB()),
		adminTestMasterKey,
		func(context.Context) (string, error) { return srv.URL, nil },
		hubclient.Options{AllowInsecureLocal: true},
	)
	router := hubBindingRouter(Handler{auth: authService}, client, authService)
	csrf := authService.CSRFTokenForSession(session.Token)
	paired := hubAuthed(t, router, session.Token, csrf, http.MethodPost, "/api/v1/hub-binding/pair", []byte(`{"name":"desk"}`))
	if paired.Code != http.StatusOK || !strings.Contains(paired.Body.String(), hub.userCode) || strings.Contains(paired.Body.String(), hub.token) {
		t.Fatalf("pair = %d %s", paired.Code, paired.Body.String())
	}
	row, err := store.NewHubBindingRepository(st.DB()).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	privText, err := crypto.DecryptSecret(adminTestMasterKey, row.PrivateKeyEnc)
	if err != nil || strings.Contains(paired.Body.String(), privText) || strings.Contains(row.PollTokenEnc, hub.token) || row.PairingCodeHint != hub.userCode {
		t.Fatalf("secrets leaked err=%v hint=%q", err, row.PairingCodeHint)
	}
	token, err := crypto.DecryptSecret(adminTestMasterKey, row.PollTokenEnc)
	if err != nil || token != hub.token {
		t.Fatalf("poll token = %q err=%v", token, err)
	}

	cancelled := hubAuthed(t, router, session.Token, csrf, http.MethodDelete, "/api/v1/hub-binding/pair", nil)
	if cancelled.Code != http.StatusNoContent {
		t.Fatalf("cancel = %d %s", cancelled.Code, cancelled.Body.String())
	}
	again := hubAuthed(t, router, session.Token, csrf, http.MethodPost, "/api/v1/hub-binding/pair", []byte(`{"name":"desk"}`))
	if again.Code != http.StatusOK {
		t.Fatalf("pair again = %d %s", again.Code, again.Body.String())
	}
	if err := store.NewHubBindingRepository(st.DB()).MarkBound(ctx, hub.instanceID, "", "Ada", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	conflict := hubAuthed(t, router, session.Token, csrf, http.MethodPost, "/api/v1/hub-binding/pair", []byte(`{"name":"desk"}`))
	assertAuditPluginError(t, conflict, http.StatusConflict, "invalid_request")

	removed := hubAuthed(t, router, session.Token, csrf, http.MethodDelete, "/api/v1/hub-binding", nil)
	if removed.Code != http.StatusNoContent {
		t.Fatalf("unbind = %d %s", removed.Code, removed.Body.String())
	}
	if hub.unbinds() != 1 {
		t.Fatalf("unbind hits = %d", hub.unbinds())
	}
	cleared, err := store.NewHubBindingRepository(st.DB()).Get(ctx)
	if err != nil || cleared.Status != store.HubStatusUnbound || cleared.PublicKey == "" || cleared.InstanceID != "" || cleared.PairingCodeHint != "" {
		t.Fatalf("cleared = %#v err=%v", cleared, err)
	}
	logs, _, err := store.NewAdminAuditLogRepository(st.DB()).List(ctx, store.AdminAuditLogFilters{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	sawPair, sawUnbind := false, false
	for _, item := range logs {
		text := item.Action + " " + string(item.Metadata)
		if strings.Contains(text, hub.userCode) || strings.Contains(text, hub.token) || strings.Contains(text, privText) {
			t.Fatalf("audit leaked secret: %s", text)
		}
		if item.Action == "hub_binding.pair_started" && item.Success {
			sawPair = true
		}
		if item.Action == "hub_binding.unbound" && item.Success {
			sawUnbind = true
		}
	}
	if !sawPair || !sawUnbind {
		t.Fatalf("audits missing pair/unbind: %#v", logs)
	}
}

func TestHubBindingStatusStaysFastWhenHubHangs(t *testing.T) {
	st := openAdminJSPluginStore(t)
	ctx := context.Background()
	authService := auth.NewService(st.DB(), adminTestMasterKey)
	session, err := authService.BootstrapAdmin(ctx, "owner", "StrongPass123!", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-stop
	}))
	t.Cleanup(func() {
		close(stop)
		srv.Close()
	})
	client := hubclient.New(
		store.NewHubBindingRepository(st.DB()),
		store.NewJSPluginRepository(st.DB()),
		adminTestMasterKey,
		func(context.Context) (string, error) { return srv.URL, nil },
		hubclient.Options{AllowInsecureLocal: true},
	)
	router := hubBindingRouter(Handler{auth: authService}, client, authService)
	csrf := authService.CSRFTokenForSession(session.Token)
	done := make(chan *httptest.ResponseRecorder, 1)
	started := time.Now()
	go func() {
		done <- hubAuthed(t, router, session.Token, csrf, http.MethodPost, "/api/v1/hub-binding/pair", []byte(`{"name":"desk"}`))
	}()
	time.Sleep(40 * time.Millisecond)
	began := time.Now()
	status := hubAuthed(t, router, session.Token, "", http.MethodGet, "/api/v1/hub-binding", nil)
	if status.Code != http.StatusOK || time.Since(began) > 100*time.Millisecond {
		t.Fatalf("status = %d in %s body=%s", status.Code, time.Since(began), status.Body.String())
	}
	select {
	case rec := <-done:
		if rec.Code < 400 || time.Since(started) > 7*time.Second {
			t.Fatalf("pair = %d in %s body=%s", rec.Code, time.Since(started), rec.Body.String())
		}
	case <-time.After(7 * time.Second):
		t.Fatal("pair did not fail")
	}
}

func hubBindingRouter(h Handler, client *hubclient.Client, authService *auth.Service) http.Handler {
	router := chi.NewRouter()
	router.Route("/api/v1", func(v1 chi.Router) {
		v1.Use(authService.RequireAdminSession)
		v1.Use(authService.RequireAdminCSRF)
		RegisterHubBinding(v1, h, client)
	})
	return router
}

func hubAuthed(t *testing.T, router http.Handler, token, csrf, method, target string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(&http.Cookie{Name: "xlyra_admin_session", Value: token})
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set(auth.CSRFHeaderName, csrf)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

var errMissingHub = missingHubError{}

type missingHubError struct{}

func (missingHubError) Error() string { return "未缓存已验签的市场 root" }

type adminFakeHub struct {
	mu         sync.Mutex
	userCode   string
	token      string
	instanceID string
	pub        ed25519.PublicKey
	unbindHits int
}

func newAdminFakeHub() *adminFakeHub {
	return &adminFakeHub{userCode: "K7QM-2XFD", token: "poll-token-0123456789abcdef0123456789", instanceID: "inst-1"}
}

func (h *adminFakeHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	h.mu.Lock()
	defer h.mu.Unlock()
	switch r.URL.Path {
	case "/api/instance/pair/start":
		var req struct {
			PublicKey string `json:"publicKey"`
		}
		_ = json.Unmarshal(body, &req)
		raw, _ := base64.StdEncoding.DecodeString(req.PublicKey)
		h.pub = ed25519.PublicKey(raw)
		writeAdminHubJSON(w, map[string]any{
			"userCode": h.userCode, "pollToken": h.token,
			"expiresAt": time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339), "intervalSeconds": 3,
		})
	case "/api/instance/unbind":
		unix, _ := strconv.ParseInt(r.Header.Get("X-XLyra-Timestamp"), 10, 64)
		sig, _ := base64.StdEncoding.DecodeString(r.Header.Get("X-XLyra-Signature"))
		message := hubclient.CanonicalMessage(r.Method, r.URL.Path, unix, body)
		if r.Header.Get("X-XLyra-Instance") != h.instanceID || !ed25519.Verify(h.pub, []byte(message), sig) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		h.unbindHits++
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (h *adminFakeHub) unbinds() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.unbindHits
}

func writeAdminHubJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

// Hub 返回 401/403 时，xLyra 自己的接口必须返回 502，不能透传：
// 前端把 401 当作"管理员登录失效"，透传会把管理员踢下线。
func TestHubBindingHubErrorsAreNotPassedThrough(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		st := openAdminJSPluginStore(t)
		ctx := context.Background()
		authService := auth.NewService(st.DB(), adminTestMasterKey)
		session, err := authService.BootstrapAdmin(ctx, "owner", "StrongPass123!", "", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"code":"bad_signature","message":"Hub 拒绝了请求"}}`))
		}))
		t.Cleanup(hub.Close)
		client := hubclient.New(
			store.NewHubBindingRepository(st.DB()),
			store.NewJSPluginRepository(st.DB()),
			adminTestMasterKey,
			func(context.Context) (string, error) { return hub.URL, nil },
			hubclient.Options{AllowInsecureLocal: true},
		)
		router := hubBindingRouter(Handler{auth: authService}, client, authService)
		rec := hubAuthed(t, router, session.Token, authService.CSRFTokenForSession(session.Token), http.MethodPost, "/api/v1/hub-binding/pair", []byte(`{}`))
		if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "hub_error") {
			t.Fatalf("Hub %d -> xLyra %d %s", status, rec.Code, rec.Body.String())
		}
	}
}
