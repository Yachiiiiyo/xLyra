package jsplugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const pipelineTestID = "acme.pipeline"

func pipelineTestSource(body string) string {
	return `export const meta = { apiVersion: 1, id: "acme.pipeline" };` + "\n" + body
}

func boolPtr(v bool) *bool { return &v }

// pipelineTestSection is a valid section with a phone rule, a dictionary and an allowlist.
func pipelineTestSection() *GatewayPipelineSection {
	return &GatewayPipelineSection{
		Scope:               PipelineScopeAPIKey,
		Exclusive:           true,
		FailurePolicy:       PipelineFailClosed,
		UnsupportedEndpoint: PipelineUnsupportedReject,
		Transform: PipelineTransformSpec{
			Mode:       PipelineTransformTokenPlaceholder,
			SystemHint: "keep [[TYPE_N]] as is",
		},
		Types: []PipelineTypeSpec{
			{Type: "SECRET", EnabledBy: "secret"},
			{Type: "PHONE", Label: LocalizedText{"zh": "手机号", "en": "Phone"}},
			{Type: "CUSTOM"},
		},
		Rules: []PipelineRuleSpec{
			{Type: "PHONE", Pattern: `(?:\+?86[- ]?)?(1[3-9]\d{9})`, Group: 1, Boundary: "digit"},
			{Type: "SECRET", Pattern: `sk-[A-Za-z0-9]{20,}`, MinEntropy: 3, ContextAny: []string{"key"}, ContextWindow: 30},
		},
		Dictionaries: []PipelineDictSpec{{Param: "dictionary", Type: "CUSTOM"}},
		Allowlist:    &PipelineAllowlistSpec{Param: "allowlist"},
		Inputs: []AutomationInputSpec{
			{Name: "keys", Type: EntityAPIKey, Multiple: true},
			{Name: "secret", Type: "boolean", Default: true},
			{Name: "dictionary", Type: "string", Control: ControlTextarea},
			{Name: "allowlist", Type: "string", Control: ControlTextarea},
		},
	}
}

func pipelineTestManifest(source string, extras func(*Manifest)) Manifest {
	manifest := Manifest{
		ID: pipelineTestID, Name: "Pipeline", Version: "1.0.0", APIVersion: 1, HostAPI: 2,
		Permissions:     []string{PermissionPayloadInspectRaw},
		GatewayPipeline: pipelineTestSection(),
		SHA256:          map[string]string{"plugin.js": hashSource(source)},
	}
	if extras != nil {
		extras(&manifest)
	}
	return manifest
}

func TestGatewayPipelineDeclarativePluginLoads(t *testing.T) {
	source := pipelineTestSource("")
	plugin, err := NewPlugin(pipelineTestManifest(source, nil), source, nil, 0)
	if err != nil {
		t.Fatalf("declarative pipeline: %v", err)
	}
	if err := plugin.SelfTest(context.Background()); err != nil {
		t.Fatalf("declarative pipeline selftest: %v", err)
	}
	if got := ScopeOfManifest(plugin.Manifest, nil); got != ScopeGatewayPipeline {
		t.Fatalf("scope = %q", got)
	}
	if !RequiresTrustedSignature(plugin.Manifest) {
		t.Fatal("payload.inspect_raw must need a trusted signature")
	}
	if plugin.Manifest.KindLabel() != KindHooks {
		t.Fatalf("kind label = %q, no kind may be added for the pipeline", plugin.Manifest.KindLabel())
	}
}

func TestGatewayPipelineManifestRoundTrips(t *testing.T) {
	source := pipelineTestSource("")
	manifest := pipelineTestManifest(source, nil)
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.DeclaresGatewayPipeline() || parsed.GatewayPipeline.Rules[0].Group != 1 || parsed.GatewayPipeline.FailurePolicyOrDefault() != PipelineFailClosed {
		t.Fatalf("round trip lost the section: %#v", parsed.GatewayPipeline)
	}
	if _, err := NewPlugin(parsed, source, nil, 0); err != nil {
		t.Fatalf("parsed manifest: %v", err)
	}
}

func TestGatewayPipelinePermissionExclusions(t *testing.T) {
	source := pipelineTestSource("")
	for _, perm := range []string{"http", "storage", "schedule", "net:api.example.com", "net:*.example.com"} {
		manifest := pipelineTestManifest(source, func(m *Manifest) { m.Permissions = append(m.Permissions, perm) })
		if _, err := NewPlugin(manifest, source, nil, 0); err == nil || !strings.Contains(err.Error(), "cannot be combined with "+perm) {
			t.Fatalf("%s with payload.inspect_raw: %v", perm, err)
		}
	}
	withoutSection := kindlessManifest(kindlessSource(`export function probe() { return { error: "x" }; }`), func(m *Manifest) {
		m.Permissions = []string{PermissionPayloadInspectRaw}
	})
	if _, err := NewPlugin(withoutSection, kindlessSource(`export function probe() { return { error: "x" }; }`), nil, 0); err == nil || !strings.Contains(err.Error(), "needs a gatewayPipeline section") {
		t.Fatalf("payload.inspect_raw without section: %v", err)
	}
	withAutomation := pipelineTestManifest(source, func(m *Manifest) { m.Automation = automationSection() })
	if _, err := NewPlugin(withAutomation, source, nil, 0); err == nil {
		t.Fatal("payload.inspect_raw with an automation section must fail")
	}
	withSchedule := pipelineTestManifest(source, func(m *Manifest) { m.Schedule = ScheduleSection{EveryMinutes: 60} })
	if _, err := NewPlugin(withSchedule, source, nil, 0); err == nil || !strings.Contains(err.Error(), "schedule") {
		t.Fatalf("payload.inspect_raw with schedule: %v", err)
	}
}

func TestGatewayPipelineHookRules(t *testing.T) {
	hookSource := pipelineTestSource(`export function inspectSegments(ctx, input) { return {}; }`)
	if _, err := NewPlugin(pipelineTestManifest(hookSource, nil), hookSource, nil, 0); err != nil {
		t.Fatalf("pipeline with inspectSegments: %v", err)
	}
	noPermission := pipelineTestManifest(hookSource, func(m *Manifest) { m.Permissions = nil })
	if _, err := NewPlugin(noPermission, hookSource, nil, 0); err == nil || !strings.Contains(err.Error(), "requires permission payload.inspect_raw") {
		t.Fatalf("inspectSegments without permission: %v", err)
	}
	noSection := kindlessSource(`export function inspectSegments(ctx, input) { return {}; }`)
	if _, err := NewPlugin(kindlessManifest(noSection, nil), noSection, nil, 0); err == nil || !strings.Contains(err.Error(), "gatewayPipeline") {
		t.Fatalf("inspectSegments without section: %v", err)
	}
	for _, export := range []string{
		`export function onSchedule(ctx, steps) { return { result: {} }; }`,
		`export function onEvent(ctx, event) { return {}; }`,
		`export function classifyError(ctx, input) { return { class: "unknown" }; }`,
	} {
		source := pipelineTestSource(export)
		if _, err := NewPlugin(pipelineTestManifest(source, nil), source, nil, 0); err == nil {
			t.Fatalf("pipeline exporting %q must fail", export)
		}
	}
	if !isHotHookName(HookInspectSegments) || !contains(KnownHookNames(), HookInspectSegments) {
		t.Fatal("inspectSegments must be a known hot hook")
	}
}

func TestGatewayPipelineSectionExclusions(t *testing.T) {
	source := pipelineTestSource("")
	withProtocol := pipelineTestManifest(source, func(m *Manifest) {
		m.Protocol = ProtocolSection{Name: "x", DownstreamPath: "/v1/x", EndpointType: "openai"}
	})
	if _, err := NewPlugin(withProtocol, source, nil, 0); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("pipeline + protocol: %v", err)
	}
	withKind := pipelineTestManifest(source, func(m *Manifest) { m.Kind = KindErrorClassifier })
	if _, err := NewPlugin(withKind, source, nil, 0); err == nil || !strings.Contains(err.Error(), "without kind") {
		t.Fatalf("pipeline with kind: %v", err)
	}
}

func TestGatewayPipelineSectionValidation(t *testing.T) {
	source := pipelineTestSource("")
	cases := []struct {
		name   string
		mutate func(*GatewayPipelineSection)
		want   string
	}{
		{"scope", func(s *GatewayPipelineSection) { s.Scope = "site" }, "scope"},
		{"failure policy", func(s *GatewayPipelineSection) { s.FailurePolicy = "retry" }, "failurePolicy"},
		{"unsupported endpoint", func(s *GatewayPipelineSection) { s.UnsupportedEndpoint = "drop" }, "unsupportedEndpoint"},
		{"inspect bytes", func(s *GatewayPipelineSection) { s.MaxInspectBytes = 32 << 20 }, "maxInspectBytes"},
		{"type pattern", func(s *GatewayPipelineSection) { s.Types[0].Type = "secret" }, "must match"},
		{"duplicate type", func(s *GatewayPipelineSection) { s.Types[1].Type = "SECRET" }, "duplicate type"},
		{"enabledBy unknown", func(s *GatewayPipelineSection) { s.Types[0].EnabledBy = "nope" }, "not an input"},
		{"enabledBy not boolean", func(s *GatewayPipelineSection) { s.Types[0].EnabledBy = "dictionary" }, "must have type \"boolean\""},
		{"rule type", func(s *GatewayPipelineSection) { s.Rules[0].Type = "EMAIL" }, "not declared"},
		{"rule pattern", func(s *GatewayPipelineSection) { s.Rules[0].Pattern = `(a` }, "pattern"},
		{"rule backreference", func(s *GatewayPipelineSection) { s.Rules[0].Pattern = `(a)\1` }, "pattern"},
		{"rule group", func(s *GatewayPipelineSection) { s.Rules[0].Group = 2 }, "out of range"},
		{"rule boundary", func(s *GatewayPipelineSection) { s.Rules[0].Boundary = "space" }, "boundary"},
		{"rule validate", func(s *GatewayPipelineSection) { s.Rules[0].Validate = "iban" }, "validate"},
		{"context window", func(s *GatewayPipelineSection) { s.Rules[0].ContextWindow = 10 }, "needs contextAny"},
		{"dictionary param", func(s *GatewayPipelineSection) { s.Dictionaries[0].Param = "secret" }, "must have type \"string\""},
		{"dictionary type", func(s *GatewayPipelineSection) { s.Dictionaries[0].Type = "NAME" }, "not declared"},
		{"allowlist param", func(s *GatewayPipelineSection) { s.Allowlist.Param = "keys" }, "must have type \"string\""},
		{"no api key input", func(s *GatewayPipelineSection) { s.Inputs = s.Inputs[1:] }, "exactly one input"},
		{"two api key inputs", func(s *GatewayPipelineSection) {
			s.Inputs = append(s.Inputs, AutomationInputSpec{Name: "more", Type: EntityAPIKey})
		}, "exactly one input"},
		{"site input", func(s *GatewayPipelineSection) {
			s.Inputs = append(s.Inputs, AutomationInputSpec{Name: "site", Type: EntitySite})
		}, "cannot be picked"},
		{"system hint without placeholders", func(s *GatewayPipelineSection) { s.Transform.Mode = PipelineTransformNone }, "systemHint needs mode"},
		{"reversible without placeholders", func(s *GatewayPipelineSection) {
			s.Transform = PipelineTransformSpec{Mode: PipelineTransformNone, Reversible: boolPtr(true)}
		}, "reversible needs mode"},
		{"system hint control chars", func(s *GatewayPipelineSection) { s.Transform.SystemHint = "a\x00b" }, "control characters"},
		{"rules without types", func(s *GatewayPipelineSection) {
			s.Types = nil
			s.Transform = PipelineTransformSpec{}
			s.Dictionaries = nil
		}, "gatewayPipeline.types"},
	}
	for _, tc := range cases {
		manifest := pipelineTestManifest(source, func(m *Manifest) { tc.mutate(m.GatewayPipeline) })
		_, err := NewPlugin(manifest, source, nil, 0)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: got %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestGatewayPipelineDefaultsAndHostOptions(t *testing.T) {
	section := *pipelineTestSection()
	section.FailurePolicy = ""
	section.UnsupportedEndpoint = ""
	if section.FailurePolicyOrDefault() != PipelineFailOpen || section.UnsupportedEndpointOrDefault() != PipelineUnsupportedPass {
		t.Fatal("policies default to fail_open and pass")
	}
	if section.MaxInspectBytesOrDefault() != 8<<20 || section.TotalTimeoutMsOrDefault() != 1000 || section.Transform.PropagateMinLengthOrDefault() != 2 {
		t.Fatal("limits default to 8 MiB, 1000 ms and 2 characters")
	}
	if !section.Transform.IsReversible() {
		t.Fatal("token_placeholder is reversible by default")
	}
	keys := func(specs []PipelineHostOptionSpec) string {
		var out []string
		for _, spec := range specs {
			out = append(out, spec.Key)
		}
		return strings.Join(out, ",")
	}
	if got := keys(PipelineHostOptionSpecs(section)); got != "propagateSameValue,scanToolDefinitions,transformReasoning" {
		t.Fatalf("host options = %s", got)
	}
	options, err := ResolvePipelineHostOptions(section, nil)
	if err != nil || !options.PropagateSameValue || options.ScanToolDefinitions || options.TransformReasoning {
		t.Fatalf("defaults = %#v, %v", options, err)
	}
	options, err = ResolvePipelineHostOptions(section, map[string]any{"propagateSameValue": false, "transformReasoning": true})
	if err != nil || options.PropagateSameValue || !options.TransformReasoning {
		t.Fatalf("changed = %#v, %v", options, err)
	}
	none := section
	none.Transform = PipelineTransformSpec{Mode: PipelineTransformNone}
	if got := keys(PipelineHostOptionSpecs(none)); got != "scanToolDefinitions" {
		t.Fatalf("mode none host options = %s", got)
	}
	if _, err := ResolvePipelineHostOptions(none, map[string]any{"transformReasoning": true}); err == nil {
		t.Fatal("transformReasoning is not an option without reversible placeholders")
	}
}
