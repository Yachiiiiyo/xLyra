package jsplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/color"
	"strings"
	"testing"
	"time"

	"xlyra/server/internal/store"
)

func TestPluginIconUsesVersionHeaders(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	manager := NewManager(st, nil)
	repo := store.NewJSPluginRepository(st.DB())
	pngBytes := solidPNG(t, 64, 64, color.NRGBA{255, 255, 255, 255})

	older, err := manager.Upload(ctx, "", quotaIconPackage(t, "acme.probe", "1.0.0", "assets/icon.png", map[string][]byte{
		"assets/icon.png": pngBytes,
	}))
	if err != nil {
		t.Fatalf("upload icon: %v", err)
	}
	if _, err := manager.Upload(ctx, "", quotaIconPackage(t, "acme.probe", "2.0.0", "", nil)); err != nil {
		t.Fatalf("upload plain: %v", err)
	}
	stamp := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	if err := st.DB().Model(&store.JSPluginVersion{}).Where("plugin_id = ? AND version = ?", "acme.probe", "1.0.0").Update("created_at", stamp).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Model(&store.JSPluginVersion{}).Where("plugin_id = ? AND version = ?", "acme.probe", "2.0.0").Update("created_at", stamp.Add(time.Hour)).Error; err != nil {
		t.Fatal(err)
	}

	headers, err := repo.ListVersionHeadersForPlugin(ctx, "acme.probe")
	if err != nil {
		t.Fatal(err)
	}
	if len(headers) != 2 {
		t.Fatalf("headers = %d", len(headers))
	}
	for _, row := range headers {
		if len(row.Package) != 0 || row.PackageSHA256 == "" || len(row.Manifest) == 0 || row.CreatedAt.IsZero() {
			t.Fatalf("header = package %d sha %q manifest %d at %v", len(row.Package), row.PackageSHA256, len(row.Manifest), row.CreatedAt)
		}
	}
	full, err := repo.GetVersion(ctx, "acme.probe", "1.0.0")
	if err != nil || len(full.Package) == 0 {
		t.Fatalf("selected package was not loaded: %v %d", err, len(full.Package))
	}

	if _, err := manager.PluginIcon(ctx, "acme.probe"); !errors.Is(err, ErrPluginIconNotFound) {
		t.Fatalf("latest without icon: %v", err)
	}
	if err := repo.SetVersionStatus(ctx, "acme.probe", "1.0.0", store.JSPluginStatusEnabled, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Exec(`UPDATE js_plugin_versions SET package = ? WHERE plugin_id = ? AND version = ?`, []byte{1, 2, 3}, "acme.probe", "2.0.0").Error; err != nil {
		t.Fatal(err)
	}
	got, err := manager.PluginIcon(ctx, "acme.probe")
	if err != nil {
		t.Fatal(err)
	}
	if got.ContentType != "image/png" || !bytes.Equal(got.Bytes, pngBytes) || got.SHA256 != older.PackageSHA256 {
		t.Fatalf("enabled icon = %q %q %d", got.ContentType, got.SHA256, len(got.Bytes))
	}
	if _, err := manager.PluginIcon(ctx, "missing.example"); !errors.Is(err, ErrPluginIconNotFound) {
		t.Fatalf("missing plugin: %v", err)
	}
}

func TestStoredDarkIconStillEnables(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	manager := NewManager(st, nil)
	raw := quotaIconPackage(t, "acme.probe", "1.0.0", "assets/icon.png", map[string][]byte{
		"assets/icon.png":  glyphPNG(t, color.NRGBA{0, 0, 0, 255}),
		"fixtures/ok.json": []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"empty"}}`),
	})
	pkg, err := ReadPackage(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CheckIconLegibility(raw, pkg.Manifest); err == nil || !strings.Contains(err.Error(), "图标有透明背景") {
		t.Fatalf("legibility = %v", err)
	}
	manifestJSON, err := json.Marshal(pkg.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	repo := store.NewJSPluginRepository(st.DB())
	if err := repo.CreatePluginVersion(ctx, store.JSPluginVersion{
		PluginID: pkg.Manifest.ID, Version: pkg.Manifest.Version,
		Manifest: store.JSON(manifestJSON), Package: raw, PackageSHA256: pkg.SHA256,
		Status: store.JSPluginStatusVerified, SelfTest: store.JSON(`{"ok":true}`),
	}, store.JSPluginSourceUploaded); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(ctx, pkg.Manifest.ID, pkg.Manifest.Version, EnableOptions{ConfirmUntrusted: true}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reload(ctx); err != nil {
		t.Fatal(err)
	}
}
