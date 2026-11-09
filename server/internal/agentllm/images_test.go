package agentllm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"xlyra/server/internal/agentproxy"
	"xlyra/server/internal/gateway"
	"xlyra/server/internal/store"
)

func newImageTestHandler() (*Handler, *fakeAgentRepository) {
	repo := &fakeAgentRepository{
		run: store.AgentRun{AgentInstanceID: BuiltinInstanceID, SessionID: "sess-1", RunID: "run-1", Model: "gpt-5", Status: store.AgentRunActive},
		tokens: []fakeAgentTokenEntry{{
			value:     "live-token",
			input:     store.AgentRunInput{AgentInstanceID: "agent-1", SessionID: "sess-1", RunID: "run-1", Model: "gpt-5"},
			expiresAt: time.Now().Add(time.Hour),
		}},
		internalAPIKey: store.APIKey{ID: uuid.New()},
	}
	h := newTestHandler(repo)
	return &h, repo
}

func imageRequest(token, body string) (*httptest.ResponseRecorder, *http.Request) {
	req := httptest.NewRequest(http.MethodPost, "/internal/agent-llm/images", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return httptest.NewRecorder(), req
}

func TestImagesAuthenticationAndValidation(t *testing.T) {
	h, _ := newImageTestHandler()
	h.SetImageModelsSource(func() []agentproxy.ImageModelSelection {
		return []agentproxy.ImageModelSelection{{Model: "gpt-image-2"}}
	})
	for _, token := range []string{"", "bad-token"} {
		rec, req := imageRequest(token, `{"prompt":"a cat"}`)
		h.Images(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: status=%d", token, rec.Code)
		}
	}
	for _, body := range []string{`{"prompt":" "}`, `{"prompt":"` + strings.Repeat("图", 4001) + `"}`, `{"prompt":"cat","size":"large"}`, `{"prompt":"cat","quality":"best"}`} {
		rec, req := imageRequest("live-token", body)
		h.Images(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body prefix %.40s: status=%d body=%s", body, rec.Code, rec.Body.String())
		}
	}
}

func TestImagesRequiresConfigurationAndUsesLiveSource(t *testing.T) {
	h, _ := newImageTestHandler()
	models := []agentproxy.ImageModelSelection{}
	h.SetImageModelsSource(func() []agentproxy.ImageModelSelection {
		return append([]agentproxy.ImageModelSelection(nil), models...)
	})
	rec, req := imageRequest("live-token", `{"prompt":"cat"}`)
	h.Images(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409", rec.Code)
	}
	siteID := uuid.New()
	models = []agentproxy.ImageModelSelection{{Model: "gpt-image-2", SiteIDs: []string{siteID.String(), "invalid"}}}
	h.imageGenerate = func(_ context.Context, req gateway.ImageGenerationRequest) gateway.ImageGenerationResult {
		if len(req.Models) != 1 || req.Models[0].Model != "gpt-image-2" || len(req.Models[0].SiteIDs) != 1 || req.Models[0].SiteIDs[0] != siteID {
			t.Fatalf("models=%+v", req.Models)
		}
		return gateway.ImageGenerationResult{OK: true, B64: "aGk=", Model: "gpt-image-2"}
	}
	rec, req = imageRequest("live-token", `{"prompt":"cat","model":"not-the-run-model"}`)
	h.Images(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestImagesSuccessFailureQuotaAndEndRunCleanup(t *testing.T) {
	h, repo := newImageTestHandler()
	h.SetImageModelsSource(func() []agentproxy.ImageModelSelection {
		return []agentproxy.ImageModelSelection{{Model: "gpt-image-2"}}
	})
	calls := 0
	h.imageGenerate = func(_ context.Context, req gateway.ImageGenerationRequest) gateway.ImageGenerationResult {
		calls++
		return gateway.ImageGenerationResult{
			OK: true, B64: "aGk=", RevisedPrompt: "a small cat", Model: "gpt-image-2", SiteName: "站点一",
			Attempts: []gateway.ImageGenerationAttempt{{Model: "gpt-image-1", SiteName: "旧站点", Error: "upstream"}},
		}
	}
	for index := 0; index < 3; index++ {
		rec, req := imageRequest("live-token", `{"prompt":"cat","model":"other-model","size":"auto","quality":"high"}`)
		h.Images(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: status=%d body=%s", index+1, rec.Code, rec.Body.String())
		}
		if index == 0 {
			var payload struct {
				Data struct {
					B64      string `json:"b64_json"`
					Model    string `json:"model"`
					Site     string `json:"site"`
					Attempts []struct {
						Model string `json:"model"`
						Site  string `json:"site"`
						Error string `json:"error"`
					} `json:"attempts"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Data.B64 != "aGk=" || payload.Data.Model != "gpt-image-2" || payload.Data.Site != "站点一" || len(payload.Data.Attempts) != 1 || payload.Data.Attempts[0].Site != "旧站点" {
				t.Fatalf("unexpected response: %+v", payload.Data)
			}
		}
	}
	rec, req := imageRequest("live-token", `{"prompt":"cat"}`)
	h.Images(rec, req)
	if rec.Code != http.StatusTooManyRequests || calls != 3 {
		t.Fatalf("fourth call: status=%d calls=%d", rec.Code, calls)
	}
	if err := h.EndRun(context.Background(), "sess-1", "run-1", "gpt-5"); err != nil {
		t.Fatal(err)
	}
	h.imageTracker.mu.Lock()
	remaining := len(h.imageTracker.runs)
	h.imageTracker.mu.Unlock()
	if remaining != 0 || repo.endedInput == nil {
		t.Fatalf("EndRun did not clear image state: remaining=%d", remaining)
	}

	h2, _ := newImageTestHandler()
	h2.SetImageModelsSource(func() []agentproxy.ImageModelSelection {
		return []agentproxy.ImageModelSelection{{Model: "gpt-image-2"}}
	})
	h2.imageGenerate = func(context.Context, gateway.ImageGenerationRequest) gateway.ImageGenerationResult {
		return gateway.ImageGenerationResult{ErrorMessage: "model-a: denied"}
	}
	rec, req = imageRequest("live-token", `{"prompt":"cat"}`)
	h2.Images(rec, req)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"code":"upstream_failed"`) {
		t.Fatalf("failure response: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
