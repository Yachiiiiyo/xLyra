package hubclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"xlyra/server/internal/crypto"
	"xlyra/server/internal/store"
)

func TestOwnsPlugin(t *testing.T) {
	const signer = "SHA256:abc"
	active := DeveloperProfile{
		Status:     DeveloperActive,
		Namespaces: []DeveloperNamespace{{Name: "acme", Status: DeveloperActive}, {Name: "other", Status: "pending"}},
		Keys:       []DeveloperKey{{Fingerprint: signer, Status: DeveloperActive}, {Fingerprint: "SHA256:old", Status: "revoked"}},
	}
	if !OwnsPlugin("acme.demo", signer, active) {
		t.Fatal("active namespace and active key should own the plugin")
	}
	if OwnsPlugin("acme.demo", "SHA256:missing", active) {
		t.Fatal("unknown signer")
	}
	if OwnsPlugin("acme.demo", "SHA256:old", active) {
		t.Fatal("revoked key")
	}
	if OwnsPlugin("other.demo", signer, active) {
		t.Fatal("pending namespace")
	}
	if OwnsPlugin("acme-extra.demo", signer, active) {
		t.Fatal("namespace must be a dotted prefix")
	}
	suspended := active
	suspended.Status = DeveloperSuspended
	if OwnsPlugin("acme.demo", signer, suspended) {
		t.Fatal("suspended developer")
	}
	none := emptyDeveloper()
	if OwnsPlugin("acme.demo", signer, none) {
		t.Fatal("no developer")
	}
}

func TestDeveloperProfileUnavailableJSON(t *testing.T) {
	raw, err := json.Marshal(DeveloperProfile{
		Status:      DeveloperNone,
		Unavailable: true,
		Namespaces:  []DeveloperNamespace{},
		Keys:        []DeveloperKey{},
		Plugins:     []DeveloperPlugin{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"status":"none"`)) || !bytes.Contains(raw, []byte(`"unavailable":true`)) {
		t.Fatalf("json = %s", raw)
	}
	ok, err := json.Marshal(emptyDeveloper())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ok, []byte(`"unavailable":true`)) {
		t.Fatalf("available profile = %s", ok)
	}
}

func TestDeveloperCacheAndNotDeveloper(t *testing.T) {
	st := openHubStore(t)
	var mu sync.Mutex
	hits := 0
	mode := "active"
	var pub ed25519.PublicKey
	var client *Client
	pub, client = boundClient(t, st, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/api/instance/developer" {
			mu.Lock()
			hits++
			current := mode
			mu.Unlock()
			if !signedOK(t, r, body, pub) {
				t.Errorf("developer request was not signed")
			}
			if current == "active" {
				writeDevJSON(w, http.StatusOK, map[string]any{
					"status": "active", "displayName": "Ada",
					"namespaces": []map[string]string{{"name": "acme", "status": "active"}},
					"keys":       []map[string]string{{"fingerprint": "SHA256:k", "label": "laptop", "status": "active"}},
					"plugins":    []map[string]string{{"id": "acme.demo", "latestVersion": "1.0.0", "latestStatus": "published"}},
				})
				return
			}
			writeDevJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{"code": "not_developer", "message": "no"}})
		}
	})
	ctx := context.Background()
	first, err := client.Developer(ctx, false)
	if err != nil || first.Status != DeveloperActive || first.DisplayName != "Ada" || first.Namespaces[0].Name != "acme" {
		t.Fatalf("first = %#v err=%v", first, err)
	}
	if _, err := client.Developer(ctx, false); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if hits != 1 {
		t.Fatalf("cache missed, hits=%d", hits)
	}
	mu.Unlock()
	if _, err := client.Developer(ctx, true); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if hits != 2 {
		t.Fatalf("refresh hits=%d", hits)
	}
	mode = "none"
	mu.Unlock()
	// Still inside the window: a non-forced read keeps the active cache.
	cached, err := client.Developer(ctx, false)
	if err != nil || cached.Status != DeveloperActive {
		t.Fatalf("stale cache = %#v err=%v", cached, err)
	}
	client.mu.Lock()
	client.developer.at = client.now().Add(-developerCacheTTL - time.Second)
	client.mu.Unlock()
	again, err := client.Developer(ctx, false)
	if err != nil || again.Status != DeveloperNone || again.Unavailable {
		t.Fatalf("not developer = %#v err=%v", again, err)
	}
	if err := store.NewHubBindingRepository(st.DB()).ClearBinding(ctx, store.HubStatusUnbound, ""); err != nil {
		t.Fatal(err)
	}
	none, err := client.Developer(ctx, true)
	if err != nil || none.Status != DeveloperNone {
		t.Fatalf("unbound = %#v err=%v", none, err)
	}
	mu.Lock()
	after := hits
	mu.Unlock()
	if _, err := client.Developer(ctx, false); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if hits != after {
		t.Fatalf("unbound called hub, hits %d -> %d", after, hits)
	}
	mu.Unlock()
}

func TestDeveloperFailureIsNotCached(t *testing.T) {
	st := openHubStore(t)
	var mu sync.Mutex
	hits := 0
	fail := true
	var pub ed25519.PublicKey
	var client *Client
	pub, client = boundClient(t, st, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/api/instance/developer" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		hits++
		down := fail
		mu.Unlock()
		if !signedOK(t, r, body, pub) {
			t.Errorf("developer request was not signed")
		}
		if down {
			writeDevJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]string{"code": "unavailable", "message": "down"}})
			return
		}
		writeDevJSON(w, http.StatusOK, map[string]any{
			"status": "active", "displayName": "Ada",
			"namespaces": []map[string]string{{"name": "acme", "status": "active"}},
			"keys":       []map[string]string{{"fingerprint": "SHA256:k", "label": "laptop", "status": "active"}},
		})
	})
	ctx := context.Background()
	first, err := client.Developer(ctx, false)
	if err != nil || !first.Unavailable || first.Status != DeveloperNone {
		t.Fatalf("first = %#v err=%v", first, err)
	}
	second, err := client.Developer(ctx, false)
	if err != nil || !second.Unavailable {
		t.Fatalf("second = %#v err=%v", second, err)
	}
	mu.Lock()
	if hits != 2 {
		t.Fatalf("failure was cached, hits=%d", hits)
	}
	fail = false
	mu.Unlock()
	third, err := client.Developer(ctx, false)
	if err != nil || third.Unavailable || third.Status != DeveloperActive {
		t.Fatalf("third = %#v err=%v", third, err)
	}
	if _, err := client.Developer(ctx, false); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if hits != 3 {
		t.Fatalf("success cache hits=%d", hits)
	}
	mu.Unlock()
}

func TestSubmitForwardsSignedPackage(t *testing.T) {
	st := openHubStore(t)
	pkg := []byte("signed-xlp-bytes")
	var got multipartParts
	var pub ed25519.PublicKey
	var client *Client
	pub, client = boundClient(t, st, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/api/instance/submissions" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if !signedOK(t, r, body, pub) {
			t.Errorf("submission was not signed over the multipart body")
		}
		got = readParts(t, r.Header.Get("Content-Type"), body)
		writeDevJSON(w, http.StatusOK, map[string]string{"id": "sub-1", "status": "checking"})
	})
	item, err := client.SubmitPackage(context.Background(), "acme.demo", "1.2.0", pkg, "修复额度")
	if err != nil {
		t.Fatal(err)
	}
	if item.ID != "sub-1" || item.Status != "checking" || item.PluginID != "acme.demo" || item.Version != "1.2.0" {
		t.Fatalf("item = %#v", item)
	}
	if got.changelog != "修复额度" || !bytes.Equal(got.pkg, pkg) {
		t.Fatalf("multipart changelog=%q pkg=%q", got.changelog, got.pkg)
	}
	cached, ok := client.CachedSubmission("acme.demo")
	if !ok || cached.ID != "sub-1" {
		t.Fatalf("cache = %#v ok=%v", cached, ok)
	}
}

func TestDeveloperFailureDoesNotBlockStatus(t *testing.T) {
	st := openHubStore(t)
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-stop
	}))
	t.Cleanup(func() {
		close(stop)
		srv.Close()
	})
	client := New(store.NewHubBindingRepository(st.DB()), store.NewJSPluginRepository(st.DB()), hubTestMasterKey,
		func(context.Context) (string, error) { return srv.URL, nil },
		Options{AllowInsecureLocal: true, HTTPTimeout: 200 * time.Millisecond})
	if client.http.Timeout != 200*time.Millisecond {
		t.Fatal(client.http.Timeout)
	}
	defaults := New(store.NewHubBindingRepository(st.DB()), store.NewJSPluginRepository(st.DB()), hubTestMasterKey,
		func(context.Context) (string, error) { return srv.URL, nil }, Options{})
	if defaults.http.Timeout != defaultHTTPTimeout {
		t.Fatalf("default timeout = %s", defaults.http.Timeout)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = pub
	enc, err := crypto.EncryptSecret(hubTestMasterKey, base64.StdEncoding.EncodeToString(priv))
	if err != nil {
		t.Fatal(err)
	}
	repo := store.NewHubBindingRepository(st.DB())
	if _, err := repo.EnsureIdentity(context.Background(), func() (string, string, error) {
		return base64.StdEncoding.EncodeToString(pub), enc, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkBound(context.Background(), "inst-1", "desk", "Ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	done := make(chan DeveloperProfile, 1)
	go func() {
		profile, _ := client.Developer(context.Background(), true)
		done <- profile
	}()
	time.Sleep(30 * time.Millisecond)
	began := time.Now()
	view, err := client.Status(context.Background())
	if err != nil || time.Since(began) > 100*time.Millisecond || view.Status != store.HubStatusBound {
		t.Fatalf("status took %s err=%v view=%#v", time.Since(began), err, view)
	}
	select {
	case profile := <-done:
		if !profile.Unavailable || profile.Status != DeveloperNone {
			t.Fatalf("profile = %#v", profile)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("developer call did not return")
	}
}

type multipartParts struct {
	changelog string
	pkg       []byte
}

func boundClient(t *testing.T, st *store.Store, fn func(http.ResponseWriter, *http.Request)) (ed25519.PublicKey, *Client) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.EncryptSecret(hubTestMasterKey, base64.StdEncoding.EncodeToString(priv))
	if err != nil {
		t.Fatal(err)
	}
	repo := store.NewHubBindingRepository(st.DB())
	if _, err := repo.EnsureIdentity(context.Background(), func() (string, string, error) {
		return base64.StdEncoding.EncodeToString(pub), enc, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkBound(context.Background(), "inst-1", "desk", "Ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(fn))
	t.Cleanup(srv.Close)
	client := New(repo, store.NewJSPluginRepository(st.DB()), hubTestMasterKey,
		func(context.Context) (string, error) { return srv.URL, nil },
		Options{AllowInsecureLocal: true, HTTPTimeout: time.Second})
	return pub, client
}

func signedOK(t *testing.T, r *http.Request, body []byte, pub ed25519.PublicKey) bool {
	t.Helper()
	unix, err := strconv.ParseInt(r.Header.Get("X-XLyra-Timestamp"), 10, 64)
	if err != nil || r.Header.Get("X-XLyra-Instance") != "inst-1" {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(r.Header.Get("X-XLyra-Signature"))
	if err != nil {
		return false
	}
	message := CanonicalMessage(r.Method, r.URL.Path, unix, body)
	return ed25519.Verify(pub, []byte(message), sig)
}

func writeDevJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func readParts(t *testing.T, contentType string, body []byte) multipartParts {
	t.Helper()
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	var got multipartParts
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(part)
		switch part.FormName() {
		case "changelog":
			got.changelog = string(raw)
		case "package":
			got.pkg = raw
		}
	}
	return got
}
