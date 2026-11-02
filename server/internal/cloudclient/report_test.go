package cloudclient

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"xlyra/server/internal/store"
)

func TestReportKeyWhitelistAndPluginSelection(t *testing.T) {
	plugins := SelectPlugins([]store.JSPlugin{
		{ID: "acme.demo", Source: store.JSPluginSourceMarket},
		{ID: "local.tool", Source: store.JSPluginSourceUploaded},
		{ID: "core.builtin", Source: "builtin"},
		{ID: "empty.skip", Source: store.JSPluginSourceUploaded},
	}, []store.JSPluginVersion{
		{PluginID: "acme.demo", Version: "2.0.0", Status: store.JSPluginStatusDisabled, Origin: store.JSPluginOriginMarket},
		{PluginID: "acme.demo", Version: "1.0.0", Status: store.JSPluginStatusEnabled, Origin: store.JSPluginOriginMarket},
		{PluginID: "local.tool", Version: "0.1.0", Status: store.JSPluginStatusBroken, Origin: store.JSPluginOriginManual},
		{PluginID: "local.tool", Version: "0.9.0", Status: "uploaded", Origin: ""},
		{PluginID: "core.builtin", Version: "3.0.0", Status: store.JSPluginStatusEnabled, Origin: store.JSPluginOriginManual},
	})
	if len(plugins) != 3 {
		t.Fatalf("plugins = %#v", plugins)
	}
	if plugins[0].ID != "acme.demo" || plugins[0].Version != "1.0.0" || plugins[0].Status != "enabled" || !plugins[0].Enabled || plugins[0].Origin != "market" {
		t.Fatalf("market plugin = %#v", plugins[0])
	}
	if plugins[1].Version != "0.9.0" || plugins[1].Status != "disabled" || plugins[1].Enabled || plugins[1].Origin != "manual" {
		t.Fatalf("manual plugin = %#v", plugins[1])
	}
	if plugins[2].Origin != "builtin" || plugins[2].Status != "enabled" {
		t.Fatalf("builtin plugin = %#v", plugins[2])
	}
	broken, enabled := MapPluginStatus(store.JSPluginStatusBroken)
	if broken != "broken" || enabled {
		t.Fatalf("broken map = %s %v", broken, enabled)
	}

	report := BuildReport("1.15.1", Platform{OS: "linux", Arch: "amd64", Runtime: "go1.26.0"}, plugins, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	assertExactKeys(t, raw, "xlyraVersion", "platform", "plugins", "counts", "sentAt")
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	assertExactKeys(t, decoded["platform"], "os", "arch", "runtime")
	assertExactKeys(t, decoded["counts"], "plugins", "enabledPlugins")
	var items []json.RawMessage
	if err := json.Unmarshal(decoded["plugins"], &items); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		assertExactKeys(t, item, "id", "version", "status", "origin", "enabled")
	}
	text := string(raw)
	for _, forbidden := range []string{"apiKey", "api_key", "secret", "password", "baseUrl", "siteUrl", "token", "config", "users", "pollToken", "privateKey"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("report contains %s: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, `"sentAt":"2026-01-02T03:04:05Z"`) || !strings.Contains(text, `"enabledPlugins":2`) {
		t.Fatalf("report = %s", text)
	}
}

func assertExactKeys(t *testing.T, raw json.RawMessage, want ...string) {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	if len(object) != len(want) {
		t.Fatalf("keys = %v, want %v", keysOf(object), want)
	}
	for _, key := range want {
		if _, ok := object[key]; !ok {
			t.Fatalf("missing %s in %v", key, keysOf(object))
		}
	}
}

func keysOf(object map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	return keys
}
