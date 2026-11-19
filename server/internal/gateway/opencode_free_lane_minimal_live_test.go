package gateway

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"xlyra/server/internal/httpclient"
)

// TestLiveOpenCodeZenFreeLaneMinimalBody probes whether the free lane accepts
// a bare request (no official system prompt, no tool fingerprint) through the
// same header+transport pair. Skipped unless XLYRA_LIVE_OPENCODE_ZEN=1; it
// decides whether body shaping must live in a plugin's rewritePayload.
func TestLiveOpenCodeZenFreeLaneMinimalBody(t *testing.T) {
	if os.Getenv("XLYRA_LIVE_OPENCODE_ZEN") == "" {
		t.Skip("set XLYRA_LIVE_OPENCODE_ZEN=1 to run the live OpenCode Zen free-lane check")
	}
	body := `{"model":"nemotron-3.5-lightning-free","messages":[{"role":"user","content":"Reply with exactly: bare-ok"}],"max_tokens":32,"stream":true}`
	req, err := http.NewRequest(http.MethodPost, "https://opencode.ai/inference/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	applyOpenCodeGoGatewayHeaders(req, gatewayRequest{DownstreamHeaders: http.Header{}}, "public", true)

	manager := httpclient.NewManager(nil)
	client, err := manager.Client(httpclient.Profile{
		TLSFingerprint:        httpclient.TLSFingerprintBun,
		RequestTimeout:        60 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("bun client: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("free-lane request: %v", err)
	}
	defer resp.Body.Close()
	preview := make([]byte, 400)
	n, _ := io.ReadFull(resp.Body, preview)
	t.Logf("HTTP %d, body: %s", resp.StatusCode, preview[:min(n, 250)])
	// The verdict is logged either way; see the run output for 200 vs 403.
	if resp.StatusCode != http.StatusOK {
		t.Logf("bare body REJECTED (status %d) — body shaping is required", resp.StatusCode)
	}
}
