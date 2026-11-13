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

// TestLiveOpenCodeZenFreeLaneBunTransport sends a real free-lane chat request
// through applyOpenCodeGoGatewayHeaders and the Bun wire transport, the same
// pair the gateway uses for a site with gateway.impersonate_opencode_client
// enabled. It is skipped unless XLYRA_LIVE_OPENCODE_ZEN=1, and it spends a
// little of the upstream free-tier quota. The body fixture is the official
// CLI request shape captured from opencode2api (official system prompt and
// toolset); the tool-fingerprint body shaping itself belongs to plugins.
func TestLiveOpenCodeZenFreeLaneBunTransport(t *testing.T) {
	if os.Getenv("XLYRA_LIVE_OPENCODE_ZEN") == "" {
		t.Skip("set XLYRA_LIVE_OPENCODE_ZEN=1 to run the live OpenCode Zen free-lane check")
	}
	body, err := os.ReadFile("testdata/opencode_free_chat.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, "https://opencode.ai/inference/v1/chat/completions", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	applyOpenCodeGoGatewayHeaders(req, gatewayRequest{DownstreamHeaders: http.Header{}}, "public", true)

	manager := httpclient.NewManager(nil)
	client, err := manager.Client(httpclient.Profile{
		TLSFingerprint:       httpclient.TLSFingerprintBun,
		RequestTimeout:       60 * time.Second,
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
	t.Logf("HTTP %d, body: %s", resp.StatusCode, preview[:min(n, 200)])
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("free lane answered HTTP %d, want 200", resp.StatusCode)
	}
}
