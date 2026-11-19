package site

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"xlyra/server/internal/adapter"
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/store"
)

func TestPluginAccountStorageScopeFollowsLoginAndRefresh(t *testing.T) {
	st := openPluginAccountStore(t)
	ctx := context.Background()
	const pluginID = "acme.example"
	const siteType = "acme.example/pool"

	source := `export const meta = { apiVersion: 1, id: "acme.example", kind: "site_provider" };
export function startLogin(ctx, steps) {
  storage.set("phase", "login");
  if (steps.length === 0) return { request: { method: "POST", path: "/login/start" } };
  return { result: { kind: "account_login", status: "complete", credential: { access_token: "{{secret.access_token}}", refresh_token: "{{secret.refresh_token}}", subject: "user-1" } } };
}
export function pollLogin(ctx, steps) {
  if (steps.length === 0) return { request: { method: "POST", path: "/login/poll" } };
  return { result: { kind: "account_login", status: "complete", credential: { access_token: "{{secret.access_token}}", refresh_token: "{{secret.refresh_token}}", subject: "user-1" } } };
}
export function refreshTokens(ctx, steps) {
  storage.set("phase", "refresh");
  if (steps.length === 0) return { request: { method: "POST", path: "/refresh" } };
  return { result: { kind: "account_refresh", status: "ok", credential: { access_token: "{{secret.access_token}}", refresh_token: "{{secret.refresh_token}}", subject: "user-1" } } };
}
export function summarizeAccount() {
  storage.set("summary", "ok");
  return { result: { kind: "account_summary", status: "ok", entries: [] } };
}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"accessToken":"ACCESS-1","refreshToken":"REFRESH-1"}`)
	}))
	t.Cleanup(upstream.Close)
	manifest := jsplugin.Manifest{
		ID: pluginID, Name: "Example", Version: "1.0.0", APIVersion: 1, HostAPI: 2,
		Kind: jsplugin.KindSiteProvider, Permissions: []string{"storage"},
		SiteProvider: jsplugin.SiteProviderSection{
			SiteType: siteType,
			Display:  jsplugin.SiteProviderDisplay{Name: jsplugin.LocalizedText{"zh": "示例", "en": "Example"}},
			Hosts:    []string{"api.example.cn"},
			Credential: jsplugin.SiteProviderCredential{
				Model: "account_pool",
				Fields: []jsplugin.SiteProviderField{
					{Name: "access_token", Secret: true},
					{Name: "refresh_token", Secret: true},
					{Name: "subject"},
				},
				AccessTokenField:  "access_token",
				RefreshTokenField: "refresh_token",
				Identity:          jsplugin.Sources("field.subject"),
			},
			Protocol: jsplugin.SiteProviderProtocol{
				Kind: "openai_chat", Path: "/v1/chat/completions", OfficialBaseURL: "https://api.example.cn",
				DefaultEndpointTypes: []string{"openai"},
			},
			Regions: []jsplugin.SiteProviderRegion{{
				ID: "cn", Label: jsplugin.LocalizedText{"zh": "中国", "en": "China"}, BaseURL: "https://api.example.cn",
			}},
			DefaultRegion: "cn",
			Login: &jsplugin.SiteProviderLogin{Kind: "custom_poll", SecretPaths: map[string]string{
				"accessToken": "access_token", "refreshToken": "refresh_token",
			}},
			Refresh: &jsplugin.SiteProviderRefresh{Kind: "custom_steps", SecretPaths: map[string]string{
				"accessToken": "access_token", "refreshToken": "refresh_token",
			}},
		},
		SHA256: map[string]string{"plugin.js": jsplugin.HashSource(source)},
	}
	plugin, err := jsplugin.NewPlugin(manifest, source, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.NewJSPluginRepository(st.DB()).UpsertPlugin(ctx, pluginID, store.JSPluginSourceUploaded); err != nil {
		t.Fatal(err)
	}
	plugin.SetStorageRepository(store.NewJSPluginStorageRepository(st.DB()))
	client := upstream.Client()
	client.Transport = rewriteHostTransport{base: upstream.URL, inner: client.Transport}
	runtime, err := jsplugin.NewAccountProviderRuntime(plugin, client)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &accountStorageRuntime{runtime: runtime}

	spec, err := jsplugin.NewAccountPoolSpec(manifest.SiteProvider)
	if err != nil {
		t.Fatal(err)
	}
	ClearPluginSiteTypes()
	t.Cleanup(ClearPluginSiteTypes)
	if err := SetPluginSiteTypes([]PluginSiteType{{
		Descriptor: SiteTypeDescriptor{SiteType: siteType, DisplayName: "Pool", CredentialKey: siteType, ShowInCreateDialog: true},
		PluginID:   pluginID,
		Enabled:    true,
		Hosts:      []string{"api.example.cn"},
		Pool:       &spec,
	}}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(st, siteTestMasterKey)
	if err := svc.SetPluginAccountRuntimes(map[string]PluginAccountRuntime{siteType: adapter}); err != nil {
		t.Fatal(err)
	}
	provider, ok := svc.AccountProviderFor(siteType)
	if !ok {
		t.Fatal("account provider missing")
	}
	meta, err := json.Marshal(map[string]any{"gateway": map[string]any{"plugin_settings": map[string]any{pluginID: map[string]any{"region": "intl"}}}})
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.NewSiteRepository(st.DB()).Create(ctx, store.CreateSiteParams{
		Name: "Pool", Slug: "pool-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12],
		SiteType: siteType, BaseURL: "https://api.example.cn", Status: "active", Enabled: true, RoutingPriority: 1,
		Meta: store.JSON(meta),
	})
	if err != nil {
		t.Fatal(err)
	}

	started, err := provider.StartLogin(ctx, created.ID, AccountLoginParams{})
	if err != nil {
		t.Fatal(err)
	}
	if started.Session == "" {
		t.Fatal("login session missing")
	}
	assertStorageValue(t, st, pluginID, jsplugin.StorageScopeSite, created.ID.String(), "phase", "login")

	completed, err := provider.PollLogin(ctx, created.ID, started.Session, nil)
	if err != nil {
		t.Fatal(err)
	}
	account := pluginAccountFromAny(t, completed.Account)
	refreshed, err := provider.Refresh(ctx, created.ID, account.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	if pluginAccountFromAny(t, refreshed).CredentialID != account.CredentialID {
		t.Fatal("refresh returned a different account")
	}
	assertStorageValue(t, st, pluginID, jsplugin.StorageScopeCredential, account.CredentialID.String(), "phase", "refresh")
	assertStorageValue(t, st, pluginID, jsplugin.StorageScopeCredential, account.CredentialID.String(), "summary", "ok")
	if got, ok := storageValue(t, st, pluginID, jsplugin.StorageScopeSite, created.ID.String(), "phase"); !ok || got != "login" {
		t.Fatalf("login scope was overwritten: %q ok=%v", got, ok)
	}
}

func assertStorageValue(t *testing.T, st *store.Store, pluginID, scopeType, scopeID, key, want string) {
	t.Helper()
	got, ok := storageValue(t, st, pluginID, scopeType, scopeID, key)
	if !ok || got != want {
		t.Fatalf("%s/%s %s = %q ok=%v, want %q", scopeType, scopeID, key, got, ok, want)
	}
}

func storageValue(t *testing.T, st *store.Store, pluginID, scopeType, scopeID, key string) (string, bool) {
	t.Helper()
	raw, ok, err := store.NewJSPluginStorageRepository(st.DB()).Get(context.Background(), pluginID, scopeType, scopeID, key)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value, true
}

type rewriteHostTransport struct {
	base  string
	inner http.RoundTripper
}

func (t rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, err := url.Parse(t.base)
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.URL.Scheme = target.Scheme
	clone.URL.Host = target.Host
	inner := t.inner
	if inner == nil {
		inner = http.DefaultTransport
	}
	return inner.RoundTrip(clone)
}

type accountStorageRuntime struct {
	runtime *jsplugin.AccountProviderRuntime
}

func (a *accountStorageRuntime) StartLogin(ctx context.Context, params map[string]string) (PluginLoginStart, error) {
	session, err := a.runtime.StartLogin(ctx, params)
	if err != nil {
		return PluginLoginStart{}, err
	}
	return PluginLoginStart{ID: session.ID, Kind: session.Kind, Status: session.Status, Identity: session.Identity, Credential: session.Credential}, nil
}

func (a *accountStorageRuntime) PollLogin(ctx context.Context, sessionID string) (PluginLoginPoll, error) {
	poll, err := a.runtime.PollLogin(ctx, jsplugin.LoginSession{ID: sessionID})
	if err != nil {
		return PluginLoginPoll{}, err
	}
	return PluginLoginPoll{Status: poll.Status, Identity: poll.Identity, Credential: poll.Credential}, nil
}

func (a *accountStorageRuntime) Refresh(ctx context.Context, fields map[string]string) (map[string]string, error) {
	return a.runtime.Refresh(ctx, fields)
}

func (a *accountStorageRuntime) Summarize(ctx context.Context, baseURL string, fields map[string]string) (PluginAccountSummary, error) {
	summary, err := a.runtime.Summarize(ctx, jsplugin.AccountSnapshot{BaseURL: baseURL, Fields: fields})
	if err != nil {
		return PluginAccountSummary{}, err
	}
	return PluginAccountSummary{Status: summary.Status, Remark: summary.Remark, Label: summary.Label}, nil
}

func (a *accountStorageRuntime) ListModels(context.Context, map[string]string) ([]adapter.Model, error) {
	return nil, nil
}

func (a *accountStorageRuntime) ParseImport([]byte) ([]PluginImportedAccount, error) {
	return nil, ErrAccountImportUnsupported
}
