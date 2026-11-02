package config

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
