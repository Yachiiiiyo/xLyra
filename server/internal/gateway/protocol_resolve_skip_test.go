package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"xlyra/server/internal/auth"
	"xlyra/server/internal/credential"
	routeengine "xlyra/server/internal/router"
	"xlyra/server/internal/store"
)

// slugFailingResolver fails protocol resolution for the listed site slugs and
// defers to the real resolver for every other candidate.
type slugFailingResolver struct {
	inner upstreamProtocolResolver
	fail  map[string]bool
}

func (r slugFailingResolver) Resolve(ctx context.Context, request gatewayRequest, candidate routeengine.Candidate) (gatewayProtocolAdapter, error) {
	if r.fail[candidate.Site.Slug] {
		return nil, errors.New("credential does not support request endpoint")
	}
	return r.inner.Resolve(ctx, request, candidate)
}

func newProtocolResolveSkipFixture(t *testing.T, harness *softMappingHarness, sites ...store.Site) (Handler, store.APIKey) {
	t.Helper()

	canonical := store.CanonicalModel{ID: uuid.New(), ModelKey: "shared-model", Status: "active"}
	encrypted, masked, err := credential.NewService("test-master-key").Encrypt("sk-resolve-skip-test")
	if err != nil {
		t.Fatalf("encrypt credential: %v", err)
	}
	harness.apiKey = store.APIKey{ID: uuid.New(), Name: "resolve-skip-key", Status: "active", ModelPolicy: "allow_all", SitePolicy: "allow_all", QuotaUnlimited: true, ModelMappings: store.JSON(`[]`)}
	harness.canonicalModels = map[string]store.CanonicalModel{canonical.ModelKey: canonical}
	harness.sites = map[uuid.UUID]store.Site{}
	harness.credentials = map[uuid.UUID]store.SiteCredential{}
	for _, item := range sites {
		harness.sites[item.ID] = item
		harness.siteModels = append(harness.siteModels, softMappingSiteModel(canonical, item, "upstream-"+item.Slug))
		harness.credentials[item.ID] = store.SiteCredential{ID: uuid.New(), SiteID: item.ID, CredentialType: "api_key", EncryptedSecret: encrypted, MaskedSecret: masked}
	}
	return newSoftMappingHandler(t, harness), harness.apiKey
}

func serveChat(handler Handler, apiKey store.APIKey, resolver upstreamProtocolResolver) *httptest.ResponseRecorder {
	body := `{"model":"shared-model","messages":[{"role":"user","content":"hi"}],"stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithAPIKey(req.Context(), apiKey))
	recorder := httptest.NewRecorder()
	handler.serveEndpoint(recorder, req, chatCompletionsEndpointAdapter{}, resolver)
	return recorder
}

func TestServeEndpointSkipsCandidateWhoseProtocolCannotResolve(t *testing.T) {
	t.Parallel()

	harness := &softMappingHarness{}
	upstream := softMappingUpstreamServer(t, harness, nil)
	primary := store.Site{ID: uuid.New(), Name: "Primary", Slug: "primary", SiteType: "openai", BaseURL: upstream.URL + "/primary", Enabled: true, Status: "active", RoutingPriority: 4.5}
	backup := store.Site{ID: uuid.New(), Name: "Backup", Slug: "backup", SiteType: "openai", BaseURL: upstream.URL + "/backup", Enabled: true, Status: "active", RoutingPriority: 1}
	handler, apiKey := newProtocolResolveSkipFixture(t, harness, primary, backup)
	resolver := slugFailingResolver{inner: openAIProtocolResolver{db: handler.db}, fail: map[string]bool{"primary": true}}

	recorder := serveChat(handler, apiKey, resolver)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want the backup candidate to serve the request", recorder.Code, recorder.Body.String())
	}
	harness.mu.Lock()
	defer harness.mu.Unlock()
	if len(harness.upstreamCalls) != 1 || harness.upstreamCalls[0].Model != "upstream-backup" {
		t.Fatalf("upstream calls = %#v, want exactly one call to the backup site", harness.upstreamCalls)
	}
}

func TestServeEndpointReportsProtocolResolutionFailureWhenNoCandidateCanResolve(t *testing.T) {
	t.Parallel()

	harness := &softMappingHarness{}
	upstream := softMappingUpstreamServer(t, harness, nil)
	only := store.Site{ID: uuid.New(), Name: "Only", Slug: "only", SiteType: "openai", BaseURL: upstream.URL + "/only", Enabled: true, Status: "active", RoutingPriority: 1}
	handler, apiKey := newProtocolResolveSkipFixture(t, harness, only)
	resolver := slugFailingResolver{inner: openAIProtocolResolver{db: handler.db}, fail: map[string]bool{"only": true}}

	recorder := serveChat(handler, apiKey, resolver)

	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "protocol_resolution_failed") {
		t.Fatalf("status = %d body=%s, want 502 protocol_resolution_failed", recorder.Code, recorder.Body.String())
	}
}
