package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"xlyra/server/internal/auth"
	routeengine "xlyra/server/internal/router"
	"xlyra/server/internal/store"
)

func chatRequestWithMessages(t *testing.T, payload string) gatewayRequest {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	request, failure := (chatCompletionsEndpointAdapter{}).DecodeRequest(req)
	if failure != nil {
		t.Fatalf("decode request: %v", failure.message)
	}
	return request
}

func TestRouteAffinitySeedFollowsTheConversationNotTheTurn(t *testing.T) {
	t.Parallel()

	first := chatRequestWithMessages(t, `{"model":"m","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"plan my trip"}]}`)
	later := chatRequestWithMessages(t, `{"model":"m","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"plan my trip"},{"role":"assistant","content":"where to?"},{"role":"user","content":"Tokyo"}]}`)
	other := chatRequestWithMessages(t, `{"model":"m","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"explain rust lifetimes"}]}`)

	seed := routeAffinitySeed(first)
	if seed == "" || !strings.HasPrefix(seed, "lineage:") {
		t.Fatalf("seed = %q, want a lineage seed for a plain chat request", seed)
	}
	if routeAffinitySeed(later) != seed {
		t.Fatal("later turns of one conversation must keep the same seed")
	}
	if routeAffinitySeed(other) == seed {
		t.Fatal("a different conversation must get a different seed")
	}
}

func TestRouteAffinitySeedPrefersExplicitSessionIdentity(t *testing.T) {
	t.Parallel()

	request := chatRequestWithMessages(t, `{"model":"m","messages":[{"role":"user","content":"hi"}],"prompt_cache_key":"cache-123"}`)
	if got := routeAffinitySeed(request); got != "cache-key:cache-123" {
		t.Fatalf("seed = %q, want the prompt cache key", got)
	}
	request.Payload["previous_response_id"] = "resp_9"
	if got := routeAffinitySeed(request); got != "hint:previous_response_id:resp_9" {
		t.Fatalf("seed = %q, want the session hint to win", got)
	}
	if got := routeAffinitySeed(gatewayRequest{}); got != "" {
		t.Fatalf("seed = %q, want empty for a request that identifies nothing", got)
	}
}

func TestRoutePickMetadataOnlyWhenPlanned(t *testing.T) {
	t.Parallel()

	if got := routePickMetadata(t.Context()); got != nil {
		t.Fatalf("metadata = %#v, want none before a pick", got)
	}
	ctx := withRoutePick(t.Context(), routeengine.RoutePick{Mode: routeengine.RoutePickSticky, TierSize: 3})
	got, ok := routePickMetadata(ctx).(map[string]any)
	if !ok || got["mode"] != routeengine.RoutePickSticky || got["tier_size"] != 3 {
		t.Fatalf("metadata = %#v", routePickMetadata(ctx))
	}
}

func TestServeEndpointSpreadsEqualPrioritySitesButKeepsAConversationOnOne(t *testing.T) {
	t.Parallel()

	harness := &softMappingHarness{}
	upstream := softMappingUpstreamServer(t, harness, nil)
	left := store.Site{ID: uuid.New(), Name: "Left", Slug: "left", SiteType: "openai", BaseURL: upstream.URL + "/left", Enabled: true, Status: "active", RoutingPriority: 4.7}
	right := store.Site{ID: uuid.New(), Name: "Right", Slug: "right", SiteType: "openai", BaseURL: upstream.URL + "/right", Enabled: true, Status: "active", RoutingPriority: 4.7}
	lower := store.Site{ID: uuid.New(), Name: "Lower", Slug: "lower", SiteType: "openai", BaseURL: upstream.URL + "/lower", Enabled: true, Status: "active", RoutingPriority: 4.5}
	handler, apiKey := newProtocolResolveSkipFixture(t, harness, left, right, lower)
	resolver := openAIProtocolResolver{db: handler.db}

	chat := func(conversation string, turns int) *httptest.ResponseRecorder {
		messages := `{"role":"user","content":"` + conversation + `"}`
		for i := 1; i < turns; i++ {
			messages += `,{"role":"assistant","content":"ok"},{"role":"user","content":"more"}`
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"shared-model","messages":[`+messages+`],"stream":false}`))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(auth.WithAPIKey(req.Context(), apiKey))
		recorder := httptest.NewRecorder()
		handler.serveEndpoint(recorder, req, chatCompletionsEndpointAdapter{}, resolver)
		return recorder
	}
	called := func() string {
		harness.mu.Lock()
		defer harness.mu.Unlock()
		return harness.upstreamCalls[len(harness.upstreamCalls)-1].Model
	}

	seen := map[string]int{}
	for i := 0; i < 40; i++ {
		conversation := "conversation number " + string(rune('A'+i%26)) + string(rune('a'+i/26))
		if recorder := chat(conversation, 1); recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		first := called()
		seen[first]++
		for turn := 2; turn <= 4; turn++ {
			chat(conversation, turn)
			if got := called(); got != first {
				t.Fatalf("conversation %q turn %d went to %s, first turn went to %s", conversation, turn, got, first)
			}
		}
	}
	if seen["upstream-lower"] != 0 {
		t.Fatalf("calls = %v: the lower priority site must not lead while the tied pair is healthy", seen)
	}
	if seen["upstream-left"] < 8 || seen["upstream-right"] < 8 {
		t.Fatalf("calls = %v, want conversations spread across both equal-priority sites", seen)
	}
}
