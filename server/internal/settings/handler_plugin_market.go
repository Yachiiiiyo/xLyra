package settings

import (
	"net/http"

	"xlyra/server/internal/config"
	"xlyra/server/internal/httpclient"
	"xlyra/server/internal/httpx"
)

func (h Handler) GetPluginMarket(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, config.ReadPluginMarketConfig(h.confFile))
}

func (h Handler) UpdatePluginMarket(w http.ResponseWriter, r *http.Request) {
	if h.confFile == nil {
		httpx.Error(w, r, http.StatusServiceUnavailable, "config_unavailable", "config persistence is not available")
		return
	}
	var req map[string]any
	if err := httpx.DecodeJSONBody(r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}

	next := config.PluginMarketConfigFromRaw(req)
	if err := config.ValidatePluginMarketConfig(next); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_plugin_market_config", err.Error())
		return
	}
	if next.ProxyID != "" && !pluginMarketProxyConfigured(h.confFile, next.ProxyID) {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_plugin_market_config", "proxy_id is not a configured proxy")
		return
	}

	if err := h.confFile.Set(config.PluginMarketConfigPath, config.PluginMarketConfigToMap(next)); err != nil {
		h.logError("update plugin market config", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "config_write_error", "failed to save config")
		return
	}

	h.logInfo("plugin market config updated", "enabled", next.Enabled, "auto_update", next.AutoUpdate, "index_sync_hours", next.IndexSyncHours)
	httpx.JSON(w, http.StatusOK, next)
}

func pluginMarketProxyConfigured(confFile *config.ConfigFile, id string) bool {
	for _, item := range httpclient.ReadNetworkConfig(confFile).Proxies {
		if item.ID == id {
			return true
		}
	}
	return false
}
