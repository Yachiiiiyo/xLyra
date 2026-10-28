package hubclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"xlyra/server/internal/crypto"
	"xlyra/server/internal/store"
)

func TestHubAddressGuards(t *testing.T) {
	client := &Client{allowLocal: true, http: newHTTPClient(time.Second, true), now: time.Now}
	if err := client.validateHub("http://example.com/hub"); err == nil {
		t.Fatal("accepted public http hub")
	}
	if err := client.validateHub("https://hub.example"); err != nil {
		t.Fatal(err)
	}
	if err := client.validateHub("http://127.0.0.1:9"); err != nil {
		t.Fatal(err)
	}
	if err := client.validateHub("https://user:pass@hub.example"); err == nil {
		t.Fatal("accepted hub userinfo")
	}

	evilHits := 0
	evil := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		evilHits++
	}))
	t.Cleanup(evil.Close)
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/steal", http.StatusFound)
	}))
	t.Cleanup(good.Close)
	if _, _, err := client.post(context.Background(), good.URL, "/api/instance/pair/start", []byte(`{}`), nil); err == nil || evilHits != 0 {
		t.Fatalf("redirect err=%v evilHits=%d", err, evilHits)
	}
}

func TestHubClientRejectsInternalTargets(t *testing.T) {
	client := newHTTPClient(time.Second, false)
	if client.CheckRedirect == nil || client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("redirects are followed")
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.DialContext == nil || transport.Proxy != nil {
		t.Fatal("hub transport is not guarded")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, rawURL := range []string{"https://127.0.0.1/health", "https://169.254.169.254/latest/meta-data/"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if resp != nil {
			resp.Body.Close()
			t.Fatalf("%s connected", rawURL)
		}
		if err == nil || !strings.Contains(err.Error(), "blocked connection") {
			t.Fatalf("%s = %v", rawURL, err)
		}
	}
}

func TestUnboundInstanceSendsNothing(t *testing.T) {
	st := openHubStore(t)
	hub := newFakeHub("approve")
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)
	client := newTestClient(t, st, srv.URL, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go client.Run(ctx)
	time.Sleep(180 * time.Millisecond)
	if hub.total() != 0 {
		t.Fatalf("unbound hub hits = %d", hub.total())
	}
}

func TestPairApproveHeartbeatAndSecrets(t *testing.T) {
	st := openHubStore(t)
	hub := newFakeHub("approve")
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)
	var auditMu sync.Mutex
	var audits []string
	client := newTestClient(t, st, srv.URL, func(_ context.Context, action string, metadata map[string]any) {
		raw, _ := json.Marshal(metadata)
		auditMu.Lock()
		audits = append(audits, action+" "+string(raw))
		auditMu.Unlock()
	})
	view, err := client.StartPairing(context.Background(), "desk")
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != store.HubStatusPairing || view.Pairing == nil || view.Pairing.UserCode != hub.userCode {
		t.Fatalf("view = %#v", view)
	}
	assertPairingSecrets(t, st, hub)
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), hub.token) || strings.Contains(string(raw), "BEGIN") {
		t.Fatalf("status leaked poll token: %s", raw)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go client.Run(ctx)
	repo := store.NewHubBindingRepository(st.DB())
	waitUntil(t, 5*time.Second, func() bool {
		row, err := repo.Get(context.Background())
		return err == nil && row.Status == store.HubStatusBound && row.InstanceName == "renamed" && row.UserDisplayName == "Ada" && row.LastSyncAt != nil
	})
	row, err := repo.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if row.InstanceID != hub.instanceID || row.PollTokenEnc != "" || row.PairingCodeHint != "" {
		t.Fatalf("bound row = %#v", row)
	}
	auditMu.Lock()
	seenAudits := append([]string(nil), audits...)
	auditMu.Unlock()
	if !strings.Contains(strings.Join(seenAudits, "\n"), "hub_binding.bound") {
		t.Fatalf("audits = %v", seenAudits)
	}
	for _, item := range seenAudits {
		if strings.Contains(item, hub.userCode) || strings.Contains(item, hub.token) {
			t.Fatalf("audit leaked secret: %s", item)
		}
	}
	assertHeartbeatWhitelist(t, hub.lastBody())
	if hub.count("/api/instance/heartbeat") < 1 || hub.count("/api/instance/pair/poll") < 2 {
		t.Fatalf("hits = %#v", hub.snapshot())
	}
}

func TestPairingCodeSurvivesRestart(t *testing.T) {
	st := openHubStore(t)
	hub := newFakeHub("approve")
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)
	client := newTestClient(t, st, srv.URL, nil)
	if _, err := client.StartPairing(context.Background(), "desk"); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	client.pending = nil
	client.mu.Unlock()
	other := newTestClient(t, st, srv.URL, nil)
	view, err := other.Status(context.Background())
	if err != nil || view.Pairing == nil || view.Pairing.UserCode != hub.userCode {
		t.Fatalf("restarted status = %#v err=%v", view, err)
	}
	if err := other.CancelPairing(context.Background()); err != nil {
		t.Fatal(err)
	}
	cleared, err := other.Status(context.Background())
	if err != nil || cleared.Pairing != nil || cleared.Status != store.HubStatusUnbound {
		t.Fatalf("cancelled = %#v err=%v", cleared, err)
	}
	row, err := store.NewHubBindingRepository(st.DB()).Get(context.Background())
	if err != nil || row.PairingCodeHint != "" {
		t.Fatalf("hint after cancel = %q err=%v", row.PairingCodeHint, err)
	}

	if _, err := client.StartPairing(context.Background(), "desk"); err != nil {
		t.Fatal(err)
	}
	repo := store.NewHubBindingRepository(st.DB())
	if err := repo.MarkBound(context.Background(), hub.instanceID, "desk", "Ada", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	bound, err := other.Status(context.Background())
	if err != nil || bound.Pairing != nil || bound.Status != store.HubStatusBound {
		t.Fatalf("bound = %#v err=%v", bound, err)
	}
	if err := other.Unbind(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := repo.Get(context.Background())
	if err != nil || after.PairingCodeHint != "" || after.Status != store.HubStatusUnbound {
		t.Fatalf("unbound = %#v err=%v", after, err)
	}
}

func TestPairDeniedAndExpired(t *testing.T) {
	for _, mode := range []string{"deny", "expire"} {
		t.Run(mode, func(t *testing.T) {
			st := openHubStore(t)
			hub := newFakeHub(mode)
			srv := httptest.NewServer(hub)
			t.Cleanup(srv.Close)
			client := newTestClient(t, st, srv.URL, nil)
			if _, err := client.StartPairing(context.Background(), ""); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			go client.Run(ctx)
			repo := store.NewHubBindingRepository(st.DB())
			want := "配对被拒绝"
			if mode == "expire" {
				want = "配对已过期"
			}
			waitUntil(t, 5*time.Second, func() bool {
				row, err := repo.Get(context.Background())
				return err == nil && row.Status == store.HubStatusUnbound && row.LastError == want
			})
			if hub.count("/api/instance/heartbeat") != 0 {
				t.Fatalf("heartbeat hits = %d", hub.count("/api/instance/heartbeat"))
			}
		})
	}
}

func TestRevokedStopsAndClockSkewBacksOff(t *testing.T) {
	t.Run("revoked", func(t *testing.T) {
		st := openHubStore(t)
		hub := newFakeHub("revoke")
		srv := httptest.NewServer(hub)
		t.Cleanup(srv.Close)
		client := newTestClient(t, st, srv.URL, nil)
		if _, err := client.StartPairing(context.Background(), "desk"); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go client.Run(ctx)
		repo := store.NewHubBindingRepository(st.DB())
		waitUntil(t, 5*time.Second, func() bool {
			row, err := repo.Get(context.Background())
			return err == nil && row.Status == store.HubStatusRevoked && row.LastError == "已在账号侧解除连接" && row.PublicKey != "" && row.InstanceID == ""
		})
		stable := hub.count("/api/instance/heartbeat")
		time.Sleep(250 * time.Millisecond)
		if got := hub.count("/api/instance/heartbeat"); got != stable {
			t.Fatalf("heartbeats after revoke = %d, was %d", got, stable)
		}
		row, _ := repo.Get(context.Background())
		if row.PrivateKeyEnc == "" || row.PublicKey == "" {
			t.Fatal("revoked row dropped the key pair")
		}
	})

	t.Run("clock skew", func(t *testing.T) {
		st := openHubStore(t)
		hub := newFakeHub("skew")
		srv := httptest.NewServer(hub)
		t.Cleanup(srv.Close)
		client := newTestClient(t, st, srv.URL, nil)
		if _, err := client.StartPairing(context.Background(), "desk"); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		started := time.Now()
		go client.Run(ctx)
		repo := store.NewHubBindingRepository(st.DB())
		waitUntil(t, 5*time.Second, func() bool {
			row, err := repo.Get(context.Background())
			return err == nil && row.Status == store.HubStatusBound && strings.Contains(row.LastError, "2026-01-02T03:04:05Z")
		})
		time.Sleep(500 * time.Millisecond)
		hits := hub.count("/api/instance/heartbeat")
		if hits < 2 || hits > 8 {
			t.Fatalf("heartbeat hits in %s = %d, want a backed-off handful", time.Since(started), hits)
		}
		row, _ := repo.Get(context.Background())
		if row.Status != store.HubStatusBound {
			t.Fatalf("status = %s", row.Status)
		}
	})
}

func TestStatusDoesNotWaitForAHungHub(t *testing.T) {
	st := openHubStore(t)
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-stop
	}))
	t.Cleanup(func() {
		close(stop)
		srv.Close()
	})
	client := newTestClient(t, st, srv.URL, nil)
	done := make(chan error, 1)
	started := time.Now()
	go func() {
		_, err := client.StartPairing(context.Background(), "desk")
		done <- err
	}()
	time.Sleep(40 * time.Millisecond)
	began := time.Now()
	view, err := client.Status(context.Background())
	if err != nil || time.Since(began) > 100*time.Millisecond {
		t.Fatalf("status took %s err=%v view=%#v", time.Since(began), err, view)
	}
	select {
	case err := <-done:
		if err == nil || time.Since(started) > 7*time.Second {
			t.Fatalf("start err=%v elapsed=%s", err, time.Since(started))
		}
	case <-time.After(7 * time.Second):
		t.Fatal("StartPairing did not fail")
	}
}

func newTestClient(t *testing.T, st *store.Store, hubURL string, audit func(context.Context, string, map[string]any)) *Client {
	t.Helper()
	return New(
		store.NewHubBindingRepository(st.DB()),
		store.NewJSPluginRepository(st.DB()),
		hubTestMasterKey,
		func(context.Context) (string, error) { return hubURL, nil },
		Options{
			AllowInsecureLocal:     true,
			HeartbeatInterval:      30 * time.Millisecond,
			FirstHeartbeatDelay:    0,
			HasFirstHeartbeatDelay: true,
			BackoffMin:             80 * time.Millisecond,
			BackoffMax:             300 * time.Millisecond,
			PollInterval:           20 * time.Millisecond,
			LeaseRetry:             20 * time.Millisecond,
			LeaseTTL:               2 * time.Second,
			LeaseRefresh:           200 * time.Millisecond,
			Audit:                  audit,
		},
	)
}

func assertPairingSecrets(t *testing.T, st *store.Store, hub *fakeHub) {
	t.Helper()
	row, err := store.NewHubBindingRepository(st.DB()).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	token, err := crypto.DecryptSecret(hubTestMasterKey, row.PollTokenEnc)
	if err != nil || token != hub.token || row.PollTokenEnc == hub.token {
		t.Fatalf("poll token storage token=%q err=%v enc=%q", token, err, row.PollTokenEnc)
	}
	privText, err := crypto.DecryptSecret(hubTestMasterKey, row.PrivateKeyEnc)
	if err != nil || privText == row.PrivateKeyEnc {
		t.Fatalf("private key storage err=%v", err)
	}
	privRaw, err := base64.StdEncoding.DecodeString(privText)
	if err != nil {
		t.Fatal(err)
	}
	pubRaw, err := base64.StdEncoding.DecodeString(row.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	derived := ed25519.PrivateKey(privRaw).Public().(ed25519.PublicKey)
	if !bytes.Equal(derived, pubRaw) {
		t.Fatal("stored public key does not match the private key")
	}
	if row.PairingCodeHint != hub.userCode {
		t.Fatalf("pairing code hint = %q", row.PairingCodeHint)
	}
	blob := strings.Join([]string{row.PublicKey, row.PrivateKeyEnc, row.PollTokenEnc, row.InstanceID, row.LastError, row.InstanceName, row.UserDisplayName, row.HubURL}, "\n")
	if strings.Contains(blob, hub.userCode) || strings.Contains(blob, strings.ReplaceAll(hub.userCode, "-", "")) || strings.Contains(blob, hub.token) || strings.Contains(blob, privText) {
		t.Fatal("database row contains userCode outside the pairing hint, pollToken, or the private key")
	}
	if strings.Contains(row.PairingCodeHint, hub.token) || strings.Contains(row.PairingCodeHint, privText) {
		t.Fatal("pairing hint contains pollToken or the private key")
	}
}

func assertHeartbeatWhitelist(t *testing.T, raw []byte) {
	t.Helper()
	if len(raw) == 0 {
		t.Fatal("missing heartbeat body")
	}
	assertExactKeys(t, raw, "xlyraVersion", "platform", "plugins", "counts", "sentAt")
	for _, forbidden := range []string{"apiKey", "secret", "password", "baseUrl", "siteUrl", "token", "pollToken", "privateKey"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("heartbeat contains %s", forbidden)
		}
	}
}

func waitUntil(t *testing.T, limit time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("condition was not met")
}

type fakeHub struct {
	mu         sync.Mutex
	mode       string
	hits       map[string]int
	polls      int
	hearts     int
	pub        ed25519.PublicKey
	userCode   string
	token      string
	instanceID string
	heartBody  []byte
}

func newFakeHub(mode string) *fakeHub {
	return &fakeHub{
		mode:       mode,
		hits:       map[string]int{},
		userCode:   "K7QM-2XFD",
		token:      "poll-token-0123456789abcdef0123456789",
		instanceID: "inst-1",
	}
}

func (h *fakeHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hits[r.URL.Path]++
	switch r.URL.Path {
	case "/api/instance/pair/start":
		var req struct {
			PublicKey string `json:"publicKey"`
		}
		_ = json.Unmarshal(body, &req)
		raw, err := base64.StdEncoding.DecodeString(req.PublicKey)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			writeHubError(w, http.StatusBadRequest, "invalid_request", "bad public key", "")
			return
		}
		h.pub = ed25519.PublicKey(raw)
		writeJSON(w, http.StatusOK, map[string]any{
			"userCode":        h.userCode,
			"pollToken":       h.token,
			"expiresAt":       time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339),
			"intervalSeconds": 3,
		})
	case "/api/instance/pair/poll":
		h.polls++
		if h.mode == "deny" {
			writeJSON(w, http.StatusOK, map[string]any{"status": "denied"})
			return
		}
		if h.mode == "expire" {
			writeJSON(w, http.StatusOK, map[string]any{"status": "expired"})
			return
		}
		if h.polls == 1 {
			writeJSON(w, http.StatusOK, map[string]any{"status": "pending"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "approved", "instanceId": h.instanceID, "instanceName": "from-cloud", "userDisplayName": "Ada",
		})
	case "/api/instance/heartbeat":
		if !h.verify(w, r, body) {
			return
		}
		h.hearts++
		h.heartBody = append([]byte(nil), body...)
		if h.mode == "skew" {
			writeHubError(w, http.StatusUnauthorized, "clock_skew", "clock skew", "2026-01-02T03:04:05Z")
			return
		}
		if h.mode == "revoke" && h.hearts > 1 {
			writeHubError(w, http.StatusUnauthorized, "instance_revoked", "revoked", "")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "serverTime": time.Now().UTC().Format(time.RFC3339),
			"instanceName": "renamed", "userDisplayName": "Ada",
		})
	case "/api/instance/unbind":
		if !h.verify(w, r, body) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeHubError(w, http.StatusNotFound, "not_found", "not found", "")
	}
}

func (h *fakeHub) verify(w http.ResponseWriter, r *http.Request, body []byte) bool {
	if r.Header.Get("X-XLyra-Instance") != h.instanceID {
		writeHubError(w, http.StatusUnauthorized, "bad_signature", "bad instance", "")
		return false
	}
	unix, err := strconv.ParseInt(r.Header.Get("X-XLyra-Timestamp"), 10, 64)
	if err != nil {
		writeHubError(w, http.StatusUnauthorized, "bad_signature", "bad timestamp", "")
		return false
	}
	if delta := time.Now().Unix() - unix; delta > 300 || delta < -300 {
		writeHubError(w, http.StatusUnauthorized, "clock_skew", "clock skew", time.Now().UTC().Format(time.RFC3339))
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(r.Header.Get("X-XLyra-Signature"))
	message := CanonicalMessage(r.Method, r.URL.Path, unix, body)
	if err != nil || !ed25519.Verify(h.pub, []byte(message), sig) {
		writeHubError(w, http.StatusUnauthorized, "bad_signature", "bad signature", "")
		return false
	}
	return true
}

func (h *fakeHub) count(path string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hits[path]
}

func (h *fakeHub) total() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, hits := range h.hits {
		n += hits
	}
	return n
}

func (h *fakeHub) snapshot() map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]int, len(h.hits))
	for key, value := range h.hits {
		out[key] = value
	}
	return out
}

func (h *fakeHub) lastBody() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]byte(nil), h.heartBody...)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeHubError(w http.ResponseWriter, status int, code, message, serverTime string) {
	body := map[string]any{"code": code, "message": message}
	if serverTime != "" {
		body["serverTime"] = serverTime
	}
	writeJSON(w, status, map[string]any{"error": body})
}
