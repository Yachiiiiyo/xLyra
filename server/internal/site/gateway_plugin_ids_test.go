package site

import (
	"strings"
	"testing"
)

func TestSitePluginIDsMergesLegacyFieldsInOrder(t *testing.T) {
	t.Parallel()
	probe := "plugin:from-probe"
	cfg := &GatewayConfig{
		PluginIDs:  []string{" new-a ", "new-b", "new-a", "bad id", ""},
		QuotaProbe: &probe,
		Plugins: map[string]string{
			"pricing_parse":    "from-price",
			"model_list":       "from-models",
			"error_classifier": "new-b",
			"credential_check": "from-check",
			"bogus":            "nope",
		},
	}
	got := SitePluginIDs(cfg)
	want := []string{"new-a", "new-b", "from-probe", "from-models", "from-check", "from-price"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	if SitePluginIDs(nil) != nil {
		t.Fatal("nil config should return nil")
	}
}

func TestMergePluginIDsClearsLegacyBindings(t *testing.T) {
	t.Parallel()
	probe := "plugin:old-probe"
	raw, err := MergeSiteGatewayConfig(nil, &GatewayConfig{
		QuotaProbe: &probe,
		Plugins:    map[string]string{"model_list": "old-models", "credential_check": "old-check"},
	})
	if err != nil {
		t.Fatal(err)
	}
	merged, err := MergeSiteGatewayConfig(raw, &GatewayConfig{PluginIDs: []string{" mounted ", "mounted", ""}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := GatewayConfigFromSiteMeta(merged)
	if cfg == nil || len(cfg.Plugins) != 0 || cfg.QuotaProbe != nil || strings.Join(cfg.PluginIDs, ",") != "mounted" {
		t.Fatalf("after plugin_ids write: %#v", cfg)
	}

	builtin := QuotaProbeTypeSub2API
	kept, err := MergeSiteGatewayConfig(nil, &GatewayConfig{QuotaProbe: &builtin, Plugins: map[string]string{"model_list": "old"}})
	if err != nil {
		t.Fatal(err)
	}
	kept, err = MergeSiteGatewayConfig(kept, &GatewayConfig{PluginIDs: []string{"mounted"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg = GatewayConfigFromSiteMeta(kept)
	if cfg.QuotaProbe == nil || *cfg.QuotaProbe != QuotaProbeTypeSub2API || len(cfg.Plugins) != 0 || strings.Join(SitePluginIDs(cfg), ",") != "mounted" {
		t.Fatalf("builtin probe was not kept: %#v plugins=%v", cfg.QuotaProbe, cfg.Plugins)
	}

	cleared, err := MergeSiteGatewayConfig(kept, &GatewayConfig{PluginIDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	cfg = GatewayConfigFromSiteMeta(cleared)
	if ids := SitePluginIDs(cfg); len(ids) != 0 {
		t.Fatalf("empty plugin_ids did not clear the list: %v", ids)
	}
	if cfg.QuotaProbe == nil || *cfg.QuotaProbe != QuotaProbeTypeSub2API {
		t.Fatalf("clearing plugin_ids dropped the builtin probe: %#v", cfg.QuotaProbe)
	}
}
