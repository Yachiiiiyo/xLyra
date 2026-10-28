package config

import (
	"fmt"
	"strings"
)

const PluginMarketConfigPath = "global.plugin_market"

type PluginMarketConfig struct {
	Enabled        bool   `json:"enabled"`
	SourceURL      string `json:"source_url"`
	ProxyID        string `json:"proxy_id"`
	AutoUpdate     bool   `json:"auto_update"`
	IndexSyncHours int    `json:"index_sync_hours"`
}

func DefaultPluginMarketConfig() PluginMarketConfig {
	return PluginMarketConfig{
		Enabled:        true,
		SourceURL:      "",
		ProxyID:        "",
		AutoUpdate:     true,
		IndexSyncHours: 6,
	}
}

func ReadPluginMarketConfig(confFile *ConfigFile) PluginMarketConfig {
	if confFile == nil {
		return DefaultPluginMarketConfig()
	}
	raw, ok := confFile.Get(PluginMarketConfigPath)
	if !ok || raw == nil {
		return DefaultPluginMarketConfig()
	}
	return PluginMarketConfigFromRaw(raw)
}

func PluginMarketConfigFromRaw(raw any) PluginMarketConfig {
	cfg := DefaultPluginMarketConfig()
	root, ok := raw.(map[string]any)
	if !ok {
		return cfg
	}
	cfg.Enabled = boolFromMap(root, "enabled", cfg.Enabled)
	cfg.SourceURL = stringFromMap(root, "source_url", cfg.SourceURL)
	cfg.ProxyID = stringFromMap(root, "proxy_id", cfg.ProxyID)
	cfg.AutoUpdate = boolFromMap(root, "auto_update", cfg.AutoUpdate)
	cfg.IndexSyncHours = intFromMap(root, "index_sync_hours", cfg.IndexSyncHours)
	return NormalizePluginMarketConfig(cfg)
}

func NormalizePluginMarketConfig(cfg PluginMarketConfig) PluginMarketConfig {
	cfg.SourceURL = strings.TrimSpace(cfg.SourceURL)
	cfg.ProxyID = strings.TrimSpace(cfg.ProxyID)
	if cfg.IndexSyncHours == 0 {
		cfg.IndexSyncHours = DefaultPluginMarketConfig().IndexSyncHours
	}
	return cfg
}

func ValidatePluginMarketConfig(cfg PluginMarketConfig) error {
	if cfg.SourceURL != "" && (!strings.HasPrefix(cfg.SourceURL, "https://") || !strings.HasSuffix(cfg.SourceURL, "/")) {
		return fmt.Errorf("source_url must start with https:// and end with /")
	}
	if cfg.IndexSyncHours < 1 || cfg.IndexSyncHours > 24 {
		return fmt.Errorf("index_sync_hours must be between 1 and 24")
	}
	return nil
}

func PluginMarketConfigToMap(cfg PluginMarketConfig) map[string]any {
	return map[string]any{
		"enabled":          cfg.Enabled,
		"source_url":       cfg.SourceURL,
		"proxy_id":         cfg.ProxyID,
		"auto_update":      cfg.AutoUpdate,
		"index_sync_hours": cfg.IndexSyncHours,
	}
}
