package admin

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"xlyra/server/internal/auth"
	"xlyra/server/internal/config"
	"xlyra/server/internal/crypto"
	"xlyra/server/internal/gateway"
	"xlyra/server/internal/httpclient"
	"xlyra/server/internal/cloudclient"
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/jsplugin/market"
	"xlyra/server/internal/marketproto"
	routeengine "xlyra/server/internal/router"
	"xlyra/server/internal/store"
)

const flowMasterKey = "flow-master-key"

// xLyra Cloud 地址只在本测试里注入到 cloudclient.New，并只允许 http 指向回环。
// 生产路由仍从已验签 root 的 endpoints.hub 取地址，且不打开 AllowInsecureLocal。
// 市场同步用测试根公钥（不改内置根公钥）和显式 source_url 指向假对象存储的 HTTP 镜像。
// 已验签 root 的 registry/hub 仍必须是 https，这里没有放宽这条规则。

func TestDeveloperFlowCrossRepo(t *testing.T) {
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		t.Setenv(key, "")
	}
	cloud := startFlowCloud(t)
	st := openFlowStore(t)
	ctx := context.Background()
	rootPub, err := base64.StdEncoding.DecodeString(cloud.RootKey)
	if err != nil || len(rootPub) != ed25519.PublicKeySize {
		t.Fatalf("root key: %v", err)
	}
	repo := store.NewJSPluginRepository(st.DB())
	marketSvc, err := market.NewService(repo, httpclient.NewManager(nil), nil, []ed25519.PublicKey{rootPub}, []string{cloud.RegistryURL})
	if err != nil {
		t.Fatal(err)
	}
	marketSvc.SetConfig(config.PluginMarketConfig{Enabled: true, SourceURL: cloud.RegistryURL, AutoUpdate: true, IndexSyncHours: 6})
	authSvc := auth.NewService(st.DB(), flowMasterKey)
	session, err := authSvc.BootstrapAdmin(ctx, "owner", "StrongPass123!", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	cloudClient := cloudclient.New(
		store.NewCloudBindingRepository(st.DB()),
		repo,
		flowMasterKey,
		func(context.Context) (string, error) { return cloud.CloudURL, nil },
		cloudclient.Options{
			AllowInsecureLocal: true, HTTPTimeout: 5 * time.Second,
			PollInterval: 50 * time.Millisecond, LeaseRetry: 50 * time.Millisecond,
			HeartbeatInterval: time.Hour, HasFirstHeartbeatDelay: true, FirstHeartbeatDelay: time.Hour,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	)
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go cloudClient.Run(runCtx)
	manager := jsplugin.NewManager(st, nil)
	h := Handler{auth: authSvc}.WithTrafficFlowStore(st).WithJSPlugins(manager).WithPluginMarket(marketSvc).WithCloudClient(cloudClient)
	gw := gateway.NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), authSvc, routeengine.NewService(st), st, flowMasterKey)
	router := chi.NewRouter()
	router.Route("/api/v1", func(v1 chi.Router) {
		v1.Use(authSvc.RequireAdminSession)
		v1.Use(authSvc.RequireAdminCSRF)
		v1.Get("/js-plugins", h.ListJSPlugins)
		v1.Get("/cloud-binding/developer", func(w http.ResponseWriter, r *http.Request) {
			h.GetCloudDeveloper(cloudClient, w, r)
		})
		v1.Post("/cloud-binding/submissions", func(w http.ResponseWriter, r *http.Request) {
			h.CreateCloudSubmission(cloudClient, w, r)
		})
		RegisterPluginMarket(v1, h)
	})
	router.Route("/v1", func(v1 chi.Router) {
		v1.Use(authSvc.RequireAPIKey)
		v1.Post("/chat/completions", gw.ChatCompletions)
		v1.Get("/models", gw.Models)
	})
	api := xlyraAPI{router: router, token: session.Token, csrf: authSvc.CSRFTokenForSession(session.Token)}

	console := newCloudConsole(t, cloud)
	console.login(t)
	packed := packSamples(t)
	signer := packed.key.Public().(ed25519.PublicKey)
	if _, err := cloudClient.StartPairing(ctx, "flow-desk"); err != nil {
		t.Fatal(err)
	}
	status, err := cloudClient.Status(ctx)
	if err != nil || status.Pairing == nil || status.Pairing.UserCode == "" {
		t.Fatalf("pairing status %+v %v", status, err)
	}
	lookup := console.postJSON(t, "/api/instances/pairings/lookup", `{"code":`+strconv.Quote(status.Pairing.UserCode)+`}`)
	var found struct {
		PairingID string `json:"pairingId"`
	}
	if err := json.Unmarshal([]byte(lookup), &found); err != nil || found.PairingID == "" {
		t.Fatalf("lookup %s", lookup)
	}
	console.postJSON(t, "/api/instances/pairings/"+found.PairingID+"/approve", `{"name":"flow-desk"}`)
	waitBound(t, cloudClient)
	t.Log("step1 bound")

	console.postJSON(t, "/api/dev/apply", `{"displayName":"联调开发者","contact":"dev@example.test","agreementVersion":"v1"}`)
	console.postJSON(t, "/api/admin/developers/"+console.userID+"/approve", `{}`)
	challengeBody := console.postJSON(t, "/api/dev/keys/challenge", `{"publicKey":`+strconv.Quote(base64.StdEncoding.EncodeToString(signer))+`,"label":"flow"}`)
	var challenge struct {
		ID          string `json:"id"`
		Message     string `json:"message"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.Unmarshal([]byte(challengeBody), &challenge); err != nil || challenge.ID == "" || challenge.Message == "" {
		t.Fatalf("challenge %s", challengeBody)
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(packed.key, []byte(challenge.Message)))
	console.postJSON(t, "/api/dev/keys", `{"challengeId":`+strconv.Quote(challenge.ID)+`,"signature":`+strconv.Quote(sig)+`}`)
	console.postJSON(t, "/api/dev/namespaces", `{"name":"testns","displayName":"Test"}`)
	console.elevate(t)
	console.postJSON(t, "/api/admin/namespaces/testns/approve", `{}`)
	t.Log("step2 developer")

	install(t, manager, packed.samples["0.1.0"])
	install(t, manager, packed.samples["0.2.0"])
	install(t, manager, packed.samples["0.3.0"])
	if err := manager.Enable(ctx, "testns.sample", "0.1.0", jsplugin.EnableOptions{ConfirmUntrusted: true}); err != nil {
		t.Fatal(err)
	}
	api.get(t, "/api/v1/cloud-binding/developer?refresh=1")
	item := pluginItem(t, api.get(t, "/api/v1/js-plugins"), "testns.sample")
	if item["owned"] != true || item["market_ahead"] != true {
		t.Fatalf("owned/ahead %#v", item)
	}
	dev := decodeJSON[map[string]any](t, mustBody(t, api.get(t, "/api/v1/cloud-binding/developer")))
	if dev["status"] != "active" {
		t.Fatalf("developer %s", dev["status"])
	}
	t.Log("step3 owned ahead")

	submitViaXLyra(t, api, "0.1.0", "首次上架")
	cloud.postControl(t, "/check")
	console.elevate(t)
	console.postJSON(t, "/api/admin/versions/testns.sample/0.1.0/approve", `{}`)
	cloud.postControl(t, "/publish")

	console.elevate(t)
	console.putJSON(t, "/api/admin/settings/review", `{"autoApproveLight":true}`)
	if err := manager.Enable(ctx, "testns.sample", "0.2.0", jsplugin.EnableOptions{ConfirmUntrusted: true}); err != nil {
		t.Fatal(err)
	}
	api.get(t, "/api/v1/cloud-binding/developer?refresh=1")
	ahead := pluginItem(t, api.get(t, "/api/v1/js-plugins"), "testns.sample")
	if ahead["owned"] != true || ahead["market_ahead"] != true {
		t.Fatalf("0.2.0 ahead %#v", ahead)
	}
	submitViaXLyra(t, api, "0.2.0", "轻档自动批准")
	cloud.postControl(t, "/check")
	if err := console.waitVersion(t, "0.2.0", "approved", "published"); err != nil {
		t.Fatal(err)
	}
	cloud.postControl(t, "/publish")
	syncMarket(t, api)
	assertIndex(t, marketSvc, "0.2.0", "轻档自动批准", packed.digests["0.2.0"], challenge.Fingerprint)
	if err := manager.Disable(ctx, "testns.sample"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(ctx, "testns.sample", "0.1.0", jsplugin.EnableOptions{ConfirmUntrusted: true}); err != nil {
		t.Fatal(err)
	}
	api.get(t, "/api/v1/cloud-binding/developer?refresh=1")
	updated := pluginItem(t, api.get(t, "/api/v1/js-plugins"), "testns.sample")
	if updated["update_available"] != true || updated["update_version"] != "0.2.0" {
		t.Fatalf("update %#v", updated)
	}
	marketItem := marketPlugin(t, api.get(t, "/api/v1/plugin-market"), "testns.sample")
	if marketItem["update_available"] != true {
		t.Fatalf("market %#v", marketItem)
	}
	t.Log("step4 published update")

	console.elevate(t)
	console.postJSON(t, "/api/admin/versions/testns.sample/0.2.0/takedown", `{"reason":"联调下架"}`)
	cloud.postControl(t, "/publish")
	syncMarket(t, api)
	snap, err := marketSvc.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if indexHas(snap.Index, "testns.sample", "0.2.0") {
		t.Fatal("taken down version still in the index")
	}
	console.elevate(t)
	console.postJSON(t, "/api/admin/developers/"+console.userID+"/keys/revoke", `{"fingerprint":`+strconv.Quote(challenge.Fingerprint)+`,"reason":"联调吊销"}`)
	rawStatus, rawBody := signedSubmit(t, st, cloud.CloudURL, packed.samples["0.3.0"], "吊销后不应成功")
	assertCloudError(t, rawStatus, rawBody, "no_active_key", "signer_not_registered")
	console.elevate(t)
	console.postJSON(t, "/api/admin/developers/"+console.userID+"/suspend", `{"reason":"联调撤销"}`)
	refreshed := decodeJSON[map[string]any](t, mustBody(t, api.get(t, "/api/v1/cloud-binding/developer?refresh=1")))
	if refreshed["status"] != "suspended" {
		t.Fatalf("suspended %v", refreshed["status"])
	}
	denied := api.post(t, "/api/v1/cloud-binding/submissions", []byte(`{"pluginId":"testns.sample","version":"0.3.0","changelog":"撤销后"}`))
	if denied.Code == http.StatusOK {
		t.Fatalf("submit still open %s", denied.Body.String())
	}
	rawStatus, rawBody = signedSubmit(t, st, cloud.CloudURL, packed.samples["0.3.0"], "撤销后不应成功")
	assertCloudError(t, rawStatus, rawBody, "not_developer")
	t.Log("step5 revoked")

	proxyBefore := timeGateway(t, router)
	cloud.stop(t)
	began := time.Now()
	listed := api.get(t, "/api/v1/js-plugins")
	catalog := api.get(t, "/api/v1/plugin-market")
	devDown := api.get(t, "/api/v1/cloud-binding/developer?refresh=1")
	proxyAfter := timeGateway(t, router)
	if listed.Code != http.StatusOK || catalog.Code != http.StatusOK || devDown.Code != http.StatusOK {
		t.Fatalf("cloud down plugins=%d market=%d dev=%d", listed.Code, catalog.Code, devDown.Code)
	}
	if time.Since(began) > 3*time.Second || proxyAfter > 2*time.Second || proxyBefore > 2*time.Second {
		t.Fatalf("cloud down stalled list=%s proxy %s -> %s", time.Since(began), proxyBefore, proxyAfter)
	}
	t.Log("step6 cloud down")
}

type flowCloud struct {
	CloudURL    string `json:"hubUrl"`
	Origin      string `json:"origin"`
	ControlURL  string `json:"controlUrl"`
	RegistryURL string `json:"registryUrl"`
	RootKey     string `json:"rootKey"`
	cmd         *exec.Cmd
	stopPath    string
	log         *bytes.Buffer
	done        chan struct{}
	stopOnce    sync.Once
}

func startFlowCloud(t *testing.T) *flowCloud {
	t.Helper()
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready.json")
	serverDir := cloudServerDir(t)
	var log bytes.Buffer
	cmd := exec.Command("go", "test", "-count=1", "-timeout", "9m", "-run", "^TestFlowlinkServe$", "./internal/publisher")
	cmd.Dir = serverDir
	cmd.Env = append(os.Environ(), "HUB_FLOWLINK_READY="+ready)
	cmd.Stdout = &log
	cmd.Stderr = &log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cloud := &flowCloud{cmd: cmd, stopPath: ready + ".stop", log: &log, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(cloud.done)
	}()
	t.Cleanup(func() {
		cloud.stop(t)
	})
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(ready)
		if err == nil && json.Unmarshal(body, cloud) == nil && cloud.CloudURL != "" {
			return cloud
		}
		select {
		case <-cloud.done:
			deadline = time.Time{}
		default:
			time.Sleep(200 * time.Millisecond)
		}
	}
	t.Fatalf("xLyra Cloud 联调进程没有就绪: %s", clip(log.String(), 800))
	return nil
}

func (h *flowCloud) stop(t *testing.T) {
	t.Helper()
	if h == nil || h.cmd == nil || h.done == nil {
		return
	}
	h.stopOnce.Do(func() {
		_ = os.WriteFile(h.stopPath, []byte("stop"), 0o600)
		select {
		case <-h.done:
		case <-time.After(20 * time.Second):
			if h.cmd.Process != nil {
				_ = h.cmd.Process.Kill()
			}
			<-h.done
		}
	})
}

func (h *flowCloud) postControl(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.ControlURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		t.Fatalf("%s %d %s", path, resp.StatusCode, clip(string(body), 500))
	}
}

type cloudConsole struct {
	t      *testing.T
	base   string
	origin string
	http   *http.Client
	jar    map[string]*http.Cookie
	csrf   string
	secret string
	userID string
}

func newCloudConsole(t *testing.T, cloud *flowCloud) *cloudConsole {
	t.Helper()
	return &cloudConsole{
		t: t, base: cloud.CloudURL, origin: cloud.Origin,
		http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		jar: map[string]*http.Cookie{},
	}
}

func (c *cloudConsole) login(t *testing.T) {
	t.Helper()
	status, hdr, body := c.do(t, http.MethodGet, "/api/auth/github/start", "", false)
	if status != http.StatusFound {
		t.Fatalf("github start %d %s", status, body)
	}
	ghReq, err := http.NewRequest(http.MethodGet, hdr.Get("Location"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ghResp, err := c.http.Do(ghReq)
	if err != nil {
		t.Fatal(err)
	}
	defer ghResp.Body.Close()
	_, _ = io.Copy(io.Discard, ghResp.Body)
	cb, err := url.Parse(ghResp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	status, _, body = c.do(t, http.MethodGet, cb.RequestURI(), "", false)
	if status != http.StatusFound {
		t.Fatalf("callback %d %s", status, body)
	}
	state := decodeJSON[struct {
		Stage string `json:"stage"`
		User  *struct {
			ID string `json:"id"`
		} `json:"user"`
		CSRFToken *string `json:"csrfToken"`
	}](t, c.get(t, "/api/auth/state"))
	if state.Stage != "totp_setup" || state.User == nil || state.CSRFToken == nil {
		t.Fatalf("state %+v", state)
	}
	c.userID = state.User.ID
	c.csrf = *state.CSRFToken
	started := decodeJSON[struct {
		Secret string `json:"secret"`
	}](t, c.postJSON(t, "/api/auth/totp/enroll/start", ""))
	c.secret = started.Secret
	code, err := totpCode(c.secret, time.Now().Unix()/30)
	if err != nil {
		t.Fatal(err)
	}
	c.postJSON(t, "/api/auth/totp/enroll/confirm", `{"code":"`+code+`"}`)
	c.reloadCSRF(t)
}

func (c *cloudConsole) elevate(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		step := time.Now().Unix() / 30
		advanced := false
		for _, delta := range []int64{1, 0, -1} {
			code, err := totpCode(c.secret, step+delta)
			if err != nil {
				t.Fatal(err)
			}
			status, _, body := c.do(t, http.MethodPost, "/api/auth/elevate", `{"code":"`+code+`"}`, true)
			last = body
			if status == http.StatusOK {
				c.reloadCSRF(t)
				return
			}
			if strings.Contains(body, "totp_replayed") {
				advanced = true
				continue
			}
			t.Fatalf("elevate %d %s", status, body)
		}
		if !advanced {
			t.Fatalf("elevate %s", last)
		}
		time.Sleep(time.Until(time.Unix((step+1)*30, 0)) + 200*time.Millisecond)
	}
	t.Fatalf("elevate %s", last)
}

func (c *cloudConsole) reloadCSRF(t *testing.T) {
	t.Helper()
	state := decodeJSON[struct {
		CSRFToken *string `json:"csrfToken"`
	}](t, c.get(t, "/api/auth/state"))
	if state.CSRFToken == nil || *state.CSRFToken == "" {
		t.Fatal("missing csrf")
	}
	c.csrf = *state.CSRFToken
}

func (c *cloudConsole) get(t *testing.T, path string) string {
	t.Helper()
	status, _, body := c.do(t, http.MethodGet, path, "", false)
	if status != http.StatusOK {
		t.Fatalf("GET %s %d %s", path, status, body)
	}
	return body
}

func (c *cloudConsole) postJSON(t *testing.T, path, body string) string {
	t.Helper()
	status, _, raw := c.do(t, http.MethodPost, path, body, true)
	if status >= 300 {
		t.Fatalf("POST %s %d %s", path, status, raw)
	}
	return raw
}

func (c *cloudConsole) putJSON(t *testing.T, path, body string) string {
	t.Helper()
	status, _, raw := c.do(t, http.MethodPut, path, body, true)
	if status >= 300 {
		t.Fatalf("PUT %s %d %s", path, status, raw)
	}
	return raw
}

func (c *cloudConsole) do(t *testing.T, method, path, body string, csrf bool) (int, http.Header, string) {
	t.Helper()
	var reader io.Reader
	if body != "" || method == http.MethodPost || method == http.MethodPut {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if method == http.MethodPost || method == http.MethodPut {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", c.origin)
		if csrf {
			req.Header.Set("X-CSRF-Token", c.csrf)
		}
	}
	for _, ck := range c.jar {
		req.AddCookie(ck)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	for _, ck := range resp.Cookies() {
		if ck.MaxAge < 0 {
			delete(c.jar, ck.Name)
			continue
		}
		c.jar[ck.Name] = ck
	}
	return resp.StatusCode, resp.Header, string(raw)
}

func (c *cloudConsole) waitVersion(t *testing.T, version string, want ...string) error {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		status, _, body := c.do(t, http.MethodGet, "/api/admin/versions/testns.sample/"+version, "", false)
		var view struct {
			Status string `json:"status"`
		}
		if status == http.StatusOK {
			_ = json.Unmarshal([]byte(body), &view)
			last = view.Status
			for _, item := range want {
				if view.Status == item {
					return nil
				}
			}
		} else {
			last = body
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("%s status %s", version, clip(last, 200))
}

type sampleSet struct {
	key     ed25519.PrivateKey
	samples map[string][]byte
	digests map[string]string
}

func packSamples(t *testing.T) sampleSet {
	t.Helper()
	bin := buildPlugin(t)
	root := t.TempDir()
	keyPath := filepath.Join(root, "key.pem")
	proj := filepath.Join(root, "proj")
	out := runTool(t, root, bin, "keygen", "--out", keyPath)
	pubLine := lineValue(out, "public_key=")
	if pubLine == "" {
		t.Fatalf("keygen %s", out)
	}
	runTool(t, root, bin, "init", "--kind", "quota_probe", "--id", "testns.sample", proj)
	versions := map[string][]byte{}
	digests := map[string]string{}
	for _, ver := range []string{"0.1.0", "0.2.0", "0.3.0"} {
		setManifestVersion(t, filepath.Join(proj, "manifest.json"), ver)
		xlp := filepath.Join(root, ver+".xlp")
		runTool(t, root, bin, "pack", "--sign", keyPath, "--out", xlp, proj)
		raw, err := os.ReadFile(xlp)
		if err != nil {
			t.Fatal(err)
		}
		pkg, err := jsplugin.ReadPackage(raw, true)
		if err != nil {
			t.Fatal(err)
		}
		versions[ver] = raw
		digests[ver] = pkg.SHA256
	}
	return sampleSet{key: loadPEM(t, keyPath), samples: versions, digests: digests}
}

func setManifestVersion(t *testing.T, path, version string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["version"] = version
	next, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(next, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func install(t *testing.T, manager *jsplugin.Manager, raw []byte) {
	t.Helper()
	if _, err := manager.Upload(context.Background(), "flow", raw); err != nil {
		t.Fatal(err)
	}
}

func submitViaXLyra(t *testing.T, api xlyraAPI, version, changelog string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"pluginId": "testns.sample", "version": version, "changelog": changelog})
	rec := api.post(t, "/api/v1/cloud-binding/submissions", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit %s %d %s", version, rec.Code, rec.Body.String())
	}
}

func syncMarket(t *testing.T, api xlyraAPI) {
	t.Helper()
	rec := api.post(t, "/api/v1/plugin-market/sync", []byte(`{}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("sync %d %s", rec.Code, rec.Body.String())
	}
	var status struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil || status.Error != "" {
		t.Fatalf("sync %s", rec.Body.String())
	}
}

func assertIndex(t *testing.T, svc *market.Service, version, changelog, digest, fingerprint string) {
	t.Helper()
	snap, err := svc.Snapshot(context.Background())
	if err != nil || snap.Index == nil {
		t.Fatal(err)
	}
	for _, plugin := range snap.Index.Plugins {
		if plugin.ID != "testns.sample" {
			continue
		}
		for _, ver := range plugin.Versions {
			if ver.Version != version {
				continue
			}
			if ver.Changelog != changelog || ver.PackageDigest != digest {
				t.Fatalf("index changelog=%q digest=%s want %q %s", ver.Changelog, ver.PackageDigest, changelog, digest)
			}
			if len(ver.Attestation.Payload) == 0 || len(ver.Attestation.Signatures) == 0 {
				t.Fatal("missing attestation")
			}
			payload, err := base64.StdEncoding.DecodeString(ver.Attestation.Payload)
			if err != nil {
				t.Fatal(err)
			}
			var att struct {
				PackageDigest        string `json:"packageDigest"`
				DeveloperFingerprint string `json:"developerFingerprint"`
			}
			if err := json.Unmarshal(payload, &att); err != nil {
				t.Fatal(err)
			}
			if att.PackageDigest != digest || att.DeveloperFingerprint != fingerprint {
				t.Fatalf("attestation %+v", att)
			}
			return
		}
	}
	t.Fatalf("index missing %s", version)
}

func indexHas(idx *marketproto.Index, id, version string) bool {
	if idx == nil {
		return false
	}
	for _, plugin := range idx.Plugins {
		if plugin.ID != id {
			continue
		}
		for _, ver := range plugin.Versions {
			if ver.Version == version {
				return true
			}
		}
	}
	return false
}

func signedSubmit(t *testing.T, st *store.Store, cloudURL string, pkg []byte, changelog string) (int, []byte) {
	t.Helper()
	row, err := store.NewCloudBindingRepository(st.DB()).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	plain, err := crypto.DecryptSecret(flowMasterKey, row.PrivateKeyEnc)
	if err != nil {
		t.Fatal(err)
	}
	rawKey, err := base64.StdEncoding.DecodeString(plain)
	if err != nil {
		t.Fatal(err)
	}
	body, contentType := multipartBody(t, pkg, changelog)
	sum := sha256.Sum256(body)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	msg := "xlyra-instance-v1\nPOST\n/api/instance/submissions\n" + ts + "\n" + fmt.Sprintf("%x", sum[:])
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(rawKey), []byte(msg)))
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(cloudURL, "/")+"/api/instance/submissions", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-XLyra-Instance", row.InstanceID)
	req.Header.Set("X-XLyra-Timestamp", ts)
	req.Header.Set("X-XLyra-Signature", sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, respBody
}

func assertCloudError(t *testing.T, status int, body []byte, codes ...string) {
	t.Helper()
	var doc struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || doc.Error.Code == "" || doc.Error.Message == "" {
		t.Fatalf("error body %d %s", status, body)
	}
	for _, code := range codes {
		if doc.Error.Code == code {
			return
		}
	}
	t.Fatalf("error code %s want %v", doc.Error.Code, codes)
}

func multipartBody(t *testing.T, pkg []byte, changelog string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("package", "plugin.xlp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(pkg); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteField("changelog", changelog); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), w.FormDataContentType()
}

type xlyraAPI struct {
	router http.Handler
	token  string
	csrf   string
}

func (a xlyraAPI) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	return a.call(t, http.MethodGet, path, nil)
}

func (a xlyraAPI) post(t *testing.T, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	return a.call(t, http.MethodPost, path, body)
}

func (a xlyraAPI) call(t *testing.T, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(&http.Cookie{Name: "xlyra_admin_session", Value: a.token})
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set(auth.CSRFHeaderName, a.csrf)
	}
	rec := httptest.NewRecorder()
	a.router.ServeHTTP(rec, req)
	return rec
}

func timeGateway(t *testing.T, router http.Handler) time.Duration {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	began := time.Now()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	elapsed := time.Since(began)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("proxy %d %s", rec.Code, rec.Body.String())
	}
	return elapsed
}

func waitBound(t *testing.T, client *cloudclient.Client) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last cloudclient.Status
	var err error
	for time.Now().Before(deadline) {
		last, err = client.Status(context.Background())
		if err == nil && last.Status == store.CloudStatusBound {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("not bound %+v %v", last, err)
}

func pluginItem(t *testing.T, rec *httptest.ResponseRecorder, id string) map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("plugins %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, item := range body.Items {
		if item["id"] == id {
			return item
		}
	}
	t.Fatalf("missing %s", id)
	return nil
}

func marketPlugin(t *testing.T, rec *httptest.ResponseRecorder, id string) map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("market %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Plugins []map[string]any `json:"plugins"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, item := range body.Plugins {
		if item["id"] == id {
			return item
		}
	}
	t.Fatalf("market missing %s in %s", id, rec.Body.String())
	return nil
}

func openFlowStore(t *testing.T) *store.Store {
	t.Helper()
	cfg := config.Config{
		AppEnv: "test", DBConnectTimeout: 2 * time.Second, DBMaxConns: 4,
		PostgresDSN: strings.TrimSpace(os.Getenv("POSTGRES_DSN")),
		DBHost:      envOr("DB_HOST", "127.0.0.1"), DBPort: 5432,
		DBName: envOr("DB_NAME", "xlyra"), DBUser: envOr("DB_USER", "postgres"),
		DBPassword: envOr("DB_PASSWORD", "postgres"), DBSSLMode: envOr("DB_SSLMODE", "disable"),
	}
	if value := strings.TrimSpace(os.Getenv("DB_PORT")); value != "" {
		port, err := strconv.Atoi(value)
		if err != nil {
			t.Fatal(err)
		}
		cfg.DBPort = port
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("PostgreSQL: %v", err)
	}
	schema := "xlyra_cloudflow_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := base.DB().Exec(fmt.Sprintf("CREATE SCHEMA %q", schema)).Error; err != nil {
		base.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = base.DB().Exec(fmt.Sprintf("DROP SCHEMA %q CASCADE", schema)).Error
		base.Close()
	})
	parsed, err := url.Parse(cfg.DatabaseDSN())
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	cfg.PostgresDSN = parsed.String()
	var migrateErr error
	for attempt := 0; attempt < 5; attempt++ {
		if migrateErr = store.EnsureDatabaseInitialized(ctx, cfg); migrateErr == nil {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if migrateErr != nil {
		t.Fatal(migrateErr)
	}
	st, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

func cloudServerDir(t *testing.T) string {
	t.Helper()
	if env := strings.TrimSpace(os.Getenv("XLYRA_CLOUD_ROOT")); env != "" {
		return filepath.Join(env, "server")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for i := 0; i < 8; i++ {
		sibling := filepath.Join(dir, "xLyra-Hub", "server")
		if st, err := os.Stat(filepath.Join(sibling, "go.mod")); err == nil && !st.IsDir() {
			return sibling
		}
		next := filepath.Dir(dir)
		if next == dir {
			break
		}
		dir = next
	}
	t.Fatal("找不到 xLyra-Hub")
	return ""
}

func buildPlugin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "xlyra-plugin")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/xlyra-plugin")
	cmd.Dir = moduleRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build plugin: %s", clip(string(out), 400))
	}
	return bin
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for i := 0; i < 6; i++ {
		if st, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !st.IsDir() {
			raw, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
			if strings.Contains(string(raw), "module xlyra/server") {
				return dir
			}
		}
		next := filepath.Dir(dir)
		if next == dir {
			break
		}
		dir = next
	}
	t.Fatal("找不到 xlyra module")
	return ""
}

func runTool(t *testing.T, dir, bin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + dir, "LANG=C.UTF-8"}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: %v %s", args[0], err, clip(stderr.String(), 400))
	}
	return stdout.String() + stderr.String()
}

func loadPEM(t *testing.T, path string) ed25519.PrivateKey {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("pem")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		t.Fatal("not ed25519")
	}
	return priv
}

func totpCode(secret string, step int64) (string, error) {
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		return "", err
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, raw)
	_, _ = mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := int(sum[len(sum)-1] & 0x0f)
	bin := (uint32(sum[off])&0x7f)<<24 | uint32(sum[off+1])<<16 | uint32(sum[off+2])<<8 | uint32(sum[off+3])
	return fmt.Sprintf("%06d", bin%1000000), nil
}

func mustBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func decodeJSON[T any](t *testing.T, raw string) T {
	t.Helper()
	var out T
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("json %s: %s", err, clip(raw, 300))
	}
	return out
}

func lineValue(text, prefix string) string {
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
