package jsplugin

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"xlyra/server/internal/textmatch"
)

func TestBuiltinSelfTest(t *testing.T) {
	registry, err := LoadBuiltins()
	if err != nil {
		t.Fatalf("load builtins: %v", err)
	}
	for _, probeType := range []string{"kimi", "glm", "moonshot", "deepseek", "sub2api", "newapi", "xlyra"} {
		plugin, ok := registry.ByProbeType(probeType)
		if !ok {
			t.Fatalf("missing builtin %s", probeType)
		}
		if err := plugin.SelfTest(context.Background()); err != nil {
			t.Fatalf("selftest %s: %v", probeType, err)
		}
	}
}

func TestHeapLimitAvailable(t *testing.T) {
	if !HeapLimitAvailable() {
		t.Fatal("allocation budget and result bound must both be configured")
	}
}

func TestAllocLimitDiscardsRuntime(t *testing.T) {
	for _, source := range []string{
		`let s = "x"; for (;;) s += s;`,
		`new Array(1e8).fill(0);`,
	} {
		plugin := mustTestPlugin(t, "xlyra.test.bomb", wrapProbe(source))
		_, err := plugin.CallProbe(context.Background(), ProbeContext{}, nil)
		if err == nil || !strings.Contains(err.Error(), KindHeapLimit) {
			t.Fatalf("source %q error = %v, want %s", source, err, KindHeapLimit)
		}
		plugin.pool.mu.Lock()
		total, idle := plugin.pool.total, len(plugin.pool.idle)
		plugin.pool.mu.Unlock()
		if total != 0 || idle != 0 {
			t.Fatalf("pool after alloc limit total=%d idle=%d", total, idle)
		}
	}
}

func TestResultTooLargeKeepsRuntime(t *testing.T) {
	source := wrapProbe(`const s = "x".repeat(4 * 1024 * 1024); return [s, s, s, s, s];`)
	plugin := mustTestPlugin(t, "xlyra.test.bomb", source)
	_, err := plugin.CallProbe(context.Background(), ProbeContext{}, nil)
	if err == nil || !strings.Contains(err.Error(), KindResponseTooLarge) {
		t.Fatalf("error = %v, want %s", err, KindResponseTooLarge)
	}
	plugin.pool.mu.Lock()
	total, idle := plugin.pool.total, len(plugin.pool.idle)
	plugin.pool.mu.Unlock()
	if total != 1 || idle != 1 {
		t.Fatalf("pool after result limit total=%d idle=%d, want the runtime reused", total, idle)
	}
}

func TestLiveLimitDiscardsLeakingRuntime(t *testing.T) {
	source := `const leak = [];
export const meta = { apiVersion: 1, id: "xlyra.test.leak", kind: "quota_probe" };
export function probe() {
  leak.push(new Uint8Array(4 * 1024 * 1024));
  return { result: { kind: "balance", entries: [{ label: "balance", remaining: leak.length }] } };
}`
	plugin := mustTestPlugin(t, "xlyra.test.leak", source)
	var hitErr error
	for i := 0; i < 8; i++ {
		_, err := plugin.CallProbe(context.Background(), ProbeContext{}, nil)
		if err != nil {
			hitErr = err
			break
		}
	}
	var call *CallError
	if !errors.As(hitErr, &call) || call.Kind != KindHeapLimit || call.Interrupt != "live_limit" {
		t.Fatalf("error = %#v, want %s (live_limit)", hitErr, KindHeapLimit)
	}
	plugin.pool.mu.Lock()
	total, idle := plugin.pool.total, len(plugin.pool.idle)
	plugin.pool.mu.Unlock()
	if total != 0 || idle != 0 {
		t.Fatalf("pool after live limit total=%d idle=%d, want discarded", total, idle)
	}
	// A subsequent call starts a fresh runtime cleanly.
	decision, err := plugin.CallProbe(context.Background(), ProbeContext{}, nil)
	if err != nil {
		t.Fatalf("fresh call after live limit: %v", err)
	}
	if decision.Result == nil || len(decision.Result.Entries) != 1 || *decision.Result.Entries[0].Remaining != 1 {
		t.Fatalf("fresh call decision = %#v, want remaining=1", decision.Result)
	}
}

func TestResultNodeLimitKeepsRuntime(t *testing.T) {
	source := `let calls = 0;
export const meta = { apiVersion: 1, id: "xlyra.test.nodes", kind: "quota_probe" };
export function probe() {
  calls++;
  if (calls === 1) {
    return new Array(1048577);
  }
  return { result: { kind: "balance", entries: [{ label: "balance", remaining: calls }] } };
}`
	plugin := mustTestPlugin(t, "xlyra.test.nodes", source)
	_, err := plugin.CallProbe(context.Background(), ProbeContext{}, nil)
	var call *CallError
	if !errors.As(err, &call) || call.Kind != KindResponseTooLarge {
		t.Fatalf("error = %#v, want %s", err, KindResponseTooLarge)
	}
	plugin.pool.mu.Lock()
	total, idle := plugin.pool.total, len(plugin.pool.idle)
	plugin.pool.mu.Unlock()
	if total != 1 || idle != 1 {
		t.Fatalf("pool after node limit total=%d idle=%d, want the runtime reused", total, idle)
	}
	decision, err := plugin.CallProbe(context.Background(), ProbeContext{}, nil)
	if err != nil {
		t.Fatalf("follow-up call after node limit: %v", err)
	}
	if decision.Result == nil || len(decision.Result.Entries) != 1 || *decision.Result.Entries[0].Remaining != 2 {
		t.Fatalf("follow-up decision = %#v, want remaining=2 on same runtime", decision.Result)
	}
}

func TestAllocLimitIncludesJSStack(t *testing.T) {
	source := `export const meta = { apiVersion: 1, id: "xlyra.test.allocstack", kind: "quota_probe" };
function allocBombHelper() {
  let s = "x";
  for (;;) s += s;
}
export function probe() {
  return allocBombHelper();
}`
	plugin := mustTestPlugin(t, "xlyra.test.allocstack", source)
	_, err := plugin.CallProbe(context.Background(), ProbeContext{}, nil)
	var call *CallError
	if !errors.As(err, &call) || call.Kind != KindHeapLimit || call.Interrupt != "alloc_limit" {
		t.Fatalf("error = %#v, want %s (alloc_limit)", err, KindHeapLimit)
	}
	if !strings.Contains(call.Detail, "allocBombHelper") || !strings.Contains(call.Detail, "probe") {
		t.Fatalf("CallError.Detail = %q, want JS stack with allocBombHelper and probe", call.Detail)
	}
	plugin.pool.mu.Lock()
	total, idle := plugin.pool.total, len(plugin.pool.idle)
	plugin.pool.mu.Unlock()
	if total != 0 || idle != 0 {
		t.Fatalf("pool after alloc limit total=%d idle=%d, want discarded", total, idle)
	}
}

func TestNativeUint8ArrayAndNoBootstrapGlobal(t *testing.T) {
	source := `export const meta = { apiVersion: 1, id: "xlyra.test.nativeu8", kind: "quota_probe" };
export const detAB = new ArrayBuffer(8);
const detU8 = new Uint8Array(detAB);
function expectThrow(fn, want) {
  try {
    fn();
  } catch (e) {
    if (String(e).includes(want)) return null;
    return "got " + String(e) + ", want " + want;
  }
  return "did not throw " + want;
}
export function probe(ctx) {
  if ("__xlyraRawHost" in globalThis || typeof globalThis.__xlyraRawHost !== "undefined") {
    return { error: "__xlyraRawHost leaked" };
  }
  const key16 = bytes.fromUTF8("0123456789abcdef");
  const iv12 = bytes.fromUTF8("0123456789ab");
  const enc = crypto.aesEncrypt("aes-128-gcm", key16, iv12, "hi");
  const dec = crypto.aesDecrypt("aes-128-gcm", key16, iv12, enc);
  const checks = [
    ["fromUTF8", bytes.fromUTF8("hi")],
    ["fromUTF8Empty", bytes.fromUTF8("")],
    ["fromHex", bytes.fromHex("6869")],
    ["fromBase64", bytes.fromBase64("aGk=")],
    ["fromBase64URL", bytes.fromBase64URL("aGk")],
    ["concat", bytes.concat([bytes.fromUTF8("a"), bytes.fromUTF8("b")])],
    ["hexDecode", encoding.hexDecode("6869")],
    ["base64Decode", encoding.base64Decode("aGk=")],
    ["base64URLDecode", encoding.base64URLDecode("aGk")],
    ["utf8Encode", encoding.utf8Encode("hi")],
    ["digestBytes", crypto.digestBytes("sha256", "hi")],
    ["hmacBytes", crypto.hmacBytes("sha256", "k", "m")],
    ["randomBytes", crypto.randomBytes(16)],
    ["aesEncrypt", enc],
    ["aesDecrypt", dec],
  ];
  for (const [name, val] of checks) {
    if (!(val instanceof Uint8Array)) return { error: name + " not Uint8Array" };
  }
  if (bytes.toUTF8(dec) !== "hi") return { error: "aes roundtrip" };
  if (bytes.toUTF8(bytes.fromUTF8("")) !== "") return { error: "empty utf8" };

  // Subarray zero-copy slice view works across bytes/crypto.
  const full = bytes.fromUTF8("abcdef");
  const sub = full.subarray(2, 5);
  if (bytes.toUTF8(sub) !== "cde") return { error: "subarray toUTF8" };
  if (bytes.toHex(sub) !== "636465") return { error: "subarray toHex" };
  if (!bytes.equal(sub, bytes.fromUTF8("cde"))) return { error: "subarray equal" };
  if (bytes.toUTF8(bytes.concat([sub, bytes.fromUTF8("!")])) !== "cde!") return { error: "subarray concat" };
  if (crypto.sha256(sub) !== crypto.sha256("cde")) return { error: "subarray sha256" };

  // Invalid binary arguments fail cleanly.
  const errs = [
    expectThrow(() => bytes.toHex("not-u8"), "expected Uint8Array"),
    expectThrow(() => bytes.toHex(new Uint16Array(2)), "expected Uint8Array"),
    expectThrow(() => bytes.concat("not-array"), "expected array of Uint8Array"),
    expectThrow(() => bytes.concat([bytes.fromUTF8("a"), "bad"]), "chunks[1]: expected Uint8Array"),
    expectThrow(() => crypto.sha256(123), "expected string or Uint8Array"),
    expectThrow(() => crypto.aesEncrypt("aes-128-gcm", key16, iv12, "hi", 123), "expected string or Uint8Array"),
  ];
  for (const msg of errs) {
    if (msg) return { error: msg };
  }

  // Detached Uint8Array fails cleanly after host detaches detAB.
  if (ctx.baseURL === "check-detached") {
    const detErrs = [
      expectThrow(() => bytes.toHex(detU8), "detached byte buffer"),
      expectThrow(() => bytes.concat([detU8]), "detached byte buffer"),
      expectThrow(() => crypto.sha256(detU8), "detached byte buffer"),
      expectThrow(() => crypto.aesEncrypt("aes-128-gcm", key16, iv12, "hi", detU8), "detached byte buffer"),
    ];
    for (const msg of detErrs) {
      if (msg) return { error: msg };
    }
  }
  return { result: { kind: "balance", entries: [{ label: "balance", remaining: 1 }] } };
}`
	plugin := mustTestPlugin(t, "xlyra.test.nativeu8", source)
	decision, err := plugin.CallProbe(context.Background(), ProbeContext{}, nil)
	if err != nil {
		t.Fatalf("CallProbe: %v", err)
	}
	if decision.Error != "" {
		t.Fatalf("probe error: %s", decision.Error)
	}

	// Inspect the warm pooled session: globalThis stays in fast/shape mode and stats work.
	plugin.pool.mu.Lock()
	if len(plugin.pool.idle) != 1 {
		plugin.pool.mu.Unlock()
		t.Fatalf("expected 1 idle session, got %d", len(plugin.pool.idle))
	}
	sess := plugin.pool.idle[0]
	plugin.pool.mu.Unlock()

	if sess.rt.Realm().Global.IsDictionaryMode() {
		t.Fatal("globalThis degraded to dictionary mode")
	}
	st := sess.stats()
	if st.LiveBytes <= 0 || st.ICEntries <= 0 || st.RegisterStackBytes <= 0 {
		t.Fatalf("unexpected runtime stats: %+v", st)
	}

	detAB, ok := sess.rt.Export("detAB")
	if !ok {
		t.Fatal("missing detAB export")
	}
	if err := sess.rt.Realm().DetachArrayBuffer(detAB); err != nil {
		t.Fatalf("DetachArrayBuffer: %v", err)
	}
	decision, err = plugin.CallProbe(context.Background(), ProbeContext{BaseURL: "check-detached"}, nil)
	if err != nil {
		t.Fatalf("CallProbe(check-detached): %v", err)
	}
	if decision.Error != "" {
		t.Fatalf("detached check error: %s", decision.Error)
	}
}

func TestObjectTemplateHotPathShapes(t *testing.T) {
	streamSource := `export const meta = { apiVersion: 1, id: "acme.streamtpl" };
export function transformStream(ctx, event) {
  const ctxKeys = Object.keys(ctx).join(",");
  const evKeys = Object.keys(event).join(",");
  const ver = ctx.settings ? ctx.settings.clientVersion : "none";
  return { data: ctxKeys + "|" + evKeys + "|" + ctx.siteType + "|" + ctx.model + "|" + ctx.protocol + "|" + ver + "|" + (event.event || "") + "|" + event.data };
}`
	manifest := Manifest{
		ID: "acme.streamtpl", Name: "streamtpl", Version: "1.0.0", APIVersion: 1, HostAPI: 2,
		XLyra: ">=1.14.0", License: "AGPL-3.0",
		Settings: []SettingSpec{{Key: "clientVersion", Type: SettingTypeString, Default: "2.0.0"}},
		SHA256:   map[string]string{"plugin.js": HashSource(streamSource)},
	}
	plugin, err := NewPlugin(manifest, streamSource, nil, 1)
	if err != nil {
		t.Fatalf("NewPlugin: %v", err)
	}

	// Event without optional "event" field omits "event" key; settings are injected into StreamTransformContext.
	res1, _, _, err := plugin.CallTransformStream(context.Background(), StreamTransformContext{
		SiteType: "openai", Model: "gpt-4o", Protocol: "openai_chat",
	}, StreamTransformEvent{Data: "hello"})
	if err != nil {
		t.Fatalf("CallTransformStream(1): %v", err)
	}
	want1 := "model,protocol,settings,siteType|data|openai|gpt-4o|openai_chat|2.0.0||hello"
	if res1.Data == nil || *res1.Data != want1 {
		t.Fatalf("res1.Data = %v, want %q", res1.Data, want1)
	}

	// Event with "event" field includes both keys.
	res2, _, _, err := plugin.CallTransformStream(context.Background(), StreamTransformContext{
		SiteType: "openai", Model: "gpt-4o", Protocol: "openai_chat",
	}, StreamTransformEvent{Event: "delta", Data: "world"})
	if err != nil {
		t.Fatalf("CallTransformStream(2): %v", err)
	}
	want2 := "model,protocol,settings,siteType|data,event|openai|gpt-4o|openai_chat|2.0.0|delta|world"
	if res2.Data == nil || *res2.Data != want2 {
		t.Fatalf("res2.Data = %v, want %q", res2.Data, want2)
	}

	// Without settings declared, "settings" key is omitted from StreamTransformContext.
	noSettingsManifest := manifest
	noSettingsManifest.Settings = nil
	noSettingsPlugin, err := NewPlugin(noSettingsManifest, streamSource, nil, 1)
	if err != nil {
		t.Fatalf("NewPlugin(no settings): %v", err)
	}
	res3, _, _, err := noSettingsPlugin.CallTransformStream(context.Background(), StreamTransformContext{
		SiteType: "openai", Model: "gpt-4o", Protocol: "openai_chat",
	}, StreamTransformEvent{Data: "plain"})
	if err != nil {
		t.Fatalf("CallTransformStream(3): %v", err)
	}
	want3 := "model,protocol,siteType|data|openai|gpt-4o|openai_chat|none||plain"
	if res3.Data == nil || *res3.Data != want3 {
		t.Fatalf("res3.Data = %v, want %q", res3.Data, want3)
	}

	// Pointer forms for StreamTransformContext/Event work symmetrically without mutating caller structs.
	ptrCtx := &StreamTransformContext{SiteType: "openai", Model: "gpt-4o", Protocol: "openai_chat"}
	ptrEv := &StreamTransformEvent{Event: "msg", Data: "ptr"}
	rawPtrOut, _, _, err := plugin.callHook(context.Background(), HookTransformStream, streamHookTimeout, 0, ptrCtx, ptrEv)
	if err != nil {
		t.Fatalf("callHook(ptr stream): %v", err)
	}
	if ptrCtx.Settings != nil {
		t.Fatalf("injectContextSettings mutated caller *StreamTransformContext: %#v", ptrCtx.Settings)
	}
	ptrRes, err := decodeStreamTransformResult(rawPtrOut)
	if err != nil {
		t.Fatalf("decodeStreamTransformResult(ptr): %v", err)
	}
	wantPtr := "model,protocol,settings,siteType|data,event|openai|gpt-4o|openai_chat|2.0.0|msg|ptr"
	if ptrRes.Data == nil || *ptrRes.Data != wantPtr {
		t.Fatalf("ptrRes.Data = %v, want %q", ptrRes.Data, wantPtr)
	}

	// InspectSegmentsContext, InspectSegmentsBinding, InspectSegmentsInput, SegmentItem, and SegmentFinding shapes.
	inspectSource := `export const meta = { apiVersion: 1, id: "acme.pipeline" };
export function inspectSegments(ctx, input) {
  const ctxKeys = Object.keys(ctx).join(",");
  const bindKeys = Object.keys(ctx.binding).join(",");
  const paramsKind = ctx.binding.params === null ? "null" : Object.keys(ctx.binding.params).join(",");
  const settingsKind = !("settings" in ctx) ? "absent" : Object.keys(ctx.settings).join(",") + "=" + (ctx.settings.mode || "");
  const inKeys = Object.keys(input).join(",");
  const segs = input.segments.map(s => Object.keys(s).join(",") + ":" + s.id + ":" + s.field + ":" + (s.role || "") + ":" + s.text).join(";");
  const finds = input.findings.map(f => Object.keys(f).join(",") + ":" + f.segment + ":" + f.start + ":" + f.end + ":" + f.type + ":" + (f.source || "") + ":" + (f.score ?? "")).join(";");
  return {
    reject: {
      status: 400,
      code: "shape_report",
      message: [ctxKeys, bindKeys, paramsKind, settingsKind, inKeys, segs, finds].join("|")
    }
  };
}`
	inspectManifest := pipelineTestManifest(inspectSource, func(m *Manifest) {
		m.Settings = []SettingSpec{{Key: "mode", Type: SettingTypeString, Default: "strict"}}
	})
	inspectWithSettings, err := NewPlugin(inspectManifest, inspectSource, nil, 1)
	if err != nil {
		t.Fatalf("NewPlugin(inspect with settings): %v", err)
	}
	inspectNoSettingsManifest := pipelineTestManifest(inspectSource, nil)
	inspectNoSettings, err := NewPlugin(inspectNoSettingsManifest, inspectSource, nil, 1)
	if err != nil {
		t.Fatalf("NewPlugin(inspect no settings): %v", err)
	}

	fullInput := InspectSegmentsInput{
		Segments: []SegmentItem{
			{ID: "s0", Field: "message.text", Text: "alpha"},
			{ID: "s1", Field: "message.text", Role: "user", Text: "beta"},
		},
		Findings: []SegmentFinding{
			{Segment: "s0", Start: 0, End: 1, Type: "PII"},
			{Segment: "s0", Start: 1, End: 2, Type: "PII", Source: "rule"},
			{Segment: "s1", Start: 0, End: 2, Type: "PII", Score: 0.75},
			{Segment: "s1", Start: 2, End: 4, Type: "PII", Source: "dictionary", Score: 0.9},
		},
	}
	wantSegs := "field,id,text:s0:message.text::alpha;field,id,role,text:s1:message.text:user:beta"
	wantFinds := strings.Join([]string{
		"end,segment,start,type:s0:0:1:PII::",
		"end,segment,source,start,type:s0:1:2:PII:rule:",
		"end,score,segment,start,type:s1:0:2:PII::0.75",
		"end,score,segment,source,start,type:s1:2:4:PII:dictionary:0.9",
	}, ";")

	// 1. With settings injected and Params == nil -> params is null, settings is present.
	raw1, _, _, err := inspectWithSettings.callHook(context.Background(), HookInspectSegments, protocolHookTimeout, 0,
		InspectSegmentsContext{Endpoint: "/v1/chat", Model: "m1", Binding: InspectSegmentsBinding{ID: "b1", Params: nil}},
		fullInput,
	)
	if err != nil {
		t.Fatalf("inspectWithSettings callHook: %v", err)
	}
	dec1, err := decodeInspectSegments(raw1)
	if err != nil || dec1.Reject == nil {
		t.Fatalf("dec1 = %#v, err = %v", dec1, err)
	}
	wantInspect1 := strings.Join([]string{
		"binding,endpoint,model,settings", "id,params", "null", "mode=strict", "findings,segments", wantSegs, wantFinds,
	}, "|")
	if dec1.Reject.Message != wantInspect1 {
		t.Fatalf("inspect(with settings, nil params) =\n %q\nwant:\n %q", dec1.Reject.Message, wantInspect1)
	}

	// 2. Without settings declared and Params == {} -> settings key absent, params is empty object.
	raw2, _, _, err := inspectNoSettings.callHook(context.Background(), HookInspectSegments, protocolHookTimeout, 0,
		InspectSegmentsContext{Endpoint: "/v1/chat", Model: "m1", Binding: InspectSegmentsBinding{ID: "b2", Params: map[string]any{}}},
		fullInput,
	)
	if err != nil {
		t.Fatalf("inspectNoSettings callHook: %v", err)
	}
	dec2, err := decodeInspectSegments(raw2)
	if err != nil || dec2.Reject == nil {
		t.Fatalf("dec2 = %#v, err = %v", dec2, err)
	}
	wantInspect2 := strings.Join([]string{
		"binding,endpoint,model", "id,params", "", "absent", "findings,segments", wantSegs, wantFinds,
	}, "|")
	if dec2.Reject.Message != wantInspect2 {
		t.Fatalf("inspect(no settings, empty params) =\n %q\nwant:\n %q", dec2.Reject.Message, wantInspect2)
	}

	// 3. Pointer forms (*InspectSegmentsContext, *InspectSegmentsInput) + *InspectSegmentsBinding symmetry.
	ptrInspectCtx := &InspectSegmentsContext{Endpoint: "/v1/chat", Model: "m1", Binding: InspectSegmentsBinding{ID: "b3", Params: map[string]any{"k": "v"}}}
	raw3, _, _, err := inspectWithSettings.callHook(context.Background(), HookInspectSegments, protocolHookTimeout, 0, ptrInspectCtx, &fullInput)
	if err != nil {
		t.Fatalf("inspectWithSettings ptr callHook: %v", err)
	}
	if ptrInspectCtx.Settings != nil {
		t.Fatalf("injectContextSettings mutated caller *InspectSegmentsContext: %#v", ptrInspectCtx.Settings)
	}
	dec3, err := decodeInspectSegments(raw3)
	if err != nil || dec3.Reject == nil {
		t.Fatalf("dec3 = %#v, err = %v", dec3, err)
	}
	wantInspect3 := strings.Join([]string{
		"binding,endpoint,model,settings", "id,params", "k", "mode=strict", "findings,segments", wantSegs, wantFinds,
	}, "|")
	if dec3.Reject.Message != wantInspect3 {
		t.Fatalf("inspect(ptr ctx/input) =\n %q\nwant:\n %q", dec3.Reject.Message, wantInspect3)
	}

	// Verify *InspectSegmentsBinding converts symmetrically via argToValue.
	bindSource := `export const meta = { apiVersion: 1, id: "xlyra.test.bindptr", kind: "quota_probe" };
export function probe(b, nilB) {
  if (nilB !== null) return { error: "expected null for nil binding pointer" };
  if (Object.keys(b).join(",") !== "id,params" || b.id !== "bp1" || b.params.flag !== true) {
    return { error: "bad binding: " + JSON.stringify(b) };
  }
  return { result: { kind: "balance", entries: [{ label: "ok", remaining: 1 }] } };
}`
	bindPlugin := mustTestPlugin(t, "xlyra.test.bindptr", bindSource)
	sess, err := bindPlugin.pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var nilBind *InspectSegmentsBinding
	outBind, _, _, discard, err := sess.call(context.Background(), probeHookTimeout, bindPlugin.program.hooks["probe"], []any{
		&InspectSegmentsBinding{ID: "bp1", Params: map[string]any{"flag": true}},
		nilBind,
	})
	bindPlugin.pool.Release(sess, discard)
	if err != nil {
		t.Fatalf("binding ptr call: %v", err)
	}
	if decBind, err := decodeDecision(outBind); err != nil || decBind.Error != "" {
		t.Fatalf("binding ptr decision = %#v, err = %v", decBind, err)
	}
}

func TestToGoAndExportMetaTimeoutsAndLogs(t *testing.T) {
	t.Run("togo_getter_timeout_and_logs", func(t *testing.T) {
		source := `export const meta = { apiVersion: 1, id: "xlyra.test.togogetter", kind: "quota_probe" };
export function probe() {
  return {
    get x() {
      log.info("inside-togo-getter");
      for (;;) {}
    }
  };
}`
		plugin := mustTestPlugin(t, "xlyra.test.togogetter", source)
		started := time.Now()
		decision, err := plugin.CallProbe(context.Background(), ProbeContext{}, nil)
		if time.Since(started) > 3*time.Second {
			t.Fatalf("ToGo getter loop took too long: %s", time.Since(started))
		}
		var call *CallError
		if !errors.As(err, &call) || call.Kind != KindTimeout {
			t.Fatalf("error = %#v, want %s", err, KindTimeout)
		}
		if len(decision.Logs) != 1 || decision.Logs[0].Message != "inside-togo-getter" {
			t.Fatalf("decision.Logs = %#v, want getter log captured", decision.Logs)
		}
		plugin.pool.mu.Lock()
		total, idle := plugin.pool.total, len(plugin.pool.idle)
		plugin.pool.mu.Unlock()
		if total != 0 || idle != 0 {
			t.Fatalf("pool after ToGo getter timeout total=%d idle=%d, want discarded", total, idle)
		}
	})

	t.Run("togo_proxy_ownKeys_timeout", func(t *testing.T) {
		source := `export const meta = { apiVersion: 1, id: "xlyra.test.togoproxy", kind: "quota_probe" };
export function probe() {
  return new Proxy({}, { ownKeys() { for (;;) {} } });
}`
		plugin := mustTestPlugin(t, "xlyra.test.togoproxy", source)
		started := time.Now()
		_, err := plugin.CallProbe(context.Background(), ProbeContext{}, nil)
		if time.Since(started) > 3*time.Second {
			t.Fatalf("ToGo Proxy ownKeys loop took too long: %s", time.Since(started))
		}
		var call *CallError
		if !errors.As(err, &call) || call.Kind != KindTimeout {
			t.Fatalf("error = %#v, want %s", err, KindTimeout)
		}
		plugin.pool.mu.Lock()
		total, idle := plugin.pool.total, len(plugin.pool.idle)
		plugin.pool.mu.Unlock()
		if total != 0 || idle != 0 {
			t.Fatalf("pool after ToGo Proxy timeout total=%d idle=%d, want discarded", total, idle)
		}
	})

	t.Run("togo_microtask_timeout", func(t *testing.T) {
		source := `export const meta = { apiVersion: 1, id: "xlyra.test.togomicrotask", kind: "quota_probe" };
export function probe() {
  return {
    get x() {
      Promise.resolve().then(() => { for (;;) {} });
      return 1;
    }
  };
}`
		plugin := mustTestPlugin(t, "xlyra.test.togomicrotask", source)
		started := time.Now()
		_, err := plugin.CallProbe(context.Background(), ProbeContext{}, nil)
		if time.Since(started) > 3*time.Second {
			t.Fatalf("ToGo microtask loop took too long: %s", time.Since(started))
		}
		var call *CallError
		if !errors.As(err, &call) || call.Kind != KindTimeout {
			t.Fatalf("error = %#v, want %s", err, KindTimeout)
		}
		plugin.pool.mu.Lock()
		total, idle := plugin.pool.total, len(plugin.pool.idle)
		plugin.pool.mu.Unlock()
		if total != 0 || idle != 0 {
			t.Fatalf("pool after ToGo microtask timeout total=%d idle=%d, want discarded", total, idle)
		}
	})

	t.Run("togo_getter_logs_captured_on_success", func(t *testing.T) {
		source := `export const meta = { apiVersion: 1, id: "xlyra.test.togolog", kind: "quota_probe" };
export function probe() {
  return {
    get result() {
      log.info("getter-emitted", { phase: "togo" });
      return { kind: "balance", entries: [{ label: "balance", remaining: 7 }] };
    }
  };
}`
		plugin := mustTestPlugin(t, "xlyra.test.togolog", source)
		decision, err := plugin.CallProbe(context.Background(), ProbeContext{}, nil)
		if err != nil {
			t.Fatalf("CallProbe: %v", err)
		}
		if len(decision.Logs) != 1 || decision.Logs[0].Message != "getter-emitted" || decision.Logs[0].Fields["phase"] != "togo" {
			t.Fatalf("decision.Logs = %#v, want getter-emitted", decision.Logs)
		}
	})

	t.Run("exportMeta_getter_timeout", func(t *testing.T) {
		source := `export const meta = { get id() { for (;;) {} } };
export function probe() { return { result: { kind: "balance", entries: [] } }; }`
		manifest := testManifest("xlyra.test.metaloop", source)
		started := time.Now()
		_, err := NewPlugin(manifest, source, []Fixture{{Name: "unused"}}, 1)
		if time.Since(started) > 3*time.Second {
			t.Fatalf("NewPlugin exportMeta loop took too long: %s", time.Since(started))
		}
		var call *CallError
		if !errors.As(err, &call) || call.Kind != KindTimeout {
			t.Fatalf("NewPlugin error = %#v, want %s", err, KindTimeout)
		}
		if errors.Unwrap(call) == nil {
			t.Fatal("CallError.Unwrap() = nil, want underlying interrupt error preserved")
		}
	})

	t.Run("load_live_limit_classified_as_heap_limit", func(t *testing.T) {
		source := `export const meta = { apiVersion: 1, id: "xlyra.test.loadlive", kind: "quota_probe" };
const huge = new Uint8Array(20 * 1024 * 1024);
export function probe() { return { result: { kind: "balance", entries: [{ label: "n", remaining: huge.length }] } }; }`
		manifest := testManifest("xlyra.test.loadlive", source)
		_, err := NewPlugin(manifest, source, []Fixture{{Name: "unused"}}, 1)
		var call *CallError
		if !errors.As(err, &call) || call.Kind != KindHeapLimit || call.Interrupt != "live_limit" {
			t.Fatalf("NewPlugin error = %#v, want %s (live_limit)", err, KindHeapLimit)
		}
		if errors.Unwrap(call) == nil {
			t.Fatal("CallError.Unwrap() = nil, want underlying live limit error preserved")
		}
	})
}

func TestLiveDebtAndSingleCallAllocAuditRegressions(t *testing.T) {
	t.Run("stream_event_data_cross_call_leak", func(t *testing.T) {
		source := `const leak = [];
export const meta = { apiVersion: 1, id: "acme.streamleak" };
export function transformStream(ctx, event) {
  leak.push(event.data);
  return {};
}`
		manifest := Manifest{
			ID: "acme.streamleak", Name: "streamleak", Version: "1.0.0", APIVersion: 1, HostAPI: 2,
			XLyra: ">=1.14.0", License: "AGPL-3.0",
			SHA256: map[string]string{"plugin.js": HashSource(source)},
		}
		plugin, err := NewPlugin(manifest, source, nil, 1)
		if err != nil {
			t.Fatalf("NewPlugin: %v", err)
		}
		payload := strings.Repeat("a", 1<<20)
		var hitErr error
		for i := 0; i < 24; i++ {
			_, _, _, err := plugin.CallTransformStream(context.Background(), StreamTransformContext{
				SiteType: "openai", Model: "gpt-4o", Protocol: "openai_chat",
			}, StreamTransformEvent{Data: payload})
			if err != nil {
				hitErr = err
				break
			}
		}
		var call *CallError
		if !errors.As(hitErr, &call) || call.Kind != KindHeapLimit || call.Interrupt != "live_limit" {
			t.Fatalf("error = %#v, want %s (live_limit)", hitErr, KindHeapLimit)
		}
		plugin.pool.mu.Lock()
		total, idle := plugin.pool.total, len(plugin.pool.idle)
		plugin.pool.mu.Unlock()
		if total != 0 || idle != 0 {
			t.Fatalf("pool after stream leak total=%d idle=%d, want discarded", total, idle)
		}
	})

	t.Run("inspect_segments_text_cross_call_leak_and_max_live_override", func(t *testing.T) {
		source := pipelineTestSource(`const leak = [];
export function inspectSegments(ctx, input) {
  leak.push(input.segments[0].text);
  return {};
}`)
		plugin, err := NewPlugin(pipelineTestManifest(source, nil), source, nil, 1)
		if err != nil {
			t.Fatalf("NewPlugin: %v", err)
		}
		// Lower MaxLiveBytes on the warm runtime to verify checkLiveLimit respects rt.MaxLiveBytes().
		targetPool := plugin.poolForHook(HookInspectSegments)
		targetPool.mu.Lock()
		if len(targetPool.idle) != 1 {
			targetPool.mu.Unlock()
			t.Fatalf("expected 1 warm session, got %d", len(targetPool.idle))
		}
		targetPool.idle[0].rt.SetMaxLiveBytes(1 << 20)
		targetPool.mu.Unlock()

		segText := strings.Repeat("x", 300<<10)
		var hitErr error
		for i := 0; i < 8; i++ {
			_, err := plugin.InspectPipelineSegments(context.Background(), PipelineInspection{
				Endpoint: "/v1/chat/completions",
				Segments: []PipelineSegment{{ID: fmt.Sprintf("s%d", i), Field: "message.text", Text: segText}},
				Findings: [][]textmatch.Span{nil},
			})
			if err != nil {
				hitErr = err
				break
			}
		}
		var call *CallError
		if !errors.As(hitErr, &call) || call.Kind != KindHeapLimit || call.Interrupt != "live_limit" {
			t.Fatalf("error = %#v, want %s (live_limit)", hitErr, KindHeapLimit)
		}
		targetPool.mu.Lock()
		total, idle := targetPool.total, len(targetPool.idle)
		targetPool.mu.Unlock()
		if total != 0 || idle != 0 {
			t.Fatalf("pool after inspect leak total=%d idle=%d, want discarded", total, idle)
		}
	})

	t.Run("parse_response_unmaterialized_go_map_leak", func(t *testing.T) {
		source := `export const meta = { apiVersion: 1, id: "acme.protoleak", kind: "protocol" };
const leak = [];
export function decodeRequest() { return { model: "m" }; }
export function buildRequest() { return { path: "/v1", payload: {} }; }
export function parseResponse(ctx, resp) {
  leak.push(resp);
  return { passthrough: true };
}`
		manifest := Manifest{
			ID: "acme.protoleak", Name: "protoleak", Version: "1.0.0", APIVersion: 1, HostAPI: 2,
			XLyra: ">=1.14.0", Kind: KindProtocol, License: "AGPL-3.0",
			Protocol: ProtocolSection{
				Name: "protoleak", DownstreamPath: "/v1/protoleak", EndpointType: "openai_chat",
				Method: "POST", Auth: "bearer",
			},
			SHA256: map[string]string{"plugin.js": HashSource(source)},
		}
		plugin, err := NewPlugin(manifest, source, nil, 1)
		if err != nil {
			t.Fatalf("NewPlugin: %v", err)
		}
		var hitErr error
		for i := 0; i < 8; i++ {
			bigBody := strings.Repeat("b", 4<<20)
			_, _, _, err := plugin.CallParseResponse(context.Background(), ProtocolBuildContext{}, ProtocolParseInput{
				Status:  200,
				Headers: map[string]string{"content-type": "application/json"},
				Body:    bigBody,
			})
			if err != nil {
				hitErr = err
				break
			}
		}
		var call *CallError
		if !errors.As(hitErr, &call) || call.Kind != KindHeapLimit || call.Interrupt != "live_limit" {
			t.Fatalf("error = %#v, want %s (live_limit)", hitErr, KindHeapLimit)
		}
		plugin.pool.mu.Lock()
		total, idle := plugin.pool.total, len(plugin.pool.idle)
		plugin.pool.mu.Unlock()
		if total != 0 || idle != 0 {
			t.Fatalf("pool after parseResponse leak total=%d idle=%d, want discarded", total, idle)
		}
	})

	t.Run("bytes_toHex_loop_hits_alloc_limit_in_single_call", func(t *testing.T) {
		source := wrapProbe(`
const u = new Uint8Array(4 * 1024 * 1024);
const leak = [];
for (let i = 0; i < 40; i++) {
  leak.push(bytes.toHex(u));
}
return { result: { kind: "balance", entries: [{ label: "n", remaining: leak.length }] } };`)
		plugin := mustTestPlugin(t, "xlyra.test.bomb", source)
		_, _, _, err := plugin.callHook(context.Background(), "probe", 5*time.Second, 0, ProbeContext{}.asMap(), []any{})
		var call *CallError
		if !errors.As(err, &call) || call.Kind != KindHeapLimit || call.Interrupt != "alloc_limit" {
			t.Fatalf("error = %#v, want %s (alloc_limit)", err, KindHeapLimit)
		}
		plugin.pool.mu.Lock()
		total, idle := plugin.pool.total, len(plugin.pool.idle)
		plugin.pool.mu.Unlock()
		if total != 0 || idle != 0 {
			t.Fatalf("pool after bytes.toHex alloc limit total=%d idle=%d, want discarded", total, idle)
		}
	})

	t.Run("storage_get_loop_hits_alloc_limit_in_single_call", func(t *testing.T) {
		source := `export const meta = { apiVersion: 1, id: "acme.storagealloc", kind: "quota_probe" };
export function probe() {
  const leak = [];
  for (let i = 0; i < 2000; i++) {
    leak.push(storage.get("big"));
  }
  return { result: { kind: "balance", entries: [{ label: "n", remaining: leak.length }] } };
}`
		manifest := testManifest("acme.storagealloc", source)
		manifest.HostAPI = 2
		manifest.Permissions = []string{PermissionStorage}
		plugin, err := NewPlugin(manifest, source, []Fixture{{Name: "unused"}}, 1)
		if err != nil {
			t.Fatalf("NewPlugin: %v", err)
		}
		mem, err := NewMemoryStorage(map[string]any{
			"big": map[string]any{"blob": strings.Repeat("s", 60000)},
		})
		if err != nil {
			t.Fatalf("NewMemoryStorage: %v", err)
		}
		_, _, _, err = plugin.callHook(WithCallStorage(context.Background(), mem), "probe", 5*time.Second, 0, ProbeContext{}.asMap(), []any{})
		var call *CallError
		if !errors.As(err, &call) || call.Kind != KindHeapLimit || call.Interrupt != "alloc_limit" {
			t.Fatalf("error = %#v, want %s (alloc_limit)", err, KindHeapLimit)
		}
		plugin.pool.mu.Lock()
		total, idle := plugin.pool.total, len(plugin.pool.idle)
		plugin.pool.mu.Unlock()
		if total != 0 || idle != 0 {
			t.Fatalf("pool after storage.get alloc limit total=%d idle=%d, want discarded", total, idle)
		}
	})

	t.Run("codec_base64_rejects_oversized_inputs", func(t *testing.T) {
		maxEnc := base64.StdEncoding.EncodedLen(maxBinaryInputBytes)
		source := fmt.Sprintf(`export const meta = { apiVersion: 1, id: "xlyra.test.b64cap", kind: "quota_probe" };
function expectThrow(fn, want) {
  try { fn(); } catch (e) {
    if (String(e).includes(want)) return null;
    return "got " + String(e) + ", want " + want;
  }
  return "did not throw " + want;
}
export function probe() {
  const over = "a".repeat(%d);
  const overB64 = "A".repeat(%d);
  const errs = [
    expectThrow(() => codec.base64Encode(over), "input exceeds"),
    expectThrow(() => codec.base64URLEncode(over), "input exceeds"),
    expectThrow(() => codec.base64Decode(overB64), "input exceeds"),
    expectThrow(() => codec.base64URLDecode(overB64), "input exceeds"),
  ];
  for (const msg of errs) {
    if (msg) return { error: msg };
  }
  return { result: { kind: "balance", entries: [{ label: "ok", remaining: 1 }] } };
}`, maxBinaryInputBytes+1, maxEnc+4)
		plugin := mustTestPlugin(t, "xlyra.test.b64cap", source)
		decision, err := plugin.CallProbe(context.Background(), ProbeContext{}, nil)
		if err != nil {
			t.Fatalf("CallProbe: %v", err)
		}
		if decision.Error != "" {
			t.Fatalf("probe error: %s", decision.Error)
		}
	})

	t.Run("compound_argToValue_enforces_alloc_budget", func(t *testing.T) {
		source := pipelineTestSource(`export function inspectSegments(ctx, input) { return {}; }`)
		plugin, err := NewPlugin(pipelineTestManifest(source, nil), source, nil, 1)
		if err != nil {
			t.Fatalf("NewPlugin: %v", err)
		}
		// 400k empty SegmentItems exceed the 64 MiB single-call MaxAllocBytes budget when
		// argToValue builds all items under one realm budget without wiping allocUsed.
		many := make([]SegmentItem, 400000)
		_, _, _, err = plugin.callHook(context.Background(), HookInspectSegments, protocolHookTimeout, 0,
			InspectSegmentsContext{Endpoint: "/v1/chat", Model: "m", Binding: InspectSegmentsBinding{ID: "b"}},
			InspectSegmentsInput{Segments: many},
		)
		var call *CallError
		if !errors.As(err, &call) || call.Kind != KindHeapLimit || call.Interrupt != "alloc_limit" {
			t.Fatalf("error = %#v, want %s (alloc_limit)", err, KindHeapLimit)
		}
	})
}

func TestBytesConcatHardeningAndDetachedTypeErrors(t *testing.T) {
	source := `export const meta = { apiVersion: 1, id: "xlyra.test.concat", kind: "quota_probe" };
function expectThrow(fn, want) {
  try {
    fn();
  } catch (e) {
    if (String(e).includes(want)) return null;
    return "got " + String(e) + ", want " + want;
  }
  return "did not throw " + want;
}
export function probe() {
  // 1. Sparse + Proxy prototype new Array(1e8) rejected immediately by maxConcatChunks.
  const empty = new Uint8Array(0);
  const sparse = new Array(1e8);
  Object.setPrototypeOf(sparse, new Proxy({}, { get() { return empty; } }));
  let msg = expectThrow(() => bytes.concat(sparse), "too many chunks");
  if (msg) return { error: "sparse: " + msg };

  // 2. Dense > maxConcatChunks empty-chunk array rejected immediately.
  const dense = new Array(65537).fill(empty);
  msg = expectThrow(() => bytes.concat(dense), "too many chunks");
  if (msg) return { error: "dense: " + msg };

  // 3. Getter detaching a[0] via transfer() during a[1] read preserves pre-detach snapshot.
  const abTransfer = new ArrayBuffer(5);
  new Uint8Array(abTransfer).set(bytes.fromUTF8("hello"));
  const arrTransfer = [];
  Object.defineProperty(arrTransfer, 0, { enumerable: true, get() { return new Uint8Array(abTransfer); } });
  Object.defineProperty(arrTransfer, 1, {
    enumerable: true,
    get() {
      abTransfer.transfer();
      return bytes.fromUTF8("!");
    }
  });
  const outTransfer = bytes.concat(arrTransfer);
  if (!abTransfer.detached) return { error: "expected abTransfer detached" };
  if (bytes.toUTF8(outTransfer) !== "hello!") {
    return { error: "transfer snapshot got " + bytes.toUTF8(outTransfer) };
  }

  // 4. Getter shrinking resizable a[0] via resize(0) during a[1] read preserves pre-shrink snapshot.
  const abResize = new ArrayBuffer(5, { maxByteLength: 16 });
  new Uint8Array(abResize).set(bytes.fromUTF8("world"));
  const arrResize = [];
  Object.defineProperty(arrResize, 0, { enumerable: true, get() { return new Uint8Array(abResize); } });
  Object.defineProperty(arrResize, 1, {
    enumerable: true,
    get() {
      abResize.resize(0);
      return bytes.fromUTF8("?");
    }
  });
  const outResize = bytes.concat(arrResize);
  if (abResize.byteLength !== 0) return { error: "expected abResize shrunk to 0" };
  if (bytes.toUTF8(outResize) !== "world?") {
    return { error: "resize snapshot got " + bytes.toUTF8(outResize) };
  }

  // 5. Detached non-Uint8Array views/buffers return type errors, NOT "detached byte buffer".
  const abDet = new ArrayBuffer(8);
  const detU8 = new Uint8Array(abDet);
  const detU16 = new Uint16Array(abDet);
  const detClamped = new Uint8ClampedArray(abDet);
  const detDV = new DataView(abDet);
  abDet.transfer();

  // Also test out-of-bounds resizable Uint8Array -> "detached byte buffer".
  const rabOOB = new ArrayBuffer(8, { maxByteLength: 16 });
  const oobU8 = new Uint8Array(rabOOB, 4, 4);
  rabOOB.resize(2);

  const checks = [
    expectThrow(() => bytes.toHex(detU8), "detached byte buffer"),
    expectThrow(() => bytes.concat([detU8]), "chunks[0]: detached byte buffer"),
    expectThrow(() => crypto.sha256(detU8), "detached byte buffer"),
    expectThrow(() => bytes.toHex(oobU8), "detached byte buffer"),
    expectThrow(() => bytes.concat([oobU8]), "chunks[0]: detached byte buffer"),
    expectThrow(() => crypto.sha256(oobU8), "detached byte buffer"),

    expectThrow(() => bytes.toHex(detU16), "expected Uint8Array"),
    expectThrow(() => bytes.toHex(detClamped), "expected Uint8Array"),
    expectThrow(() => bytes.toHex(abDet), "expected Uint8Array"),
    expectThrow(() => bytes.toHex(detDV), "expected Uint8Array"),

    expectThrow(() => bytes.concat([detU16]), "chunks[0]: expected Uint8Array"),
    expectThrow(() => bytes.concat([detClamped]), "chunks[0]: expected Uint8Array"),
    expectThrow(() => bytes.concat([abDet]), "chunks[0]: expected Uint8Array"),
    expectThrow(() => bytes.concat([detDV]), "chunks[0]: expected Uint8Array"),

    expectThrow(() => crypto.sha256(detU16), "expected string or Uint8Array"),
    expectThrow(() => crypto.sha256(detClamped), "expected string or Uint8Array"),
    expectThrow(() => crypto.sha256(abDet), "expected string or Uint8Array"),
    expectThrow(() => crypto.sha256(detDV), "expected string or Uint8Array"),
  ];
  for (const errMsg of checks) {
    if (errMsg) return { error: errMsg };
  }
  return { result: { kind: "balance", entries: [{ label: "ok", remaining: 1 }] } };
}`
	plugin := mustTestPlugin(t, "xlyra.test.concat", source)
	started := time.Now()
	decision, err := plugin.CallProbe(context.Background(), ProbeContext{}, nil)
	if err != nil {
		t.Fatalf("CallProbe: %v", err)
	}
	if decision.Error != "" {
		t.Fatalf("probe error: %s", decision.Error)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatalf("bytes.concat hardening test took too long: %s", time.Since(started))
	}
}

func TestPoolWaitLimitIsExhausted(t *testing.T) {
	pool, held := heldPool(t)
	pool.wait = 30 * time.Millisecond
	before := runtimeGoroutines()
	_, err := pool.Acquire(context.Background())
	var call *CallError
	if !errors.As(err, &call) || call.Kind != KindPoolExhausted {
		t.Fatalf("error = %v, want %s", err, KindPoolExhausted)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("pool exhaustion must not unwrap to a context error")
	}
	pool.Release(held, true)
	time.Sleep(20 * time.Millisecond)
	if after := runtimeGoroutines(); after > before+2 {
		t.Fatalf("goroutines before %d after %d", before, after)
	}
}

func TestPoolWaitReturnsCallerContextError(t *testing.T) {
	pool, held := heldPool(t)
	defer pool.Release(held, true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := pool.Acquire(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want the caller deadline", err)
	}
	var call *CallError
	if errors.As(err, &call) {
		t.Fatalf("caller deadline was wrapped as %s", call.Kind)
	}
}

func TestCallProbeReportsPoolExhausted(t *testing.T) {
	plugin := mustTestPlugin(t, "xlyra.test.bomb", wrapProbe(`return { result: { kind: "balance", entries: [{ label: "balance", remaining: 1 }] } };`))
	plugin.pool.max = 1
	plugin.pool.wait = 30 * time.Millisecond
	held, err := plugin.pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer plugin.pool.Release(held, true)
	_, err = plugin.CallProbe(context.Background(), ProbeContext{}, nil)
	var call *CallError
	if !errors.As(err, &call) || call.Kind != KindPoolExhausted || call.PluginID != "xlyra.test.bomb" {
		t.Fatalf("error = %#v, want annotated %s", err, KindPoolExhausted)
	}
}

func TestPluginLogIsBounded(t *testing.T) {
	source := wrapProbe(`
  log.info("short", { blob: "x".repeat(4096) });
  log.warn("kept", { step: 1 });
  for (let i = 0; i < 60; i++) log.debug("line " + i);
  return { result: { kind: "balance", entries: [{ label: "balance", remaining: 1 }] } };`)
	plugin := mustTestPlugin(t, "xlyra.test.bomb", source)
	decision, err := plugin.CallProbe(context.Background(), ProbeContext{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.Logs) != maxPluginLogs || decision.DroppedLogs != 12 {
		t.Fatalf("logs = %d dropped = %d", len(decision.Logs), decision.DroppedLogs)
	}
	big := decision.Logs[0]
	if big.Fields != nil || len(big.Message) > maxPluginLogBytes || !strings.HasPrefix(big.Message, `short {"blob":"xxx`) {
		t.Fatalf("oversized entry kept %d fields, message %d bytes", len(big.Fields), len(big.Message))
	}
	kept := decision.Logs[1]
	if kept.Level != "warn" || kept.Message != "kept" || kept.Fields["step"] == nil {
		t.Fatalf("small entry = %#v", kept)
	}
}

func TestClipUTF8BytesStaysWithinLimit(t *testing.T) {
	for _, value := range []string{strings.Repeat("中", 400), strings.Repeat("a", 2000), "short"} {
		got := clipUTF8Bytes(value, maxPluginLogBytes)
		if len(got) > maxPluginLogBytes || !utf8.ValidString(got) {
			t.Fatalf("clip %d bytes -> %d bytes valid=%v", len(value), len(got), utf8.ValidString(got))
		}
	}
}

func heldPool(t *testing.T) (*pool, *session) {
	t.Helper()
	program, err := compileProgram("pool.js", wrapProbe(`return { result: { kind: "balance", entries: [{ label: "balance", remaining: 1 }] } };`), "probe")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := newPool(0, 1, func() (*session, error) { return newSession(program) })
	if err != nil {
		t.Fatal(err)
	}
	held, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return pool, held
}

func TestRuntimeStateIsNotShared(t *testing.T) {
	source := `let n = 0;
export const meta = { apiVersion: 1, id: "xlyra.test.state", kind: "quota_probe" };
export function probe() {
  n += 1;
  return { result: { kind: "balance", entries: [{ label: "balance", remaining: n }] } };
}`
	plugin := mustTestPlugin(t, "xlyra.test.state", source)
	program := plugin.program
	first, err := newSession(program)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newSession(program)
	if err != nil {
		t.Fatal(err)
	}
	probe := program.hooks["probe"]
	one, _, _, _, err := first.call(context.Background(), time.Second, probe, []any{map[string]any{}, []any{}})
	if err != nil {
		t.Fatal(err)
	}
	two, _, _, _, err := second.call(context.Background(), time.Second, probe, []any{map[string]any{}, []any{}})
	if err != nil {
		t.Fatal(err)
	}
	again, _, _, _, err := first.call(context.Background(), time.Second, probe, []any{map[string]any{}, []any{}})
	if err != nil {
		t.Fatal(err)
	}
	if remaining(one) != 1 || remaining(two) != 1 || remaining(again) != 2 {
		t.Fatalf("runtimes leaked state: first %v second %v again %v", remaining(one), remaining(two), remaining(again))
	}
}

func remaining(value any) float64 {
	object, _ := value.(map[string]any)
	result, _ := object["result"].(map[string]any)
	entries, _ := result["entries"].([]any)
	entry, _ := entries[0].(map[string]any)
	switch n := entry["remaining"].(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	default:
		return -1
	}
}

func wrapProbe(body string) string {
	return `export const meta = { apiVersion: 1, id: "xlyra.test.bomb", kind: "quota_probe" };
export function probe() { ` + body + ` }`
}

func runtimeGoroutines() int { return runtime.NumGoroutine() }

func TestHookTimeoutDiscardsRuntime(t *testing.T) {
	source := `export const meta = { apiVersion: 1, id: "xlyra.test.loop", kind: "quota_probe" };
export function probe() { for (;;) {} }`
	plugin := mustTestPlugin(t, "xlyra.test.loop", source)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := plugin.CallProbe(ctx, ProbeContext{Now: 1}, nil)
	if err == nil || !strings.Contains(err.Error(), KindTimeout) {
		t.Fatalf("error = %v, want timeout", err)
	}
	plugin.pool.mu.Lock()
	total := plugin.pool.total
	idle := len(plugin.pool.idle)
	plugin.pool.mu.Unlock()
	if total != 0 || idle != 0 {
		t.Fatalf("pool after interrupt total=%d idle=%d, want the runtime discarded", total, idle)
	}
}

func TestPromiseHookIsShapeError(t *testing.T) {
	source := `export const meta = { apiVersion: 1, id: "xlyra.test.promise", kind: "quota_probe" };
export async function probe() { return { result: { kind: "balance", entries: [] } }; }`
	plugin := mustTestPlugin(t, "xlyra.test.promise", source)
	_, err := plugin.CallProbe(context.Background(), ProbeContext{}, nil)
	if err == nil || !strings.Contains(err.Error(), KindShape) {
		t.Fatalf("error = %v, want shape error", err)
	}
}

func TestEvalAndImportAreRejected(t *testing.T) {
	evalSource := `export const meta = { apiVersion: 1, id: "xlyra.test.eval", kind: "quota_probe" };
export function probe() { return eval("1"); }`
	plugin := mustTestPlugin(t, "xlyra.test.eval", evalSource)
	_, err := plugin.CallProbe(context.Background(), ProbeContext{}, nil)
	if err == nil || !strings.Contains(err.Error(), "DisableDynamicCode") {
		t.Fatalf("eval error = %v", err)
	}

	importSource := `import "other.js";
export const meta = { apiVersion: 1, id: "xlyra.test.import", kind: "quota_probe" };
export function probe() { return { result: { kind: "balance", entries: [] } }; }`
	manifest := testManifest("xlyra.test.import", importSource)
	if _, err := NewPlugin(manifest, importSource, []Fixture{{Name: "unused"}}, 0); err == nil {
		t.Fatal("static import was accepted")
	}
}

func TestHostFunctions(t *testing.T) {
	source := `export const meta = { apiVersion: 1, id: "xlyra.test.host", kind: "quota_probe" };
export function probe() {
  if (codec.base64Decode(codec.base64Encode("hi")) !== "hi") return { error: "base64" };
  if (codec.base64URLDecode(codec.base64URLEncode("hi")) !== "hi") return { error: "base64url" };
  const mac = crypto.hmacSHA256("key", "msg");
  if (typeof mac !== "string" || mac.length < 8) return { error: "hmac" };
  const digest = crypto.sha256("hi");
  const token = jwt.signHS256({ sub: "a" }, "secret");
  const payload = jwt.decodeHS256(token, "secret");
  if (!payload || payload.sub !== "a") return { error: "jwt" };
  try { jwt.decodeHS256(token, "other"); return { error: "jwt accepted a bad secret" }; } catch (e) {}
  if (typeof utils.uuid() !== "string") return { error: "uuid" };
  log.info("probe ok");
  return { result: { kind: "balance", plan: digest, entries: [{ label: "balance", remaining: 1 }] } };
}`
	plugin := mustTestPlugin(t, "xlyra.test.host", source)
	decision, err := plugin.CallProbe(context.Background(), ProbeContext{}, nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if decision.Result == nil || decision.Result.Plan == "" {
		t.Fatalf("decision = %#v", decision)
	}
	sum, err := sha256Hex("hi")
	if err != nil || decision.Result.Plan != sum {
		t.Fatalf("sha256 plan = %q, want %q (%v)", decision.Result.Plan, sum, err)
	}
}

func TestMoejsImportOnlyInEngine(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(body)
		if strings.Contains(text, "github.com/Calcium-Ion/moejs") {
			t.Errorf("%s imports Calcium-Ion/moejs", path)
		}
		if strings.Contains(text, "github.com/Yachiiiiyo/moejs") && filepath.Base(path) != "engine.go" {
			t.Errorf("%s imports moejs; only engine.go may do that", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mod), "Calcium-Ion/moejs") {
		t.Fatal("go.mod tracks Calcium-Ion/moejs")
	}
	if !strings.Contains(string(mod), "github.com/Yachiiiiyo/moejs") {
		t.Fatal("go.mod does not require the fork")
	}
	if strings.Contains(string(mod), "replace github.com/Yachiiiiyo/moejs") {
		t.Fatal("go.mod replaces the fork; require a published tag instead")
	}
}

func mustTestPlugin(t *testing.T, id, source string) *Plugin {
	t.Helper()
	// Pool behavior tests assume a single warm runtime unless they configure the pool further.
	plugin, err := NewPlugin(testManifest(id, source), source, []Fixture{{Name: "unused"}}, 1)
	if err != nil {
		t.Fatalf("new plugin: %v", err)
	}
	return plugin
}

func testManifest(id, source string) Manifest {
	return Manifest{
		ID: id, Name: id, Version: "1.0.0", APIVersion: 1, HostAPI: 1,
		XLyra: ">=1.14.0", Kind: "quota_probe", License: "AGPL-3.0",
		QuotaProbe: QuotaProbeSection{BaseURLMode: "as_is"},
		SHA256:     map[string]string{"plugin.js": HashSource(source)},
	}
}
