package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"xlyra/server/internal/jsplugin"
)

func TestJSPluginSettingsHTTP(t *testing.T) {
	st := openAdminJSPluginStore(t)
	manager := jsplugin.NewManager(st, nil)
	h := Handler{}.WithJSPlugins(manager)
	ctx := context.Background()

	const id = "acme.settings"
	source := "export const meta = { apiVersion: 1, id: \"acme.settings\", kind: \"quota_probe\" };\nexport function probe() { return { error: \"empty\" }; }\n"
	min := 1.0
	max := 5.0
	manifest := jsplugin.Manifest{
		ID: id, Name: "Settings", Version: "1.0.0", APIVersion: 1, HostAPI: 1,
		Kind: jsplugin.KindQuotaProbe, QuotaProbe: jsplugin.QuotaProbeSection{BaseURLMode: "as_is"},
		Settings: []jsplugin.SettingSpec{
			{Key: "region", Type: jsplugin.SettingTypeString, Default: "cn", Options: []string{"cn", "intl"}},
			{Key: "retries", Type: jsplugin.SettingTypeNumber, Default: float64(1), Min: &min, Max: &max},
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

	got := settingsCall(t, h.GetJSPluginSettings, http.MethodGet, id, "")
	if got.Code != http.StatusOK {
		t.Fatalf("get: %d %s", got.Code, got.Body.String())
	}
	var body struct {
		Schema   []jsplugin.SettingSpec `json:"schema"`
		Values   map[string]any         `json:"values"`
		Resolved map[string]any         `json:"resolved"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Schema) != 2 || len(body.Values) != 0 || body.Resolved["region"] != "cn" || body.Resolved["retries"] != float64(1) {
		t.Fatalf("get body = %#v", body)
	}

	updated := settingsCall(t, h.UpdateJSPluginSettings, http.MethodPut, id, `{"values":{"region":"intl"}}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("put: %d %s", updated.Code, updated.Body.String())
	}
	var putBody struct {
		OK       bool           `json:"ok"`
		Resolved map[string]any `json:"resolved"`
	}
	if err := json.Unmarshal(updated.Body.Bytes(), &putBody); err != nil {
		t.Fatal(err)
	}
	if !putBody.OK || putBody.Resolved["region"] != "intl" || putBody.Resolved["retries"] != float64(1) {
		t.Fatalf("put body = %#v", putBody)
	}

	invalid := settingsCall(t, h.UpdateJSPluginSettings, http.MethodPut, id, `{"values":{"region":"eu"}}`)
	invalidBody := decodeLifecycleError(t, invalid)
	if invalid.Code != http.StatusBadRequest || invalidBody.Error.Code != "js_plugin_settings_invalid" {
		t.Fatalf("invalid put: %d %#v", invalid.Code, invalidBody.Error)
	}
	missing := settingsCall(t, h.GetJSPluginSettings, http.MethodGet, "missing.example", "")
	missingBody := decodeLifecycleError(t, missing)
	if missing.Code != http.StatusNotFound || missingBody.Error.Code != "not_found" {
		t.Fatalf("missing get: %d %#v", missing.Code, missingBody.Error)
	}
}

func settingsCall(t *testing.T, handler func(http.ResponseWriter, *http.Request), method, id, body string) *httptestResponse {
	t.Helper()
	req := adminRequestWithRouteVars(adminTestRequest(method, "/", body), map[string]string{"id": id})
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := adminPerform(handler, req)
	return &httptestResponse{Code: rec.Code, Body: rec.Body}
}
