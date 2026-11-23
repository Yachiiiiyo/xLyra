package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"xlyra/server/internal/auth"
	"xlyra/server/internal/credential"
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/store"
	"xlyra/server/internal/textmatch"
	"xlyra/server/internal/textpipeline"
	"xlyra/server/internal/textxform"
)

func pipelineJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := textpipeline.MarshalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

var pipelineTestGen atomic.Int64

const pipelinePersonHook = `export function inspectSegments(ctx, input) {
  if (ctx.binding.params.flag !== true) throw new Error("params");
  const add = [];
  for (const s of input.segments) {
    const i = s.text.indexOf("张三");
    if (i >= 0) add.push({ segment: s.id, start: i, end: i + 2, type: "PERSON" });
  }
  return { add };
}`

func pipelineGatewayPlugin(t *testing.T, hook string, mutate func(*jsplugin.GatewayPipelineSection)) *jsplugin.Plugin {
	t.Helper()
	source := `export const meta = { apiVersion: 1, id: "acme.pipeline" };` + "\n" + hook
	section := &jsplugin.GatewayPipelineSection{
		Scope:               jsplugin.PipelineScopeAPIKey,
		Exclusive:           true,
		FailurePolicy:       jsplugin.PipelineFailClosed,
		UnsupportedEndpoint: jsplugin.PipelineUnsupportedReject,
		Transform:           jsplugin.PipelineTransformSpec{Mode: jsplugin.PipelineTransformTokenPlaceholder, SystemHint: "Keep [[TYPE_N]] placeholders."},
		Types:               []jsplugin.PipelineTypeSpec{{Type: "PHONE"}, {Type: "PERSON"}},
		Rules:               []jsplugin.PipelineRuleSpec{{Type: "PHONE", Pattern: `1[3-9]\d{9}`, Boundary: "digit"}},
		Inputs: []jsplugin.AutomationInputSpec{
			{Name: "keys", Type: jsplugin.EntityAPIKey, Multiple: true},
			{Name: "flag", Type: "boolean", Default: true},
		},
	}
	if mutate != nil {
		mutate(section)
	}
	manifest := jsplugin.Manifest{
		ID: "acme.pipeline", Name: "Pipeline", Version: "1.0.0", APIVersion: 1, HostAPI: 2,
		GatewayPipeline: section,
		SHA256:          map[string]string{"plugin.js": jsplugin.HashSource(source)},
	}
	if hook != "" {
		manifest.Permissions = []string{jsplugin.PermissionPayloadInspectRaw}
	}
	plugin, err := jsplugin.NewPlugin(manifest, source, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	return plugin
}

// installPipeline binds plugin to apiKeyID in a fresh default catalog generation.
func installPipeline(t *testing.T, apiKeyID uuid.UUID, binding *jsplugin.ResolvedPipelineBinding) {
	t.Helper()
	textpipeline.DefaultCache = textmatch.NewSegmentCache(textmatch.DefaultCacheBytes)
	catalog := jsplugin.DefaultCatalog()
	previous := catalog.Current()
	builtins, err := jsplugin.LoadBuiltins()
	if err != nil {
		t.Fatal(err)
	}
	plugins := builtins.Plugins()
	if binding.Plugin != nil {
		plugins = append(plugins, binding.Plugin)
	}
	catalog.Replace(&jsplugin.Snapshot{
		Gen:       9300 + pipelineTestGen.Add(1),
		Registry:  jsplugin.NewRegistry(plugins...),
		Pipelines: jsplugin.NewPipelineIndex(map[uuid.UUID]*jsplugin.ResolvedPipelineBinding{apiKeyID: binding}),
	})
	t.Cleanup(func() {
		if previous != nil {
			catalog.Replace(previous)
		} else {
			_ = catalog.InitBuiltins()
		}
	})
}

func activePipelineBinding(t *testing.T, plugin *jsplugin.Plugin, params map[string]any) *jsplugin.ResolvedPipelineBinding {
	t.Helper()
	if params == nil {
		params = map[string]any{"flag": true}
	}
	binding, err := jsplugin.ResolvePipelineBinding(plugin, uuid.New(), "test", params, jsplugin.PipelineHostOptions{PropagateSameValue: true})
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func chatPipelineRequest(content string) gatewayRequest {
	payload := map[string]any{"model": "pipe-model", "messages": []any{map[string]any{"role": "user", "content": content}}}
	request := gatewayRequest{DownstreamPath: gatewayEndpointChatCompletions, RequestedModel: "pipe-model", Payload: payload}
	rebuildCanonicalRequest(&request)
	return request
}

type pipelineUpstream struct {
	mu     sync.Mutex
	bodies []string
}

func (u *pipelineUpstream) snapshot() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.bodies...)
}

// newPipelineGatewayFixture serves one canonical model from two sites; the
// first upstream call fails with 500 so the request fails over.
func newPipelineGatewayFixture(t *testing.T, reply string) (Handler, store.APIKey, *pipelineUpstream) {
	t.Helper()
	upstream := &pipelineUpstream{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		upstream.mu.Lock()
		upstream.bodies = append(upstream.bodies, string(raw))
		first := len(upstream.bodies) == 1
		upstream.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if first {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream exploded","type":"server_error"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"chatcmpl-pipe","object":"chat.completion","model":"up","choices":[{"index":0,"message":{"role":"assistant","content":` + reply + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
	}))
	t.Cleanup(server.Close)

	harness := &softMappingHarness{}
	canonical := store.CanonicalModel{ID: uuid.New(), ModelKey: "pipe-model", Status: "active"}
	siteA := store.Site{ID: uuid.New(), Name: "Site A", Slug: "site-a", SiteType: "openai", BaseURL: server.URL + "/a", Enabled: true, Status: "active"}
	siteB := store.Site{ID: uuid.New(), Name: "Site B", Slug: "site-b", SiteType: "openai", BaseURL: server.URL + "/b", Enabled: true, Status: "active"}
	encrypted, masked, err := credential.NewService("test-master-key").Encrypt("sk-pipeline-test")
	if err != nil {
		t.Fatal(err)
	}
	harness.apiKey = store.APIKey{ID: uuid.New(), Name: "pipeline-key", Status: "active", ModelPolicy: "allow_all", SitePolicy: "allow_all", QuotaUnlimited: true}
	harness.canonicalModels = map[string]store.CanonicalModel{canonical.ModelKey: canonical}
	harness.siteModels = []store.SiteModel{softMappingSiteModel(canonical, siteA, "up-a"), softMappingSiteModel(canonical, siteB, "up-b")}
	harness.sites = map[uuid.UUID]store.Site{siteA.ID: siteA, siteB.ID: siteB}
	harness.credentials = map[uuid.UUID]store.SiteCredential{
		siteA.ID: {ID: uuid.New(), SiteID: siteA.ID, CredentialType: "api_key", EncryptedSecret: encrypted, MaskedSecret: masked},
		siteB.ID: {ID: uuid.New(), SiteID: siteB.ID, CredentialType: "api_key", EncryptedSecret: encrypted, MaskedSecret: masked},
	}
	return newSoftMappingHandler(t, harness), harness.apiKey, upstream
}

func postPipelineChat(t *testing.T, handler Handler, apiKey store.APIKey, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, gatewayEndpointChatCompletions, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithAPIKey(req.Context(), apiKey))
	recorder := httptest.NewRecorder()
	handler.ChatCompletions(recorder, req)
	return recorder
}

func TestGatewayPipelineRunsOnceAcrossFailover(t *testing.T) {
	handler, apiKey, upstream := newPipelineGatewayFixture(t, `"ok"`)
	plugin := pipelineGatewayPlugin(t, pipelinePersonHook, nil)
	installPipeline(t, apiKey.ID, activePipelineBinding(t, plugin, nil))
	before := jsplugin.DefaultCatalog().Breaker().Metrics(plugin.Manifest.ID, plugin.Manifest.Version).Calls

	content := "😀😀 我的手机 13812345678，名字张三。再说一次 13812345678"
	recorder := postPipelineChat(t, handler, apiKey, `{"model":"pipe-model","stream":false,"messages":[{"role":"user","content":"`+content+`"}]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d %s", recorder.Code, recorder.Body.String())
	}
	bodies := upstream.snapshot()
	if len(bodies) != 2 {
		t.Fatalf("upstream calls = %d, want a failover", len(bodies))
	}
	var sent []string
	for i, body := range bodies {
		if strings.Contains(body, "13812345678") || strings.Contains(body, "张三") {
			t.Fatalf("upstream %d got raw text: %s", i, body)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			t.Fatal(err)
		}
		messages := payload["messages"].([]any)
		if system := messages[0].(map[string]any); system["role"] != "system" || system["content"] != "Keep [[TYPE_N]] placeholders." {
			t.Fatalf("upstream %d system hint = %#v", i, system)
		}
		if got := messages[1].(map[string]any)["content"]; got != "😀😀 我的手机 [[PHONE_1]]，名字[[PERSON_1]]。再说一次 [[PHONE_1]]" {
			t.Fatalf("upstream %d content = %q", i, got)
		}
		sent = append(sent, pipelineJSON(t, messages))
	}
	if sent[0] != sent[1] {
		t.Fatal("both attempts must send the same transformed body")
	}
	if calls := jsplugin.DefaultCatalog().Breaker().Metrics(plugin.Manifest.ID, plugin.Manifest.Version).Calls - before; calls != 1 {
		t.Fatalf("inspectSegments calls = %d, want 1", calls)
	}
}

func TestGatewayPipelineHookFailuresBlockTheRequest(t *testing.T) {
	cases := map[string]string{
		"throw":        `export function inspectSegments(ctx, input) { throw new Error("leak " + input.segments[0].text); }`,
		"timeout":      `export function inspectSegments(ctx, input) { for (;;) {} }`,
		"out of range": `export function inspectSegments(ctx, input) { return { add: [{ segment: "seg_0", start: 0, end: 9999, type: "PHONE" }] }; }`,
		"surrogate":    `export function inspectSegments(ctx, input) { return { add: [{ segment: "seg_0", start: 1, end: 3, type: "PHONE" }] }; }`,
		"bad type":     `export function inspectSegments(ctx, input) { return { add: [{ segment: "seg_0", start: 0, end: 1, type: "EMAIL" }] }; }`,
		"bad drop":     `export function inspectSegments(ctx, input) { return { drop: [5] }; }`,
		"bad shape":    `export function inspectSegments(ctx, input) { return "nope"; }`,
	}
	for name, hook := range cases {
		t.Run(name, func(t *testing.T) {
			handler, apiKey, upstream := newPipelineGatewayFixture(t, `"ok"`)
			installPipeline(t, apiKey.ID, activePipelineBinding(t, pipelineGatewayPlugin(t, hook, nil), nil))
			recorder := postPipelineChat(t, handler, apiKey, `{"model":"pipe-model","messages":[{"role":"user","content":"😀secret 13812345678"}]}`)
			if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), textpipeline.CodePipelineFailed) {
				t.Fatalf("status = %d %s", recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), "secret") || strings.Contains(recorder.Body.String(), "13812345678") {
				t.Fatalf("error echoes request text: %s", recorder.Body.String())
			}
			if calls := upstream.snapshot(); len(calls) != 0 {
				t.Fatalf("upstream was called %d times", len(calls))
			}
		})
	}
}

func TestGatewayPipelineFailOpenPassesTheOriginal(t *testing.T) {
	plugin := pipelineGatewayPlugin(t, `export function inspectSegments() { throw new Error("x"); }`, func(s *jsplugin.GatewayPipelineSection) {
		s.FailurePolicy = jsplugin.PipelineFailOpen
	})
	binding := activePipelineBinding(t, plugin, nil)
	apiKeyID := uuid.New()
	installPipeline(t, apiKeyID, binding)
	request := chatPipelineRequest("call 13812345678")
	ctx, failure := Handler{}.applyGatewayIngressPipeline(context.Background(), apiKeyID, &request)
	if failure != nil {
		t.Fatalf("fail_open failure = %#v", failure)
	}
	if textxform.SessionFromContext(ctx) != nil {
		t.Fatal("a bypassed pipeline attaches no session")
	}
	if got := request.Payload["messages"].([]any)[0].(map[string]any)["content"]; got != "call 13812345678" {
		t.Fatalf("payload changed: %q", got)
	}
	if state := gatewayPipelineStateFrom(ctx); state == nil || state.metrics.Outcome != textpipeline.OutcomeBypassed {
		t.Fatalf("outcome = %#v", state)
	}
}

func TestGatewayPipelinePoliciesAndRejects(t *testing.T) {
	apiKeyID := uuid.New()
	run := func(binding *jsplugin.ResolvedPipelineBinding, request gatewayRequest) (context.Context, *chatFailure, gatewayRequest) {
		installPipeline(t, apiKeyID, binding)
		ctx, failure := Handler{}.applyGatewayIngressPipeline(context.Background(), apiKeyID, &request)
		return ctx, failure, request
	}
	declarative := pipelineGatewayPlugin(t, "", nil)

	_, failure, _ := run(&jsplugin.ResolvedPipelineBinding{BindingID: uuid.New(), PluginID: "acme.pipeline", Status: jsplugin.PipelineBindingUnavailable, FailurePolicy: jsplugin.PipelineFailClosed, UnavailableReason: jsplugin.PipelineReasonDisabled}, chatPipelineRequest("x"))
	if failure == nil || failure.status != http.StatusServiceUnavailable || failure.code != textpipeline.CodePipelineUnavailable {
		t.Fatalf("unavailable = %#v", failure)
	}

	speech := gatewayRequest{DownstreamPath: gatewayEndpointAudioSpeech, Payload: map[string]any{"input": "13812345678"}}
	_, failure, _ = run(activePipelineBinding(t, declarative, nil), speech)
	if failure == nil || failure.status != http.StatusBadRequest || failure.code != textpipeline.CodeUnsupportedEndpoint {
		t.Fatalf("unsupported endpoint = %#v", failure)
	}
	passing := pipelineGatewayPlugin(t, "", func(s *jsplugin.GatewayPipelineSection) { s.UnsupportedEndpoint = jsplugin.PipelineUnsupportedPass })
	_, failure, after := run(activePipelineBinding(t, passing, nil), speech)
	if failure != nil || after.Payload["input"] != "13812345678" {
		t.Fatalf("unsupported endpoint pass = %#v %#v", failure, after.Payload)
	}
	for _, path := range []string{"/v1/plugins/acme"} {
		_, failure, _ = run(activePipelineBinding(t, declarative, nil), gatewayRequest{DownstreamPath: path, Payload: map[string]any{}})
		if failure == nil || failure.code != textpipeline.CodeUnsupportedEndpoint {
			t.Fatalf("%s = %#v", path, failure)
		}
	}

	small := pipelineGatewayPlugin(t, "", func(s *jsplugin.GatewayPipelineSection) { s.MaxInspectBytes = 16 })
	_, failure, _ = run(activePipelineBinding(t, small, nil), chatPipelineRequest(strings.Repeat("x", 32)))
	if failure == nil || failure.status != http.StatusRequestEntityTooLarge || failure.code != textpipeline.CodePayloadTooLarge {
		t.Fatalf("too large = %#v", failure)
	}

	rejecting := pipelineGatewayPlugin(t, `export function inspectSegments(ctx, input) {
  return { reject: { status: 403, code: "content_blocked", message: "blocked: " + input.segments[0].text } };
}`, nil)
	_, failure, _ = run(activePipelineBinding(t, rejecting, nil), chatPipelineRequest("forbidden words"))
	if failure == nil || failure.status != http.StatusForbidden || failure.code != "content_blocked" || strings.Contains(failure.message, "forbidden") {
		t.Fatalf("reject = %#v", failure)
	}
	quiet := pipelineGatewayPlugin(t, `export function inspectSegments() { return { reject: {} }; }`, nil)
	_, failure, _ = run(activePipelineBinding(t, quiet, nil), chatPipelineRequest("x"))
	if failure == nil || failure.status != http.StatusBadRequest || failure.code != jsplugin.DefaultPipelineRejectCode {
		t.Fatalf("default reject = %#v", failure)
	}

	dropping := pipelineGatewayPlugin(t, `export function inspectSegments(ctx, input) { return { drop: input.findings.map((_, i) => i) }; }`, nil)
	ctx, failure, after := run(activePipelineBinding(t, dropping, nil), chatPipelineRequest("call 13812345678"))
	if failure != nil || after.Payload["messages"].([]any)[0].(map[string]any)["content"] != "call 13812345678" {
		t.Fatalf("dropped findings = %#v %#v", failure, after.Payload)
	}
	if session := textxform.SessionFromContext(ctx); session == nil || session.HasReplacements() {
		t.Fatal("nothing replaced, but the session is there for responses")
	}

	ctx, failure, after = run(activePipelineBinding(t, declarative, nil), chatPipelineRequest("call 13812345678"))
	if failure != nil {
		t.Fatal(failure)
	}
	if after.Canonical == nil || !strings.Contains(pipelineJSON(t, after.Canonical.Messages), "[[PHONE_1]]") || strings.Contains(pipelineJSON(t, after.Canonical.Messages), "13812345678") {
		t.Fatalf("canonical form was not rebuilt: %s", pipelineJSON(t, after.Canonical))
	}
	session := textxform.SessionFromContext(ctx)
	if original, ok := session.Lookup("[[PHONE_1]]"); !ok || original != "13812345678" || !session.Reversible {
		t.Fatal("session maps placeholders back")
	}
	state := gatewayPipelineStateFrom(ctx)
	if state == nil || state.metrics.Outcome != textpipeline.OutcomeTransformed || state.metrics.Counts["PHONE"] != 1 || state.metrics.Segments != 1 {
		t.Fatalf("metrics = %#v", state)
	}
	if raw := pipelineJSON(t, state.metrics); strings.Contains(raw, "13812345678") || strings.Contains(raw, "[[PHONE") {
		t.Fatalf("metrics hold text: %s", raw)
	}
	// The same request again comes from the cache.
	_, _, _ = run(activePipelineBinding(t, declarative, nil), chatPipelineRequest("call 13812345678"))
	request := chatPipelineRequest("call 13812345678")
	ctx, _ = Handler{}.applyGatewayIngressPipeline(context.Background(), apiKeyID, &request)
	if state := gatewayPipelineStateFrom(ctx); state.metrics.Cached != 1 {
		t.Fatalf("second run cached = %d", state.metrics.Cached)
	}
}

func TestGatewayPipelineUnboundKeyIsUntouched(t *testing.T) {
	installPipeline(t, uuid.New(), activePipelineBinding(t, pipelineGatewayPlugin(t, "", nil), nil))
	request := chatPipelineRequest("call 13812345678")
	ctx, failure := Handler{}.applyGatewayIngressPipeline(context.Background(), uuid.New(), &request)
	if failure != nil || gatewayPipelineStateFrom(ctx) != nil || textxform.SessionFromContext(ctx) != nil {
		t.Fatal("an unbound key must pass without any pipeline state")
	}
}
