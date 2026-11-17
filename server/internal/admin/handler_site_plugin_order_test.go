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

func TestSitePluginOrderHTTP(t *testing.T) {
	st := openAdminJSPluginStore(t)
	ctx := context.Background()
	manager := jsplugin.NewManager(st, nil)
	authService := auth.NewService(st.DB(), adminTestMasterKey)
	sites := sitepkg.NewService(st, adminTestMasterKey)
	h := Handler{auth: authService, sites: sites}.WithJSPlugins(manager)

	uploadOrderPlugin(t, ctx, manager, "acme.first")
	uploadOrderPlugin(t, ctx, manager, "acme.second")
	siteRepo := store.NewSiteRepository(st.DB())
	item, err := siteRepo.Create(ctx, store.CreateSiteParams{
		Name: "Ordered", Slug: "ordered-" + uuid.NewString()[:8], SiteType: "newapi",
		BaseURL: "https://example.com", Status: "active", Enabled: true, RoutingPriority: 1,
		Meta: store.JSON(`{"gateway":{"plugin_ids":["acme.first"],"request_timeout_ms":1500}}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	ok := pluginOrderCall(t, h, item.ID.String(), `{"plugin_ids":[" acme.second ","acme.first","acme.second"]}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("put: %d %s", ok.Code, ok.Body.String())
	}
	var body struct {
		GatewayConfig struct {
			PluginIDs []string `json:"plugin_ids"`
		} `json:"gateway_config"`
	}
	if err := json.Unmarshal(ok.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	saved, err := siteRepo.GetByID(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	cfg := sitepkg.GatewayConfigFromSiteMeta(saved.Meta)
	if cfg == nil || len(cfg.PluginIDs) != 2 || cfg.PluginIDs[0] != "acme.second" || cfg.PluginIDs[1] != "acme.first" || cfg.RequestTimeoutMS == nil || *cfg.RequestTimeoutMS != 1500 {
		t.Fatalf("stored = %#v response = %#v", cfg, body.GatewayConfig)
	}

	unknown := pluginOrderCall(t, h, item.ID.String(), `{"plugin_ids":["acme.missing"]}`)
	unknownBody := decodeLifecycleError(t, unknown)
	if unknown.Code != http.StatusBadRequest || unknownBody.Error.Code != "js_plugin_order_invalid" {
		t.Fatalf("unknown: %d %#v", unknown.Code, unknownBody.Error)
	}
	disabled := pluginOrderCall(t, h, item.ID.String(), `{"plugin_ids":["acme.third"]}`)
	disabledBody := decodeLifecycleError(t, disabled)
	if disabled.Code != http.StatusBadRequest || disabledBody.Error.Code != "js_plugin_order_invalid" {
		t.Fatalf("disabled: %d %#v", disabled.Code, disabledBody.Error)
	}
	missing := pluginOrderCall(t, h, uuid.NewString(), `{"plugin_ids":["acme.first"]}`)
	missingBody := decodeLifecycleError(t, missing)
	if missing.Code != http.StatusNotFound || missingBody.Error.Code != "not_found" {
		t.Fatalf("missing site: %d %#v", missing.Code, missingBody.Error)
	}

	logs, _, err := authService.ListAuditLogs(ctx, store.AdminAuditLogFilters{Action: "site.plugin_order", PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	sawSuccess := false
	for _, log := range logs {
		if log.Success {
			sawSuccess = true
		}
	}
	if !sawSuccess {
		t.Fatalf("audit = %+v", logs)
	}
}

func uploadOrderPlugin(t *testing.T, ctx context.Context, manager *jsplugin.Manager, id string) {
	t.Helper()
	source := "export const meta = { apiVersion: 1, id: \"" + id + "\", kind: \"quota_probe\" };\nexport function probe() { return { error: \"empty\" }; }\n"
	manifest := jsplugin.Manifest{
		ID: id, Name: id, Version: "1.0.0", APIVersion: 1, HostAPI: 1,
		Kind: jsplugin.KindQuotaProbe, QuotaProbe: jsplugin.QuotaProbeSection{BaseURLMode: "as_is"},
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
}

func pluginOrderCall(t *testing.T, h Handler, siteID, body string) *httptestResponse {
	t.Helper()
	req := adminRequestWithRouteVars(adminTestRequest(http.MethodPut, "/", body), map[string]string{"siteID": siteID})
	req.Header.Set("Content-Type", "application/json")
	rec := adminPerform(h.UpdateSitePluginOrder, req)
	return &httptestResponse{Code: rec.Code, Body: rec.Body}
}
