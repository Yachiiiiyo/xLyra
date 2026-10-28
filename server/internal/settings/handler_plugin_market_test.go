package settings

import (
	"net/http"
	"strings"
	"testing"

	"xlyra/server/internal/config"
)

func TestGetPluginMarket_Defaults(t *testing.T) {
	h, _ := settingsHandlerWithConfig(t)
	rec := settingsPerform(h.GetPluginMarket, settingsTestRequest(http.MethodGet, "/settings/plugin-market", ""))

	settingsAssertStatus(t, rec, http.StatusOK)
	cfg := settingsDecodeJSON[config.PluginMarketConfig](t, rec)
	if cfg != config.DefaultPluginMarketConfig() {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
}

func TestUpdatePluginMarket_PersistsConfig(t *testing.T) {
	h, _ := settingsHandlerWithConfig(t)
	proxy := settingsRawJSONRequest(http.MethodPut, "/settings/system-proxy", `{"proxies":[{"id":"local","name":"Local","url":"http://127.0.0.1:7890","type":"http"}]}`)
	settingsAssertStatus(t, settingsPerform(h.UpdateSystemProxy, proxy), http.StatusOK)

	body := `{
		"enabled": false,
		"source_url": "https://registry.example.com/v1/",
		"proxy_id": "local",
		"auto_update": false,
		"index_sync_hours": 12
	}`
	rec := settingsPerform(h.UpdatePluginMarket, settingsRawJSONRequest(http.MethodPut, "/settings/plugin-market", body))
	settingsAssertStatus(t, rec, http.StatusOK)
	cfg := settingsDecodeJSON[config.PluginMarketConfig](t, rec)
	if cfg.Enabled || cfg.AutoUpdate || cfg.SourceURL != "https://registry.example.com/v1/" || cfg.ProxyID != "local" || cfg.IndexSyncHours != 12 {
		t.Fatalf("unexpected saved config: %#v", cfg)
	}

	rec = settingsPerform(h.GetPluginMarket, settingsTestRequest(http.MethodGet, "/settings/plugin-market", ""))
	settingsAssertStatus(t, rec, http.StatusOK)
	persisted := settingsDecodeJSON[config.PluginMarketConfig](t, rec)
	if persisted != cfg {
		t.Fatalf("plugin market config was not persisted: %#v", persisted)
	}
}

func TestUpdatePluginMarket_RejectsUnknownProxy(t *testing.T) {
	h, _ := settingsHandlerWithConfig(t)
	rec := settingsPerform(h.UpdatePluginMarket, settingsRawJSONRequest(http.MethodPut, "/settings/plugin-market", `{"proxy_id":"missing"}`))
	settingsAssertStatus(t, rec, http.StatusBadRequest)
	body := settingsDecodeJSON[struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}](t, rec)
	if body.Error.Code != "invalid_plugin_market_config" || !strings.Contains(body.Error.Message, "proxy_id") {
		t.Fatalf("unexpected error: %#v", body.Error)
	}

	rec = settingsPerform(h.GetPluginMarket, settingsTestRequest(http.MethodGet, "/settings/plugin-market", ""))
	settingsAssertStatus(t, rec, http.StatusOK)
	cfg := settingsDecodeJSON[config.PluginMarketConfig](t, rec)
	if cfg != config.DefaultPluginMarketConfig() {
		t.Fatalf("rejected update should not persist: %#v", cfg)
	}
}
