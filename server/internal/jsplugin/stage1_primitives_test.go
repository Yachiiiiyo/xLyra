package jsplugin

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func TestStage1BytesEncodingAndCryptoPrimitives(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))

	source := `export const meta = { apiVersion: 1, id: "acme.crypto", kind: "quota_probe" };
export function probe(ctx) {
  const u8 = bytes.fromUTF8("hello 密钥");
  if (!(u8 instanceof Uint8Array)) return { error: "not Uint8Array" };
  if (bytes.toUTF8(u8) !== "hello 密钥") return { error: "utf8 roundtrip" };

  const hex = bytes.toHex(u8);
  const fromHex = bytes.fromHex(hex);
  if (!bytes.equal(u8, fromHex)) return { error: "hex roundtrip" };

  const b64 = bytes.toBase64(u8);
  if (!bytes.equal(u8, bytes.fromBase64(b64))) return { error: "b64 roundtrip" };

  const b64u = bytes.toBase64URL(u8);
  if (!bytes.equal(u8, bytes.fromBase64URL(b64u))) return { error: "b64url roundtrip" };

  const joined = bytes.concat([bytes.fromUTF8("ab"), bytes.fromUTF8("cd")]);
  if (encoding.utf8Decode(joined) !== "abcd") return { error: "concat" };
  if (encoding.hexEncode("abc") !== "616263") return { error: "encHexEncode" };
  if (encoding.utf8Decode(encoding.hexDecode("616263")) !== "abc") return { error: "encHexDecode" };
  if (encoding.utf8Decode(encoding.base64Decode(encoding.base64Encode("xyz"))) !== "xyz") return { error: "encB64" };
  if (encoding.utf8Decode(encoding.base64URLDecode(encoding.base64URLEncode("xyz?"))) !== "xyz?") return { error: "encB64URL" };

  if (crypto.md5("abc") !== "900150983cd24fb0d6963f7d28e17f72") return { error: "md5" };
  if (crypto.sha1("abc") !== "a9993e364706816aba3e25717850c26c9cd0d89d") return { error: "sha1" };
  if (crypto.sha256("abc") !== "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad") return { error: "sha256" };
  if (crypto.sha256("abc", "base64") !== "ungWv48Bz+pBQUDeXa4iI7ADYaOWF3qctBD/YfIAFa0=") return { error: "sha256 b64" };
  if (crypto.sha512("abc").length !== 128) return { error: "sha512" };

  const dBytes = crypto.digestBytes("sha256", "abc");
  if (!(dBytes instanceof Uint8Array) || bytes.toHex(dBytes) !== crypto.sha256("abc")) return { error: "digestBytes" };

  const hHex = crypto.hmac("sha256", "key", "msg", "hex");
  const hBytes = crypto.hmacBytes("sha256", "key", "msg");
  if (bytes.toHex(hBytes) !== hHex) return { error: "hmacBytes" };
  if (bytes.toBase64(hBytes) !== crypto.hmacSHA256("key", "msg")) return { error: "hmac compat" };

  const rnd = crypto.randomBytes(24);
  if (!(rnd instanceof Uint8Array) || rnd.length !== 24) return { error: "randomBytes" };

  // AES-256-GCM with AAD
  const key32 = bytes.fromUTF8("0123456789abcdef0123456789abcdef");
  const iv12 = bytes.fromUTF8("0123456789ab");
  const aad = bytes.fromUTF8("header-auth");
  const gcmCipher = crypto.aesEncrypt("aes-256-gcm", key32, iv12, "secret payload", aad);
  const gcmPlain = crypto.aesDecrypt("aes-256-gcm", key32, iv12, gcmCipher, aad);
  if (bytes.toUTF8(gcmPlain) !== "secret payload") return { error: "aes-256-gcm" };

  // AES-128-CBC with PKCS#7
  const key16 = bytes.fromUTF8("0123456789abcdef");
  const iv16 = bytes.fromUTF8("fedcba9876543210");
  const cbcCipher = crypto.aesEncrypt("aes-128-cbc", key16, iv16, "cbc message");
  const cbcPlain = crypto.aesDecrypt("aes-128-cbc", key16, iv16, cbcCipher);
  if (bytes.toUTF8(cbcPlain) !== "cbc message") return { error: "aes-128-cbc" };

  // RSA OAEP-SHA256, OAEP-SHA1, and PKCS1v15
  const rsaOAEP256 = bytes.toBase64(crypto.rsaEncrypt(ctx.settings.rsaPub, "oaep256-msg", "oaep-sha256"));
  const rsaOAEP1 = bytes.toBase64(crypto.rsaEncrypt(ctx.settings.rsaPub, "oaep1-msg", "oaep-sha1"));
  const rsaPKCS1 = bytes.toBase64(crypto.rsaEncrypt(ctx.settings.rsaPub, "pkcs1-msg", "pkcs1v15"));

  return {
    result: {
      kind: "balance",
      plan: rsaOAEP256 + "|" + rsaOAEP1 + "|" + rsaPKCS1,
      entries: [{ label: "balance", remaining: 1 }]
    }
  };
}`
	manifest := testManifest("acme.crypto", source)
	manifest.HostAPI = 2
	manifest.Settings = []SettingSpec{
		{Key: "rsaPub", Type: SettingTypeString, Default: pubPEM},
	}
	plugin, err := NewPlugin(manifest, source, []Fixture{{Name: "unused"}}, 1)
	if err != nil {
		t.Fatalf("NewPlugin: %v", err)
	}
	decision, err := plugin.CallProbe(context.Background(), ProbeContext{Now: 1}, nil)
	if err != nil {
		t.Fatalf("CallProbe: %v", err)
	}
	if decision.Error != "" {
		t.Fatalf("probe returned error: %s", decision.Error)
	}
	parts := strings.Split(decision.Result.Plan, "|")
	if len(parts) != 3 {
		t.Fatalf("plan = %q", decision.Result.Plan)
	}
	cipher256, err := bytesFromBase64(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	plain256, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, privKey, cipher256, nil)
	if err != nil || string(plain256) != "oaep256-msg" {
		t.Fatalf("OAEP-SHA256 decrypt = %q, err = %v", plain256, err)
	}
	cipher1, err := bytesFromBase64(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	plain1, err := rsa.DecryptOAEP(sha1.New(), rand.Reader, privKey, cipher1, nil)
	if err != nil || string(plain1) != "oaep1-msg" {
		t.Fatalf("OAEP-SHA1 decrypt = %q, err = %v", plain1, err)
	}
	cipherPKCS1, err := bytesFromBase64(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	plainPKCS1, err := rsa.DecryptPKCS1v15(rand.Reader, privKey, cipherPKCS1)
	if err != nil || string(plainPKCS1) != "pkcs1-msg" {
		t.Fatalf("PKCS1v15 decrypt = %q, err = %v", plainPKCS1, err)
	}
}

func TestStage1StoragePermissionQuotaAndSecretLeakGuard(t *testing.T) {
	source := `export const meta = { apiVersion: 1, id: "acme.storage", kind: "quota_probe" };
export function probe(ctx) {
  if (ctx.baseURL === "https://leak.example") {
    storage.set("tok", { leaked: "super-secret-token-value" });
    return { result: { kind: "balance", entries: [{ label: "ok", remaining: 1 }] } };
  }
  const prev = storage.get("counter") || 0;
  storage.set("counter", prev + 1);
  storage.set("temp", "remove-me");
  storage.delete("temp");
  const keys = storage.keys();
  return {
    result: {
      kind: "balance",
      plan: keys.join(","),
      entries: [{ label: "balance", remaining: storage.get("counter") }]
    }
  };
}`
	// 1. Without "storage" permission -> throws permission_denied
	noPermManifest := testManifest("acme.storage", source)
	noPermManifest.HostAPI = 2
	noPermPlugin, err := NewPlugin(noPermManifest, source, []Fixture{{Name: "unused"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	mem, _ := NewMemoryStorage(nil)
	_, err = noPermPlugin.CallProbe(WithCallStorage(context.Background(), mem), ProbeContext{}, nil)
	if err == nil || !strings.Contains(err.Error(), "permission_denied") {
		t.Fatalf("expected permission_denied without storage permission, got %v", err)
	}

	// 2. With "storage" permission -> get/set/delete/keys work
	withPermManifest := noPermManifest
	withPermManifest.Permissions = []string{"storage"}
	plugin, err := NewPlugin(withPermManifest, source, []Fixture{{Name: "unused"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithCallStorage(context.Background(), mem)
	d1, err := plugin.CallProbe(ctx, ProbeContext{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d1.Result == nil || d1.Result.Plan != "counter" || *d1.Result.Entries[0].Remaining != 1 {
		t.Fatalf("first call = %#v", d1.Result)
	}
	d2, err := plugin.CallProbe(ctx, ProbeContext{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d2.Result == nil || *d2.Result.Entries[0].Remaining != 2 {
		t.Fatalf("second call = %#v", d2.Result)
	}

	// 3. SecretBag leak guard rejects storing any value containing a secret
	bag := NewSecretBag()
	bag.Set("access_token", "super-secret-token-value")
	leakCtx := WithCallSecretBag(ctx, bag)
	_, err = plugin.CallProbe(leakCtx, ProbeContext{BaseURL: "https://leak.example"}, nil)
	if err == nil || !strings.Contains(err.Error(), "secret_leak_rejected") {
		t.Fatalf("expected secret_leak_rejected, got %v", err)
	}

	// 4. Key count limit (64) & byte capacity limit (64 KiB)
	limitStore, _ := NewMemoryStorage(nil)
	for i := 0; i < MaxStorageKeysPerScope; i++ {
		if err := limitStore.Set(context.Background(), "k"+string(rune('A'+i/26))+string(rune('a'+i%26)), i); err != nil {
			t.Fatalf("set key %d: %v", i, err)
		}
	}
	if err := limitStore.Set(context.Background(), "extra-key", 1); err == nil {
		t.Fatal("expected 65th key to be rejected")
	}
	byteStore, _ := NewMemoryStorage(nil)
	if err := byteStore.Set(context.Background(), "huge", strings.Repeat("x", MaxStorageBytesPerScope)); err == nil {
		t.Fatal("expected oversized value to be rejected")
	}
}

func TestStage1SettingsResolutionAndValidation(t *testing.T) {
	minVal, maxVal := 1.0, 10.0
	specs := []SettingSpec{
		{Key: "clientVersion", Type: SettingTypeString, Default: "1.0.0", Pattern: `^[0-9.]{1,16}$`},
		{Key: "retries", Type: SettingTypeNumber, Default: 3.0, Min: &minVal, Max: &maxVal},
		{Key: "strictMode", Type: SettingTypeBoolean, Default: false},
		{Key: "channel", Type: SettingTypeString, Default: "stable", Options: []string{"stable", "beta"}},
	}
	if err := validateSettingSpecs(specs); err != nil {
		t.Fatalf("validateSettingSpecs: %v", err)
	}
	if _, err := ValidateSettingValues(specs, map[string]any{"clientVersion": "bad-version!"}); err == nil {
		t.Fatal("expected pattern mismatch to fail")
	}
	if _, err := ValidateSettingValues(specs, map[string]any{"retries": 99}); err == nil {
		t.Fatal("expected max bound violation to fail")
	}
	if _, err := ValidateSettingValues(specs, map[string]any{"unknownKey": "x"}); err == nil {
		t.Fatal("expected unknown setting key to fail")
	}

	source := `export const meta = { apiVersion: 1, id: "acme.settings", kind: "quota_probe" };
export function probe(ctx) {
  const s = ctx.settings;
  return {
    result: {
      kind: "balance",
      plan: s.clientVersion + ":" + s.channel + ":" + String(s.strictMode),
      entries: [{ label: "retries", remaining: s.retries }]
    }
  };
}`
	manifest := testManifest("acme.settings", source)
	manifest.HostAPI = 2
	manifest.Settings = specs
	plugin, err := NewPlugin(manifest, source, []Fixture{{Name: "unused"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	plugin.SetGlobalSettings(map[string]any{"clientVersion": "2.1.0", "retries": 5})

	// Global overrides apply over defaults
	d1, err := plugin.CallProbe(context.Background(), ProbeContext{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d1.Result.Plan != "2.1.0:stable:false" || *d1.Result.Entries[0].Remaining != 5 {
		t.Fatalf("global settings result = %#v", d1.Result)
	}

	// Site overrides apply over global overrides
	siteCtx := WithSiteSettings(context.Background(), map[string]any{"channel": "beta", "strictMode": true, "retries": 8})
	d2, err := plugin.CallProbe(siteCtx, ProbeContext{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d2.Result.Plan != "2.1.0:beta:true" || *d2.Result.Entries[0].Remaining != 8 {
		t.Fatalf("site override result = %#v", d2.Result)
	}
}

func TestMixedPluginIsolatesHotAndColdPools(t *testing.T) {
	plugin := providerWith(t, siteProviderHookSource, nil)
	if !plugin.HasSeparateHotPool() {
		t.Fatal("expected site_provider with both probe and rewritePayload to allocate separate hot and cold pools")
	}
	// Exhaust the cold pool completely
	plugin.pool.max = 1
	plugin.pool.wait = 20 * time.Millisecond
	heldCold, err := plugin.pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer plugin.pool.Release(heldCold, true)

	// Cold-path probe must report pool exhausted
	if _, err := plugin.CallProbe(context.Background(), ProbeContext{}, nil); err == nil || !strings.Contains(err.Error(), KindPoolExhausted) {
		t.Fatalf("cold CallProbe error = %v, want %s", err, KindPoolExhausted)
	}

	// Hot-path rewritePayload and classify must still succeed immediately on hotPool!
	rewritten, err := plugin.CallRewritePayload(context.Background(), "rewritePayload", RewritePayloadContext{
		SiteType: "acme.example/example", Model: "m1", Stream: true, Protocol: "openai_chat",
	}, map[string]any{"model": "m1", "stream": true})
	if err != nil || rewritten["extra"] != "yes" {
		t.Fatalf("hot CallRewritePayload failed while cold pool was exhausted: %v (%#v)", err, rewritten)
	}
	verdict, err := plugin.CallErrorClassifyHook(context.Background(), "classify", ErrorClassifyContext{SiteType: "acme.example/example"}, ErrorClassifyInput{Status: 429})
	if err != nil || verdict.Class != "limited" {
		t.Fatalf("hot CallErrorClassifyHook failed while cold pool was exhausted: %v (%#v)", err, verdict)
	}
}

func TestSelfTestSupportsAccountScheduleAndStorageFixtures(t *testing.T) {
	source := meta("acme.example", KindSiteProvider) + `
export function startLogin(ctx, steps) {
  return { result: { kind: "account_login", status: "complete" } };
}
export function pollLogin(ctx, steps) {
  return { result: { kind: "account_login", status: "complete" } };
}
export function refreshTokens(ctx, steps) {
  return { result: { kind: "account_refresh", status: "ok" } };
}
export function summarizeAccount(ctx, steps) {
  return { result: { kind: "account_summary", status: "ok", entries: [] } };
}
export function onSchedule(ctx, steps) {
  if (steps.length === 0) {
    return { request: { method: "POST", path: "/v1/checkin" } };
  }
  const count = (storage.get("checkins") || 0) + 1;
  storage.set("checkins", count);
  return { result: { ok: true, count, version: ctx.settings.clientVersion } };
}
`
	manifest := poolManifest(source, poolSection())
	manifest.HostAPI = 2
	manifest.Permissions = []string{"storage", "schedule"}
	manifest.Settings = []SettingSpec{
		{Key: "clientVersion", Type: SettingTypeString, Default: "1.0.0"},
	}
	fixtures := []Fixture{
		{
			Name: "start login", File: "fixtures/start-login.json", Hook: "startLogin",
			Ctx: fixtureContext{SiteType: "acme.example/pool", BaseURL: "https://api.example.com", Now: 1000, Params: map[string]string{"tenant": "acme"}},
			Expect: fixtureExpectation{
				Result: map[string]any{"kind": "account_login", "status": "complete"},
			},
		},
		{
			Name: "refresh tokens", File: "fixtures/refresh.json", Hook: "refreshTokens",
			Ctx: fixtureContext{SiteType: "acme.example/pool", BaseURL: "https://api.example.com", Now: 1000, Fields: map[string]string{"email": "a@example.com"}},
			Expect: fixtureExpectation{
				Result: map[string]any{"kind": "account_refresh", "status": "ok"},
			},
		},
		{
			Name: "summarize account", File: "fixtures/summarize.json", Hook: "summarizeAccount",
			Ctx: fixtureContext{SiteType: "acme.example/pool", BaseURL: "https://api.example.com", Now: 1000},
			Expect: fixtureExpectation{
				Result: map[string]any{"kind": "account_summary", "status": "ok"},
			},
		},
		{
			Name: "schedule checkin with storage and settings", File: "fixtures/schedule.json", Hook: "onSchedule",
			Ctx:     fixtureContext{SiteType: "acme.example/pool", BaseURL: "https://api.example.com", Now: 1000, Settings: map[string]any{"clientVersion": "3.2.1"}},
			Storage: map[string]any{"checkins": 4},
			Responses: []FixtureResponse{{
				Match: FixtureMatch{Method: "POST", Path: "/v1/checkin"}, Status: 200, JSON: map[string]any{"ok": true},
			}},
			Expect: fixtureExpectation{
				Requests: []FixtureMatch{{Method: "POST", Path: "/v1/checkin"}},
				Result:   map[string]any{"ok": true, "count": 5.0, "version": "3.2.1"},
				Storage:  map[string]any{"checkins": 5.0},
			},
		},
	}
	plugin, err := NewPlugin(manifest, source, fixtures, 1)
	if err != nil {
		t.Fatalf("NewPlugin: %v", err)
	}
	if err := plugin.SelfTest(context.Background()); err != nil {
		t.Fatalf("SelfTest: %v", err)
	}
}
