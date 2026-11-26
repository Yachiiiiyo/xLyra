package jsplugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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
