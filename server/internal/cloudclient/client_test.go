package cloudclient

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

func TestCloudAddressGuards(t *testing.T) {
	client := &Client{allowLocal: true, http: newHTTPClient(time.Second, true), now: time.Now}
	if err := client.validateCloud("http://example.com/cloud"); err == nil {
		t.Fatal("accepted public http cloud")
	}
	if err := client.validateCloud("https://cloud.example"); err != nil {
		t.Fatal(err)
	}
	if err := client.validateCloud("http://127.0.0.1:9"); err != nil {
		t.Fatal(err)
	}
	if err := client.validateCloud("https://user:pass@cloud.example"); err == nil {
		t.Fatal("accepted cloud userinfo")
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

func TestCloudClientRejectsInternalTargets(t *testing.T) {
	client := newHTTPClient(time.Second, false)
	if client.CheckRedirect == nil || client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("redirects are followed")
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.DialContext == nil || transport.Proxy != nil {
		t.Fatal("cloud transport is not guarded")
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
	st := openCloudStore(t)
	cloud := newFakeCloud("approve")
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	client := newTestClient(t, st, srv.URL, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go client.Run(ctx)
	time.Sleep(180 * time.Millisecond)
	if cloud.total() != 0 {
		t.Fatalf("unbound cloud hits = %d", cloud.total())
	}
}

func TestPairApproveHeartbeatAndSecrets(t *testing.T) {
	st := openCloudStore(t)
	cloud := newFakeCloud("approve")
	srv := httptest.NewServer(cloud)
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
	if view.Status != store.CloudStatusPairing || view.Pairing == nil || view.Pairing.UserCode != cloud.userCode {
		t.Fatalf("view = %#v", view)
	}
	assertPairingSecrets(t, st, cloud)
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), cloud.token) || strings.Contains(string(raw), "BEGIN") {
		t.Fatalf("status leaked poll token: %s", raw)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go client.Run(ctx)
	repo := store.NewCloudBindingRepository(st.DB())
	waitUntil(t, 5*time.Second, func() bool {
		row, err := repo.Get(context.Background())
		return err == nil && row.Status == store.CloudStatusBound && row.InstanceName == "renamed" && row.UserDisplayName == "Ada" && row.LastSyncAt != nil
	})
	row, err := repo.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if row.InstanceID != cloud.instanceID || row.PollTokenEnc != "" || row.PairingCodeHint != "" {
		t.Fatalf("bound row = %#v", row)
	}
	auditMu.Lock()
	seenAudits := append([]string(nil), audits...)
	auditMu.Unlock()
	if !strings.Contains(strings.Join(seenAudits, "\n"), "cloud_binding.bound") {
		t.Fatalf("audits = %v", seenAudits)
	}
	for _, item := range seenAudits {
		if strings.Contains(item, cloud.userCode) || strings.Contains(item, cloud.token) {
			t.Fatalf("audit leaked secret: %s", item)
		}
	}
	assertHeartbeatWhitelist(t, cloud.lastBody())
	if cloud.count("/api/instance/heartbeat") < 1 || cloud.count("/api/instance/pair/poll") < 2 {
		t.Fatalf("hits = %#v", cloud.snapshot())
	}
}

func TestPairingCodeSurvivesRestart(t *testing.T) {
	st := openCloudStore(t)
	cloud := newFakeCloud("approve")
	srv := httptest.NewServer(cloud)
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
	if err != nil || view.Pairing == nil || view.Pairing.UserCode != cloud.userCode {
		t.Fatalf("restarted status = %#v err=%v", view, err)
	}
	if err := other.CancelPairing(context.Background()); err != nil {
		t.Fatal(err)
	}
	cleared, err := other.Status(context.Background())
	if err != nil || cleared.Pairing != nil || cleared.Status != store.CloudStatusUnbound {
		t.Fatalf("cancelled = %#v err=%v", cleared, err)
	}
	row, err := store.NewCloudBindingRepository(st.DB()).Get(context.Background())
	if err != nil || row.PairingCodeHint != "" {
		t.Fatalf("hint after cancel = %q err=%v", row.PairingCodeHint, err)
	}

	if _, err := client.StartPairing(context.Background(), "desk"); err != nil {
		t.Fatal(err)
	}
	repo := store.NewCloudBindingRepository(st.DB())
	if err := repo.MarkBound(context.Background(), cloud.instanceID, "desk", "Ada", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	bound, err := other.Status(context.Background())
	if err != nil || bound.Pairing != nil || bound.Status != store.CloudStatusBound {
		t.Fatalf("bound = %#v err=%v", bound, err)
	}
	if err := other.Unbind(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := repo.Get(context.Background())
	if err != nil || after.PairingCodeHint != "" || after.Status != store.CloudStatusUnbound {
		t.Fatalf("unbound = %#v err=%v", after, err)
	}
}

func TestPairDeniedAndExpired(t *testing.T) {
	for _, mode := range []string{"deny", "expire"} {
		t.Run(mode, func(t *testing.T) {
			st := openCloudStore(t)
			cloud := newFakeCloud(mode)
			srv := httptest.NewServer(cloud)
			t.Cleanup(srv.Close)
			client := newTestClient(t, st, srv.URL, nil)
			if _, err := client.StartPairing(context.Background(), ""); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			go client.Run(ctx)
			repo := store.NewCloudBindingRepository(st.DB())
			want := "配对被拒绝"
			if mode == "expire" {
				want = "配对已过期"
			}
			waitUntil(t, 5*time.Second, func() bool {
				row, err := repo.Get(context.Background())
				return err == nil && row.Status == store.CloudStatusUnbound && row.LastError == want
			})
			if cloud.count("/api/instance/heartbeat") != 0 {
				t.Fatalf("heartbeat hits = %d", cloud.count("/api/instance/heartbeat"))
			}
		})
	}
}

func TestRevokedStopsAndClockSkewBacksOff(t *testing.T) {
	t.Run("revoked", func(t *testing.T) {
		st := openCloudStore(t)
		cloud := newFakeCloud("revoke")
		srv := httptest.NewServer(cloud)
		t.Cleanup(srv.Close)
		client := newTestClient(t, st, srv.URL, nil)
		if _, err := client.StartPairing(context.Background(), "desk"); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go client.Run(ctx)
		repo := store.NewCloudBindingRepository(st.DB())
		waitUntil(t, 5*time.Second, func() bool {
			row, err := repo.Get(context.Background())
			return err == nil && row.Status == store.CloudStatusRevoked && row.LastError == "已在账号侧解除连接" && row.PublicKey != "" && row.InstanceID == ""
		})
		stable := cloud.count("/api/instance/heartbeat")
		time.Sleep(250 * time.Millisecond)
		if got := cloud.count("/api/instance/heartbeat"); got != stable {
			t.Fatalf("heartbeats after revoke = %d, was %d", got, stable)
		}
		row, _ := repo.Get(context.Background())
		if row.PrivateKeyEnc == "" || row.PublicKey == "" {
			t.Fatal("revoked row dropped the key pair")
		}
	})

	t.Run("clock skew", func(t *testing.T) {
		st := openCloudStore(t)
		cloud := newFakeCloud("skew")
		srv := httptest.NewServer(cloud)
		t.Cleanup(srv.Close)
		client := newTestClient(t, st, srv.URL, nil)
		if _, err := client.StartPairing(context.Background(), "desk"); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		started := time.Now()
		go client.Run(ctx)
		repo := store.NewCloudBindingRepository(st.DB())
		waitUntil(t, 5*time.Second, func() bool {
			row, err := repo.Get(context.Background())
			return err == nil && row.Status == store.CloudStatusBound && strings.Contains(row.LastError, "2026-01-02T03:04:05Z")
		})
		time.Sleep(500 * time.Millisecond)
		hits := cloud.count("/api/instance/heartbeat")
		if hits < 2 || hits > 8 {
			t.Fatalf("heartbeat hits in %s = %d, want a backed-off handful", time.Since(started), hits)
		}
		row, _ := repo.Get(context.Background())
		if row.Status != store.CloudStatusBound {
			t.Fatalf("status = %s", row.Status)
		}
	})
}

func TestStatusDoesNotWaitForAHungCloud(t *testing.T) {
	st := openCloudStore(t)
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

func newTestClient(t *testing.T, st *store.Store, cloudURL string, audit func(context.Context, string, map[string]any)) *Client {
	t.Helper()
	return New(
		store.NewCloudBindingRepository(st.DB()),
		store.NewJSPluginRepository(st.DB()),
		cloudTestMasterKey,
		func(context.Context) (string, error) { return cloudURL, nil },
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

func assertPairingSecrets(t *testing.T, st *store.Store, cloud *fakeCloud) {
	t.Helper()
	row, err := store.NewCloudBindingRepository(st.DB()).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	token, err := crypto.DecryptSecret(cloudTestMasterKey, row.PollTokenEnc)
	if err != nil || token != cloud.token || row.PollTokenEnc == cloud.token {
		t.Fatalf("poll token storage token=%q err=%v enc=%q", token, err, row.PollTokenEnc)
	}
	privText, err := crypto.DecryptSecret(cloudTestMasterKey, row.PrivateKeyEnc)
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
	if row.PairingCodeHint != cloud.userCode {
		t.Fatalf("pairing code hint = %q", row.PairingCodeHint)
	}
	blob := strings.Join([]string{row.PublicKey, row.PrivateKeyEnc, row.PollTokenEnc, row.InstanceID, row.LastError, row.InstanceName, row.UserDisplayName, row.CloudURL}, "\n")
	if strings.Contains(blob, cloud.userCode) || strings.Contains(blob, strings.ReplaceAll(cloud.userCode, "-", "")) || strings.Contains(blob, cloud.token) || strings.Contains(blob, privText) {
		t.Fatal("database row contains userCode outside the pairing hint, pollToken, or the private key")
	}
	if strings.Contains(row.PairingCodeHint, cloud.token) || strings.Contains(row.PairingCodeHint, privText) {
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

type fakeCloud struct {
	mu             sync.Mutex
	mode           string
	hits           map[string]int
	polls          int
	hearts         int
	pub            ed25519.PublicKey
	userCode       string
	token          string
	instanceID     string
	heartBody      []byte
	pushes         []pushCall
	pushPutStatus  int
	pushPutCode    string
	dropPush       bool
	dropSend       bool
	sendStatus     int
	sendCode       string
	sendResult     string
	sendRetryAfter string
	sendBody       []byte
}

func newFakeCloud(mode string) *fakeCloud {
	return &fakeCloud{
		mode:       mode,
		hits:       map[string]int{},
		userCode:   "K7QM-2XFD",
		token:      "poll-token-0123456789abcdef0123456789",
		instanceID: "inst-1",
	}
}

func (h *fakeCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	h.mu.Lock()
	h.hits[r.URL.Path]++
	kind := ""
	status, code, message, serverTime := 0, "", "", ""
	var payload any
	switch r.URL.Path {
	case "/api/instance/pair/start":
		var req struct {
			PublicKey string `json:"publicKey"`
		}
		_ = json.Unmarshal(body, &req)
		raw, err := base64.StdEncoding.DecodeString(req.PublicKey)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			status, code, message = http.StatusBadRequest, "invalid_request", "bad public key"
			break
		}
		h.pub = ed25519.PublicKey(raw)
		kind = "json"
		payload = map[string]any{
			"userCode":        h.userCode,
			"pollToken":       h.token,
			"expiresAt":       time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339),
			"intervalSeconds": 3,
		}
	case "/api/instance/pair/poll":
		h.polls++
		kind = "json"
		switch {
		case h.mode == "deny":
			payload = map[string]any{"status": "denied"}
		case h.mode == "expire":
			payload = map[string]any{"status": "expired"}
		case h.polls == 1:
			payload = map[string]any{"status": "pending"}
		default:
			payload = map[string]any{
				"status": "approved", "instanceId": h.instanceID, "instanceName": "from-cloud", "userDisplayName": "Ada",
			}
		}
	case "/api/instance/heartbeat":
		var ok bool
		ok, code, message, serverTime = h.signatureError(r, body)
		if !ok {
			status = http.StatusUnauthorized
			break
		}
		h.hearts++
		h.heartBody = append([]byte(nil), body...)
		if h.mode == "skew" {
			status, code, message, serverTime = http.StatusUnauthorized, "clock_skew", "clock skew", "2026-01-02T03:04:05Z"
			break
		}
		if h.mode == "revoke" && h.hearts > 1 {
			status, code, message = http.StatusUnauthorized, "instance_revoked", "revoked"
			break
		}
		kind = "json"
		payload = map[string]any{
			"ok": true, "serverTime": time.Now().UTC().Format(time.RFC3339),
			"instanceName": "renamed", "userDisplayName": "Ada",
		}
	case "/api/instance/unbind":
		var ok bool
		ok, code, message, serverTime = h.signatureError(r, body)
		if !ok {
			status = http.StatusUnauthorized
			break
		}
		kind = "empty"
		status = http.StatusNoContent
	default:
		if strings.HasPrefix(r.URL.Path, "/api/instance/push/") {
			kind = "push"
			break
		}
		status, code, message = http.StatusNotFound, "not_found", "not found"
	}
	h.mu.Unlock()
	switch kind {
	case "json":
		writeJSON(w, http.StatusOK, payload)
	case "empty":
		w.WriteHeader(status)
	case "push":
		h.servePush(w, r, body)
	default:
		writeCloudError(w, status, code, message, serverTime)
	}
}

func (h *fakeCloud) verify(w http.ResponseWriter, r *http.Request, body []byte) bool {
	h.mu.Lock()
	ok, code, message, serverTime := h.signatureError(r, body)
	h.mu.Unlock()
	if ok {
		return true
	}
	writeCloudError(w, http.StatusUnauthorized, code, message, serverTime)
	return false
}

func (h *fakeCloud) signatureError(r *http.Request, body []byte) (bool, string, string, string) {
	if r.Header.Get("X-XLyra-Instance") != h.instanceID {
		return false, "bad_signature", "bad instance", ""
	}
	unix, err := strconv.ParseInt(r.Header.Get("X-XLyra-Timestamp"), 10, 64)
	if err != nil {
		return false, "bad_signature", "bad timestamp", ""
	}
	if delta := time.Now().Unix() - unix; delta > 300 || delta < -300 {
		return false, "clock_skew", "clock skew", time.Now().UTC().Format(time.RFC3339)
	}
	sig, err := base64.StdEncoding.DecodeString(r.Header.Get("X-XLyra-Signature"))
	message := CanonicalMessage(r.Method, r.URL.Path, unix, body)
	if err != nil || !ed25519.Verify(h.pub, []byte(message), sig) {
		return false, "bad_signature", "bad signature", ""
	}
	return true, "", "", ""
}

func (h *fakeCloud) count(path string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hits[path]
}

func (h *fakeCloud) total() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, hits := range h.hits {
		n += hits
	}
	return n
}

func (h *fakeCloud) snapshot() map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]int, len(h.hits))
	for key, value := range h.hits {
		out[key] = value
	}
	return out
}

func (h *fakeCloud) lastBody() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]byte(nil), h.heartBody...)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeCloudError(w http.ResponseWriter, status int, code, message, serverTime string) {
	body := map[string]any{"code": code, "message": message}
	if serverTime != "" {
		body["serverTime"] = serverTime
	}
	writeJSON(w, status, map[string]any{"error": body})
}
