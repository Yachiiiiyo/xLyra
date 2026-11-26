package gateway

import (
	"net/http"
	"testing"

	"xlyra/server/internal/jsplugin"
	routeengine "xlyra/server/internal/router"
)

func TestJSProtocolAdapterUpstreamCall(t *testing.T) {
	plugin := &jsplugin.Plugin{
		Manifest: jsplugin.Manifest{
			Protocol: jsplugin.ProtocolSection{
				Method: "GET",
				Auth:   "header:X-Api-Key",
			},
		},
	}
	adapter := newJSProtocolAdapter(t.Context(), plugin, gatewayRequest{}, routeengine.Candidate{})
	req, err := newUpstreamHTTPRequest(t.Context(), adapter, "https://example.com/v1/test", []byte(`{}`))
	if err != nil {
		t.Fatalf("newUpstreamHTTPRequest: %v", err)
	}
	if req.Method != http.MethodGet {
		t.Fatalf("method = %q, want GET", req.Method)
	}
	applyUpstreamAuth(req, adapter, "secret")
	if req.Header.Get("X-Api-Key") != "secret" {
		t.Fatalf("auth header = %q", req.Header.Get("X-Api-Key"))
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatalf("unexpected bearer auth")
	}
}

func TestEmptyUpstreamKeyOmitsAuthHeaders(t *testing.T) {
	t.Parallel()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://example.com/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer stale")
	applyUpstreamAuth(req, nil, "")
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("default applyUpstreamAuth Authorization = %q, want empty", got)
	}

	req.Header.Set("x-api-key", "stale")
	(anthropicMessagesProtocolAdapter{}).ApplyUpstreamHeaders(req, "", "", false)
	if got := req.Header.Get("x-api-key"); got != "" {
		t.Fatalf("anthropic x-api-key = %q, want empty", got)
	}

	req.Header.Set("x-api-key", "stale")
	(providerAnthropicMessagesProtocolAdapter{}).ApplyUpstreamHeaders(req, "   ", "", false)
	if got := req.Header.Get("x-api-key"); got != "" {
		t.Fatalf("provider anthropic x-api-key = %q, want empty", got)
	}

	req.Header.Set("x-goog-api-key", "stale")
	applyGoogleGatewayHeaders(req, "")
	if got := req.Header.Get("x-goog-api-key"); got != "" {
		t.Fatalf("google x-goog-api-key = %q, want empty", got)
	}

	bearerPlugin := &jsplugin.Plugin{
		Manifest: jsplugin.Manifest{
			Protocol: jsplugin.ProtocolSection{Auth: "bearer"},
		},
	}
	req.Header.Set("Authorization", "Bearer stale")
	newJSProtocolAdapter(t.Context(), bearerPlugin, gatewayRequest{}, routeengine.Candidate{}).ApplyUpstreamAuth(req, "")
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("js protocol bearer Authorization = %q, want empty", got)
	}

	headerPlugin := &jsplugin.Plugin{
		Manifest: jsplugin.Manifest{
			Protocol: jsplugin.ProtocolSection{Auth: "header:X-Api-Key"},
		},
	}
	req.Header.Set("X-Api-Key", "stale")
	newJSProtocolAdapter(t.Context(), headerPlugin, gatewayRequest{}, routeengine.Candidate{}).ApplyUpstreamAuth(req, "")
	if got := req.Header.Get("X-Api-Key"); got != "" {
		t.Fatalf("js protocol header X-Api-Key = %q, want empty", got)
	}
}
