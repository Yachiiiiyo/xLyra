package site

import (
	"encoding/json"
	"testing"
)

func TestNormalizeQuotaProbeTypeAllowsPluginSelector(t *testing.T) {
	t.Parallel()
	got, err := NormalizeQuotaProbeType("plugin:com.example.foo-quota")
	if err != nil || got != "plugin:com.example.foo-quota" {
		t.Fatalf("NormalizeQuotaProbeType = %q, err = %v", got, err)
	}
	if _, err := NormalizeQuotaProbeType("plugin:"); err == nil {
		t.Fatal("expected empty plugin id to fail")
	}
}

func TestGatewayPluginSettingsNormalizeAndMerge(t *testing.T) {
	t.Parallel()
	existingRaw := []byte(`{"gateway":{"plugin_settings":{" acme.example ":{" region ":"cn","drop":null,"nested":{"no":1}},"kept.example":{"mode":"on"}}}}`)
	existing := GatewayConfigFromSiteMeta(existingRaw)
	if existing == nil || existing.PluginSettings["acme.example"]["region"] != "cn" || len(existing.PluginSettings["acme.example"]) != 1 {
		t.Fatalf("normalized existing = %#v", existing)
	}
	if _, ok := existing.PluginSettings["kept.example"]; !ok {
		t.Fatal("kept plugin was dropped")
	}

	merged, err := MergeSiteGatewayConfig(existingRaw, &GatewayConfig{PluginSettings: map[string]map[string]any{
		"acme.example": {"region": "intl"},
		"new.example":  {"mode": "fast"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := GatewayConfigFromSiteMeta(merged)
	if got.PluginSettings["acme.example"]["region"] != "intl" || got.PluginSettings["new.example"]["mode"] != "fast" || got.PluginSettings["kept.example"]["mode"] != "on" {
		t.Fatalf("merged = %#v", got.PluginSettings)
	}

	deleted, err := MergeSiteGatewayConfig(merged, &GatewayConfig{PluginSettings: map[string]map[string]any{
		"new.example": {},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got = GatewayConfigFromSiteMeta(deleted)
	if _, ok := got.PluginSettings["new.example"]; ok {
		t.Fatalf("empty patch kept plugin: %#v", got.PluginSettings)
	}
	if got.PluginSettings["acme.example"]["region"] != "intl" {
		t.Fatalf("delete touched another plugin: %#v", got.PluginSettings)
	}

	kept, err := MergeSiteGatewayConfig(deleted, nil)
	if err != nil {
		t.Fatal(err)
	}
	var same map[string]any
	if err := json.Unmarshal(deleted, &same); err != nil {
		t.Fatal(err)
	}
	var after map[string]any
	if err := json.Unmarshal(kept, &after); err != nil {
		t.Fatal(err)
	}
	if GatewayConfigFromSiteMeta(kept).PluginSettings["acme.example"]["region"] != "intl" || len(after) != len(same) {
		t.Fatalf("nil patch changed meta: %s", kept)
	}
}
