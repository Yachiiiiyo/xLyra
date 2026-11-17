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

func TestKindlessRejectsProtocolSection(t *testing.T) {
	source := kindlessSource(`export function probe() { return { error: "x" }; }`)
	manifest := kindlessManifest(source, func(m *Manifest) {
		m.Protocol.Name = "acme"
	})
	err := validateManifest(manifest, source)
	if err == nil || !strings.Contains(err.Error(), `"kind": "protocol"`) {
		t.Fatalf("err = %v", err)
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
