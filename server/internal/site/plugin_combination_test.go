package site

import (
	"context"
	"errors"
	"testing"

	"xlyra/server/internal/adapter"
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/store"
	"xlyra/server/internal/upstream"
)

func resultHook(t *testing.T, id, body string) *jsplugin.Plugin {
	t.Helper()
	return kindlessHookPlugin(t, id, body)
}

func TestListModelsMergesByPriority(t *testing.T) {
	high := resultHook(t, "acme.high", `export function listModels() { return { result: { models: [
		{ name: "shared", displayName: "from-high", capabilities: { vision: true } },
		{ name: "only-high" }
	] } }; }`)
	low := resultHook(t, "acme.low", `export function listModels() { return { result: { models: [
		{ name: "shared", displayName: "from-low" },
		{ name: "only-low" }
	] } }; }`)
	useCatalog(t, high, low)
	s := &Service{}
	lister, ok := s.modelListerFor(siteWithPluginIDs([]string{"acme.high", "acme.low"}), nil)
	if !ok {
		t.Fatal("lister missing")
	}
	if _, single := lister.(pluginModelLister); single {
		t.Fatal("two sources were not merged")
	}
	models, err := lister.ListModels(context.Background(), adapter.SiteConfig{BaseURL: "https://api.example.com"}, "k")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 || models[0].UpstreamName != "shared" || models[0].DisplayName != "from-high" || models[0].Capabilities["vision"] != true || models[1].UpstreamName != "only-high" || models[2].UpstreamName != "only-low" {
		t.Fatalf("models = %+v", models)
	}
}

func TestListModelsKeepsOtherSourcesWhenOneFails(t *testing.T) {
	broken := resultHook(t, "acme.broken", `export function listModels() { throw new Error("boom"); }`)
	fine := resultHook(t, "acme.fine", `export function listModels() { return { result: { models: [{ name: "kept" }] } }; }`)
	useCatalog(t, broken, fine)
	lister, ok := (&Service{}).modelListerFor(siteWithPluginIDs([]string{"acme.broken", "acme.fine"}), nil)
	if !ok {
		t.Fatal("lister missing")
	}
	models, err := lister.ListModels(context.Background(), adapter.SiteConfig{BaseURL: "https://api.example.com"}, "k")
	if err != nil || len(models) != 1 || models[0].UpstreamName != "kept" {
		t.Fatalf("models = %+v err = %v", models, err)
	}
}

func TestListModelsFallsBackToSiteTypeThenAdapter(t *testing.T) {
	base := siteTypeBasePlugin(t, "acme.base")
	useCatalog(t, base)
	installProviderSite(t, base, true, []string{"api.example.com"})
	var calls int
	module := countingModule{siteType: "acme.base/chat", calls: &calls}
	s := &Service{}
	item := store.Site{SiteType: "acme.base/chat"}
	lister, ok := s.modelListerFor(item, module)
	if !ok {
		t.Fatal("base lister missing")
	}
	models, err := lister.ListModels(context.Background(), adapter.SiteConfig{BaseURL: "https://api.example.com"}, "k")
	if err != nil || len(models) != 1 || models[0].UpstreamName != "base" || calls != 0 {
		t.Fatalf("models = %+v err = %v calls = %d", models, err, calls)
	}

	mounted := resultHook(t, "acme.mounted", `export function listModels() { return { result: { models: [{ name: "base", displayName: "mounted" }, { name: "extra" }] } }; }`)
	useCatalog(t, base, mounted)
	item = siteWithPluginIDs([]string{"acme.mounted"})
	item.SiteType = "acme.base/chat"
	lister, ok = s.modelListerFor(item, module)
	if !ok {
		t.Fatal("merged lister missing")
	}
	models, err = lister.ListModels(context.Background(), adapter.SiteConfig{BaseURL: "https://api.example.com"}, "k")
	if err != nil || len(models) != 3 || models[0].DisplayName != "mounted" || models[1].UpstreamName != "extra" || models[2].UpstreamName != "generic-model" || calls != 1 {
		t.Fatalf("merged = %+v err = %v calls = %d", models, err, calls)
	}
}

func checkHook(t *testing.T, id, status string) *jsplugin.Plugin {
	t.Helper()
	return resultHook(t, id, `export function check() { return { result: { status: "`+status+`", message: "`+id+`" } }; }`)
}

func TestCheckRequiresEveryPlugin(t *testing.T) {
	okPlugin := checkHook(t, "acme.ok", "ok")
	bad := checkHook(t, "acme.bad", "invalid")
	useCatalog(t, okPlugin, bad)
	s := &Service{}
	cfg := adapter.SiteConfig{BaseURL: "https://api.example.com", Client: nil}

	validator, found := s.credentialValidatorFor(siteWithPluginIDs([]string{"acme.bad", "acme.ok"}), nil)
	if !found {
		t.Fatal("validator missing")
	}
	err := validator.ValidateCredentials(context.Background(), cfg, "k")
	if err == nil || !upstream.ClassifyError(err).CredentialInvalid() {
		t.Fatal(err)
	}
	if !checkMessage(err, "acme.bad") {
		t.Fatalf("invalid message = %v", err)
	}

	flaky := resultHook(t, "acme.flaky", `export function check() { throw new Error("timeout"); }`)
	useCatalog(t, flaky, okPlugin)
	validator, found = s.credentialValidatorFor(siteWithPluginIDs([]string{"acme.flaky", "acme.ok"}), nil)
	if !found {
		t.Fatal("flaky validator missing")
	}
	err = validator.ValidateCredentials(context.Background(), cfg, "k")
	if err == nil || upstream.ClassifyError(err).CredentialInvalid() {
		t.Fatalf("a thrown check was treated as invalid: %v", err)
	}

	useCatalog(t, okPlugin, checkHook(t, "acme.also", "ok"))
	validator, found = s.credentialValidatorFor(siteWithPluginIDs([]string{"acme.ok", "acme.also"}), nil)
	if !found {
		t.Fatal("passing validator missing")
	}
	if err := validator.ValidateCredentials(context.Background(), cfg, "k"); err != nil {
		t.Fatal(err)
	}
}

func checkMessage(err error, want string) bool {
	var httpErr *upstream.HTTPError
	return errors.As(err, &httpErr) && httpErr.Failure.Message == want
}

func TestMergePricingSnapshotsOverridesByPriority(t *testing.T) {
	highRatio, lowRatio := 3.0, 1.0
	high := pricingSnapshotFromPlugin(jsplugin.PricingParseResult{
		Groups: []jsplugin.PricingGroupEntry{{Name: "default", DisplayName: "High", Ratio: &highRatio}},
		Items: []jsplugin.PricingItemEntry{
			{Model: "shared", Group: "default", Currency: "USD", ModelRatio: &highRatio},
			{Model: "only-high", Currency: "USD"},
		},
	}, "raw-high")
	low := pricingSnapshotFromPlugin(jsplugin.PricingParseResult{
		Groups: []jsplugin.PricingGroupEntry{{Name: "default", DisplayName: "Low", Ratio: &lowRatio}, {Name: "extra"}},
		Items: []jsplugin.PricingItemEntry{
			{Model: "shared", Group: "default", Currency: "CNY", ModelRatio: &lowRatio},
			{Model: "only-low", Group: "extra"},
		},
	}, "raw-low")
	merged := mergePricingSnapshots([]adapter.PricingSnapshot{high, low})
	if merged.Raw != "raw-high" {
		t.Fatalf("raw = %#v", merged.Raw)
	}
	if len(merged.Groups) != 2 || merged.Groups[0].DisplayName != "High" || merged.Groups[1].GroupName != "extra" {
		t.Fatalf("groups = %+v", merged.Groups)
	}
	if len(merged.Items) != 3 || merged.Items[0].ModelName != "shared" || merged.Items[0].Currency != "USD" || merged.Items[0].ModelRatio != 3 || merged.Items[1].ModelName != "only-high" || merged.Items[2].ModelName != "only-low" {
		t.Fatalf("items = %+v", merged.Items)
	}
}

func TestDescribeModelsPrefersMountedPlugins(t *testing.T) {
	mounted := kindsTestPlugin(t, jsplugin.KindModelMetadata, "acme.mounted", header("acme.mounted", jsplugin.KindModelMetadata)+`
export function describeModels(ctx, input) {
  return { models: input.models.map((id) => ({ id, name: "Mounted", capabilities: { vision: true } })) };
}`)
	other := kindsTestPlugin(t, jsplugin.KindModelMetadata, "acme.other", header("acme.other", jsplugin.KindModelMetadata)+`
export function describeModels(ctx, input) {
  return { models: input.models.map((id) => ({ id, name: "Other", capabilities: { vision: false, reasoning: true } })) };
}`)
	useCatalog(t, other, mounted)
	s := &Service{}
	model := s.applyModelMetadataPlugins(context.Background(), siteWithPluginIDs([]string{"acme.mounted"}), adapter.Model{UpstreamName: "m"})
	if model.DisplayName != "Mounted" || model.Capabilities["vision"] != true || model.Capabilities["reasoning"] != true {
		t.Fatalf("model = %+v", model)
	}
	order := describeModelsPlugins(s.activeJSRegistry(), siteWithPluginIDs([]string{"acme.mounted"}))
	if len(order) != 2 || order[0].Manifest.ID != "acme.mounted" || order[1].Manifest.ID != "acme.other" {
		t.Fatalf("order = %v", pluginIDsOf(order))
	}
}

func TestProbeSourcesCombineMountedPlugins(t *testing.T) {
	first := resultHook(t, "acme.first", `export function probe() { return { result: { kind: "balance", entries: [{ label: "first", remaining: 1 }] } }; }`)
	second := resultHook(t, "acme.second", `export function probe() { return { result: { kind: "balance", entries: [{ label: "second", remaining: 2 }] } }; }`)
	useCatalog(t, first, second)
	s := &Service{}
	item := siteWithPluginIDs([]string{"acme.first", "acme.second"})
	sources := probeSourcesForSite(s, item)
	if len(sources) != 2 || sources[0] != QuotaProbePluginPrefix+"acme.first" {
		t.Fatalf("sources = %v", sources)
	}
	result := s.probeQuotaSources(context.Background(), nil, sources, item.SiteType, "api_key", "https://api.example.com", "k")
	if result.Status != "ok" || len(result.Entries) != 2 || result.Entries[0].Label != "first" || result.Entries[1].Label != "second" {
		t.Fatalf("result = %+v", result)
	}
	if single := probeSourcesForSite(s, siteWithPluginIDs([]string{"acme.first"})); single != nil {
		t.Fatalf("one source changed the path: %v", single)
	}
}

func TestProbeSourceFailureDoesNotDropOthers(t *testing.T) {
	broken := resultHook(t, "acme.broken", `export function probe() { return { error: "nope" }; }`)
	fine := resultHook(t, "acme.fine", `export function probe() { return { result: { kind: "balance", entries: [{ label: "fine", remaining: 3 }] } }; }`)
	useCatalog(t, broken, fine)
	s := &Service{}
	sources := []string{QuotaProbePluginPrefix + "acme.broken", QuotaProbePluginPrefix + "acme.fine"}
	result := s.probeQuotaSources(context.Background(), nil, sources, "custom", "api_key", "https://api.example.com", "k")
	if result.Status != "ok" || len(result.Entries) != 2 || result.Entries[0].Label != "acme.broken" || result.Entries[0].Name != "nope" || result.Entries[1].Label != "fine" {
		t.Fatalf("result = %+v", result)
	}
}
