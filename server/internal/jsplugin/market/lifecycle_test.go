package market

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xlyra/server/internal/auth"
	"xlyra/server/internal/config"
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/marketproto"
	"xlyra/server/internal/store"
)

type memAudit struct {
	items []auth.AuditInput
}

func (m *memAudit) RecordAudit(_ context.Context, input auth.AuditInput) {
	m.items = append(m.items, input)
}

func (m *memAudit) find(action, pluginID string) (auth.AuditInput, bool) {
	for _, item := range m.items {
		if item.Action == action && item.ResourceID == pluginID {
			return item, true
		}
	}
	return auth.AuditInput{}, false
}

func TestAutoUpgradeRollbackAndBackfill(t *testing.T) {
	st := openMarketStore(t)
	repo := store.NewJSPluginRepository(st.DB())
	mgr := jsplugin.NewManager(st, nil)
	rootPriv, online := testKeys()
	devKey := fixedKey(0x44)
	devFP := jsplugin.PublicKeyFingerprint(devKey.Public().(ed25519.PublicKey))
	issued, exp := freshWindow()
	okFixture := map[string][]byte{"fixtures/ok.json": []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"empty"}}`)}
	oldRaw, oldPkg := packPlugin(t, devKey, probeManifest("acme.auto", "1.0.0"), probeSource("acme.auto"), okFixture, true)
	newRaw, newPkg := packPlugin(t, devKey, probeManifest("acme.auto", "1.1.0"), probeSource("acme.auto")+"// next\n", okFixture, true)
	manualRaw, manualPkg := packPlugin(t, devKey, probeManifest("acme.hand", "1.0.0"), probeSource("acme.hand"), okFixture, true)
	none := []string{}
	okCompat := compatCurrent()
	plugins := []marketproto.Plugin{
		pluginDoc("acme.auto", jsplugin.KindQuotaProbe, []string{devFP},
			versionEntry(newPkg, newRaw, attestPackage(t, online, newPkg, none, none, issued), okCompat, none, none, issued),
			versionEntry(oldPkg, oldRaw, attestPackage(t, online, oldPkg, none, none, issued), okCompat, none, none, issued),
		),
		pluginDoc("acme.hand", jsplugin.KindQuotaProbe, []string{devFP},
			versionEntry(manualPkg, manualRaw, attestPackage(t, online, manualPkg, none, none, issued), okCompat, none, none, issued),
		),
	}
	cloud := newCloud(signCatalog(t, rootPriv, online, 1, 1, 1, issued, exp, plugins, nil))
	cloud.put("packages/"+marketproto.FileSHA256(oldRaw)+".xlp", oldRaw)
	cloud.put("packages/"+marketproto.FileSHA256(newRaw)+".xlp", newRaw)
	cloud.put("packages/"+marketproto.FileSHA256(manualRaw)+".xlp", manualRaw)
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	base := srv.URL + "/"
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, []string{base}, config.PluginMarketConfig{Enabled: true, IndexSyncHours: 6, SourceURL: base, AutoUpdate: true})
	aud := &memAudit{}
	mgr.SetMarketTrust(svc)
	svc.SetRuntime(mgr)
	svc.SetAuditor(aud)
	ctx := context.Background()
	if status, err := svc.SyncOnce(ctx, true); err != nil || status.Error != "" {
		t.Fatalf("sync = %+v %v", status, err)
	}
	if _, err := mgr.Upload(ctx, "", manualRaw); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Install(ctx, "", "acme.auto", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Enable(ctx, "acme.auto", "1.0.0", jsplugin.EnableOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertBinding(ctx, store.JSPluginBinding{
		PluginID: "acme.auto", Version: "1.0.0", Kind: jsplugin.KindQuotaProbe,
		ScopeType: store.JSPluginScopeEndpoint, ScopeID: "auto-slug",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetMarketPolicy(ctx, "acme.auto", store.JSPluginUpdatePolicyAuto); err != nil {
		t.Fatal(err)
	}
	state, err := repo.GetMarketState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.applyPending(ctx, state); err != nil {
		t.Fatal(err)
	}
	enabled := enabledVersion(t, repo, "acme.auto")
	if enabled != "1.1.0" {
		t.Fatalf("enabled = %s", enabled)
	}
	bindings, err := repo.ListBindings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 || bindings[0].Version != "1.1.0" {
		t.Fatalf("binding = %+v", bindings)
	}
	record, err := repo.FindRecentAppliedAutoUpdate(ctx, "acme.auto", "1.1.0", autoRollbackWindow)
	if err != nil || record.FromVersion != "1.0.0" || record.Status != store.JSPluginAutoUpdateApplied {
		t.Fatalf("auto record = %+v %v", record, err)
	}
	entry, ok := aud.find("js_plugin.auto_update", "acme.auto")
	if !ok || !entry.Success || entry.Actor.Type != "system" {
		t.Fatalf("audit = %+v", entry)
	}
	if metaString(entry.Metadata, "actor") != auditActorAutoUpdate || metaString(entry.Metadata, "package_sha256") != newPkg.SHA256 {
		t.Fatalf("audit metadata = %#v", entry.Metadata)
	}
	hand, err := repo.GetVersion(ctx, "acme.hand", "1.0.0")
	if err != nil || hand.Origin != store.JSPluginOriginMarket || !attestationPresent(hand.Attestation) {
		t.Fatalf("backfill origin=%s attestation=%d err=%v", hand.Origin, len(hand.Attestation), err)
	}

	svc.RollbackAutoUpdate(ctx, "acme.auto", "1.1.0", "error_rate")
	if enabledVersion(t, repo, "acme.auto") != "1.0.0" {
		t.Fatalf("rolled back to %s", enabledVersion(t, repo, "acme.auto"))
	}
	rolled, err := repo.GetVersion(ctx, "acme.auto", "1.1.0")
	if err != nil || rolled.Status != store.JSPluginStatusDisabled {
		t.Fatalf("new version status = %s %v", rolled.Status, err)
	}
	again, err := repo.FindRecentAppliedAutoUpdate(ctx, "acme.auto", "1.1.0", autoRollbackWindow)
	if err == nil {
		t.Fatalf("applied record still visible: %+v", again)
	}
	sub, err := repo.GetMarketSubscription(ctx, "acme.auto")
	if err != nil || sub.PausedReason != pausedAutoRollback || sub.UpdatePolicy != store.JSPluginUpdatePolicyAuto {
		t.Fatalf("subscription = %+v %v", sub, err)
	}
	state, err = repo.GetMarketState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.applyPending(ctx, state); err != nil {
		t.Fatal(err)
	}
	if enabledVersion(t, repo, "acme.auto") != "1.0.0" {
		t.Fatalf("paused plugin upgraded to %s", enabledVersion(t, repo, "acme.auto"))
	}
	bindings, err = repo.ListBindings(ctx)
	if err != nil || len(bindings) != 1 || bindings[0].Version != "1.0.0" {
		t.Fatalf("binding after rollback = %+v %v", bindings, err)
	}
}

func TestRevocationEffects(t *testing.T) {
	st := openMarketStore(t)
	repo := store.NewJSPluginRepository(st.DB())
	mgr := jsplugin.NewManager(st, nil)
	rootPriv, online := testKeys()
	devKey := fixedKey(0x44)
	otherKey := fixedKey(0x55)
	devFP := jsplugin.PublicKeyFingerprint(devKey.Public().(ed25519.PublicKey))
	otherFP := jsplugin.PublicKeyFingerprint(otherKey.Public().(ed25519.PublicKey))
	issued, exp := freshWindow()
	okFixture := map[string][]byte{"fixtures/ok.json": []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"empty"}}`)}
	none := []string{}
	okCompat := compatCurrent()

	secRaw, secPkg := packPlugin(t, devKey, probeManifest("acme.sec", "1.0.0"), probeSource("acme.sec"), okFixture, true)
	rollNewRaw, rollNew := packPlugin(t, devKey, probeManifest("acme.roll", "1.1.0"), probeSource("acme.roll")+"// new\n", okFixture, true)
	rollOldRaw, rollOld := packPlugin(t, devKey, probeManifest("acme.roll", "1.0.0"), probeSource("acme.roll"), okFixture, true)
	plugRaw, plugPkg := packPlugin(t, devKey, probeManifest("acme.plug", "1.0.0"), probeSource("acme.plug"), okFixture, true)
	keyRaw, keyPkg := packPlugin(t, devKey, probeManifest("acme.key", "1.0.0"), probeSource("acme.key"), okFixture, true)
	depRaw, depPkg := packPlugin(t, devKey, probeManifest("acme.dep", "1.0.0"), probeSource("acme.dep"), okFixture, true)
	manualRaw, manualPkg := packPlugin(t, devKey, probeManifest("acme.manualsec", "1.0.0"), probeSource("acme.manualsec"), okFixture, true)
	hostNewRaw, hostNew := packPlugin(t, devKey, siteManifest("acme.hostroll", "1.1.0", []string{"api.example.com"}), siteSource("acme.hostroll")+"// new\n", nil, true)
	hostOldRaw, hostOld := packPlugin(t, devKey, siteManifest("acme.hostroll", "1.0.0", []string{"api.example.com", "files.example.com"}), siteSource("acme.hostroll"), nil, true)
	strangerRaw, stranger := packPlugin(t, otherKey, probeManifest("acme.strangeroll", "1.0.0"), probeSource("acme.strangeroll"), okFixture, true)
	badRaw, badPkg := packPlugin(t, devKey, probeManifest("acme.strangeroll", "1.1.0"), probeSource("acme.strangeroll")+"// bad\n", okFixture, true)

	plugins := []marketproto.Plugin{
		pluginDoc("acme.sec", jsplugin.KindQuotaProbe, []string{devFP}, versionEntry(secPkg, secRaw, attestPackage(t, online, secPkg, none, none, issued), okCompat, none, none, issued)),
		pluginDoc("acme.roll", jsplugin.KindQuotaProbe, []string{devFP},
			versionEntry(rollNew, rollNewRaw, attestPackage(t, online, rollNew, none, none, issued), okCompat, none, none, issued),
			versionEntry(rollOld, rollOldRaw, attestPackage(t, online, rollOld, none, none, issued), okCompat, none, none, issued)),
		pluginDoc("acme.plug", jsplugin.KindQuotaProbe, []string{devFP}, versionEntry(plugPkg, plugRaw, attestPackage(t, online, plugPkg, none, none, issued), okCompat, none, none, issued)),
		pluginDoc("acme.key", jsplugin.KindQuotaProbe, []string{devFP}, versionEntry(keyPkg, keyRaw, attestPackage(t, online, keyPkg, none, none, issued), okCompat, none, none, issued)),
		pluginDoc("acme.dep", jsplugin.KindQuotaProbe, []string{devFP}, versionEntry(depPkg, depRaw, attestPackage(t, online, depPkg, none, none, issued), okCompat, none, none, issued)),
		pluginDoc("acme.manualsec", jsplugin.KindQuotaProbe, []string{devFP}, versionEntry(manualPkg, manualRaw, attestPackage(t, online, manualPkg, none, none, issued), okCompat, none, none, issued)),
		pluginDoc("acme.hostroll", jsplugin.KindSiteProvider, []string{devFP},
			versionEntry(hostNew, hostNewRaw, attestPackage(t, online, hostNew, hostNew.Manifest.SiteProvider.Hosts, none, issued), okCompat, hostNew.Manifest.SiteProvider.Hosts, none, issued),
			versionEntry(hostOld, hostOldRaw, attestPackage(t, online, hostOld, hostOld.Manifest.SiteProvider.Hosts, none, issued), okCompat, hostOld.Manifest.SiteProvider.Hosts, none, issued)),
		pluginDoc("acme.strangeroll", jsplugin.KindQuotaProbe, []string{devFP, otherFP},
			versionEntry(badPkg, badRaw, attestPackage(t, online, badPkg, none, none, issued), okCompat, none, none, issued),
			versionEntry(stranger, strangerRaw, attestPackage(t, online, stranger, none, none, issued), okCompat, none, none, issued)),
	}
	entries := []marketproto.RevocationEntry{
		marketRevocation("rev-sec", "acme.sec", secPkg.Manifest.Version, secPkg.SHA256, "security", issued),
		marketRollback("rev-roll", "acme.roll", rollNew.Manifest.Version, rollNew.SHA256, "1.0.0", issued),
		{ID: "rev-plug", Level: "security", Reason: "plugin", At: issued.UTC().Format(time.RFC3339), Target: marketproto.RevocationTarget{Type: "plugin", PluginID: "acme.plug"}},
		{ID: "rev-key", Level: "security", Reason: "key", At: issued.UTC().Format(time.RFC3339), Target: marketproto.RevocationTarget{Type: "developerKey", Fingerprint: devFP}},
		marketRevocation("rev-dep", "acme.dep", depPkg.Manifest.Version, depPkg.SHA256, "deprecated", issued),
		marketRevocation("rev-manual", "acme.manualsec", manualPkg.Manifest.Version, manualPkg.SHA256, "security", issued),
		marketRollback("rev-host", "acme.hostroll", hostNew.Manifest.Version, hostNew.SHA256, "1.0.0", issued),
		marketRollback("rev-stranger", "acme.strangeroll", badPkg.Manifest.Version, badPkg.SHA256, "1.0.0", issued),
	}
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, nil, config.PluginMarketConfig{Enabled: true, AutoUpdate: true, IndexSyncHours: 6})
	aud := &memAudit{}
	mgr.SetMarketTrust(svc)
	svc.SetRuntime(mgr)
	svc.SetAuditor(aud)
	saveSignedState(t, repo, signCatalog(t, rootPriv, online, 1, 1, 1, issued, exp, plugins, entries))
	ctx := context.Background()
	storeVersion(t, repo, secRaw, secPkg, store.JSPluginOriginMarket, store.JSPluginStatusEnabled, true, online, issued)
	storeVersion(t, repo, rollNewRaw, rollNew, store.JSPluginOriginMarket, store.JSPluginStatusEnabled, true, online, issued)
	storeVersion(t, repo, rollOldRaw, rollOld, store.JSPluginOriginMarket, store.JSPluginStatusVerified, true, online, issued)
	storeVersion(t, repo, plugRaw, plugPkg, store.JSPluginOriginMarket, store.JSPluginStatusEnabled, true, online, issued)
	storeVersion(t, repo, keyRaw, keyPkg, store.JSPluginOriginMarket, store.JSPluginStatusEnabled, true, online, issued)
	storeVersion(t, repo, depRaw, depPkg, store.JSPluginOriginMarket, store.JSPluginStatusEnabled, true, online, issued)
	storeVersion(t, repo, manualRaw, manualPkg, store.JSPluginOriginManual, store.JSPluginStatusEnabled, false, online, issued)
	storeVersion(t, repo, hostNewRaw, hostNew, store.JSPluginOriginMarket, store.JSPluginStatusEnabled, true, online, issued)
	storeVersion(t, repo, hostOldRaw, hostOld, store.JSPluginOriginMarket, store.JSPluginStatusVerified, true, online, issued)
	storeVersion(t, repo, badRaw, badPkg, store.JSPluginOriginMarket, store.JSPluginStatusEnabled, true, online, issued)
	storeVersion(t, repo, strangerRaw, stranger, store.JSPluginOriginMarket, store.JSPluginStatusVerified, true, online, issued)

	state, err := repo.GetMarketState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.applyPending(ctx, state); err != nil {
		t.Fatal(err)
	}
	if enabledVersion(t, repo, "acme.sec") != "" {
		t.Fatal("security revocation left a version enabled")
	}
	if enabledVersion(t, repo, "acme.roll") != "1.0.0" {
		t.Fatalf("rollback enabled %s", enabledVersion(t, repo, "acme.roll"))
	}
	if enabledVersion(t, repo, "acme.plug") != "" {
		t.Fatal("plugin revocation left a version enabled")
	}
	if enabledVersion(t, repo, "acme.key") != "1.0.0" {
		t.Fatal("developer key revocation disabled an attested version")
	}
	if enabledVersion(t, repo, "acme.dep") != "1.0.0" {
		t.Fatal("deprecated revocation disabled a version")
	}
	if _, ok := aud.find("js_plugin.revoke", "acme.dep"); !ok {
		t.Fatal("deprecated revocation did not notify")
	}
	if enabledVersion(t, repo, "acme.manualsec") != "" {
		t.Fatal("manual package with a revoked digest stayed enabled")
	}
	if enabledVersion(t, repo, "acme.hostroll") != "" {
		t.Fatal("rollback that adds hosts was enabled")
	}
	if enabledVersion(t, repo, "acme.strangeroll") != "" {
		t.Fatal("rollback from a different developer was enabled")
	}
	if _, ok := aud.find("js_plugin.revoke", "acme.key"); ok {
		t.Fatal("developer key revocation audited an attested market version")
	}

	if err := svc.applyPending(ctx, state); err != nil {
		t.Fatal(err)
	}
	notices := 0
	for _, item := range aud.items {
		if item.Action == "js_plugin.revoke" && item.ResourceID == "acme.dep" {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("deprecated notices = %d", notices)
	}
}

func TestExpiredRevocationsPauseAutoUpdate(t *testing.T) {
	st := openMarketStore(t)
	repo := store.NewJSPluginRepository(st.DB())
	mgr := jsplugin.NewManager(st, nil)
	rootPriv, online := testKeys()
	devKey := fixedKey(0x44)
	devFP := jsplugin.PublicKeyFingerprint(devKey.Public().(ed25519.PublicKey))
	expires := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	issued := expires.Add(-time.Hour)
	okFixture := map[string][]byte{"fixtures/ok.json": []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"empty"}}`)}
	oldRaw, oldPkg := packPlugin(t, devKey, probeManifest("acme.stale", "1.0.0"), probeSource("acme.stale"), okFixture, true)
	newRaw, newPkg := packPlugin(t, devKey, probeManifest("acme.stale", "1.1.0"), probeSource("acme.stale")+"// next\n", okFixture, true)
	none := []string{}
	okCompat := compatCurrent()
	plugins := []marketproto.Plugin{pluginDoc("acme.stale", jsplugin.KindQuotaProbe, []string{devFP},
		versionEntry(newPkg, newRaw, attestPackage(t, online, newPkg, none, none, issued), okCompat, none, none, issued),
		versionEntry(oldPkg, oldRaw, attestPackage(t, online, oldPkg, none, none, issued), okCompat, none, none, issued),
	)}
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, nil, config.PluginMarketConfig{Enabled: true, AutoUpdate: true, IndexSyncHours: 6})
	mgr.SetMarketTrust(svc)
	svc.SetRuntime(mgr)
	saveSignedState(t, repo, signCatalog(t, rootPriv, online, 1, 1, 1, issued, expires, plugins, nil))
	ctx := context.Background()
	storeVersion(t, repo, oldRaw, oldPkg, store.JSPluginOriginMarket, store.JSPluginStatusEnabled, true, online, issued)
	if err := repo.SetMarketPolicy(ctx, "acme.stale", store.JSPluginUpdatePolicyAuto); err != nil {
		t.Fatal(err)
	}
	state, err := repo.GetMarketState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := snapshotFrom(state, svc.rootKeys, time.Now())
	if err != nil || !snap.RevocationsExpired {
		t.Fatalf("expired = %v %v", snap.RevocationsExpired, err)
	}
	if err := svc.applyPending(ctx, state); err != nil {
		t.Fatal(err)
	}
	if enabledVersion(t, repo, "acme.stale") != "1.0.0" {
		t.Fatalf("expired list upgraded to %s", enabledVersion(t, repo, "acme.stale"))
	}
	view := describeUpdate(plugins[0], snap.Revocations, []store.JSPluginVersion{{
		PluginID: "acme.stale", Version: "1.0.0", Status: store.JSPluginStatusEnabled, Signer: oldPkg.Signer,
		Manifest: manifestJSON(t, oldPkg),
	}}, store.JSPluginMarketSubscription{UpdatePolicy: store.JSPluginUpdatePolicyAuto}, "1.15.1", true, true, nil)
	if view.AutoOK || view.BlockReason != UpdateReasonRevocations || !view.UpdateAvailable {
		t.Fatalf("view = %+v", view)
	}
}

func TestRevokeRollbackDownloadsMissingVersion(t *testing.T) {
	st := openMarketStore(t)
	repo := store.NewJSPluginRepository(st.DB())
	mgr := jsplugin.NewManager(st, nil)
	rootPriv, online := testKeys()
	devKey := fixedKey(0x44)
	devFP := jsplugin.PublicKeyFingerprint(devKey.Public().(ed25519.PublicKey))
	issued, exp := freshWindow()
	okFixture := map[string][]byte{"fixtures/ok.json": []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"empty"}}`)}
	oldRaw, oldPkg := packPlugin(t, devKey, probeManifest("acme.pull", "1.0.0"), probeSource("acme.pull"), okFixture, true)
	newRaw, newPkg := packPlugin(t, devKey, probeManifest("acme.pull", "1.1.0"), probeSource("acme.pull")+"// new\n", okFixture, true)
	none := []string{}
	okCompat := compatCurrent()
	plugins := []marketproto.Plugin{pluginDoc("acme.pull", jsplugin.KindQuotaProbe, []string{devFP},
		versionEntry(newPkg, newRaw, attestPackage(t, online, newPkg, none, none, issued), okCompat, none, none, issued),
		versionEntry(oldPkg, oldRaw, attestPackage(t, online, oldPkg, none, none, issued), okCompat, none, none, issued),
	)}
	entries := []marketproto.RevocationEntry{
		marketRollback("rev-pull", "acme.pull", newPkg.Manifest.Version, newPkg.SHA256, "1.0.0", issued),
	}
	cloud := newCloud(signCatalog(t, rootPriv, online, 1, 1, 1, issued, exp, plugins, entries))
	cloud.put("packages/"+marketproto.FileSHA256(oldRaw)+".xlp", oldRaw)
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, []string{srv.URL + "/"}, config.PluginMarketConfig{Enabled: true, AutoUpdate: true, IndexSyncHours: 6, SourceURL: srv.URL + "/"})
	mgr.SetMarketTrust(svc)
	svc.SetRuntime(mgr)
	saveSignedState(t, repo, signCatalog(t, rootPriv, online, 1, 1, 1, issued, exp, plugins, entries))
	ctx := context.Background()
	storeVersion(t, repo, newRaw, newPkg, store.JSPluginOriginMarket, store.JSPluginStatusEnabled, true, online, issued)
	state, err := repo.GetMarketState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.applyPending(ctx, state); err != nil {
		t.Fatal(err)
	}
	if enabledVersion(t, repo, "acme.pull") != "1.0.0" {
		t.Fatalf("downloaded rollback enabled %s", enabledVersion(t, repo, "acme.pull"))
	}
	if _, err := repo.GetVersion(ctx, "acme.pull", "1.0.0"); err != nil {
		t.Fatal(err)
	}
}

func TestAutoUpgradeLocalDigest(t *testing.T) {
	st := openMarketStore(t)
	repo := store.NewJSPluginRepository(st.DB())
	mgr := jsplugin.NewManager(st, nil)
	rootPriv, online := testKeys()
	devKey := fixedKey(0x44)
	devFP := jsplugin.PublicKeyFingerprint(devKey.Public().(ed25519.PublicKey))
	issued, exp := freshWindow()
	okFixture := map[string][]byte{"fixtures/ok.json": []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"empty"}}`)}
	none := []string{}
	okCompat := compatCurrent()

	clashOldRaw, clashOld := packPlugin(t, devKey, probeManifest("acme.clash", "1.0.0"), probeSource("acme.clash"), okFixture, true)
	clashNewRaw, clashNew := packPlugin(t, devKey, probeManifest("acme.clash", "1.1.0"), probeSource("acme.clash")+"// market\n", okFixture, true)
	clashLocalRaw, clashLocal := packPlugin(t, devKey, probeManifest("acme.clash", "1.1.0"), probeSource("acme.clash")+"// local\n", okFixture, true)
	sameOldRaw, sameOld := packPlugin(t, devKey, probeManifest("acme.same", "1.0.0"), probeSource("acme.same"), okFixture, true)
	sameNewRaw, sameNew := packPlugin(t, devKey, probeManifest("acme.same", "1.1.0"), probeSource("acme.same")+"// market\n", okFixture, true)
	plainOldRaw, plainOld := packPlugin(t, devKey, probeManifest("acme.plain", "1.0.0"), probeSource("acme.plain"), okFixture, true)
	plainNewRaw, plainNew := packPlugin(t, devKey, probeManifest("acme.plain", "1.1.0"), probeSource("acme.plain")+"// market\n", okFixture, true)
	plugins := []marketproto.Plugin{
		pluginDoc("acme.clash", jsplugin.KindQuotaProbe, []string{devFP},
			versionEntry(clashNew, clashNewRaw, attestPackage(t, online, clashNew, none, none, issued), okCompat, none, none, issued),
			versionEntry(clashOld, clashOldRaw, attestPackage(t, online, clashOld, none, none, issued), okCompat, none, none, issued)),
		pluginDoc("acme.same", jsplugin.KindQuotaProbe, []string{devFP},
			versionEntry(sameNew, sameNewRaw, attestPackage(t, online, sameNew, none, none, issued), okCompat, none, none, issued),
			versionEntry(sameOld, sameOldRaw, attestPackage(t, online, sameOld, none, none, issued), okCompat, none, none, issued)),
		pluginDoc("acme.plain", jsplugin.KindQuotaProbe, []string{devFP},
			versionEntry(plainNew, plainNewRaw, attestPackage(t, online, plainNew, none, none, issued), okCompat, none, none, issued),
			versionEntry(plainOld, plainOldRaw, attestPackage(t, online, plainOld, none, none, issued), okCompat, none, none, issued)),
	}
	cloud := newCloud(signCatalog(t, rootPriv, online, 1, 1, 1, issued, exp, plugins, nil))
	cloud.put("packages/"+marketproto.FileSHA256(clashOldRaw)+".xlp", clashOldRaw)
	cloud.put("packages/"+marketproto.FileSHA256(clashNewRaw)+".xlp", clashNewRaw)
	cloud.put("packages/"+marketproto.FileSHA256(sameOldRaw)+".xlp", sameOldRaw)
	cloud.put("packages/"+marketproto.FileSHA256(plainOldRaw)+".xlp", plainOldRaw)
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	base := srv.URL + "/"
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, []string{base}, config.PluginMarketConfig{Enabled: true, IndexSyncHours: 6, SourceURL: base, AutoUpdate: true})
	aud := &memAudit{}
	mgr.SetMarketTrust(svc)
	svc.SetRuntime(mgr)
	svc.SetAuditor(aud)
	ctx := context.Background()
	if status, err := svc.SyncOnce(ctx, true); err != nil || status.Error != "" {
		t.Fatalf("sync = %+v %v", status, err)
	}
	for _, id := range []string{"acme.clash", "acme.same", "acme.plain"} {
		if _, err := svc.Install(ctx, "", id, "1.0.0"); err != nil {
			t.Fatal(err)
		}
		if err := mgr.Enable(ctx, id, "1.0.0", jsplugin.EnableOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := repo.SetMarketPolicy(ctx, id, store.JSPluginUpdatePolicyAuto); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mgr.Upload(ctx, "", clashLocalRaw); err != nil {
		t.Fatal(err)
	}
	storeVersion(t, repo, sameNewRaw, sameNew, store.JSPluginOriginManual, store.JSPluginStatusVerified, true, online, issued)
	storeVersion(t, repo, plainNewRaw, plainNew, store.JSPluginOriginMarket, store.JSPluginStatusVerified, false, online, issued)
	state, err := repo.GetMarketState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.applyPending(ctx, state); err != nil {
		t.Fatal(err)
	}

	if enabledVersion(t, repo, "acme.clash") != "1.0.0" {
		t.Fatalf("mismatch enabled %s", enabledVersion(t, repo, "acme.clash"))
	}
	kept, err := repo.GetVersion(ctx, "acme.clash", "1.1.0")
	if err != nil || kept.PackageSHA256 != clashLocal.SHA256 || kept.Origin != store.JSPluginOriginManual {
		t.Fatalf("local package rewritten sha=%s origin=%s err=%v", kept.PackageSHA256, kept.Origin, err)
	}
	clashRec, err := repo.LatestAutoUpdate(ctx, "acme.clash", "1.1.0")
	if err != nil || clashRec.Status != store.JSPluginAutoUpdateFailed || clashRec.Reason != reasonHashMismatch {
		t.Fatalf("clash record = %+v %v", clashRec, err)
	}
	clashAudit, ok := aud.find("js_plugin.auto_update", "acme.clash")
	if !ok || clashAudit.Success || metaString(clashAudit.Metadata, "reason") != reasonHashMismatch {
		t.Fatalf("clash audit = %+v", clashAudit)
	}

	if enabledVersion(t, repo, "acme.same") != "1.1.0" {
		t.Fatalf("matching digest enabled %s", enabledVersion(t, repo, "acme.same"))
	}
	sameRow, err := repo.GetVersion(ctx, "acme.same", "1.1.0")
	if err != nil || sameRow.PackageSHA256 != sameNew.SHA256 {
		t.Fatalf("same package = %s %v", sameRow.PackageSHA256, err)
	}
	sameFile := "packages/" + marketproto.FileSHA256(sameNewRaw) + ".xlp"
	if cloud.hit(sameFile) != 0 {
		t.Fatalf("matching package downloaded %d times", cloud.hit(sameFile))
	}

	if enabledVersion(t, repo, "acme.plain") != "1.0.0" {
		t.Fatalf("untrusted upgrade enabled %s", enabledVersion(t, repo, "acme.plain"))
	}
	plainRec, err := repo.LatestAutoUpdate(ctx, "acme.plain", "1.1.0")
	if err != nil || plainRec.Status != store.JSPluginAutoUpdateFailed || plainRec.Reason != reasonNeedsConfirmation {
		t.Fatalf("plain record = %+v %v", plainRec, err)
	}
	plainAudit, ok := aud.find("js_plugin.auto_update", "acme.plain")
	if !ok || plainAudit.Success || metaString(plainAudit.Metadata, "reason") != reasonNeedsConfirmation {
		t.Fatalf("plain audit = %+v", plainAudit)
	}
}

func TestRevokeRollbackLocalDigest(t *testing.T) {
	st := openMarketStore(t)
	repo := store.NewJSPluginRepository(st.DB())
	mgr := jsplugin.NewManager(st, nil)
	rootPriv, online := testKeys()
	devKey := fixedKey(0x44)
	devFP := jsplugin.PublicKeyFingerprint(devKey.Public().(ed25519.PublicKey))
	issued, exp := freshWindow()
	okFixture := map[string][]byte{"fixtures/ok.json": []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"empty"}}`)}
	none := []string{}
	okCompat := compatCurrent()

	clashNewRaw, clashNew := packPlugin(t, devKey, probeManifest("acme.clash", "1.1.0"), probeSource("acme.clash")+"// bad\n", okFixture, true)
	clashOldRaw, clashOld := packPlugin(t, devKey, probeManifest("acme.clash", "1.0.0"), probeSource("acme.clash"), okFixture, true)
	clashLocalRaw, clashLocal := packPlugin(t, devKey, probeManifest("acme.clash", "1.0.0"), probeSource("acme.clash")+"// local\n", okFixture, true)
	sameNewRaw, sameNew := packPlugin(t, devKey, probeManifest("acme.same", "1.1.0"), probeSource("acme.same")+"// bad\n", okFixture, true)
	sameOldRaw, sameOld := packPlugin(t, devKey, probeManifest("acme.same", "1.0.0"), probeSource("acme.same"), okFixture, true)
	plainNewRaw, plainNew := packPlugin(t, devKey, probeManifest("acme.plain", "1.1.0"), probeSource("acme.plain")+"// bad\n", okFixture, true)
	plainOldRaw, plainOld := packPlugin(t, devKey, probeManifest("acme.plain", "1.0.0"), probeSource("acme.plain"), okFixture, true)
	plugins := []marketproto.Plugin{
		pluginDoc("acme.clash", jsplugin.KindQuotaProbe, []string{devFP},
			versionEntry(clashNew, clashNewRaw, attestPackage(t, online, clashNew, none, none, issued), okCompat, none, none, issued),
			versionEntry(clashOld, clashOldRaw, attestPackage(t, online, clashOld, none, none, issued), okCompat, none, none, issued)),
		pluginDoc("acme.same", jsplugin.KindQuotaProbe, []string{devFP},
			versionEntry(sameNew, sameNewRaw, attestPackage(t, online, sameNew, none, none, issued), okCompat, none, none, issued),
			versionEntry(sameOld, sameOldRaw, attestPackage(t, online, sameOld, none, none, issued), okCompat, none, none, issued)),
		pluginDoc("acme.plain", jsplugin.KindQuotaProbe, []string{devFP},
			versionEntry(plainNew, plainNewRaw, attestPackage(t, online, plainNew, none, none, issued), okCompat, none, none, issued),
			versionEntry(plainOld, plainOldRaw, attestPackage(t, online, plainOld, none, none, issued), okCompat, none, none, issued)),
	}
	entries := []marketproto.RevocationEntry{
		marketRollback("rev-clash", "acme.clash", clashNew.Manifest.Version, clashNew.SHA256, "1.0.0", issued),
		marketRollback("rev-same", "acme.same", sameNew.Manifest.Version, sameNew.SHA256, "1.0.0", issued),
		marketRollback("rev-plain", "acme.plain", plainNew.Manifest.Version, plainNew.SHA256, "1.0.0", issued),
	}
	docs := signCatalog(t, rootPriv, online, 1, 1, 1, issued, exp, plugins, entries)
	cloud := newCloud(docs)
	cloud.put("packages/"+marketproto.FileSHA256(clashOldRaw)+".xlp", clashOldRaw)
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, []string{srv.URL + "/"}, config.PluginMarketConfig{Enabled: true, AutoUpdate: true, IndexSyncHours: 6, SourceURL: srv.URL + "/"})
	aud := &memAudit{}
	mgr.SetMarketTrust(svc)
	svc.SetRuntime(mgr)
	svc.SetAuditor(aud)
	saveSignedState(t, repo, docs)
	ctx := context.Background()
	storeVersion(t, repo, clashNewRaw, clashNew, store.JSPluginOriginMarket, store.JSPluginStatusEnabled, true, online, issued)
	if _, err := mgr.Upload(ctx, "", clashLocalRaw); err != nil {
		t.Fatal(err)
	}
	storeVersion(t, repo, sameNewRaw, sameNew, store.JSPluginOriginMarket, store.JSPluginStatusEnabled, true, online, issued)
	storeVersion(t, repo, sameOldRaw, sameOld, store.JSPluginOriginManual, store.JSPluginStatusVerified, true, online, issued)
	storeVersion(t, repo, plainNewRaw, plainNew, store.JSPluginOriginMarket, store.JSPluginStatusEnabled, true, online, issued)
	storeVersion(t, repo, plainOldRaw, plainOld, store.JSPluginOriginMarket, store.JSPluginStatusVerified, false, online, issued)
	state, err := repo.GetMarketState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.applyPending(ctx, state); err != nil {
		t.Fatal(err)
	}

	if enabledVersion(t, repo, "acme.clash") != "" {
		t.Fatalf("mismatch rollback enabled %s", enabledVersion(t, repo, "acme.clash"))
	}
	kept, err := repo.GetVersion(ctx, "acme.clash", "1.0.0")
	if err != nil || kept.PackageSHA256 != clashLocal.SHA256 || kept.Origin != store.JSPluginOriginManual {
		t.Fatalf("local rollback package rewritten sha=%s origin=%s err=%v", kept.PackageSHA256, kept.Origin, err)
	}
	clashAudit, ok := aud.find("js_plugin.revoke", "acme.clash")
	if !ok || metaString(clashAudit.Metadata, "rollback_error") != reasonHashMismatch {
		t.Fatalf("clash audit = %+v", clashAudit)
	}

	if enabledVersion(t, repo, "acme.same") != "1.0.0" {
		t.Fatalf("matching rollback enabled %s", enabledVersion(t, repo, "acme.same"))
	}
	sameFile := "packages/" + marketproto.FileSHA256(sameOldRaw) + ".xlp"
	if cloud.hit(sameFile) != 0 {
		t.Fatalf("matching rollback downloaded %d times", cloud.hit(sameFile))
	}

	if enabledVersion(t, repo, "acme.plain") != "" {
		t.Fatalf("untrusted rollback enabled %s", enabledVersion(t, repo, "acme.plain"))
	}
	plainAudit, ok := aud.find("js_plugin.revoke", "acme.plain")
	if !ok || metaString(plainAudit.Metadata, "rollback_error") != reasonNeedsConfirmation {
		t.Fatalf("plain audit = %+v", plainAudit)
	}
}

func TestAutoRollbackDoesNotConfirmUntrusted(t *testing.T) {
	st := openMarketStore(t)
	repo := store.NewJSPluginRepository(st.DB())
	mgr := jsplugin.NewManager(st, nil)
	rootPriv, online := testKeys()
	devKey := fixedKey(0x44)
	devFP := jsplugin.PublicKeyFingerprint(devKey.Public().(ed25519.PublicKey))
	issued, exp := freshWindow()
	okFixture := map[string][]byte{"fixtures/ok.json": []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"empty"}}`)}
	none := []string{}
	okCompat := compatCurrent()
	delistNewRaw, delistNew := packPlugin(t, devKey, probeManifest("acme.delist", "1.1.0"), probeSource("acme.delist")+"// next\n", okFixture, true)
	delistOldRaw, delistOld := packPlugin(t, devKey, probeManifest("acme.delist", "1.0.0"), probeSource("acme.delist"), okFixture, true)
	manualNewRaw, manualNew := packPlugin(t, devKey, probeManifest("acme.manual", "1.1.0"), probeSource("acme.manual")+"// next\n", okFixture, true)
	manualOldRaw, manualOld := packPlugin(t, devKey, probeManifest("acme.manual", "1.0.0"), probeSource("acme.manual"), okFixture, true)
	trustNewRaw, trustNew := packPlugin(t, devKey, probeManifest("acme.trust", "1.1.0"), probeSource("acme.trust")+"// next\n", okFixture, true)
	trustOldRaw, trustOld := packPlugin(t, devKey, probeManifest("acme.trust", "1.0.0"), probeSource("acme.trust"), okFixture, true)
	plugins := []marketproto.Plugin{
		pluginDoc("acme.delist", jsplugin.KindQuotaProbe, []string{devFP}, versionEntry(delistNew, delistNewRaw, attestPackage(t, online, delistNew, none, none, issued), okCompat, none, none, issued)),
		pluginDoc("acme.manual", jsplugin.KindQuotaProbe, []string{devFP}, versionEntry(manualNew, manualNewRaw, attestPackage(t, online, manualNew, none, none, issued), okCompat, none, none, issued)),
		pluginDoc("acme.trust", jsplugin.KindQuotaProbe, []string{devFP}, versionEntry(trustNew, trustNewRaw, attestPackage(t, online, trustNew, none, none, issued), okCompat, none, none, issued)),
	}
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, nil, config.PluginMarketConfig{Enabled: true, AutoUpdate: true, IndexSyncHours: 6})
	aud := &memAudit{}
	mgr.SetMarketTrust(svc)
	svc.SetRuntime(mgr)
	svc.SetAuditor(aud)
	saveSignedState(t, repo, signCatalog(t, rootPriv, online, 1, 1, 1, issued, exp, plugins, nil))
	ctx := context.Background()
	storeVersion(t, repo, delistNewRaw, delistNew, store.JSPluginOriginMarket, store.JSPluginStatusEnabled, true, online, issued)
	storeVersion(t, repo, delistOldRaw, delistOld, store.JSPluginOriginMarket, store.JSPluginStatusDisabled, true, online, issued)
	storeVersion(t, repo, manualNewRaw, manualNew, store.JSPluginOriginMarket, store.JSPluginStatusEnabled, true, online, issued)
	storeVersion(t, repo, manualOldRaw, manualOld, store.JSPluginOriginManual, store.JSPluginStatusDisabled, false, online, issued)
	storeVersion(t, repo, trustNewRaw, trustNew, store.JSPluginOriginMarket, store.JSPluginStatusEnabled, true, online, issued)
	storeVersion(t, repo, trustOldRaw, trustOld, store.JSPluginOriginManual, store.JSPluginStatusDisabled, false, online, issued)
	for _, id := range []string{"acme.delist", "acme.manual", "acme.trust"} {
		if _, err := repo.CreateAutoUpdate(ctx, store.JSPluginAutoUpdate{
			PluginID: id, FromVersion: "1.0.0", ToVersion: "1.1.0", Status: store.JSPluginAutoUpdateApplied,
		}); err != nil {
			t.Fatal(err)
		}
		if err := mgr.Disable(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	svc.RollbackAutoUpdate(ctx, "acme.delist", "1.1.0", "error_rate")
	svc.RollbackAutoUpdate(ctx, "acme.manual", "1.1.0", "error_rate")
	if enabledVersion(t, repo, "acme.delist") != "" || enabledVersion(t, repo, "acme.manual") != "" {
		t.Fatalf("delist=%s manual=%s", enabledVersion(t, repo, "acme.delist"), enabledVersion(t, repo, "acme.manual"))
	}
	for _, id := range []string{"acme.delist", "acme.manual"} {
		record, err := repo.LatestAutoUpdate(ctx, id, "1.1.0")
		if err != nil || record.Status != store.JSPluginAutoUpdateFailed || record.Reason != reasonNeedsConfirmation {
			t.Fatalf("%s record = %+v %v", id, record, err)
		}
		entry, ok := aud.find("js_plugin.auto_rollback", id)
		if !ok || entry.Success || metaString(entry.Metadata, "reason") != reasonNeedsConfirmation {
			t.Fatalf("%s audit = %+v", id, entry)
		}
	}

	if _, err := mgr.AddTrustedKey(ctx, "", "acme", base64.StdEncoding.EncodeToString(devKey.Public().(ed25519.PublicKey))); err != nil {
		t.Fatal(err)
	}
	svc.RollbackAutoUpdate(ctx, "acme.trust", "1.1.0", "error_rate")
	if enabledVersion(t, repo, "acme.trust") != "1.0.0" {
		t.Fatalf("trusted rollback enabled %s", enabledVersion(t, repo, "acme.trust"))
	}
}

type runtimeSpy struct {
	enabled []string
}

func (r *runtimeSpy) Enable(_ context.Context, pluginID, version string, _ jsplugin.EnableOptions) error {
	r.enabled = append(r.enabled, pluginID+"@"+version)
	return nil
}

func (r *runtimeSpy) Disable(context.Context, string) error { return nil }

func TestRollbackSkipsRevokedFromVersion(t *testing.T) {
	st := openMarketStore(t)
	repo := store.NewJSPluginRepository(st.DB())
	mgr := jsplugin.NewManager(st, nil)
	rootPriv, online := testKeys()
	devKey := fixedKey(0x46)
	devFP := jsplugin.PublicKeyFingerprint(devKey.Public().(ed25519.PublicKey))
	issued, exp := freshWindow()
	okFixture := map[string][]byte{"fixtures/ok.json": []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"empty"}}`)}
	none := []string{}
	okCompat := compatCurrent()
	oldRaw, oldPkg := packPlugin(t, devKey, probeManifest("acme.revold", "1.0.0"), probeSource("acme.revold"), okFixture, true)
	newRaw, newPkg := packPlugin(t, devKey, probeManifest("acme.revold", "1.1.0"), probeSource("acme.revold")+"// next\n", okFixture, true)
	plugins := []marketproto.Plugin{
		pluginDoc("acme.revold", jsplugin.KindQuotaProbe, []string{devFP},
			versionEntry(newPkg, newRaw, attestPackage(t, online, newPkg, none, none, issued), okCompat, none, none, issued),
			versionEntry(oldPkg, oldRaw, attestPackage(t, online, oldPkg, none, none, issued), okCompat, none, none, issued),
		),
	}
	entries := []marketproto.RevocationEntry{
		marketRevocation("rev-old", "acme.revold", oldPkg.Manifest.Version, oldPkg.SHA256, "security", issued),
	}
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, nil, config.PluginMarketConfig{Enabled: true, AutoUpdate: true, IndexSyncHours: 6})
	aud := &memAudit{}
	mgr.SetMarketTrust(svc)
	svc.SetRuntime(mgr)
	svc.SetAuditor(aud)
	saveSignedState(t, repo, signCatalog(t, rootPriv, online, 1, 1, 1, issued, exp, plugins, entries))
	ctx := context.Background()
	storeVersion(t, repo, newRaw, newPkg, store.JSPluginOriginMarket, store.JSPluginStatusEnabled, true, online, issued)
	storeVersion(t, repo, oldRaw, oldPkg, store.JSPluginOriginMarket, store.JSPluginStatusDisabled, true, online, issued)
	if _, err := repo.CreateAutoUpdate(ctx, store.JSPluginAutoUpdate{
		PluginID: "acme.revold", FromVersion: "1.0.0", ToVersion: "1.1.0", Status: store.JSPluginAutoUpdateApplied,
	}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Disable(ctx, "acme.revold"); err != nil {
		t.Fatal(err)
	}
	svc.RollbackAutoUpdate(ctx, "acme.revold", "1.1.0", "error_rate")
	if enabled := enabledVersion(t, repo, "acme.revold"); enabled != "" {
		t.Fatalf("revoked previous enabled %s", enabled)
	}
	record, err := repo.LatestAutoUpdate(ctx, "acme.revold", "1.1.0")
	if err != nil || record.Status != store.JSPluginAutoUpdateFailed || record.Reason != UpdateReasonRevoked {
		t.Fatalf("record = %+v %v", record, err)
	}
	entry, ok := aud.find("js_plugin.auto_rollback", "acme.revold")
	if !ok || entry.Success || metaString(entry.Metadata, "reason") != UpdateReasonRevoked {
		t.Fatalf("audit = %+v", entry)
	}
}

func TestRevokeRollbackRejectsKindAndSiteType(t *testing.T) {
	st := openMarketStore(t)
	repo := store.NewJSPluginRepository(st.DB())
	rootPriv, online := testKeys()
	devKey := fixedKey(0x45)
	devFP := jsplugin.PublicKeyFingerprint(devKey.Public().(ed25519.PublicKey))
	issued, _ := freshWindow()
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, nil, config.PluginMarketConfig{Enabled: true, IndexSyncHours: 6})
	spy := &runtimeSpy{}
	svc.SetRuntime(spy)
	ctx := context.Background()

	okFixture := map[string][]byte{"fixtures/ok.json": []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"empty"}}`)}
	kindNewRaw, kindNew := packPlugin(t, devKey, probeManifest("acme.kindchg", "1.1.0"), probeSource("acme.kindchg"), okFixture, true)
	kindOldManifest := probeManifest("acme.kindchg", "1.0.0")
	kindOldManifest.Kind = jsplugin.KindModelList
	kindOldRaw, kindOld := packPlugin(t, devKey, kindOldManifest, probeSource("acme.kindchg"), okFixture, true)
	siteNew := siteManifest("acme.sitechg", "1.1.0", []string{"api.example.com"})
	siteOld := siteManifest("acme.sitechg", "1.0.0", []string{"api.example.com"})
	siteOld.SiteProvider.SiteType = "acme.sitechg/other"
	siteNewRaw, siteNewPkg := packPlugin(t, devKey, siteNew, siteSource("acme.sitechg"), nil, true)
	siteOldRaw, siteOldPkg := packPlugin(t, devKey, siteOld, siteSource("acme.sitechg"), nil, true)
	storeVersion(t, repo, kindNewRaw, kindNew, store.JSPluginOriginMarket, store.JSPluginStatusDisabled, true, online, issued)
	storeVersion(t, repo, kindOldRaw, kindOld, store.JSPluginOriginMarket, store.JSPluginStatusDisabled, true, online, issued)
	storeVersion(t, repo, siteNewRaw, siteNewPkg, store.JSPluginOriginMarket, store.JSPluginStatusDisabled, true, online, issued)
	storeVersion(t, repo, siteOldRaw, siteOldPkg, store.JSPluginOriginMarket, store.JSPluginStatusDisabled, true, online, issued)

	none := []string{}
	okCompat := compatCurrent()
	snap := Snapshot{Index: &marketproto.Index{Plugins: []marketproto.Plugin{
		pluginDoc("acme.kindchg", jsplugin.KindQuotaProbe, []string{devFP},
			versionEntry(kindOld, kindOldRaw, attestPackage(t, online, kindOld, none, none, issued), okCompat, none, none, issued)),
		pluginDoc("acme.sitechg", jsplugin.KindSiteProvider, []string{devFP},
			versionEntry(siteOldPkg, siteOldRaw, attestPackage(t, online, siteOldPkg, []string{"api.example.com"}, none, issued), okCompat, []string{"api.example.com"}, none, issued)),
	}}}
	kindRow, err := repo.GetVersion(ctx, "acme.kindchg", "1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := svc.rollbackRevoked(ctx, snap, kindRow, "1.0.0")
	if ok || err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("kind rollback = %v %v", ok, err)
	}
	siteRow, err := repo.GetVersion(ctx, "acme.sitechg", "1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	ok, err = svc.rollbackRevoked(ctx, snap, siteRow, "1.0.0")
	if ok || err == nil || !strings.Contains(err.Error(), "site type") {
		t.Fatalf("site rollback = %v %v", ok, err)
	}
	if len(spy.enabled) != 0 || enabledVersion(t, repo, "acme.kindchg") != "" || enabledVersion(t, repo, "acme.sitechg") != "" {
		t.Fatalf("rejected rollback enabled %v", spy.enabled)
	}
}

func marketRollback(id, pluginID, version, digest, rollbackTo string, at time.Time) marketproto.RevocationEntry {
	entry := marketRevocation(id, pluginID, version, digest, "security", at)
	entry.RollbackTo = rollbackTo
	return entry
}

func saveSignedState(t *testing.T, repo store.JSPluginRepository, docs signedDocs) {
	t.Helper()
	rootSHA, err := payloadSHA256(docs.root)
	if err != nil {
		t.Fatal(err)
	}
	idxSHA, err := payloadSHA256(docs.index)
	if err != nil {
		t.Fatal(err)
	}
	revSHA, err := payloadSHA256(docs.revocations)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := repo.SaveMarketState(context.Background(), store.JSPluginMarketState{
		RootSeq: 1, RootPayloadSHA256: rootSHA, RootEnvelope: docs.root,
		IndexSeq: 1, IndexPayloadSHA256: idxSHA, IndexEnvelope: docs.index, IndexSyncedAt: &now,
		RevocationsSeq: 1, RevocationsPayloadSHA256: revSHA, RevocationsEnvelope: docs.revocations, RevocationsSyncedAt: &now,
		LastAttemptAt: &now, SourceURL: "https://market.example/v1/",
	}); err != nil {
		t.Fatal(err)
	}
}

func storeVersion(t *testing.T, repo store.JSPluginRepository, raw []byte, pkg jsplugin.Package, origin, status string, attest bool, online ed25519.PrivateKey, issued time.Time) {
	t.Helper()
	row := store.JSPluginVersion{
		PluginID: pkg.Manifest.ID, Version: pkg.Manifest.Version, Manifest: manifestJSON(t, pkg),
		Package: raw, PackageSHA256: pkg.SHA256, Signer: pkg.Signer, Status: status, Origin: origin,
		SelfTest: store.JSON(`{"ok":true}`),
	}
	if attest {
		env, err := json.Marshal(attestPackage(t, online, pkg, sortedSet(pkg.Manifest.SiteProvider.Hosts), sortedSet(pkg.Manifest.Automation.Permissions), issued))
		if err != nil {
			t.Fatal(err)
		}
		row.Attestation = env
	}
	if err := repo.CreatePluginVersion(context.Background(), row, store.JSPluginSourceMarket); err != nil {
		t.Fatal(err)
	}
}

func manifestJSON(t *testing.T, pkg jsplugin.Package) store.JSON {
	t.Helper()
	raw, err := json.Marshal(pkg.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func enabledVersion(t *testing.T, repo store.JSPluginRepository, pluginID string) string {
	t.Helper()
	rows, err := repo.ListVersions(context.Background(), pluginID)
	if err != nil {
		t.Fatal(err)
	}
	enabled := ""
	for _, row := range rows {
		if row.Status == store.JSPluginStatusEnabled {
			if enabled != "" {
				t.Fatalf("%s has two enabled versions", pluginID)
			}
			enabled = row.Version
		}
	}
	return enabled
}

func metaString(metadata any, key string) string {
	fields, _ := metadata.(map[string]any)
	value, _ := fields[key].(string)
	return value
}
