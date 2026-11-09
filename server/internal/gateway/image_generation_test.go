package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"xlyra/server/internal/credential"
	"xlyra/server/internal/store"
)

type imageGenerationFixture struct {
	handler Handler
	harness *softMappingHarness
	sites   map[string]store.Site
	mu      sync.Mutex
	calls   []string
}

func newImageGenerationFixture(t *testing.T, statuses map[string]int) *imageGenerationFixture {
	t.Helper()
	fixture := &imageGenerationFixture{harness: &softMappingHarness{}, sites: make(map[string]store.Site)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")[0]
		fixture.mu.Lock()
		fixture.calls = append(fixture.calls, name)
		fixture.mu.Unlock()
		status := statuses[name]
		w.Header().Set("Content-Type", "application/json")
		if status >= 400 {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": "invalid_request_error", "message": "rejected by " + name}})
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"aGk=","revised_prompt":"revised"}]}`))
	}))
	t.Cleanup(server.Close)

	canonicalA := store.CanonicalModel{ID: uuid.New(), ModelKey: "model-a", Status: "active"}
	canonicalB := store.CanonicalModel{ID: uuid.New(), ModelKey: "model-b", Status: "active"}
	fixture.harness.apiKey = store.APIKey{ID: uuid.New(), Status: "active", ModelPolicy: "allow_all", SitePolicy: "allow_all", QuotaUnlimited: true}
	fixture.harness.canonicalModels = map[string]store.CanonicalModel{canonicalA.ModelKey: canonicalA, canonicalB.ModelKey: canonicalB}
	fixture.harness.sites = make(map[uuid.UUID]store.Site)
	fixture.harness.credentials = make(map[uuid.UUID]store.SiteCredential)
	encrypted, masked, err := credential.NewService("test-master-key").Encrypt("sk-image-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name      string
		canonical store.CanonicalModel
		priority  float64
	}{{"a1", canonicalA, 30}, {"a2", canonicalA, 20}, {"b1", canonicalB, 10}} {
		site := store.Site{ID: uuid.New(), Name: item.name, Slug: item.name, SiteType: "openai", BaseURL: server.URL + "/" + item.name, Enabled: true, Status: "active", RoutingPriority: item.priority}
		fixture.sites[item.name] = site
		fixture.harness.sites[site.ID] = site
		model := softMappingSiteModel(item.canonical, site, item.canonical.ModelKey)
		model.Capabilities = store.JSON(`{"supported_endpoint_types":["openai-image"]}`)
		fixture.harness.siteModels = append(fixture.harness.siteModels, model)
		fixture.harness.credentials[site.ID] = store.SiteCredential{ID: uuid.New(), SiteID: site.ID, CredentialType: "api_key", EncryptedSecret: encrypted, MaskedSecret: masked}
	}
	fixture.handler = newSoftMappingHandler(t, fixture.harness)
	return fixture
}

func (f *imageGenerationFixture) generate(t *testing.T, models []string, siteID *uuid.UUID) ImageGenerationResult {
	t.Helper()
	selections := make([]ImageGenerationModel, 0, len(models))
	for _, model := range models {
		selections = append(selections, ImageGenerationModel{Model: model})
	}
	return f.generateSelections(t, selections, siteID)
}

func (f *imageGenerationFixture) generateSelections(t *testing.T, models []ImageGenerationModel, siteID *uuid.UUID) ImageGenerationResult {
	t.Helper()
	return f.handler.GenerateImage(context.Background(), ImageGenerationRequest{RequestID: "image-test", APIKeyID: f.harness.apiKey.ID, Models: models, Prompt: "cat", SiteID: siteID})
}

func (f *imageGenerationFixture) upstreamCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func TestGenerateImageSiteAndModelFallback(t *testing.T) {
	t.Run("next site", func(t *testing.T) {
		f := newImageGenerationFixture(t, map[string]int{"a1": 500})
		result := f.generate(t, []string{"model-a"}, nil)
		if !result.OK || result.SiteName != "a2" || len(result.Attempts) != 1 || len(f.upstreamCalls()) != 2 {
			t.Fatalf("result=%+v calls=%v", result, f.upstreamCalls())
		}
	})
	t.Run("next model", func(t *testing.T) {
		f := newImageGenerationFixture(t, map[string]int{"a1": 500, "a2": 500})
		result := f.generate(t, []string{"model-a", "model-b"}, nil)
		if !result.OK || result.Model != "model-b" || len(result.Attempts) != 2 || len(f.upstreamCalls()) != 3 {
			t.Fatalf("result=%+v calls=%v", result, f.upstreamCalls())
		}
	})
	t.Run("invalid request skips remaining sites", func(t *testing.T) {
		f := newImageGenerationFixture(t, map[string]int{"a1": 400, "a2": 500})
		result := f.generate(t, []string{"model-a", "model-b"}, nil)
		calls := f.upstreamCalls()
		if !result.OK || result.Model != "model-b" || len(calls) != 2 || calls[0] != "a1" || calls[1] != "b1" {
			t.Fatalf("result=%+v calls=%v", result, calls)
		}
	})
	t.Run("site credential errors keep falling back across sites", func(t *testing.T) {
		for _, status := range []int{401, 403, 404} {
			f := newImageGenerationFixture(t, map[string]int{"a1": status})
			result := f.generate(t, []string{"model-a", "model-b"}, nil)
			calls := f.upstreamCalls()
			if !result.OK || result.Model != "model-a" || result.SiteName != "a2" || len(calls) != 2 {
				t.Fatalf("status=%d result=%+v calls=%v", status, result, calls)
			}
		}
	})
	t.Run("model without route", func(t *testing.T) {
		f := newImageGenerationFixture(t, map[string]int{})
		result := f.generate(t, []string{"missing", "model-b"}, nil)
		if !result.OK || len(result.Attempts) != 1 || result.Attempts[0].Model != "missing" || len(f.upstreamCalls()) != 1 {
			t.Fatalf("result=%+v calls=%v", result, f.upstreamCalls())
		}
	})
}

func TestGenerateImageSelectedSites(t *testing.T) {
	t.Run("only selected site is requested", func(t *testing.T) {
		f := newImageGenerationFixture(t, map[string]int{"a1": 500})
		result := f.generateSelections(t, []ImageGenerationModel{{Model: "model-a", SiteIDs: []uuid.UUID{f.sites["a2"].ID}}}, nil)
		if !result.OK || result.SiteName != "a2" || len(f.upstreamCalls()) != 1 || f.upstreamCalls()[0] != "a2" {
			t.Fatalf("result=%+v calls=%v", result, f.upstreamCalls())
		}
	})
	t.Run("selected sites fall back then next model", func(t *testing.T) {
		f := newImageGenerationFixture(t, map[string]int{"a1": 500, "a2": 500})
		models := []ImageGenerationModel{
			{Model: "model-a", SiteIDs: []uuid.UUID{f.sites["a1"].ID, f.sites["a2"].ID}},
			{Model: "model-b", SiteIDs: []uuid.UUID{f.sites["b1"].ID}},
		}
		result := f.generateSelections(t, models, nil)
		if !result.OK || result.Model != "model-b" || len(result.Attempts) != 2 {
			t.Fatalf("result=%+v calls=%v", result, f.upstreamCalls())
		}
		if calls := f.upstreamCalls(); len(calls) != 3 || calls[0] != "a1" || calls[1] != "a2" || calls[2] != "b1" {
			t.Fatalf("calls=%v", calls)
		}
	})
	t.Run("empty intersection has no route", func(t *testing.T) {
		f := newImageGenerationFixture(t, map[string]int{})
		siteID := f.sites["a1"].ID
		result := f.generateSelections(t, []ImageGenerationModel{{Model: "model-a", SiteIDs: []uuid.UUID{f.sites["a2"].ID}}}, &siteID)
		if result.OK || len(f.upstreamCalls()) != 0 || len(result.Attempts) != 1 {
			t.Fatalf("result=%+v calls=%v", result, f.upstreamCalls())
		}
	})
}

func TestGenerateImageSiteRestrictionFailureAndCancellation(t *testing.T) {
	f := newImageGenerationFixture(t, map[string]int{"a1": 500})
	siteID := f.sites["a2"].ID
	result := f.generate(t, []string{"model-a"}, &siteID)
	if !result.OK || len(f.upstreamCalls()) != 1 || f.upstreamCalls()[0] != "a2" {
		t.Fatalf("restricted result=%+v calls=%v", result, f.upstreamCalls())
	}

	failed := newImageGenerationFixture(t, map[string]int{"a1": 500, "a2": 500, "b1": 500})
	result = failed.generate(t, []string{"model-a", "model-b"}, nil)
	if result.OK || len(result.Attempts) != 3 || len(strings.Split(result.ErrorMessage, "\n")) != 2 {
		t.Fatalf("failure result=%+v", result)
	}

	cancelled := newImageGenerationFixture(t, map[string]int{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result = cancelled.handler.GenerateImage(ctx, ImageGenerationRequest{Models: []ImageGenerationModel{{Model: "model-a"}}, Prompt: "cat"})
	if result.OK || len(cancelled.upstreamCalls()) != 0 {
		t.Fatalf("cancelled result=%+v calls=%v", result, cancelled.upstreamCalls())
	}
}

func TestGenerateImageLegacyAttemptCap(t *testing.T) {
	f := newImageGenerationFixture(t, map[string]int{"a1": 500, "a2": 500, "b1": 500})
	result := f.handler.generateImage(context.Background(), ImageGenerationRequest{Models: []ImageGenerationModel{{Model: "model-a"}, {Model: "model-b"}}, Prompt: "cat"}, "", bridgeImageMaxAttempts)
	if result.OK || len(result.Attempts) != 3 || len(f.upstreamCalls()) != 3 {
		t.Fatalf("legacy cap result=%+v calls=%v", result, f.upstreamCalls())
	}
}
