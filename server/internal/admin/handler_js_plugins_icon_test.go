package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/store"
)

func TestListJSPluginsIconURL(t *testing.T) {
	st := openAdminJSPluginStore(t)
	ctx := context.Background()
	manager := jsplugin.NewManager(st, nil)
	h := Handler{}.WithTrafficFlowStore(st).WithJSPlugins(manager)
	repo := store.NewJSPluginRepository(st.DB())
	pngBytes := adminSolidPNG(t, 64, 64, color.NRGBA{255, 255, 255, 255})

	iconRow, err := manager.Upload(ctx, "", adminQuotaIconPackage(t, "acme-icon", "1.0.0", "assets/icon.png", pngBytes))
	if err != nil {
		t.Fatalf("upload icon: %v", err)
	}
	if _, err := manager.Upload(ctx, "", adminQuotaIconPackage(t, "acme-plain", "1.0.0", "", nil)); err != nil {
		t.Fatalf("upload plain: %v", err)
	}
	picked, err := manager.Upload(ctx, "", adminQuotaIconPackage(t, "acme-pick", "1.0.0", "assets/icon.png", pngBytes))
	if err != nil {
		t.Fatalf("upload pick v1: %v", err)
	}
	if _, err := manager.Upload(ctx, "", adminQuotaIconPackage(t, "acme-pick", "2.0.0", "", nil)); err != nil {
		t.Fatalf("upload pick v2: %v", err)
	}
	stamp := time.Date(2026, 10, 25, 2, 0, 0, 0, time.UTC)
	if err := st.DB().Model(&store.JSPluginVersion{}).Where("plugin_id = ? AND version = ?", "acme-pick", "1.0.0").Update("created_at", stamp).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Model(&store.JSPluginVersion{}).Where("plugin_id = ? AND version = ?", "acme-pick", "2.0.0").Update("created_at", stamp.Add(time.Hour)).Error; err != nil {
		t.Fatal(err)
	}

	items := listPluginItems(t, h)
	if len(items) != 3 {
		t.Fatalf("items = %d", len(items))
	}
	byID := map[string]map[string]any{}
	for _, item := range items {
		id, _ := item["id"].(string)
		byID[id] = item
	}
	if _, ok := byID["acme-plain"]["icon_url"]; !ok {
		t.Fatal("plain item omitted icon_url")
	}
	if byID["acme-plain"]["icon_url"] != "" {
		t.Fatalf("plain icon_url = %#v", byID["acme-plain"]["icon_url"])
	}
	wantIcon := "/api/v1/plugin-icons/acme-icon?v=" + iconRow.PackageSHA256[:16]
	if byID["acme-icon"]["icon_url"] != wantIcon || byID["acme-icon"]["kind"] != "quota_probe" {
		t.Fatalf("icon item = %#v", byID["acme-icon"])
	}
	if byID["acme-pick"]["icon_url"] != "" || byID["acme-pick"]["enabled_version"] != "" || byID["acme-pick"]["version_count"] != float64(2) {
		t.Fatalf("latest without icon = %#v", byID["acme-pick"])
	}

	if err := repo.SetVersionStatus(ctx, "acme-pick", "1.0.0", store.JSPluginStatusEnabled, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Exec(`UPDATE js_plugin_versions SET package = ? WHERE plugin_id = ?`, []byte{9}, "acme-pick").Error; err != nil {
		t.Fatal(err)
	}
	items = listPluginItems(t, h)
	byID = map[string]map[string]any{}
	for _, item := range items {
		id, _ := item["id"].(string)
		byID[id] = item
	}
	wantPicked := "/api/v1/plugin-icons/acme-pick?v=" + picked.PackageSHA256[:16]
	if byID["acme-pick"]["icon_url"] != wantPicked || byID["acme-pick"]["enabled_version"] != "1.0.0" {
		t.Fatalf("enabled icon = %#v", byID["acme-pick"])
	}
}

func listPluginItems(t *testing.T, h Handler) []map[string]any {
	t.Helper()
	rec := adminPerform(h.ListJSPlugins, httptest.NewRequest(http.MethodGet, "/api/v1/js-plugins", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Items
}

func adminQuotaIconPackage(t *testing.T, id, version, iconPath string, icon []byte) []byte {
	t.Helper()
	source := "export const meta = { apiVersion: 1, id: \"" + id + "\", kind: \"quota_probe\" };\nexport function probe() { return { error: \"empty\" }; }\n"
	manifest := jsplugin.Manifest{
		ID: id, Name: id, Version: version, APIVersion: 1, HostAPI: 1,
		Kind: jsplugin.KindQuotaProbe, Icon: iconPath,
		QuotaProbe: jsplugin.QuotaProbeSection{BaseURLMode: "as_is"},
		SHA256:     map[string]string{"plugin.js": jsplugin.HashSource(source)},
	}
	extra := map[string][]byte{
		"fixtures/ok.json": []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"empty"}}`),
	}
	if iconPath != "" {
		extra[iconPath] = icon
	}
	return mustPluginPackage(t, manifest, source, extra)
}

func adminSolidPNG(t *testing.T, w, h int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
