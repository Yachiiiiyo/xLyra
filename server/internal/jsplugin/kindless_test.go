package jsplugin

import (
	"context"
	"strings"
	"testing"
)

func kindlessManifest(source string, extras func(*Manifest)) Manifest {
	manifest := Manifest{
		ID: "acme.hooks", Name: "Hooks", Version: "1.0.0", APIVersion: 1, HostAPI: 1,
		SHA256: map[string]string{"plugin.js": hashSource(source)},
	}
	if extras != nil {
		extras(&manifest)
	}
	return manifest
}

func kindlessSource(body string) string {
	return `export const meta = { apiVersion: 1, id: "acme.hooks" };` + "\n" + body
}

func TestKindlessCompilesKnownHooks(t *testing.T) {
	source := kindlessSource(`
export function probe() { return { error: "later" }; }
export function classifyError() { return { class: "unknown" }; }
`)
	plugin, err := NewPlugin(kindlessManifest(source, nil), source, []Fixture{{
		Name: "one", Hook: "probe",
		Expect: fixtureExpectation{Error: "later"},
	}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if plugin.Manifest.KindLabel() != KindHooks {
		t.Fatalf("label = %q", plugin.Manifest.KindLabel())
	}
	if !plugin.HasHook("classifyError") || !plugin.HasHook("classify") {
		t.Fatal("classify alias missing")
	}
	if err := plugin.SelfTest(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestKindlessRequiresAKnownHook(t *testing.T) {
	source := kindlessSource(`export function helper() { return 1; }`)
	_, err := NewPlugin(kindlessManifest(source, nil), source, nil, 1)
	if err == nil || !strings.Contains(err.Error(), "plugin exports no known hook") {
		t.Fatalf("err = %v", err)
	}
}

func TestKindlessRejectsProtocolExport(t *testing.T) {
	source := kindlessSource(`export function decodeRequest() { return { error: { status: 400, code: "x", message: "x" } }; }`)
	_, err := NewPlugin(kindlessManifest(source, nil), source, nil, 1)
	if err == nil || !strings.Contains(err.Error(), `"kind": "protocol"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestKindlessSupportsProtocolAndAutomationSections(t *testing.T) {
	protoSource := kindlessSource(`
export function decodeRequest() { return { model: "gpt-4" }; }
export function buildRequest() { return { path: "/v1/chat", body: "{}" }; }
export function parseResponse() { return { passthrough: true }; }
`)
	protoManifest := kindlessManifest(protoSource, func(m *Manifest) {
		m.Protocol = ProtocolSection{
			Name:           "acme_proto",
			DownstreamPath: "/v1/plugins/acme",
			EndpointType:   "acme",
		}
	})
	protoPlugin, err := NewPlugin(protoManifest, protoSource, nil, 1)
	if err != nil {
		t.Fatalf("kindless protocol = %v", err)
	}
	if !protoPlugin.Manifest.DeclaresProtocol() || protoPlugin.Manifest.KindLabel() != KindProtocol || ScopeOfManifest(protoPlugin.Manifest, nil) != ScopeEndpoint {
		t.Fatalf("kindless protocol flags: declares=%v label=%q scope=%q", protoPlugin.Manifest.DeclaresProtocol(), protoPlugin.Manifest.KindLabel(), ScopeOfManifest(protoPlugin.Manifest, nil))
	}

	autoSource := kindlessSource(`export function onEvent() { return { actions: [] }; }`)
	autoManifest := kindlessManifest(autoSource, func(m *Manifest) {
		m.Automation = automationSection()
	})
	autoPlugin, err := NewPlugin(autoManifest, autoSource, nil, 1)
	if err != nil {
		t.Fatalf("kindless automation = %v", err)
	}
	if !autoPlugin.Manifest.DeclaresAutomation() || autoPlugin.Manifest.KindLabel() != KindAutomation || ScopeOfManifest(autoPlugin.Manifest, nil) != ScopeSubject {
		t.Fatalf("kindless automation flags: declares=%v label=%q scope=%q", autoPlugin.Manifest.DeclaresAutomation(), autoPlugin.Manifest.KindLabel(), ScopeOfManifest(autoPlugin.Manifest, nil))
	}
}

func TestSiteSectionAliasForSiteProvider(t *testing.T) {
	raw := []byte(`{
		"apiVersion": 1,
		"hostApi": 1,
		"id": "acme.hooks",
		"version": "1.0.0",
		"site": {
			"siteType": "acme.hooks/chat",
			"display": { "name": "Acme" },
			"hosts": ["api.acme.example"],
			"credential": { "model": "api_key" },
			"protocol": {
				"kind": "openai_chat",
				"path": "/v1/chat/completions",
				"officialBaseURL": "https://api.acme.example",
				"defaultEndpointTypes": ["openai"]
			}
		},
		"sha256": { "plugin.js": "00" }
	}`)
	manifest, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("ParseManifest site alias = %v", err)
	}
	if !manifest.DeclaresSiteType() || manifest.SiteProvider.SiteType != "acme.hooks/chat" || manifest.hasSite {
		t.Fatalf("site alias result: declares=%v siteType=%q hasSite=%v", manifest.DeclaresSiteType(), manifest.SiteProvider.SiteType, manifest.hasSite)
	}

	conflict := []byte(`{
		"apiVersion": 1,
		"hostApi": 1,
		"id": "acme.hooks",
		"version": "1.0.0",
		"site": { "siteType": "acme.hooks/a" },
		"siteProvider": { "siteType": "acme.hooks/b" },
		"sha256": { "plugin.js": "00" }
	}`)
	if _, err := ParseManifest(conflict); err == nil || !strings.Contains(err.Error(), "cannot both declare a site type") {
		t.Fatalf("conflict err = %v", err)
	}
}

func TestTransformStreamHookAndFixture(t *testing.T) {
	source := kindlessSource(`
export function transformStream(ctx, event) {
	if (event.data === "[PING]") {
		return { drop: true };
	}
	return { event: "message", data: ctx.model + ":" + event.data, done: event.data === "END" };
}
`)
	plugin, err := NewPlugin(kindlessManifest(source, nil), source, []Fixture{{
		Name:  "rewrite",
		Hook:  HookTransformStream,
		Ctx:   fixtureContext{Model: "gpt-4"},
		Input: map[string]any{"data": "END"},
		Expect: fixtureExpectation{
			Result: map[string]any{
				"event": "message",
				"data":  "gpt-4:END",
				"done":  true,
			},
		},
	}}, 1)
	if err != nil {
		t.Fatalf("NewPlugin transformStream = %v", err)
	}
	if ScopeOfManifest(plugin.Manifest, []string{HookTransformStream}) != ScopeSite {
		t.Fatalf("scope = %q", ScopeOfManifest(plugin.Manifest, []string{HookTransformStream}))
	}
	if err := plugin.SelfTest(context.Background()); err != nil {
		t.Fatalf("SelfTest transformStream = %v", err)
	}
}

func TestKindlessMetaKindMustBeEmpty(t *testing.T) {
	source := `export const meta = { apiVersion: 1, id: "acme.hooks", kind: "quota_probe" };` + "\n" +
		`export function probe() { return { error: "x" }; }` + "\n"
	_, err := NewPlugin(kindlessManifest(source, nil), source, nil, 1)
	if err == nil || !strings.Contains(err.Error(), "meta.kind") {
		t.Fatalf("err = %v", err)
	}
}

func TestKindlessExportsMustMatch(t *testing.T) {
	source := kindlessSource(`export function probe() { return { error: "x" }; }`)
	manifest := kindlessManifest(source, func(m *Manifest) { m.Exports = []string{"probe"} })
	if _, err := NewPlugin(manifest, source, nil, 1); err != nil {
		t.Fatal(err)
	}
	manifest.Exports = []string{"listModels"}
	_, err := NewPlugin(manifest, source, nil, 1)
	if err == nil || !strings.Contains(err.Error(), "manifest exports do not match the module") {
		t.Fatalf("err = %v", err)
	}
	got, err := ExportedHooks(source)
	if err != nil || len(got) != 1 || got[0] != "probe" {
		t.Fatalf("exported = %v err=%v", got, err)
	}
}

func TestHookAliases(t *testing.T) {
	onlyClassify := kindlessSource(`export function classify() { return { class: "unknown" }; }`)
	plugin, err := NewPlugin(kindlessManifest(onlyClassify, nil), onlyClassify, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !plugin.HasHook("classify") || !plugin.HasHook("classifyError") {
		t.Fatal("classify alias missing")
	}
	onlyEvent := kindlessSource(`export function probe() { return { error: "x" }; }
export function onEvent() { return { actions: [] }; }`)
	_, err = NewPlugin(kindlessManifest(onlyEvent, nil), onlyEvent, nil, 1)
	if err == nil || !strings.Contains(err.Error(), `"kind": "automation"`) {
		t.Fatalf("onEvent without kind: %v", err)
	}

	plugin = automationPlugin(t, "acme.auto", automationSection(), `export function handle() { return { actions: [] }; }`+"\n")
	if !plugin.HasHook("handle") || !plugin.HasHook("onEvent") {
		t.Fatal("handle alias missing")
	}
}

func TestKindlessFixtureNeedsHook(t *testing.T) {
	source := kindlessSource(`export function probe() { return { error: "x" }; }`)
	plugin, err := NewPlugin(kindlessManifest(source, nil), source, []Fixture{{Name: "bare"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	err = plugin.SelfTest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "fixture.hook is required for plugins without kind") {
		t.Fatalf("err = %v", err)
	}
}

func TestScopeOfManifestAndKindLabel(t *testing.T) {
	site := Manifest{SiteProvider: SiteProviderSection{SiteType: "acme.hooks/chat"}}
	if site.KindLabel() != KindSiteProvider || !site.DeclaresSiteType() {
		t.Fatalf("label=%s declares=%v", site.KindLabel(), site.DeclaresSiteType())
	}
	if ScopeOfManifest(site, nil) != ScopeSiteType {
		t.Fatal("site scope")
	}
	plain := Manifest{}
	if plain.KindLabel() != KindHooks {
		t.Fatal(plain.KindLabel())
	}
	if ScopeOfManifest(plain, []string{"classify"}) != ScopeSite {
		t.Fatal("site hook scope")
	}
	if ScopeOfManifest(plain, []string{"detect"}) != ScopeGlobal {
		t.Fatal("global hook scope")
	}
	if ScopeOfManifest(Manifest{Kind: KindQuotaProbe}, nil) != ScopeSite {
		t.Fatal("kind scope")
	}
	err := validateManifest(Manifest{ID: "acme-x", APIVersion: 1, HostAPI: 1, Kind: KindHooks, SHA256: map[string]string{"plugin.js": hashSource("x")}}, "x")
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("hooks kind: %v", err)
	}
}
