package config

import "testing"

func TestDefaultPluginMarketConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultPluginMarketConfig()
	if !cfg.Enabled || !cfg.AutoUpdate {
		t.Fatalf("market and auto update should default to enabled: %#v", cfg)
	}
	if cfg.SourceURL != "" || cfg.ProxyID != "" || cfg.IndexSyncHours != 6 {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
}
