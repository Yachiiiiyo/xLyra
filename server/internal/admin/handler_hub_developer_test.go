package admin

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
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
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/jsplugin/market"
	"xlyra/server/internal/marketproto"
	"xlyra/server/internal/store"
)

func TestInstalledPluginsExposeOwnershipAndAhead(t *testing.T) {
	env := newDevEnv(t)
	signer := env.installSigned(t, "acme.ahead", "1.3.0", env.priv, store.JSPluginStatusEnabled)
	env.installSigned(t, "acme.same", "1.2.0", env.priv, store.JSPluginStatusEnabled)
	env.installSigned(t, "acme.new", "0.1.0", env.priv, store.JSPluginStatusDisabled)
	env.installSigned(t, "other.demo", "2.0.0", env.priv, store.JSPluginStatusEnabled)
	otherPub, otherPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = otherPub
	env.installSigned(t, "acme.stranger", "3.0.0", otherPriv, store.JSPluginStatusEnabled)
	env.setDeveloper(map[string]any{
		"status": "active", "displayName": "Ada",
		"namespaces": []map[string]string{{"name": "acme", "status": "active"}, {"name": "other", "status": "pending"}},
		"keys":       []map[string]string{{"fingerprint": signer, "label": "laptop", "status": "active"}},
	})
	env.svc.SetCatalogFixture(&market.CatalogFixture{Snapshot: market.Snapshot{Index: &marketproto.Index{Plugins: []marketproto.Plugin{
		{ID: "acme.ahead", Versions: []marketproto.PluginVersion{{Version: "1.0.0"}, {Version: "1.2.0"}}},
		{ID: "acme.same", Versions: []marketproto.PluginVersion{{Version: "1.2.0"}}},
		{ID: "acme.stranger", Versions: []marketproto.PluginVersion{{Version: "1.0.0"}}},
		{ID: "other.demo", Versions: []marketproto.PluginVersion{{Version: "1.0.0"}}},
	}}}})
	if rec := env.authed(http.MethodGet, "/api/v1/hub-binding/developer", nil); rec.Code != http.StatusOK {
		t.Fatalf("developer %d %s", rec.Code, rec.Body.String())
	}
	listed := env.authed(http.MethodGet, "/api/v1/js-plugins", nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list %d %s", listed.Code, listed.Body.String())
	}
	items := pluginItems(t, listed.Body.Bytes())
	assertPlugin(t, items, "acme.ahead", true, true, "1.2.0", "1.3.0")
	assertPlugin(t, items, "acme.same", true, false, "1.2.0", "1.2.0")
	assertPlugin(t, items, "acme.new", true, true, "", "0.1.0")
	assertPlugin(t, items, "other.demo", false, true, "1.0.0", "2.0.0")
	assertPlugin(t, items, "acme.stranger", false, true, "1.0.0", "3.0.0")

	env.setDeveloper(map[string]any{"status": "pending", "namespaces": []any{}, "keys": []any{}})
	if rec := env.authed(http.MethodGet, "/api/v1/hub-binding/developer?refresh=1", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"pending"`) {
		t.Fatalf("refresh %d %s", rec.Code, rec.Body.String())
	}
	again := pluginItems(t, env.authed(http.MethodGet, "/api/v1/js-plugins", nil).Body.Bytes())
	assertPlugin(t, again, "acme.ahead", false, true, "1.2.0", "1.3.0")
}

func TestHubSubmissionSuccessAuditAndErrors(t *testing.T) {
	env := newDevEnv(t)
	signer := env.installSigned(t, "acme.demo", "1.4.0", env.priv, store.JSPluginStatusEnabled)
	env.installRaw(t, "acme.plain", "1.0.0", env.unsigned("acme.plain", "1.0.0"), "")
	env.setDeveloper(map[string]any{
		"status": "active", "displayName": "Ada",
		"namespaces": []map[string]string{{"name": "acme", "status": "active"}},
		"keys":       []map[string]string{{"fingerprint": signer, "status": "active"}},
	})
	env.svc.SetCatalogFixture(&market.CatalogFixture{Snapshot: market.Snapshot{Index: &marketproto.Index{Plugins: []marketproto.Plugin{
		{ID: "acme.demo", Versions: []marketproto.PluginVersion{{Version: "1.2.0"}}},
	}}}})
	if rec := env.authed(http.MethodGet, "/api/v1/hub-binding/developer", nil); rec.Code != http.StatusOK {
		t.Fatalf("developer %d %s", rec.Code, rec.Body.String())
	}

	denied := env.authed(http.MethodPost, "/api/v1/hub-binding/submissions", []byte(`{"pluginId":"acme.demo","version":"1.0.0","changelog":"nope"}`))
	if denied.Code != http.StatusNotFound || errorField(t, denied, "code") != "not_found" {
		t.Fatalf("missing version %d %s", denied.Code, denied.Body.String())
	}
	shortLog := env.authed(http.MethodPost, "/api/v1/hub-binding/submissions", []byte(`{"pluginId":"acme.demo","version":"1.4.0","changelog":"  "}`))
	if shortLog.Code != http.StatusBadRequest || errorField(t, shortLog, "code") != "changelog_required" {
		t.Fatalf("changelog %d %s", shortLog.Code, shortLog.Body.String())
	}
	unsigned := env.authed(http.MethodPost, "/api/v1/hub-binding/submissions", []byte(`{"pluginId":"acme.plain","version":"1.0.0","changelog":"notes"}`))
	if unsigned.Code != http.StatusBadRequest || errorField(t, unsigned, "code") != "package_unsigned" {
		t.Fatalf("unsigned %d %s", unsigned.Code, unsigned.Body.String())
	}
	env.svc.SetCatalogFixture(&market.CatalogFixture{Snapshot: market.Snapshot{Index: &marketproto.Index{Plugins: []marketproto.Plugin{
		{ID: "acme.demo", Versions: []marketproto.PluginVersion{{Version: "1.4.0"}}},
	}}}})
	behind := env.authed(http.MethodPost, "/api/v1/hub-binding/submissions", []byte(`{"pluginId":"acme.demo","version":"1.4.0","changelog":"notes"}`))
	if behind.Code != http.StatusConflict || errorField(t, behind, "code") != "version_not_greater" {
		t.Fatalf("not ahead %d %s", behind.Code, behind.Body.String())
	}
	if env.hub.submits() != 0 {
		t.Fatalf("local rejection called hub %d times", env.hub.submits())
	}
	env.svc.SetCatalogFixture(&market.CatalogFixture{Snapshot: market.Snapshot{Index: &marketproto.Index{Plugins: []marketproto.Plugin{
		{ID: "acme.demo", Versions: []marketproto.PluginVersion{{Version: "1.2.0"}}},
	}}}})

	env.hub.setCode("not_developer")
	for _, code := range []string{
		"not_developer", "no_active_key", "namespace_not_active", "signer_not_registered",
		"version_not_greater", "too_many_pending", "daily_limit", "changelog_required",
	} {
		env.hub.setCode(code)
		rec := env.authed(http.MethodPost, "/api/v1/hub-binding/submissions", []byte(`{"pluginId":"acme.demo","version":"1.4.0","changelog":"修好了额度显示"}`))
		if rec.Code != http.StatusBadGateway || errorField(t, rec, "code") != code || errorField(t, rec, "hub_code") != code {
			t.Fatalf("%s -> %d %s", code, rec.Code, rec.Body.String())
		}
		if msg := errorField(t, rec, "message"); msg == "" || msg == code {
			t.Fatalf("%s message = %q", code, msg)
		}
	}

	env.hub.setCode("")
	ok := env.authed(http.MethodPost, "/api/v1/hub-binding/submissions", []byte(`{"pluginId":"acme.demo","version":"1.4.0","changelog":"修好了额度显示"}`))
	if ok.Code != http.StatusOK {
		t.Fatalf("submit %d %s", ok.Code, ok.Body.String())
	}
	var created hubclient.Submission
	if err := json.Unmarshal(ok.Body.Bytes(), &created); err != nil || created.ID != "sub-9" || created.Status != "checking" {
		t.Fatalf("created %s err=%v", ok.Body.String(), err)
	}
	if env.hub.changelog() != "修好了额度显示" || !bytes.Equal(env.hub.savedPackage(), env.packages["acme.demo@1.4.0"]) {
		t.Fatalf("hub saw changelog=%q pkgLen=%d", env.hub.changelog(), len(env.hub.savedPackage()))
	}
	logs, _, err := store.NewAdminAuditLogRepository(env.st.DB()).List(context.Background(), store.AdminAuditLogFilters{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, item := range logs {
		if item.Action != "hub_binding.submission" || !item.Success {
			continue
		}
		text := string(item.Metadata)
		if item.AdminID.Valid && item.ResourceID == "acme.demo" && strings.Contains(text, "sub-9") && strings.Contains(text, "1.4.0") {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("audit missing: %#v", logs)
	}
	listed := pluginItems(t, env.authed(http.MethodGet, "/api/v1/js-plugins", nil).Body.Bytes())
	raw, _ := json.Marshal(listed["acme.demo"]["submission"])
	if !strings.Contains(string(raw), "sub-9") {
		t.Fatalf("list submission = %s", raw)
	}
	queried := env.authed(http.MethodGet, "/api/v1/hub-binding/submissions?pluginId=acme.demo", nil)
	if queried.Code != http.StatusOK || !strings.Contains(queried.Body.String(), "in_review") {
		t.Fatalf("query %d %s", queried.Code, queried.Body.String())
	}
}

func TestHubTimeoutDoesNotStallPluginList(t *testing.T) {
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
	repo := store.NewHubBindingRepository(st.DB())
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = pub
	enc, err := crypto.EncryptSecret(adminTestMasterKey, base64.StdEncoding.EncodeToString(priv))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnsureIdentity(ctx, func() (string, string, error) {
		return base64.StdEncoding.EncodeToString(pub), enc, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkBound(ctx, "inst-hang", "desk", "Ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	client := hubclient.New(repo, store.NewJSPluginRepository(st.DB()), adminTestMasterKey,
		func(context.Context) (string, error) { return srv.URL, nil },
		hubclient.Options{AllowInsecureLocal: true, HTTPTimeout: 200 * time.Millisecond})
	manager := jsplugin.NewManager(st, nil)
	h := Handler{auth: authService}.WithTrafficFlowStore(st).WithJSPlugins(manager).WithHubClient(client)
	router := devRouter(h, client, authService)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- hubAuthed(t, router, session.Token, "", http.MethodGet, "/api/v1/hub-binding/developer?refresh=1", nil)
	}()
	time.Sleep(40 * time.Millisecond)
	began := time.Now()
	listed := hubAuthed(t, router, session.Token, "", http.MethodGet, "/api/v1/js-plugins", nil)
	status := hubAuthed(t, router, session.Token, "", http.MethodGet, "/api/v1/hub-binding", nil)
	if listed.Code != http.StatusOK || status.Code != http.StatusOK || time.Since(began) > 150*time.Millisecond {
		t.Fatalf("list=%d status=%d in %s", listed.Code, status.Code, time.Since(began))
	}
	select {
	case rec := <-done:
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"none"`) || !strings.Contains(rec.Body.String(), `"unavailable":true`) {
			t.Fatalf("developer %d %s", rec.Code, rec.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("developer request did not return")
	}
}

type devEnv struct {
	t        *testing.T
	st       *store.Store
	auth     *auth.Service
	token    string
	csrf     string
	client   *hubclient.Client
	svc      *market.Service
	hub      *scriptHub
	priv     ed25519.PrivateKey
	router   http.Handler
	packages map[string][]byte
}

func newDevEnv(t *testing.T) *devEnv {
	t.Helper()
	st := openAdminJSPluginStore(t)
	ctx := context.Background()
	authService := auth.NewService(st.DB(), adminTestMasterKey)
	session, err := authService.BootstrapAdmin(ctx, "owner", "StrongPass123!", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	hub := &scriptHub{pub: pub, instanceID: "inst-dev"}
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)
	repo := store.NewHubBindingRepository(st.DB())
	enc, err := crypto.EncryptSecret(adminTestMasterKey, base64.StdEncoding.EncodeToString(priv))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnsureIdentity(ctx, func() (string, string, error) {
		return base64.StdEncoding.EncodeToString(pub), enc, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkBound(ctx, "inst-dev", "desk", "Ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	client := hubclient.New(repo, store.NewJSPluginRepository(st.DB()), adminTestMasterKey,
		func(context.Context) (string, error) { return srv.URL, nil },
		hubclient.Options{AllowInsecureLocal: true, HTTPTimeout: time.Second})
	svc, err := market.NewService(store.JSPluginRepository{}, nil, nil, []ed25519.PublicKey{}, []string{})
	if err != nil {
		t.Fatal(err)
	}
	manager := jsplugin.NewManager(st, nil)
	h := Handler{auth: authService}.WithTrafficFlowStore(st).WithJSPlugins(manager).WithPluginMarket(svc).WithHubClient(client)
	return &devEnv{
		t: t, st: st, auth: authService, token: session.Token, csrf: authService.CSRFTokenForSession(session.Token),
		client: client, svc: svc, hub: hub, priv: priv, router: devRouter(h, client, authService),
		packages: map[string][]byte{},
	}
}

func devRouter(h Handler, client *hubclient.Client, authService *auth.Service) http.Handler {
	router := chi.NewRouter()
	router.Route("/api/v1", func(v1 chi.Router) {
		v1.Use(authService.RequireAdminSession)
		v1.Use(authService.RequireAdminCSRF)
		v1.Get("/js-plugins", h.ListJSPlugins)
		RegisterHubBinding(v1, h, client)
	})
	return router
}

func (e *devEnv) authed(method, target string, body []byte) *httptest.ResponseRecorder {
	e.t.Helper()
	return hubAuthed(e.t, e.router, e.token, e.csrf, method, target, body)
}

func (e *devEnv) setDeveloper(payload map[string]any) {
	e.hub.setDeveloper(payload)
}

func (e *devEnv) installSigned(t *testing.T, id, version string, priv ed25519.PrivateKey, status string) string {
	t.Helper()
	raw, pkg := signedQuotaProbe(t, id, version, priv)
	e.packages[id+"@"+version] = raw
	e.storeVersion(t, id, version, raw, pkg.SHA256, pkg.Signer, status, pkg.Manifest)
	return pkg.Signer
}

func (e *devEnv) installRaw(t *testing.T, id, version string, raw []byte, signer string) {
	t.Helper()
	pkg, err := jsplugin.ReadPackage(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	e.storeVersion(t, id, version, raw, pkg.SHA256, signer, store.JSPluginStatusVerified, pkg.Manifest)
}

func (e *devEnv) unsigned(id, version string) []byte {
	e.t.Helper()
	raw, _ := signedQuotaProbe(e.t, id, version, nil)
	return raw
}

func (e *devEnv) storeVersion(t *testing.T, id, version string, raw []byte, sum, signer, status string, manifest jsplugin.Manifest) {
	t.Helper()
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	err = store.NewJSPluginRepository(e.st.DB()).CreatePluginVersion(context.Background(), store.JSPluginVersion{
		PluginID: id, Version: version, Manifest: store.JSON(encoded), Package: raw,
		PackageSHA256: sum, Signer: signer, Status: status, SelfTest: store.JSON(`{}`),
	}, store.JSPluginSourceUploaded)
	if err != nil {
		t.Fatal(err)
	}
}

func signedQuotaProbe(t *testing.T, id, version string, priv ed25519.PrivateKey) ([]byte, jsplugin.Package) {
	t.Helper()
	source := "export const meta = { apiVersion: 1, id: \"" + id + "\", kind: \"quota_probe\" };\nexport function probe() { return { error: \"empty\" }; }\n"
	manifest := jsplugin.Manifest{
		ID: id, Name: "Demo", Version: version, APIVersion: jsplugin.HookAPIVersion, HostAPI: jsplugin.HostAPIVersion,
		XLyra: ">=1.0.0", Kind: jsplugin.KindQuotaProbe, QuotaProbe: jsplugin.QuotaProbeSection{BaseURLMode: "as_is"},
		SHA256: map[string]string{"plugin.js": jsplugin.HashSource(source)},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jsplugin.BuildPackage(map[string][]byte{
		"manifest.json":    encoded,
		"plugin.js":        []byte(source),
		"fixtures/ok.json": []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"empty"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if priv != nil {
		raw, err = jsplugin.AttachSignature(raw, priv)
		if err != nil {
			t.Fatal(err)
		}
	}
	pkg, err := jsplugin.ReadPackage(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	return raw, pkg
}

func pluginItems(t *testing.T, raw []byte) map[string]map[string]any {
	t.Helper()
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, item := range body.Items {
		id, _ := item["id"].(string)
		out[id] = item
	}
	return out
}

func TestMarketAheadWithoutIndexMatchesSubmit(t *testing.T) {
	h := Handler{}
	item := map[string]any{}
	profile := hubclient.DeveloperProfile{
		Status:     hubclient.DeveloperActive,
		Namespaces: []hubclient.DeveloperNamespace{{Name: "acme", Status: hubclient.DeveloperActive}},
		Keys:       []hubclient.DeveloperKey{{Fingerprint: "SHA256:k", Status: hubclient.DeveloperActive}},
	}
	versions := []store.JSPluginVersion{{
		PluginID: "acme.demo", Version: "1.0.0", Status: store.JSPluginStatusEnabled, Signer: "SHA256:k",
	}}
	h.applyDeveloperFields(item, "acme.demo", versions, profile, nil, false)
	if item["owned"] != true || item["market_ahead"] != true || item["local_version"] != "1.0.0" || item["market_version"] != "" {
		t.Fatalf("%#v", item)
	}
}

func assertPlugin(t *testing.T, items map[string]map[string]any, id string, owned, ahead bool, marketVersion, local string) {
	t.Helper()
	item, ok := items[id]
	if !ok {
		t.Fatalf("missing %s in %#v", id, items)
	}
	if item["owned"] != owned || item["market_ahead"] != ahead || item["market_version"] != marketVersion || item["local_version"] != local {
		t.Fatalf("%s = owned %v ahead %v market %v local %v", id, item["owned"], item["market_ahead"], item["market_version"], item["local_version"])
	}
}

func errorField(t *testing.T, rec *httptest.ResponseRecorder, field string) string {
	t.Helper()
	var body struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	value, _ := body.Error[field].(string)
	return value
}

type scriptHub struct {
	mu         sync.Mutex
	pub        ed25519.PublicKey
	instanceID string
	developer  map[string]any
	code       string
	submitHits int
	lastChange string
	lastPkg    []byte
}

func (h *scriptHub) setDeveloper(payload map[string]any) {
	h.mu.Lock()
	h.developer = payload
	h.mu.Unlock()
}

func (h *scriptHub) setCode(code string) {
	h.mu.Lock()
	h.code = code
	h.mu.Unlock()
}

func (h *scriptHub) submits() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.submitHits
}

func (h *scriptHub) changelog() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastChange
}

func (h *scriptHub) savedPackage() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]byte(nil), h.lastPkg...)
}

func (h *scriptHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	unix, _ := strconv.ParseInt(r.Header.Get("X-XLyra-Timestamp"), 10, 64)
	sig, _ := base64.StdEncoding.DecodeString(r.Header.Get("X-XLyra-Signature"))
	message := hubclient.CanonicalMessage(r.Method, r.URL.Path, unix, body)
	if r.Header.Get("X-XLyra-Instance") != h.instanceID || !ed25519.Verify(h.pub, []byte(message), sig) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case "/api/instance/developer":
		h.mu.Lock()
		payload := h.developer
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	case "/api/instance/submissions":
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]string{{
				"id": "sub-9", "pluginId": r.URL.Query().Get("pluginId"), "version": "1.4.0",
				"status": "in_review", "reviewNote": "", "submittedAt": "2026-10-27T00:00:00Z",
			}}})
			return
		}
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		var changelog string
		var pkg []byte
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			raw, _ := io.ReadAll(part)
			switch part.FormName() {
			case "changelog":
				changelog = string(raw)
			case "package":
				pkg = raw
			}
		}
		h.mu.Lock()
		h.submitHits++
		h.lastChange = changelog
		h.lastPkg = pkg
		code := h.code
		h.mu.Unlock()
		if code != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": code}})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "sub-9", "status": "checking"})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}
