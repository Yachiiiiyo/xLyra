package site

import (
	"context"
	"encoding/json"
	"testing"

	"xlyra/server/internal/adapter"
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/store"
)

func kindlessHookPlugin(t *testing.T, id, body string) *jsplugin.Plugin {
	t.Helper()
	source := `export const meta = { apiVersion: 1, id: "` + id + `" };` + "\n" + body
	manifest := jsplugin.Manifest{
		ID: id, Name: id, Version: "1.0.0", APIVersion: 1, HostAPI: 1,
		Site:   jsplugin.SiteSection{PricingPath: "/api/pricing"},
		SHA256: map[string]string{"plugin.js": jsplugin.HashSource(source)},
	}
	plugin, err := jsplugin.NewPlugin(manifest, source, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	return plugin
}

func siteWithPluginIDs(ids []string) store.Site {
	meta, _ := json.Marshal(map[string]any{"gateway": map[string]any{"plugin_ids": ids}})
	return store.Site{SiteType: "custom", BaseURL: "https://api.example.com", Meta: meta}
}

func TestSiteHookPluginUsesFirstExport(t *testing.T) {
	first := kindlessHookPlugin(t, "acme.first", `export function listModels() { return { result: { models: [{ name: "from-first" }] } }; }`)
	second := kindlessHookPlugin(t, "acme.second", `export function listModels() { return { result: { models: [{ name: "from-second" }] } }; }`)
	useCatalog(t, first, second)
	s := &Service{}
	lister, ok := s.modelListerFor(siteWithPluginIDs([]string{"acme.second", "acme.first"}), nil)
	if !ok {
		t.Fatal("mounted listModels was not used")
	}
	models, err := lister.ListModels(context.Background(), adapter.SiteConfig{BaseURL: "https://api.example.com", Client: nil}, "k")
	if err != nil {
		t.Fatal(err)
	}
	// listModels 现在按优先级合并：靠前的插件覆盖同名模型，其余插件的独有模型保留。
	if len(models) != 2 || models[0].UpstreamName != "from-second" || models[1].UpstreamName != "from-first" {
		t.Fatalf("models = %+v, want the first mounted exporter ahead of the second", models)
	}
}

func TestSiteHookPluginSkipsSiteTypeBase(t *testing.T) {
	base := siteTypeBasePlugin(t, "acme.base")
	mounted := kindlessHookPlugin(t, "acme.mounted", `export function listModels() { return { result: { models: [{ name: "mounted" }] } }; }`)
	useCatalog(t, base, mounted)
	s := &Service{}
	if plugin, ok := s.siteHookPlugin(siteWithPluginIDs([]string{"acme.base"}), "listModels"); ok {
		t.Fatalf("site type base was selected: %s", plugin.Manifest.ID)
	}
	plugin, ok := s.siteHookPlugin(siteWithPluginIDs([]string{"acme.base", "acme.mounted"}), "listModels")
	if !ok || plugin.Manifest.ID != "acme.mounted" {
		t.Fatalf("mounted plugin after the base = %v", ok)
	}
}

func TestSiteHookPluginReadsLegacyKindBinding(t *testing.T) {
	plugin := kindsTestPlugin(t, jsplugin.KindModelList, "acme-models", header("acme-models", jsplugin.KindModelList)+modelListSource)
	useCatalog(t, plugin)
	got, ok := (&Service{}).siteHookPlugin(siteWithPlugins(map[string]string{"model_list": "acme-models"}), "listModels")
	if !ok || got.Manifest.ID != "acme-models" {
		t.Fatal("legacy plugins.model_list was not used")
	}
}

func TestQuotaProbeUsesMountedKindlessProbe(t *testing.T) {
	plugin := kindlessHookPlugin(t, "acme.probe", `export function probe() { return { error: "later" }; }`)
	useCatalog(t, plugin)
	item := siteWithPluginIDs([]string{"acme.probe"})
	item.SiteType = "kimi"
	if got := quotaProbeTypeForSite(&Service{}, item); got != QuotaProbePluginPrefix+"acme.probe" {
		t.Fatalf("probe type = %q", got)
	}
	builtin := QuotaProbeTypeSub2API
	meta, _ := json.Marshal(map[string]any{"gateway": map[string]any{"plugin_ids": []string{"acme.probe"}, "quota_probe": builtin}})
	item.Meta = meta
	if got := quotaProbeTypeForSite(&Service{}, item); got != QuotaProbeTypeSub2API {
		t.Fatalf("builtin probe = %q", got)
	}
}

func TestRegistryWithHookSkipsSiteTypes(t *testing.T) {
	base := siteTypeBasePlugin(t, "acme.base")
	detect := kindlessHookPlugin(t, "acme.detect", `export function detect() { return { result: { matched: false } }; }`)
	registry := jsplugin.NewRegistry(base, detect)
	got := registry.WithHook("detect")
	if len(got) != 1 || got[0].Manifest.ID != "acme.detect" {
		t.Fatalf("WithHook = %v", pluginIDsOf(got))
	}
	if len(registry.WithHook("listModels")) != 0 {
		t.Fatal("site type listModels was treated as global")
	}
}

func siteTypeBasePlugin(t *testing.T, id string) *jsplugin.Plugin {
	t.Helper()
	source := `export const meta = { apiVersion: 1, id: "` + id + `", kind: "site_provider" };
export function listModels() { return { result: { models: [{ name: "base" }] } }; }
export function detect() { return { result: { matched: true, siteType: "newapi" } }; }
`
	manifest := jsplugin.Manifest{
		ID: id, Name: id, Version: "1.0.0", APIVersion: 1, HostAPI: 1, Kind: jsplugin.KindSiteProvider,
		SiteProvider: jsplugin.SiteProviderSection{
			SiteType:   id + "/chat",
			Display:    jsplugin.SiteProviderDisplay{Name: jsplugin.LocalizedText{"en": "Example"}},
			Hosts:      []string{"api.example.com"},
			Credential: jsplugin.SiteProviderCredential{Model: "api_key"},
			Protocol: jsplugin.SiteProviderProtocol{
				Kind: "openai_chat", Path: "/v1/chat/completions", OfficialBaseURL: "https://api.example.com/v1",
				DefaultEndpointTypes: []string{"openai"},
			},
		},
		SHA256: map[string]string{"plugin.js": jsplugin.HashSource(source)},
	}
	plugin, err := jsplugin.NewPlugin(manifest, source, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	return plugin
}

func pluginIDsOf(plugins []*jsplugin.Plugin) []string {
	out := make([]string, len(plugins))
	for i, plugin := range plugins {
		out[i] = plugin.Manifest.ID
	}
	return out
}
