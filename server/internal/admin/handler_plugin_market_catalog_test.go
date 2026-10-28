package admin

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"crypto/ed25519"

	"github.com/go-chi/chi/v5"

	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/jsplugin/market"
	"xlyra/server/internal/marketproto"
	"xlyra/server/internal/store"
)

func TestListPluginMarketEmptyCache(t *testing.T) {
	h := marketCatalogHandler(t, market.CatalogFixture{Enabled: false, RootKeysConfigured: false})
	rec := adminPerform(h.ListPluginMarket, adminTestRequest(http.MethodGet, "/plugin-market", ""))
	adminAssertStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"plugins":[]`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	body := adminDecodeJSON[market.Catalog](t, rec)
	if body.Plugins == nil || len(body.Plugins) != 0 || body.Snapshot.Enabled || body.Snapshot.RootKeysConfigured {
		t.Fatalf("catalog = %+v", body)
	}
}

func TestGetPluginMarketNotFound(t *testing.T) {
	h := marketCatalogHandler(t, market.CatalogFixture{
		XLyraVersion: "1.15.1",
		Snapshot:     SnapshotWithPlugin("acme.demo"),
	})
	req := adminRequestWithRouteVars(adminTestRequest(http.MethodGet, "/plugin-market/missing", ""), map[string]string{"id": "missing"})
	rec := adminPerform(h.GetPluginMarketPlugin, req)
	assertAdminErrorCode(t, rec, http.StatusNotFound, "js_plugin_market_not_found")

	req = adminRequestWithRouteVars(adminTestRequest(http.MethodGet, "/plugin-market/acme.demo", ""), map[string]string{"id": "acme.demo"})
	rec = adminPerform(h.GetPluginMarketPlugin, req)
	adminAssertStatus(t, rec, http.StatusOK)
	item := adminDecodeJSON[market.PluginView](t, rec)
	if item.ID != "acme.demo" || !item.HasCompatibleVersion || len(item.Versions) != 1 || item.Versions[0].Changelog != "<b>x</b>" {
		t.Fatalf("item = %+v", item)
	}
}

func TestPluginMarketUnavailable(t *testing.T) {
	h := Handler{}
	rec := adminPerform(h.ListPluginMarket, adminTestRequest(http.MethodGet, "/plugin-market", ""))
	assertAdminErrorCode(t, rec, http.StatusServiceUnavailable, "plugin_market_unavailable")
	rec = adminPerform(h.GetPluginMarketPlugin, adminTestRequest(http.MethodGet, "/plugin-market/acme", ""))
	assertAdminErrorCode(t, rec, http.StatusServiceUnavailable, "plugin_market_unavailable")
}

func TestPluginMarketRoutesKeepStatusAheadOfID(t *testing.T) {
	h := marketCatalogHandler(t, market.CatalogFixture{
		Enabled:            true,
		RootKeysConfigured: true,
		SourceURL:          "https://market.example/v1/",
		XLyraVersion:       "1.15.1",
		Snapshot:           SnapshotWithPlugin("status"),
	})
	r := chi.NewRouter()
	RegisterPluginMarket(r, h)

	status := adminPerform(r.ServeHTTP, adminTestRequest(http.MethodGet, "/plugin-market/status", ""))
	adminAssertStatus(t, status, http.StatusOK)
	if strings.Contains(status.Body.String(), `"name"`) || !strings.Contains(status.Body.String(), `"root_keys_configured":true`) {
		t.Fatalf("status was handled as a plugin id: %s", status.Body.String())
	}
	rec := adminPerform(r.ServeHTTP, adminTestRequest(http.MethodGet, "/plugin-market/missing", ""))
	body := adminDecodeJSON[struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}](t, rec)
	if rec.Code != http.StatusNotFound || body.Error.Code != "js_plugin_market_not_found" {
		t.Fatalf("unknown id = %d %+v", rec.Code, body.Error)
	}
	list := adminPerform(r.ServeHTTP, adminTestRequest(http.MethodGet, "/plugin-market", ""))
	adminAssertStatus(t, list, http.StatusOK)
	if !strings.Contains(list.Body.String(), `"id":"status"`) {
		t.Fatalf("list = %s", list.Body.String())
	}
	icon := adminPerform(r.ServeHTTP, adminTestRequest(http.MethodGet, "/plugin-market/icons/not-a-sha", ""))
	iconBody := adminDecodeJSON[struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}](t, icon)
	if icon.Code != http.StatusNotFound || iconBody.Error.Code != "js_plugin_market_not_found" {
		t.Fatalf("bad icon hash = %d %+v body %s", icon.Code, iconBody.Error, icon.Body.String())
	}
}

func marketCatalogHandler(t *testing.T, fixture market.CatalogFixture) Handler {
	t.Helper()
	svc, err := market.NewService(store.JSPluginRepository{}, nil, nil, []ed25519.PublicKey{}, []string{})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetCatalogFixture(&fixture)
	return Handler{}.WithPluginMarket(svc)
}

func SnapshotWithPlugin(id string) market.Snapshot {
	return market.Snapshot{Index: &marketproto.Index{Plugins: []marketproto.Plugin{{
		ID: id, Name: "Demo", Summary: "one", Kind: "quota_probe",
		Developer: marketproto.Developer{Namespace: "acme", DisplayName: "Acme"},
		Versions: []marketproto.PluginVersion{{
			Version: "1.0.0", PublishedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
			Changelog: "<b>x</b>", Size: 4,
			Compat:      marketproto.Compat{APIVersion: jsplugin.HookAPIVersion, HostAPI: jsplugin.HostAPIVersion},
			Hosts:       []string{"api.example.com"},
			Permissions: []string{"notify"},
		}},
	}}}}
}
