package admin

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"xlyra/server/internal/config"
	"xlyra/server/internal/httpclient"
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/jsplugin/market"
	"xlyra/server/internal/marketproto"
	"xlyra/server/internal/store"
)

func TestInstallPluginMarketUnavailable(t *testing.T) {
	r := chi.NewRouter()
	RegisterPluginMarket(r, Handler{})
	rec := adminPerform(r.ServeHTTP, adminTestRequest(http.MethodPost, "/plugin-market/acme.demo/install", ""))
	assertAdminErrorCode(t, rec, http.StatusServiceUnavailable, "js_plugin_market_unavailable")
}

func TestInstallPluginMarketHTTP(t *testing.T) {
	st := openAdminJSPluginStore(t)
	repo := store.NewJSPluginRepository(st.DB())
	mgr := jsplugin.NewManager(st, nil)
	rootPriv, online := adminFixedKey(0x11), adminFixedKey(0x33)
	devKey, otherKey := adminFixedKey(0x44), adminFixedKey(0x55)
	devFP := jsplugin.PublicKeyFingerprint(devKey.Public().(ed25519.PublicKey))
	issued := time.Now().UTC().Truncate(time.Second)
	expires := issued.Add(7 * 24 * time.Hour)
	okFixture := map[string][]byte{"fixtures/ok.json": []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"empty"}}`)}
	badFixture := map[string][]byte{"fixtures/ok.json": []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"nope"}}`)}
	hosts := []string{"api.example.com", "files.example.com"}
	none := []string{}
	okCompat := marketproto.Compat{XLyra: ">=1.0.0", APIVersion: jsplugin.HookAPIVersion, HostAPI: jsplugin.HostAPIVersion}
	badCompat := marketproto.Compat{XLyra: ">=1.0.0", APIVersion: 99, HostAPI: jsplugin.HostAPIVersion}

	okNew, okNewPkg := adminPack(t, devKey, adminProbeManifest("acme.ok", "1.1.0"), adminProbeSource("acme.ok"), okFixture, true)
	okOld, okOldPkg := adminPack(t, devKey, adminProbeManifest("acme.ok", "1.0.0"), adminProbeSource("acme.ok"), okFixture, true)
	staleRaw, stalePkg := adminPack(t, devKey, adminProbeManifest("acme.stale", "1.0.0"), adminProbeSource("acme.stale"), okFixture, true)
	revokedRaw, revokedPkg := adminPack(t, devKey, adminProbeManifest("acme.revoked", "1.0.0"), adminProbeSource("acme.revoked"), okFixture, true)
	hostRaw, hostPkg := adminPack(t, devKey, adminProbeManifest("acme.hosts", "1.0.0"), adminProbeSource("acme.hosts"), okFixture, true)
	unsignedRaw, unsignedPkg := adminPack(t, devKey, adminProbeManifest("acme.unsigned", "1.0.0"), adminProbeSource("acme.unsigned"), okFixture, false)
	failRaw, failPkg := adminPack(t, devKey, adminProbeManifest("acme.fail", "1.0.0"), adminProbeSource("acme.fail"), badFixture, true)
	lockedRaw, lockedPkg := adminPack(t, devKey, adminProbeManifest("acme.locked", "1.0.0"), adminProbeSource("acme.locked"), okFixture, true)
	lockedLocal, _ := adminPack(t, otherKey, adminProbeManifest("acme.locked", "0.1.0"), adminProbeSource("acme.locked"), okFixture, true)
	clashRaw, clashPkg := adminPack(t, devKey, adminProbeManifest("acme.clash", "1.0.0"), adminProbeSource("acme.clash")+"// market\n", okFixture, true)
	clashLocal, _ := adminPack(t, devKey, adminProbeManifest("acme.clash", "1.0.0"), adminProbeSource("acme.clash")+"// local\n", okFixture, true)
	tamperRaw, tamperPkg := adminPack(t, devKey, adminProbeManifest("acme.tamper", "1.0.0"), adminProbeSource("acme.tamper"), okFixture, true)
	siteRaw, sitePkg := adminPack(t, devKey, adminSiteManifest("acme.site", "1.0.0", hosts), adminSiteSource("acme.site"), nil, true)
	junk := []byte("this is not a zip")

	entry := func(pkg jsplugin.Package, raw []byte, attHosts []string, compat marketproto.Compat) marketproto.PluginVersion {
		t.Helper()
		if attHosts == nil {
			attHosts = adminSorted(pkg.Manifest.SiteProvider.Hosts)
		}
		perms := adminSorted(pkg.Manifest.Automation.Permissions)
		return adminVersion(pkg, raw, adminAttest(t, online, pkg, attHosts, perms, issued), compat, attHosts, perms, issued)
	}
	plugins := []marketproto.Plugin{
		adminPlugin("acme.ok", jsplugin.KindQuotaProbe, []string{devFP}, entry(okNewPkg, okNew, none, okCompat), entry(okOldPkg, okOld, none, okCompat)),
		adminPlugin("acme.stale", jsplugin.KindQuotaProbe, []string{devFP}, entry(stalePkg, staleRaw, none, badCompat)),
		adminPlugin("acme.revoked", jsplugin.KindQuotaProbe, []string{devFP}, entry(revokedPkg, revokedRaw, none, okCompat)),
		adminPlugin("acme.hosts", jsplugin.KindQuotaProbe, []string{devFP}, entry(hostPkg, hostRaw, []string{"other.example"}, okCompat)),
		adminPlugin("acme.unsigned", jsplugin.KindQuotaProbe, []string{devFP}, entry(unsignedPkg, unsignedRaw, none, okCompat)),
		adminPlugin("acme.fail", jsplugin.KindQuotaProbe, []string{devFP}, entry(failPkg, failRaw, none, okCompat)),
		adminPlugin("acme.junk", jsplugin.KindQuotaProbe, []string{devFP}, adminJunkVersion(t, online, "acme.junk", devFP, junk, issued)),
		adminPlugin("acme.locked", jsplugin.KindQuotaProbe, []string{devFP}, entry(lockedPkg, lockedRaw, none, okCompat)),
		adminPlugin("acme.clash", jsplugin.KindQuotaProbe, []string{devFP}, entry(clashPkg, clashRaw, none, okCompat)),
		adminPlugin("acme.tamper", jsplugin.KindQuotaProbe, []string{devFP}, entry(tamperPkg, tamperRaw, none, okCompat)),
		adminPlugin("acme.site", jsplugin.KindSiteProvider, []string{devFP}, entry(sitePkg, siteRaw, nil, okCompat)),
	}
	revs := []marketproto.RevocationEntry{adminRevocation("rev-sec", "acme.revoked", "1.0.0", revokedPkg.SHA256, "security", issued)}
	docs := adminSignCatalog(t, rootPriv, online, 1, 1, 1, issued, expires, plugins, revs)
	cloud := newAdminCloud()
	cloud.put("root.json", docs[0])
	cloud.put("index.json", docs[1])
	cloud.put("revocations.json", docs[2])
	for _, raw := range [][]byte{okNew, okOld, staleRaw, revokedRaw, hostRaw, unsignedRaw, failRaw, junk, lockedRaw, clashRaw, tamperRaw, siteRaw} {
		cloud.put("packages/"+marketproto.FileSHA256(raw)+".xlp", raw)
	}
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	base := srv.URL + "/"
	clearAdminProxyEnv(t)
	svc, err := market.NewService(repo, httpclient.NewManager(nil), nil, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, []string{base})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetConfig(config.PluginMarketConfig{Enabled: true, IndexSyncHours: 6, SourceURL: base, AutoUpdate: true})
	mgr.SetMarketTrust(svc)
	ctx := context.Background()
	if status, err := svc.SyncOnce(ctx, true); err != nil || status.Error != "" {
		t.Fatalf("sync = %+v %v", status, err)
	}
	if _, err := mgr.Upload(ctx, "", lockedLocal); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Upload(ctx, "", clashLocal); err != nil {
		t.Fatal(err)
	}
	h := Handler{}.WithPluginMarket(svc).WithJSPlugins(mgr).WithTrafficFlowStore(st)
	r := chi.NewRouter()
	RegisterPluginMarket(r, h)
	post := func(id, body string) *httptest.ResponseRecorder {
		t.Helper()
		return adminPerform(r.ServeHTTP, adminTestRequest(http.MethodPost, "/plugin-market/"+id+"/install", body))
	}

	assertInstallError(t, post("acme.missing", ""), http.StatusNotFound, "js_plugin_market_not_found", "")
	assertInstallError(t, post("acme.ok", `{"version":"9.9.9"}`), http.StatusNotFound, "js_plugin_market_version_not_found", "")
	assertInstallError(t, post("acme.stale", ""), http.StatusConflict, "js_plugin_market_incompatible", "api_version")
	assertInstallError(t, post("acme.revoked", ""), http.StatusConflict, "js_plugin_market_revoked", "")
	lockedRec := post("acme.locked", "")
	adminAssertStatus(t, lockedRec, http.StatusCreated)
	lockedBody := adminDecodeJSON[map[string]any](t, lockedRec)
	if lockedBody["version"] != "1.0.0" || lockedBody["origin"] != "market" {
		t.Fatalf("historical signer blocked install: %#v", lockedBody)
	}
	assertInstallError(t, post("acme.hosts", ""), http.StatusUnprocessableEntity, "js_plugin_market_verify_failed", marketproto.CodeManifestMismatch)
	assertInstallError(t, post("acme.unsigned", ""), http.StatusUnprocessableEntity, "js_plugin_market_unsigned", "")
	assertInstallError(t, post("acme.junk", ""), http.StatusUnprocessableEntity, "js_plugin_package_invalid", "")
	assertInstallError(t, post("acme.fail", ""), http.StatusUnprocessableEntity, "js_plugin_market_selftest_failed", "")
	assertInstallError(t, post("acme.clash", ""), http.StatusConflict, "js_plugin_version_conflict", "")

	cloud.put("packages/"+marketproto.FileSHA256(tamperRaw)+".xlp", []byte("tampered"))
	assertInstallError(t, post("acme.tamper", ""), http.StatusBadGateway, "js_plugin_market_download_failed", "")

	rec := post("acme.ok", "")
	adminAssertStatus(t, rec, http.StatusCreated)
	body := adminDecodeJSON[map[string]any](t, rec)
	if body["origin"] != "market" || body["trust"] != string(jsplugin.TrustMarketplace) || body["version"] != "1.1.0" || body["status"] != store.JSPluginStatusVerified {
		t.Fatalf("install response = %#v", body)
	}
	rec = post("acme.ok", `{"version":"1.0.0"}`)
	adminAssertStatus(t, rec, http.StatusCreated)
	body = adminDecodeJSON[map[string]any](t, rec)
	if body["version"] != "1.0.0" || body["origin"] != "market" {
		t.Fatalf("specified version = %#v", body)
	}
	rec = post("acme.site", `{"version":"1.0.0"}`)
	adminAssertStatus(t, rec, http.StatusCreated)
	body = adminDecodeJSON[map[string]any](t, rec)
	if body["trust"] != string(jsplugin.TrustMarketplace) || body["origin"] != "market" {
		t.Fatalf("site install = %#v", body)
	}
	rows, err := repo.ListVersions(ctx, "acme.site")
	if err != nil || len(rows) != 1 || rows[0].Status != store.JSPluginStatusVerified {
		t.Fatalf("install enabled the plugin: %+v %v", rows, err)
	}

	past := issued.Add(-8 * 24 * time.Hour)
	expired := adminSignCatalog(t, rootPriv, online, 1, 1, 1, past, past.Add(24*time.Hour), plugins, revs)
	now := time.Now().UTC()
	if err := repo.SaveMarketState(ctx, store.JSPluginMarketState{
		RootEnvelope: expired[0], IndexEnvelope: expired[1], RevocationsEnvelope: expired[2],
		IndexSyncedAt: &now, RevocationsSyncedAt: &now, LastAttemptAt: &now, SourceURL: base,
	}); err != nil {
		t.Fatal(err)
	}
	assertInstallError(t, post("acme.ok", ""), http.StatusConflict, "js_plugin_market_revocations_expired", "")
}

func assertInstallError(t *testing.T, rec *httptest.ResponseRecorder, status int, code, reason string) {
	t.Helper()
	adminAssertStatus(t, rec, status)
	body := adminDecodeJSON[struct {
		Error struct {
			Code   string `json:"code"`
			Reason string `json:"reason"`
		} `json:"error"`
	}](t, rec)
	if body.Error.Code != code || (reason != "" && body.Error.Reason != reason) {
		t.Fatalf("error = %+v, want %s %s", body.Error, code, reason)
	}
}

func clearAdminProxyEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		t.Setenv(key, "")
	}
}

type adminCloud struct {
	mu        sync.Mutex
	files     map[string][]byte
	hits      map[string]int
	blockName string
	entered   chan struct{}
	release   chan struct{}
	blockOnce sync.Once
}

func newAdminCloud() *adminCloud {
	return &adminCloud{files: map[string][]byte{}, hits: map[string]int{}}
}

func (h *adminCloud) put(name string, body []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.files[name] = append([]byte(nil), body...)
}

func (h *adminCloud) hit(name string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hits[name]
}

func (h *adminCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	h.mu.Lock()
	h.hits[name]++
	body, ok := h.files[name]
	blockName := h.blockName
	h.mu.Unlock()
	if blockName != "" && name == blockName {
		h.blockOnce.Do(func() {
			if h.entered != nil {
				close(h.entered)
			}
		})
		if h.release != nil {
			<-h.release
		}
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func adminFixedKey(seed byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytesRepeat(seed))
}

func bytesRepeat(seed byte) []byte {
	out := make([]byte, ed25519.SeedSize)
	for i := range out {
		out[i] = seed
	}
	return out
}

func adminPack(t *testing.T, dev ed25519.PrivateKey, manifest jsplugin.Manifest, source string, fixtures map[string][]byte, sign bool) ([]byte, jsplugin.Package) {
	t.Helper()
	manifest.SHA256 = map[string]string{"plugin.js": jsplugin.HashSource(source)}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"manifest.json": encoded, "plugin.js": []byte(source)}
	for name, body := range fixtures {
		files[name] = body
	}
	raw, err := jsplugin.BuildPackage(files)
	if err != nil {
		t.Fatal(err)
	}
	if sign {
		raw, err = jsplugin.AttachSignature(raw, dev)
		if err != nil {
			t.Fatal(err)
		}
	}
	pkg, err := jsplugin.ReadPackage(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	return raw, pkg
}

func adminProbeManifest(id, version string) jsplugin.Manifest {
	return jsplugin.Manifest{
		ID: id, Name: "Demo", Version: version, APIVersion: jsplugin.HookAPIVersion, HostAPI: jsplugin.HostAPIVersion,
		XLyra: ">=1.0.0", Kind: jsplugin.KindQuotaProbe, QuotaProbe: jsplugin.QuotaProbeSection{BaseURLMode: "as_is"},
	}
}

func adminProbeSource(id string) string {
	return "export const meta = { apiVersion: 1, id: \"" + id + "\", kind: \"quota_probe\" };\nexport function probe() { return { error: \"empty\" }; }\n"
}

func adminSiteManifest(id, version string, hosts []string) jsplugin.Manifest {
	return jsplugin.Manifest{
		ID: id, Name: "Example", Version: version, APIVersion: jsplugin.HookAPIVersion, HostAPI: jsplugin.HostAPIVersion,
		XLyra: ">=1.0.0", Kind: jsplugin.KindSiteProvider,
		SiteProvider: jsplugin.SiteProviderSection{
			SiteType: id + "/example",
			Display: jsplugin.SiteProviderDisplay{
				Name: jsplugin.LocalizedText{"zh": "示例", "en": "Example"}, Subtitle: jsplugin.LocalizedText{"en": "API"}, BrandColor: "#336699",
			},
			Hosts: hosts, Credential: jsplugin.SiteProviderCredential{Model: "api_key"},
			Protocol: jsplugin.SiteProviderProtocol{
				Kind: "openai_chat", Path: "/v1/chat/completions", OfficialBaseURL: "https://api.example.com/v1",
				DefaultEndpointTypes: []string{"openai"}, RequestParams: json.RawMessage(`{"temperature":0}`),
			},
			Models: jsplugin.SiteProviderModels{Path: "/v1/models", Auth: "header:X-Api-Key"},
		},
	}
}

func adminSiteSource(id string) string {
	return "export const meta = { apiVersion: 1, id: \"" + id + "\", kind: \"site_provider\" };\n"
}

func adminAttest(t *testing.T, online ed25519.PrivateKey, pkg jsplugin.Package, hosts, perms []string, issued time.Time) marketproto.Envelope {
	t.Helper()
	signer := pkg.Signer
	if signer == "" {
		signer = "SHA256:dW5zaWduZWQ="
	}
	raw, err := json.Marshal(marketproto.Attestation{
		V: 1, PluginID: pkg.Manifest.ID, Version: pkg.Manifest.Version, PackageDigest: pkg.SHA256,
		DeveloperFingerprint: signer, Kind: pkg.Manifest.Kind, Hosts: hosts, Permissions: perms,
		IssuedAt: issued.UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	return marketproto.Sign(marketproto.PayloadTypeAttestation, raw, online)
}

func adminVersion(pkg jsplugin.Package, raw []byte, att marketproto.Envelope, compat marketproto.Compat, hosts, perms []string, published time.Time) marketproto.PluginVersion {
	return marketproto.PluginVersion{
		Version: pkg.Manifest.Version, PublishedAt: published.UTC().Format(time.RFC3339),
		FileSHA256: marketproto.FileSHA256(raw), PackageDigest: pkg.SHA256, Size: int64(len(raw)),
		Compat: compat, Hosts: hosts, Permissions: perms, Changelog: "notes", Attestation: att,
	}
}

func adminJunkVersion(t *testing.T, online ed25519.PrivateKey, id, fingerprint string, raw []byte, issued time.Time) marketproto.PluginVersion {
	t.Helper()
	payload, err := json.Marshal(marketproto.Attestation{
		V: 1, PluginID: id, Version: "1.0.0", PackageDigest: strings.Repeat("ab", 32), DeveloperFingerprint: fingerprint,
		Kind: jsplugin.KindQuotaProbe, Hosts: []string{}, Permissions: []string{}, IssuedAt: issued.UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	return marketproto.PluginVersion{
		Version: "1.0.0", PublishedAt: issued.UTC().Format(time.RFC3339),
		FileSHA256: marketproto.FileSHA256(raw), PackageDigest: strings.Repeat("ab", 32), Size: int64(len(raw)),
		Compat: marketproto.Compat{XLyra: ">=1.0.0", APIVersion: jsplugin.HookAPIVersion, HostAPI: jsplugin.HostAPIVersion},
		Hosts:  []string{}, Permissions: []string{}, Changelog: "junk",
		Attestation: marketproto.Sign(marketproto.PayloadTypeAttestation, payload, online),
	}
}

func adminPlugin(id, kind string, fingerprints []string, versions ...marketproto.PluginVersion) marketproto.Plugin {
	copied := append([]marketproto.PluginVersion(nil), versions...)
	for i := 1; i < len(copied); i++ {
		if copied[i-1].Version < copied[i].Version {
			copied[i-1], copied[i] = copied[i], copied[i-1]
		}
	}
	ns, _, _ := strings.Cut(id, ".")
	return marketproto.Plugin{
		ID: id, Name: "Demo", Summary: "summary", Kind: kind,
		Developer: marketproto.Developer{Namespace: ns, DisplayName: "Acme", Fingerprints: fingerprints},
		Versions:  copied,
	}
}

func adminSorted(in []string) []string {
	out := append([]string(nil), in...)
	if out == nil {
		out = []string{}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func adminRevocation(id, pluginID, version, digest, level string, at time.Time) marketproto.RevocationEntry {
	return marketproto.RevocationEntry{
		ID: id, Target: marketproto.RevocationTarget{Type: "version", PluginID: pluginID, Version: version, PackageDigest: digest},
		Level: level, Reason: "listed", At: at.UTC().Format(time.RFC3339),
	}
}

func adminSignCatalog(t *testing.T, rootPriv, online ed25519.PrivateKey, rootSeq, indexSeq, revSeq int64, generated, expires time.Time, plugins []marketproto.Plugin, entries []marketproto.RevocationEntry) [3][]byte {
	t.Helper()
	if plugins == nil {
		plugins = []marketproto.Plugin{}
	}
	if entries == nil {
		entries = []marketproto.RevocationEntry{}
	}
	pub := online.Public().(ed25519.PublicKey)
	root := marketproto.Root{
		V: 1, Seq: rootSeq, IssuedAt: generated.UTC().Format(time.RFC3339),
		OnlineKeys: []marketproto.OnlineKey{{
			KeyID: marketproto.KeyID(pub), PublicKey: base64.StdEncoding.EncodeToString(pub),
			Roles:     []string{marketproto.RoleIndex, marketproto.RoleRevocations, marketproto.RoleAttestation},
			NotBefore: "2020-01-01T00:00:00Z", NotAfter: "2099-01-01T00:00:00Z",
		}},
		RetiredKeys: []marketproto.RetiredKey{},
		Endpoints:   marketproto.Endpoints{Registry: []string{"https://127.0.0.1:1/v1/"}},
	}
	idx := marketproto.Index{V: 1, Seq: indexSeq, GeneratedAt: generated.UTC().Format(time.RFC3339), ExpiresAt: expires.UTC().Format(time.RFC3339), Plugins: plugins}
	rev := marketproto.Revocations{V: 1, Seq: revSeq, GeneratedAt: generated.UTC().Format(time.RFC3339), ExpiresAt: expires.UTC().Format(time.RFC3339), Entries: entries}
	return [3][]byte{adminSignEnv(t, marketproto.PayloadTypeRoot, root, rootPriv), adminSignEnv(t, marketproto.PayloadTypeIndex, idx, online), adminSignEnv(t, marketproto.PayloadTypeRevocations, rev, online)}
}

func adminSignEnv(t *testing.T, payloadType string, payload any, priv ed25519.PrivateKey) []byte {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	env, err := json.Marshal(marketproto.Sign(payloadType, raw, priv))
	if err != nil {
		t.Fatal(err)
	}
	return env
}
