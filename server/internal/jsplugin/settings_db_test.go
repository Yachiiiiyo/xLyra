package jsplugin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"gorm.io/gorm"

	"xlyra/server/internal/store"
)

func TestManagerPluginSettingsPersistAndReject(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	manager := NewManager(st, nil)
	repo := store.NewJSPluginRepository(st.DB())

	const id = "acme.settings"
	source := `export const meta = { apiVersion: 1, id: "acme.settings", kind: "quota_probe" };
export function probe() { return { error: "empty" }; }`
	min := 1.0
	max := 9.0
	manifest := testManifest(id, source)
	manifest.Settings = []SettingSpec{
		{Key: "region", Type: SettingTypeString, Default: "cn", Options: []string{"cn", "intl"}},
		{Key: "retries", Type: SettingTypeNumber, Default: float64(1), Min: &min, Max: &max},
		{Key: "verbose", Type: SettingTypeBoolean, Default: false},
	}
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	fixture := []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"empty"}}`)
	pkg, err := BuildPackage(map[string][]byte{"manifest.json": rawManifest, "plugin.js": []byte(source), "fixtures/ok.json": fixture})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Upload(ctx, "", pkg); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(ctx, id, manifest.Version, EnableOptions{ConfirmUntrusted: true}); err != nil {
		t.Fatal(err)
	}

	specs, stored, resolved, err := manager.GetPluginSettings(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 3 || len(stored) != 0 || resolved["region"] != "cn" || resolved["retries"] != float64(1) || resolved["verbose"] != false {
		t.Fatalf("initial settings specs=%d stored=%#v resolved=%#v", len(specs), stored, resolved)
	}

	updated, err := manager.UpdatePluginSettings(ctx, id, map[string]any{"region": "intl", "retries": float64(4)})
	if err != nil {
		t.Fatal(err)
	}
	if updated["region"] != "intl" || updated["retries"] != float64(4) || updated["verbose"] != false {
		t.Fatalf("updated = %#v", updated)
	}
	if err := manager.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	plugin, ok := manager.Catalog().Registry().ByPluginID(id)
	if !ok {
		t.Fatal("reloaded plugin missing")
	}
	effective := plugin.EffectiveSettings(nil)
	if effective["region"] != "intl" || effective["retries"] != float64(4) || effective["verbose"] != false {
		t.Fatalf("effective after reload = %#v", effective)
	}

	before, err := repo.GetPlugin(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for name, values := range map[string]map[string]any{
		"type":    {"verbose": "yes"},
		"range":   {"retries": float64(20)},
		"options": {"region": "eu"},
	} {
		_, err := manager.UpdatePluginSettings(ctx, id, values)
		var invalid *InvalidSettingsError
		if !errors.As(err, &invalid) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	after, err := repo.GetPlugin(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if string(after.Settings) != string(before.Settings) {
		t.Fatalf("invalid update persisted: before %s after %s", before.Settings, after.Settings)
	}

	if _, _, _, err := manager.GetPluginSettings(ctx, "missing.example"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("missing plugin = %v", err)
	}
}
