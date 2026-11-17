package gateway

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"xlyra/server/internal/jsplugin"
	routeengine "xlyra/server/internal/router"
	"xlyra/server/internal/site"
)

func TestMountedPluginRewritesBuiltinRequest(t *testing.T) {
	site.ClearPluginSiteTypes()
	t.Cleanup(site.ClearPluginSiteTypes)
	source := kindlessChainSource("overlay.example", `
export function signRequest(ctx, req) {
  return { headers: { "X-Signed": ctx.sessionKey ? "yes" : "no" } };
}`)
	plugin := mustChainPlugin(t, "overlay.example", source, nil)
	restore := installChainPlugins(t, plugin)
	t.Cleanup(restore)

	var mu sync.Mutex
	var first, second http.Header
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		if hits == 1 {
			first = r.Header.Clone()
		} else {
			second = r.Header.Clone()
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(server.Close)

	handler, candidate := pluginUpstreamFixture(t, "openai", server.URL, nil, true, server.Client(), 1)
	candidate.Site.PluginIDs = []string{plugin.Manifest.ID}
	handler.cacheObservationKey = []byte("session-secret")
	request := gatewayRequest{DownstreamPath: gatewayEndpointChatCompletions, Payload: map[string]any{"prompt_cache_key": "chat-1"}}
	for i := 0; i < 2; i++ {
		result := handler.forwardGatewayRequest(context.Background(), httptest.NewRecorder(), "req-overlay", 1, uuid.New(), uuid.New(), candidate, request, nil, &testGatewayProtocol{})
		if !result.success {
			t.Fatalf("attempt %d type=%s message=%s", i, result.errorType, result.errorMessage)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if first.Get("X-Trace") == "" || len(first.Get("X-Trace")) != 40 || first.Get("X-Trace") == second.Get("X-Trace") {
		t.Fatalf("random header first=%q second=%q", first.Get("X-Trace"), second.Get("X-Trace"))
	}
	if first.Get("X-Signed") != "yes" || first.Get("X-Session") == "" || first.Get("X-Session") != second.Get("X-Session") {
		t.Fatalf("headers:\n%s", formatHeaders(first))
	}
}

func TestRequestChainOrderAndSignSeesPrevious(t *testing.T) {
	site.ClearPluginSiteTypes()
	t.Cleanup(site.ClearPluginSiteTypes)
	base := mustChainPlugin(t, "base.example", siteTypeChainSource("base.example"), func(m *jsplugin.Manifest) {
		m.SiteProvider.Hosts = []string{"api.base.example"}
		m.SiteProvider.Protocol.OfficialBaseURL = "https://api.base.example"
		m.Request = jsplugin.SiteProviderRequest{Headers: jsplugin.HeaderMap(map[string]string{"X-Base": "base", "X-Chain": "base"})}
	})
	first := mustChainPlugin(t, "one.example", kindlessChainSource("one.example", `
export function signRequest() { return { headers: { "X-Chain": "one" } }; }`), nil)
	second := mustChainPlugin(t, "two.example", kindlessChainSource("two.example", `
export function signRequest(ctx, req) {
  return { headers: { "X-Chain": "two", "X-Saw": req.headers["X-Chain"] || "" } };
}`), nil)
	restore := installChainPlugins(t, base, first, second)
	t.Cleanup(restore)
	if err := site.SetPluginSiteTypes([]site.PluginSiteType{{
		Descriptor: site.SiteTypeDescriptor{SiteType: "base.example/chat", DisplayName: "Base"},
		PluginID:   base.Manifest.ID,
		Enabled:    true,
		Hosts:      []string{"api.base.example"},
		Request:    site.PluginSiteRequest{Headers: base.Manifest.Request.Headers},
	}}); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, "https://api.base.example/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	candidate := routeengine.Candidate{
		Site:  routeengine.CandidateSite{SiteType: "base.example/chat", PluginIDs: []string{first.Manifest.ID, second.Manifest.ID, base.Manifest.ID}},
		Model: routeengine.CandidateModel{UpstreamName: "m"},
	}
	if err := applyPluginRequestHeaders(req, candidate, gatewayRequest{}, nil, "sk", "https://api.base.example", "m", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := applyPluginSignChain(context.Background(), req, candidate, gatewayRequest{}, &testGatewayProtocol{}, []byte(`{"model":"m"}`), "sk", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("X-Base") != "base" || req.Header.Get("X-Chain") != "two" || req.Header.Get("X-Saw") != "one" {
		t.Fatalf("headers:\n%s", formatHeaders(req.Header))
	}
}

func TestSignRequestFailureSwitchesCredential(t *testing.T) {
	site.ClearPluginSiteTypes()
	t.Cleanup(site.ClearPluginSiteTypes)
	plugin := mustChainPlugin(t, "fail.example", kindlessChainSource("fail.example", `
export function signRequest() { throw new Error("nope"); }`), nil)
	restore := installChainPlugins(t, plugin)
	t.Cleanup(restore)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(server.Close)
	handler, candidate := pluginUpstreamFixture(t, "openai", server.URL, nil, true, server.Client(), 2)
	candidate.Site.PluginIDs = []string{plugin.Manifest.ID}
	result := handler.forwardGatewayRequest(context.Background(), httptest.NewRecorder(), "req-fail", 1, uuid.New(), uuid.New(), candidate, gatewayRequest{
		DownstreamPath: gatewayEndpointChatCompletions,
	}, nil, &testGatewayProtocol{})
	if result.success || result.errorType != "upstream_sign_failed" || result.credentialAttempt != 2 {
		t.Fatalf("success=%v type=%s attempt=%d message=%s", result.success, result.errorType, result.credentialAttempt, result.errorMessage)
	}
}

func TestSignRewriteRejectsModelChange(t *testing.T) {
	site.ClearPluginSiteTypes()
	t.Cleanup(site.ClearPluginSiteTypes)
	plugin := mustChainPlugin(t, "body.example", kindlessChainSource("body.example", `
export function signRequest(ctx, req) {
  const body = JSON.parse(req.body);
  body.model = "other";
  return { body: JSON.stringify(body) };
}`), nil)
	restore := installChainPlugins(t, plugin)
	t.Cleanup(restore)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream was called")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	handler, candidate := pluginUpstreamFixture(t, "openai", server.URL, nil, true, server.Client(), 1)
	candidate.Site.PluginIDs = []string{plugin.Manifest.ID}
	result := handler.forwardGatewayRequest(context.Background(), httptest.NewRecorder(), "req-body", 1, uuid.New(), uuid.New(), candidate, gatewayRequest{
		DownstreamPath: gatewayEndpointChatCompletions,
	}, nil, &testGatewayProtocol{payload: map[string]any{"model": "test-upstream", "stream": false}})
	if result.success || result.errorType != "upstream_sign_failed" || !strings.Contains(result.errorMessage, "changed \"model\"") {
		t.Fatalf("type=%s message=%s", result.errorType, result.errorMessage)
	}
}

func TestSignModeNeedsSecretUse(t *testing.T) {
	site.ClearPluginSiteTypes()
	t.Cleanup(site.ClearPluginSiteTypes)
	plain := mustChainPlugin(t, "plain.example", kindlessChainSource("plain.example", `
export function signRequest() {
  return { stringToSign: "a", algorithm: "hmac-sha256", header: "X-Sig" };
}`), nil)
	secretSource := kindlessChainSource("secret.example", `
export function signRequest(ctx) {
  log.info("key", { value: ctx.credential && ctx.credential.api_key });
  return { stringToSign: "a", algorithm: "hmac-sha256", header: "X-Sig", headers: { "X-Seen": ctx.credential.api_key } };
}`)
	secret := mustChainPlugin(t, "secret.example", secretSource, func(m *jsplugin.Manifest) {
		m.Permissions = []string{"secret.use"}
	})
	empty := mustChainPlugin(t, "empty.example", kindlessChainSource("empty.example", `
export function signRequest(ctx) {
  return { headers: { "X-Cred": ctx.credential ? "yes" : "no" } };
}`), nil)
	restore := installChainPlugins(t, plain, secret, empty)
	t.Cleanup(restore)

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var mu sync.Mutex
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(server.Close)

	forward := func(plugin *jsplugin.Plugin) gatewayAttemptResult {
		t.Helper()
		handler, candidate := pluginUpstreamFixture(t, "openai", server.URL, nil, true, server.Client(), 1)
		handler.logger = logger
		candidate.Site.PluginIDs = []string{plugin.Manifest.ID}
		return handler.forwardGatewayRequest(context.Background(), httptest.NewRecorder(), "req-secret", 1, uuid.New(), uuid.New(), candidate, gatewayRequest{
			DownstreamPath: gatewayEndpointChatCompletions,
		}, nil, &testGatewayProtocol{})
	}
	denied := forward(plain)
	if denied.success || denied.errorType != "upstream_sign_failed" || !strings.Contains(denied.errorMessage, "secret.use") {
		t.Fatalf("plain type=%s message=%s", denied.errorType, denied.errorMessage)
	}
	logs.Reset()
	allowed := forward(secret)
	mu.Lock()
	signed := seen.Clone()
	mu.Unlock()
	if !allowed.success || signed.Get("X-Seen") != "sk-test" || signed.Get("X-Sig") == "" {
		t.Fatalf("secret type=%s message=%s headers:\n%s", allowed.errorType, allowed.errorMessage, formatHeaders(signed))
	}
	if strings.Contains(logs.String(), "sk-test") || strings.Contains(logs.String(), "key") {
		t.Fatalf("secret leaked into logs:\n%s", logs.String())
	}
	blank := forward(empty)
	mu.Lock()
	blankHeader := seen.Get("X-Cred")
	mu.Unlock()
	if !blank.success || blankHeader != "no" {
		t.Fatalf("empty credential header=%q type=%s message=%s", blankHeader, blank.errorType, blank.errorMessage)
	}
}

func TestGatewaySessionKey(t *testing.T) {
	siteID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	secret := []byte("cache-key")
	withSession := gatewayRequest{DownstreamHeaders: http.Header{codexGatewaySessionHeader: {"chat-a"}}}
	same := gatewaySessionKey(withSession, siteID, "plugin.a", secret)
	again := gatewaySessionKey(withSession, siteID, "plugin.a", secret)
	other := gatewaySessionKey(withSession, siteID, "plugin.b", secret)
	if same == "" || same != again || same == other || len(same) != 32 {
		t.Fatalf("session=%q again=%q other=%q", same, again, other)
	}
	fromCache := gatewaySessionKey(gatewayRequest{Payload: map[string]any{"prompt_cache_key": "cache-a"}}, siteID, "plugin.a", secret)
	if fromCache == "" || fromCache == same {
		t.Fatalf("prompt cache key = %q", fromCache)
	}
	long := strings.Repeat("x", 300)
	truncated := gatewaySessionKey(gatewayRequest{Payload: map[string]any{"prompt_cache_key": long}}, siteID, "plugin.a", secret)
	cut := gatewaySessionKey(gatewayRequest{Payload: map[string]any{"prompt_cache_key": long[:256]}}, siteID, "plugin.a", secret)
	if truncated == "" || truncated != cut {
		t.Fatalf("truncation truncated=%q cut=%q", truncated, cut)
	}
	if gatewaySessionKey(gatewayRequest{}, siteID, "plugin.a", secret) != "" {
		t.Fatal("empty source produced a key")
	}
}

func TestModelMetaHeaderTemplate(t *testing.T) {
	got, err := RenderHeaderTemplate("{{model.meta.vendor}}-{{model.name}}", map[string]string{"model.name": "m"}, func(key string) (string, bool) {
		return dynamicHeaderValue(key, "m", "", map[string]any{"vendor": "acme", "rank": float64(2)}, nil)
	})
	if err != nil || got != "acme-m" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	missing, err := RenderHeaderTemplate("{{model.meta.absent}}", nil, nil)
	if err != nil || missing != "" {
		t.Fatalf("missing=%q err=%v", missing, err)
	}
}

func TestOverlayStripDownstreamHeadersPreservesAuthAndChain(t *testing.T) {
	firstSrc := kindlessChainSource("overlay.first", `export function signRequest() { return { headers: { "X-Signed": "ok" } }; }`)
	first := mustChainPlugin(t, "overlay.first", firstSrc, func(m *jsplugin.Manifest) {
		m.Request = jsplugin.SiteProviderRequest{
			Headers: jsplugin.HeaderMap(map[string]string{"X-First": "1"}),
		}
	})
	secondSrc := kindlessChainSource("overlay.strip", `export function signRequest() { return { headers: { "X-Second-Sign": "ok" } }; }`)
	second := mustChainPlugin(t, "overlay.strip", secondSrc, func(m *jsplugin.Manifest) {
		m.Request = jsplugin.SiteProviderRequest{
			StripDownstreamHeaders: true,
			Headers:                jsplugin.HeaderMap(map[string]string{"X-Opencode-Client": "cli"}),
		}
	})
	defer installChainPlugins(t, first, second)()

	req, err := http.NewRequest(http.MethodPost, "https://opencode.ai/inference/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer public")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Stainless-Lang", "js")
	req.Header.Set("User-Agent", "OpenAI/JS 4.0")

	candidate := routeengine.Candidate{
		Site: routeengine.CandidateSite{
			SiteType:  "openai",
			BaseURL:   "https://opencode.ai/inference",
			PluginIDs: []string{"overlay.first", "overlay.strip"},
		},
		Model: routeengine.CandidateModel{UpstreamName: "m"},
	}
	if err := applyPluginRequestHeaders(req, candidate, gatewayRequest{}, nil, "public", "https://opencode.ai/inference", "m", nil, []byte("secret")); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("X-Stainless-Lang") != "" || req.Header.Get("User-Agent") != "" {
		t.Fatalf("downstream headers were not stripped: %v", req.Header)
	}
	if req.Header.Get("Authorization") != "Bearer public" || req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("auth or content-type lost: %v", req.Header)
	}
	if req.Header.Get("X-First") != "1" || req.Header.Get("X-Opencode-Client") != "cli" {
		t.Fatalf("plugin headers lost: %v", req.Header)
	}
}

func kindlessChainSource(id, body string) string {
	return "export const meta = { apiVersion: 1, id: " + jsQuote(id) + " };\n" + body
}

func siteTypeChainSource(id string) string {
	return kindlessChainSource(id, `export function listModels() { return { result: { models: [] } }; }`)
}

func jsQuote(value string) string {
	return `"` + value + `"`
}

func mustChainPlugin(t *testing.T, id, source string, extras func(*jsplugin.Manifest)) *jsplugin.Plugin {
	t.Helper()
	manifest := jsplugin.Manifest{
		ID: id, Name: id, Version: "1.0.0", APIVersion: 1, HostAPI: 1,
		SHA256: map[string]string{"plugin.js": jsplugin.HashSource(source)},
		Request: jsplugin.SiteProviderRequest{Headers: jsplugin.HeaderMap(map[string]string{
			"X-Trace":   "{{random.hex:40}}",
			"X-Session": "{{session.key}}",
		})},
	}
	if strings.HasPrefix(id, "base.") {
		manifest.SiteProvider = jsplugin.SiteProviderSection{
			SiteType:   id + "/chat",
			Display:    jsplugin.SiteProviderDisplay{Name: jsplugin.LocalizedText{"en": "Base"}},
			Hosts:      []string{"api.base.example"},
			Credential: jsplugin.SiteProviderCredential{Model: "api_key"},
			Protocol: jsplugin.SiteProviderProtocol{
				Kind: "openai_chat", Path: "/v1/chat/completions", OfficialBaseURL: "https://api.base.example",
				DefaultEndpointTypes: []string{"openai"},
			},
		}
		manifest.Request = jsplugin.SiteProviderRequest{}
	}
	if extras != nil {
		extras(&manifest)
	}
	var fixtures []jsplugin.Fixture
	if !manifest.DeclaresSiteType() {
		fixtures = []jsplugin.Fixture{{Name: "unused", Hook: "signRequest"}}
	}
	plugin, err := jsplugin.NewPlugin(manifest, source, fixtures, 1)
	if err != nil {
		t.Fatal(err)
	}
	return plugin
}

func installChainPlugins(t *testing.T, plugins ...*jsplugin.Plugin) func() {
	t.Helper()
	previous := jsplugin.DefaultCatalog().Current()
	jsplugin.DefaultCatalog().Replace(&jsplugin.Snapshot{Gen: 99, Registry: jsplugin.NewRegistry(plugins...)})
	return func() {
		if previous != nil {
			jsplugin.DefaultCatalog().Replace(previous)
			return
		}
		jsplugin.DefaultCatalog().Replace(&jsplugin.Snapshot{Gen: 1, Registry: jsplugin.NewRegistry()})
	}
}
