package config

import (
	"reflect"
	"testing"
)

func TestDefaultPluginMarketConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultPluginMarketConfig()
	if !cfg.Enabled || !cfg.AutoUpdate {
		t.Fatalf("market and auto update should default to enabled: %#v", cfg)
	}
	if cfg.SourceURL != "" || cfg.ProxyID != "" || cfg.IndexSyncHours != 6 {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
	if !reflect.DeepEqual(PluginMarketConfigFromRaw(PluginMarketConfigToMap(cfg)), cfg) {
		t.Fatal("map round trip mismatch")
	}
	if !reflect.DeepEqual(ReadPluginMarketConfig(nil), cfg) {
		t.Fatal("nil config file should return defaults")
	}

	partial := PluginMarketConfigFromRaw(map[string]any{"auto_update": false})
	if !partial.Enabled || partial.AutoUpdate || partial.IndexSyncHours != 6 {
		t.Fatalf("missing fields should keep defaults: %#v", partial)
	}
	normalized := PluginMarketConfigFromRaw(map[string]any{
		"enabled":          false,
		"source_url":       "  https://registry.example.com/v1/  ",
		"proxy_id":         " local ",
		"index_sync_hours": float64(0),
	})
	if normalized.Enabled || normalized.SourceURL != "https://registry.example.com/v1/" || normalized.ProxyID != "local" || normalized.IndexSyncHours != 6 {
		t.Fatalf("normalize mismatch: %#v", normalized)
	}
}

func TestValidatePluginMarketConfigRejectsSourceURL(t *testing.T) {
	t.Parallel()

	valid := DefaultPluginMarketConfig()
	if err := ValidatePluginMarketConfig(valid); err != nil {
		t.Fatalf("empty source_url should be valid: %v", err)
	}
	valid.SourceURL = "https://registry.example.com/v1/"
	if err := ValidatePluginMarketConfig(valid); err != nil {
		t.Fatalf("https source_url ending with / should be valid: %v", err)
	}

	for _, sourceURL := range []string{
		"http://example.com/v1/",
		"https://example.com/v1",
		"https://example.com",
	} {
		got := PluginMarketConfigFromRaw(map[string]any{"source_url": sourceURL})
		if err := ValidatePluginMarketConfig(got); err == nil {
			t.Fatalf("source_url %q should be rejected", sourceURL)
		}
	}
}

func TestValidatePluginMarketConfigRejectsIndexSyncHours(t *testing.T) {
	t.Parallel()

	for _, hours := range []int{0, -1, 25} {
		cfg := DefaultPluginMarketConfig()
		cfg.IndexSyncHours = hours
		if err := ValidatePluginMarketConfig(cfg); err == nil {
			t.Fatalf("index_sync_hours %d should be rejected", hours)
		}
	}
	for _, hours := range []int{1, 24} {
		cfg := DefaultPluginMarketConfig()
		cfg.IndexSyncHours = hours
		if err := ValidatePluginMarketConfig(cfg); err != nil {
			t.Fatalf("index_sync_hours %d: %v", hours, err)
		}
	}

	got := PluginMarketConfigFromRaw(map[string]any{"index_sync_hours": float64(25)})
	if err := ValidatePluginMarketConfig(got); err == nil {
		t.Fatal("index_sync_hours 25 from raw should be rejected")
	}
}
