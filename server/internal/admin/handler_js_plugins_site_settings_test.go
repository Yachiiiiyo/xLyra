package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"xlyra/server/internal/auth"
	"xlyra/server/internal/jsplugin"
	sitepkg "xlyra/server/internal/site"
	"xlyra/server/internal/store"
)

func TestJSPluginSiteSettingsHTTP(t *testing.T) {
	st := openAdminJSPluginStore(t)
	ctx := context.Background()
	manager := jsplugin.NewManager(st, nil)
	authService := auth.NewService(st.DB(), adminTestMasterKey)
	sites := sitepkg.NewService(st, adminTestMasterKey)
	h := Handler{auth: authService, sites: sites}.WithJSPlugins(manager)

	const id = "acme.settings"
	source := "export const meta = { apiVersion: 1, id: \"acme.settings\", kind: \"quota_probe\" };\nexport function probe() { return { error: \"empty\" }; }\n"
	manifest := jsplugin.Manifest{
		ID: id, Name: "Settings", Version: "1.0.0", APIVersion: 1, HostAPI: 1,
		Kind: jsplugin.KindQuotaProbe, QuotaProbe: jsplugin.QuotaProbeSection{BaseURLMode: "as_is"},
		Settings: []jsplugin.SettingSpec{
			{Key: "region", Type: jsplugin.SettingTypeString, Default: "cn", Options: []string{"cn", "intl"}},
		},
		SHA256: map[string]string{"plugin.js": jsplugin.HashSource(source)},
	}
	if _, err := manager.Upload(ctx, "", mustPluginPackage(t, manifest, source, map[string][]byte{
		"fixtures/ok.json": []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"empty"}}`),
	})); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(ctx, id, manifest.Version, jsplugin.EnableOptions{ConfirmUntrusted: true}); err != nil {
		t.Fatal(err)
	}

	siteRepo := store.NewSiteRepository(st.DB())
	mounted, err := siteRepo.Create(ctx, store.CreateSiteParams{
		Name: "Mounted", Slug: "mounted-" + uuid.NewString()[:8], SiteType: "newapi",
		BaseURL: "https://example.com", Status: "active", Enabled: true, RoutingPriority: 1,
		Meta: store.JSON(`{"gateway":{"plugin_ids":["acme.settings"]}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := siteRepo.Create(ctx, store.CreateSiteParams{
		Name: "Other", Slug: "other-" + uuid.NewString()[:8], SiteType: "newapi",
		BaseURL: "https://example.com", Status: "active", Enabled: true, RoutingPriority: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	put := siteSettingsCall(t, h.UpdateJSPluginSiteSettings, id, mounted.ID.String(), `{"values":{"region":"intl"}}`)
	if put.Code != http.StatusOK {
		t.Fatalf("put: %d %s", put.Code, put.Body.String())
	}
	var putBody struct {
		Values map[string]any `json:"values"`
	}
	if err := json.Unmarshal(put.Body.Bytes(), &putBody); err != nil {
		t.Fatal(err)
	}
	if putBody.Values["region"] != "intl" {
		t.Fatalf("put body = %#v", putBody)
	}
	saved, err := siteRepo.GetByID(ctx, mounted.ID)
	if err != nil {
		t.Fatal(err)
	}
	cfg := sitepkg.GatewayConfigFromSiteMeta(saved.Meta)
	if cfg == nil || cfg.PluginSettings["acme.settings"]["region"] != "intl" {
		t.Fatalf("stored settings = %#v", cfg)
	}

	cleared := siteSettingsCall(t, h.UpdateJSPluginSiteSettings, id, mounted.ID.String(), `{"values":{}}`)
	if cleared.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", cleared.Code, cleared.Body.String())
	}
	saved, err = siteRepo.GetByID(ctx, mounted.ID)
	if err != nil {
		t.Fatal(err)
	}
	cfg = sitepkg.GatewayConfigFromSiteMeta(saved.Meta)
	if cfg != nil && len(cfg.PluginSettings) != 0 {
		t.Fatalf("settings not cleared: %#v", cfg.PluginSettings)
	}

	unbound := siteSettingsCall(t, h.UpdateJSPluginSiteSettings, id, other.ID.String(), `{"values":{"region":"intl"}}`)
	unboundBody := decodeLifecycleError(t, unbound)
	if unbound.Code != http.StatusConflict || unboundBody.Error.Code != "js_plugin_not_bound" {
		t.Fatalf("unbound: %d %#v", unbound.Code, unboundBody.Error)
	}

	invalid := siteSettingsCall(t, h.UpdateJSPluginSiteSettings, id, mounted.ID.String(), `{"values":{"region":"eu"}}`)
	invalidBody := decodeLifecycleError(t, invalid)
	if invalid.Code != http.StatusBadRequest || invalidBody.Error.Code != "js_plugin_settings_invalid" {
		t.Fatalf("invalid: %d %#v", invalid.Code, invalidBody.Error)
	}

	missingPlugin := siteSettingsCall(t, h.UpdateJSPluginSiteSettings, "missing.example", mounted.ID.String(), `{"values":{}}`)
	missingPluginBody := decodeLifecycleError(t, missingPlugin)
	if missingPlugin.Code != http.StatusNotFound || missingPluginBody.Error.Code != "not_found" {
		t.Fatalf("missing plugin: %d %#v", missingPlugin.Code, missingPluginBody.Error)
	}
	missingSite := siteSettingsCall(t, h.UpdateJSPluginSiteSettings, id, uuid.NewString(), `{"values":{}}`)
	missingSiteBody := decodeLifecycleError(t, missingSite)
	if missingSite.Code != http.StatusNotFound || missingSiteBody.Error.Code != "not_found" {
		t.Fatalf("missing site: %d %#v", missingSite.Code, missingSiteBody.Error)
	}

	logs, _, err := authService.ListAuditLogs(ctx, store.AdminAuditLogFilters{Action: "js_plugin.site_settings", PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) == 0 {
		t.Fatal("site settings were not audited")
	}
	var meta map[string]any
	if err := json.Unmarshal(logs[0].Metadata, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["site_id"] == "" {
		t.Fatalf("audit meta = %#v", meta)
	}
}

func siteSettingsCall(t *testing.T, handler func(http.ResponseWriter, *http.Request), pluginID, siteID, body string) *httptestResponse {
	t.Helper()
	req := adminRequestWithRouteVars(adminTestRequest(http.MethodPut, "/", body), map[string]string{
		"id":      pluginID,
		"site_id": siteID,
	})
	req.Header.Set("Content-Type", "application/json")
	rec := adminPerform(handler, req)
	return &httptestResponse{Code: rec.Code, Body: rec.Body}
}
