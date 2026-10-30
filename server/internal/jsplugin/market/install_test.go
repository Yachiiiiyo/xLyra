package market

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"xlyra/server/internal/config"
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/marketproto"
	"xlyra/server/internal/store"
)

func TestInstallBases(t *testing.T) {
	svc := &Service{registries: []string{"https://builtin.example/v1/"}}
	root := &marketproto.Root{Endpoints: marketproto.Endpoints{Registry: []string{"https://root.example/v1/"}}}
	state := store.JSPluginMarketState{SourceURL: "https://state.example/v1/"}
	manual := svc.installBases(config.PluginMarketConfig{SourceURL: " https://manual.example/v1/ "}, state, root)
	if strings.Join(manual, ",") != "https://manual.example/v1/" {
		t.Fatalf("manual bases = %#v", manual)
	}
	got := svc.installBases(config.PluginMarketConfig{}, state, root)
	want := "https://state.example/v1/,https://root.example/v1/,https://builtin.example/v1/"
	if strings.Join(got, ",") != want {
		t.Fatalf("bases = %#v", got)
	}
}

func TestMarketInstall(t *testing.T) {
	st := openMarketStore(t)
	repo := store.NewJSPluginRepository(st.DB())
	mgr := jsplugin.NewManager(st, nil)
	rootPriv, online := testKeys()
	devKey := fixedKey(0x44)
	otherKey := fixedKey(0x55)
	devFP := jsplugin.PublicKeyFingerprint(devKey.Public().(ed25519.PublicKey))
	issued, exp := freshWindow()
	okFixture := map[string][]byte{"fixtures/ok.json": []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"empty"}}`)}
	badFixture := map[string][]byte{"fixtures/ok.json": []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"nope"}}`)}
	hosts := []string{"api.example.com", "files.example.com"}

	probe220, pkg220 := packPlugin(t, devKey, probeManifest("acme.probe", "2.0.0"), probeSource("acme.probe"), okFixture, true)
	probe120, pkg120 := packPlugin(t, devKey, probeManifest("acme.probe", "1.2.0"), probeSource("acme.probe"), okFixture, true)
	probe100, pkg100 := packPlugin(t, devKey, probeManifest("acme.probe", "1.0.0"), probeSource("acme.probe"), okFixture, true)
	siteRaw, sitePkg := packPlugin(t, devKey, siteManifest("acme.site", "1.0.0", hosts), siteSource("acme.site"), nil, true)
	revokedRaw, revokedPkg := packPlugin(t, devKey, probeManifest("acme.revoked", "1.0.0"), probeSource("acme.revoked"), okFixture, true)
	deprecatedRaw, deprecatedPkg := packPlugin(t, devKey, probeManifest("acme.deprecated", "1.0.0"), probeSource("acme.deprecated"), okFixture, true)
	unsignedRaw, unsignedPkg := packPlugin(t, devKey, probeManifest("acme.unsigned", "1.0.0"), probeSource("acme.unsigned"), okFixture, false)
	hostRaw, hostPkg := packPlugin(t, devKey, probeManifest("acme.hosts", "1.0.0"), probeSource("acme.hosts"), okFixture, true)
	strangerRaw, strangerPkg := packPlugin(t, otherKey, probeManifest("acme.stranger", "1.0.0"), probeSource("acme.stranger"), okFixture, true)
	failRaw, failPkg := packPlugin(t, devKey, probeManifest("acme.fail", "1.0.0"), probeSource("acme.fail"), badFixture, true)
	manualRaw, manualPkg := packPlugin(t, devKey, probeManifest("acme.manual", "1.0.0"), probeSource("acme.manual"), okFixture, true)
	clashMarket, clashPkg := packPlugin(t, devKey, probeManifest("acme.clash", "1.0.0"), probeSource("acme.clash")+"// market\n", okFixture, true)
	clashLocal, _ := packPlugin(t, devKey, probeManifest("acme.clash", "1.0.0"), probeSource("acme.clash")+"// local\n", okFixture, true)
	lockedMarket, lockedPkg := packPlugin(t, devKey, probeManifest("acme.locked", "1.0.0"), probeSource("acme.locked"), okFixture, true)
	lockedLocal, _ := packPlugin(t, otherKey, probeManifest("acme.locked", "0.1.0"), probeSource("acme.locked"), okFixture, true)
	tamperRaw, tamperPkg := packPlugin(t, devKey, probeManifest("acme.tamper", "1.0.0"), probeSource("acme.tamper"), okFixture, true)
	laterRaw, laterPkg := packPlugin(t, devKey, probeManifest("acme.later", "1.0.0"), probeSource("acme.later"), okFixture, true)
	staleRaw, stalePkg := packPlugin(t, devKey, probeManifest("acme.stale", "1.0.0"), probeSource("acme.stale"), okFixture, true)
	junk := []byte("this is not a zip")

	none := []string{}
	entry := func(pkg jsplugin.Package, raw []byte, attHosts, attPerms []string, compat marketproto.Compat) marketproto.PluginVersion {
		t.Helper()
		if attHosts == nil {
			attHosts = sortedSet(pkg.Manifest.SiteProvider.Hosts)
		}
		if attPerms == nil {
			attPerms = sortedSet(pkg.Manifest.Automation.Permissions)
		}
		att := attestPackage(t, online, pkg, attHosts, attPerms, issued)
		return versionEntry(pkg, raw, att, compat, attHosts, attPerms, issued)
	}
	okCompat := compatCurrent()
	badCompat := marketproto.Compat{XLyra: ">=1.0.0", APIVersion: 99, HostAPI: jsplugin.HostAPIVersion}
	plugins := []marketproto.Plugin{
		pluginDoc("acme.probe", jsplugin.KindQuotaProbe, []string{devFP},
			entry(pkg220, probe220, none, none, badCompat),
			entry(pkg120, probe120, none, none, okCompat),
			entry(pkg100, probe100, none, none, okCompat),
		),
		pluginDoc("acme.site", jsplugin.KindSiteProvider, []string{devFP}, entry(sitePkg, siteRaw, nil, nil, okCompat)),
		pluginDoc("acme.revoked", jsplugin.KindQuotaProbe, []string{devFP}, entry(revokedPkg, revokedRaw, none, none, okCompat)),
		pluginDoc("acme.deprecated", jsplugin.KindQuotaProbe, []string{devFP}, entry(deprecatedPkg, deprecatedRaw, none, none, okCompat)),
		pluginDoc("acme.unsigned", jsplugin.KindQuotaProbe, []string{devFP}, entry(unsignedPkg, unsignedRaw, none, none, okCompat)),
		pluginDoc("acme.hosts", jsplugin.KindQuotaProbe, []string{devFP}, entry(hostPkg, hostRaw, []string{"other.example"}, none, okCompat)),
		pluginDoc("acme.stranger", jsplugin.KindQuotaProbe, []string{devFP}, entry(strangerPkg, strangerRaw, none, none, okCompat)),
		pluginDoc("acme.fail", jsplugin.KindQuotaProbe, []string{devFP}, entry(failPkg, failRaw, none, none, okCompat)),
		pluginDoc("acme.junk", jsplugin.KindQuotaProbe, []string{devFP}, junkEntry(t, online, "acme.junk", devFP, junk, issued)),
		pluginDoc("acme.manual", jsplugin.KindQuotaProbe, []string{devFP}, entry(manualPkg, manualRaw, none, none, okCompat)),
		pluginDoc("acme.clash", jsplugin.KindQuotaProbe, []string{devFP}, entry(clashPkg, clashMarket, none, none, okCompat)),
		pluginDoc("acme.locked", jsplugin.KindQuotaProbe, []string{devFP}, entry(lockedPkg, lockedMarket, none, none, okCompat)),
		pluginDoc("acme.tamper", jsplugin.KindQuotaProbe, []string{devFP}, entry(tamperPkg, tamperRaw, none, none, okCompat)),
		pluginDoc("acme.later", jsplugin.KindQuotaProbe, []string{devFP}, entry(laterPkg, laterRaw, none, none, okCompat)),
		pluginDoc("acme.stale", jsplugin.KindQuotaProbe, []string{devFP}, entry(stalePkg, staleRaw, none, none, badCompat)),
	}
	entries := []marketproto.RevocationEntry{
		marketRevocation("rev-sec", "acme.revoked", revokedPkg.Manifest.Version, revokedPkg.SHA256, "security", issued),
		marketRevocation("rev-old", "acme.deprecated", deprecatedPkg.Manifest.Version, deprecatedPkg.SHA256, "deprecated", issued),
	}

	cloud := newCloud(signCatalog(t, rootPriv, online, 1, 1, 1, issued, exp, plugins, entries))
	for _, raw := range [][]byte{probe220, probe120, probe100, siteRaw, revokedRaw, deprecatedRaw, unsignedRaw, hostRaw, strangerRaw, failRaw, junk, manualRaw, clashMarket, lockedMarket, tamperRaw, laterRaw, staleRaw} {
		cloud.put("packages/"+marketproto.FileSHA256(raw)+".xlp", raw)
	}
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	base := srv.URL + "/"
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusBadGateway)
	}))
	t.Cleanup(badSrv.Close)
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, []string{base}, config.PluginMarketConfig{Enabled: true, IndexSyncHours: 6, SourceURL: base, AutoUpdate: true})
	mgr.SetMarketTrust(svc)
	ctx := context.Background()
	if status, err := svc.SyncOnce(ctx, true); err != nil || status.Error != "" {
		t.Fatalf("sync = %+v, %v", status, err)
	}

	adminID := uuid.New()
	if err := st.DB().WithContext(ctx).Exec(`INSERT INTO admins (id, username, password_hash) VALUES (?, ?, ?)`, adminID, "market-admin", "x").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Upload(ctx, "", clashLocal); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Upload(ctx, "", lockedLocal); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Upload(ctx, "", manualRaw); err != nil {
		t.Fatal(err)
	}

	row, err := svc.Install(ctx, adminID.String(), "acme.probe", "")
	if err != nil {
		t.Fatal(err)
	}
	if row.Version != "1.2.0" || row.Origin != store.JSPluginOriginMarket || row.Status != store.JSPluginStatusVerified || !attestationPresent(row.Attestation) {
		t.Fatalf("default install = origin %q version %q status %q attestation %d", row.Origin, row.Version, row.Status, len(row.Attestation))
	}
	if !row.UploadedBy.Valid || row.UploadedBy.UUID != adminID {
		t.Fatalf("uploaded_by = %+v", row.UploadedBy)
	}
	if source := pluginSource(t, repo, "acme.probe"); source != store.JSPluginSourceMarket {
		t.Fatalf("source = %q", source)
	}
	trust, err := mgr.TrustStatusForVersion(ctx, row)
	if err != nil || trust != jsplugin.TrustMarketplace {
		t.Fatalf("trust = %q, %v", trust, err)
	}
	again, err := svc.Install(ctx, adminID.String(), "acme.probe", "")
	if err != nil || again.Version != "1.2.0" || versionCount(t, repo, "acme.probe") != 1 {
		t.Fatalf("idempotent = %+v count %d err %v", again.Version, versionCount(t, repo, "acme.probe"), err)
	}
	specified, err := svc.Install(ctx, adminID.String(), "acme.probe", "1.0.0")
	if err != nil || specified.Version != "1.0.0" || specified.Origin != store.JSPluginOriginMarket || versionCount(t, repo, "acme.probe") != 2 {
		t.Fatalf("specified = %q count %d err %v", specified.Version, versionCount(t, repo, "acme.probe"), err)
	}
	if err := mgr.Enable(ctx, "acme.probe", "1.2.0", jsplugin.EnableOptions{}); err != nil {
		t.Fatal(err)
	}
	enabled, err := repo.GetVersion(ctx, "acme.probe", "1.2.0")
	if err != nil || enabled.Status != store.JSPluginStatusEnabled {
		t.Fatalf("enabled status = %+v %v", enabled.Status, err)
	}
	if _, err := svc.Install(ctx, adminID.String(), "acme.probe", "1.2.0"); err != nil || versionCount(t, repo, "acme.probe") != 2 {
		t.Fatal(err)
	}
	enabled, err = repo.GetVersion(ctx, "acme.probe", "1.2.0")
	if err != nil || enabled.Status != store.JSPluginStatusEnabled {
		t.Fatalf("install changed enable status: %s", enabled.Status)
	}

	siteRow, err := svc.Install(ctx, "", "acme.site", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	siteTrust, err := mgr.TrustStatusForVersion(ctx, siteRow)
	if err != nil || siteTrust != jsplugin.TrustMarketplace {
		t.Fatalf("site trust = %q %v", siteTrust, err)
	}
	enableErr := mgr.Enable(ctx, "acme.site", "1.0.0", jsplugin.EnableOptions{})
	var untrusted *jsplugin.SiteProviderUntrustedError
	var hostsErr *jsplugin.HostsConfirmationRequiredError
	if errors.As(enableErr, &untrusted) || !errors.As(enableErr, &hostsErr) {
		t.Fatalf("site enable without hosts = %v", enableErr)
	}
	if err := mgr.Enable(ctx, "acme.site", "1.0.0", jsplugin.EnableOptions{ConfirmedHosts: hosts}); err != nil {
		t.Fatal(err)
	}

	deprecatedRow, err := svc.Install(ctx, "", "acme.deprecated", "")
	if err != nil || deprecatedRow.Origin != store.JSPluginOriginMarket {
		t.Fatalf("deprecated install = %+v %v", deprecatedRow.Origin, err)
	}

	mustInstallCode(t, installErr(svc, "acme.missing", ""), InstallCodeNotFound)
	mustInstallCode(t, installErr(svc, "acme.probe", "9.9.9"), InstallCodeVersionNotFound)
	if got := mustInstallCode(t, installErr(svc, "acme.probe", "2.0.0"), InstallCodeIncompatible); got.Reason != reasonAPIVersion {
		t.Fatalf("reason = %q", got.Reason)
	}
	if got := mustInstallCode(t, installErr(svc, "acme.stale", ""), InstallCodeIncompatible); got.Reason != reasonAPIVersion {
		t.Fatalf("latest incompatible reason = %q", got.Reason)
	}
	noVersion(t, repo, "acme.probe", "2.0.0")
	mustInstallCode(t, installErr(svc, "acme.revoked", ""), InstallCodeRevoked)
	noVersion(t, repo, "acme.revoked", "1.0.0")
	if got := mustInstallCode(t, installErr(svc, "acme.hosts", ""), InstallCodeVerifyFailed); got.Proto != marketproto.CodeManifestMismatch {
		t.Fatalf("hosts proto = %q", got.Proto)
	}
	noVersion(t, repo, "acme.hosts", "1.0.0")
	mustInstallCode(t, installErr(svc, "acme.unsigned", ""), InstallCodeUnsigned)
	noVersion(t, repo, "acme.unsigned", "1.0.0")
	mustInstallCode(t, installErr(svc, "acme.stranger", ""), InstallCodeDeveloperMismatch)
	noVersion(t, repo, "acme.stranger", "1.0.0")
	mustInstallCode(t, installErr(svc, "acme.junk", ""), InstallCodePackageInvalid)
	noVersion(t, repo, "acme.junk", "1.0.0")
	mustInstallCode(t, installErr(svc, "acme.fail", ""), InstallCodeSelfTestFailed)
	noVersion(t, repo, "acme.fail", "1.0.0")
	lockedRow, err := svc.Install(ctx, "", "acme.locked", "")
	if err != nil || lockedRow.Version != "1.0.0" || lockedRow.Signer != lockedPkg.Signer {
		t.Fatalf("historical signer blocked install: version=%s err=%v", lockedRow.Version, err)
	}
	if versionCount(t, repo, "acme.locked") != 2 {
		t.Fatal("historical version was dropped")
	}

	cloud.put("packages/"+marketproto.FileSHA256(tamperRaw)+".xlp", []byte("tampered-bytes"))
	mustInstallCode(t, installErr(svc, "acme.tamper", ""), InstallCodeDownloadFailed)
	noVersion(t, repo, "acme.tamper", "1.0.0")
	cloud.put("packages/"+marketproto.FileSHA256(tamperRaw)+".xlp", tamperRaw)

	manualRow, err := svc.Install(ctx, "", "acme.manual", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if manualRow.Origin != store.JSPluginOriginMarket || !attestationPresent(manualRow.Attestation) || manualRow.PackageSHA256 != manualPkg.SHA256 {
		t.Fatalf("backfill origin=%q attestation=%d sha=%s", manualRow.Origin, len(manualRow.Attestation), manualRow.PackageSHA256)
	}
	if source := pluginSource(t, repo, "acme.manual"); source != store.JSPluginSourceUploaded {
		t.Fatalf("manual source = %q", source)
	}
	if versionCount(t, repo, "acme.manual") != 1 {
		t.Fatal("backfill inserted a row")
	}
	manualTrust, err := mgr.TrustStatusForVersion(ctx, manualRow)
	if err != nil || manualTrust != jsplugin.TrustMarketplace {
		t.Fatalf("backfill trust = %q %v", manualTrust, err)
	}

	clashErr := installErr(svc, "acme.clash", "1.0.0")
	if !errors.Is(clashErr, jsplugin.ErrVersionContentMismatch) {
		t.Fatalf("conflict = %v", clashErr)
	}
	mustInstallCode(t, clashErr, InstallCodeVersionConflict)
	if versionCount(t, repo, "acme.clash") != 1 {
		t.Fatal("conflict inserted a row")
	}

	svc.SetConfig(config.PluginMarketConfig{Enabled: false, IndexSyncHours: 6, SourceURL: base, AutoUpdate: true})
	mustInstallCode(t, installErr(svc, "acme.later", ""), InstallCodeUnavailable)
	svc.SetConfig(config.PluginMarketConfig{Enabled: true, IndexSyncHours: 6, SourceURL: badSrv.URL + "/", AutoUpdate: true})
	mustInstallCode(t, installErr(svc, "acme.later", ""), InstallCodeDownloadFailed)
	noVersion(t, repo, "acme.later", "1.0.0")
	svc.SetConfig(config.PluginMarketConfig{Enabled: true, IndexSyncHours: 6, SourceURL: "", AutoUpdate: true})
	state, err := repo.GetMarketState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.SourceURL = badSrv.URL + "/"
	if err := repo.SaveMarketState(ctx, state); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Install(ctx, "", "acme.later", ""); err != nil {
		t.Fatal(err)
	}

	past := issued.Add(-8 * 24 * time.Hour)
	expiredDocs := signCatalog(t, rootPriv, online, 1, 1, 1, past, past.Add(24*time.Hour), plugins, entries)
	now := time.Now().UTC()
	if err := repo.SaveMarketState(ctx, store.JSPluginMarketState{
		RootEnvelope: expiredDocs.root, IndexEnvelope: expiredDocs.index, RevocationsEnvelope: expiredDocs.revocations,
		IndexSyncedAt: &now, RevocationsSyncedAt: &now, LastAttemptAt: &now, SourceURL: base,
	}); err != nil {
		t.Fatal(err)
	}
	mustInstallCode(t, installErr(svc, "acme.probe", "1.0.0"), InstallCodeRevocationsExpired)
}

func TestInstallSucceedsWhenHistoricalVersionUsesOldFingerprint(t *testing.T) {
	st := openMarketStore(t)
	repo := store.NewJSPluginRepository(st.DB())
	mgr := jsplugin.NewManager(st, nil)
	rootPriv, online := testKeys()
	oldKey := fixedKey(0x41)
	newKey := fixedKey(0x42)
	newFP := jsplugin.PublicKeyFingerprint(newKey.Public().(ed25519.PublicKey))
	issued, exp := freshWindow()
	okFixture := map[string][]byte{"fixtures/ok.json": []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"empty"}}`)}
	oldRaw, oldPkg := packPlugin(t, oldKey, probeManifest("acme.rotate", "1.0.0"), probeSource("acme.rotate"), okFixture, true)
	newRaw, newPkg := packPlugin(t, newKey, probeManifest("acme.rotate", "1.1.0"), probeSource("acme.rotate"), okFixture, true)
	if oldPkg.Signer == newFP || newPkg.Signer != newFP {
		t.Fatalf("signers old=%s new=%s index=%s", oldPkg.Signer, newPkg.Signer, newFP)
	}
	none := []string{}
	okCompat := compatCurrent()
	entry := func(pkg jsplugin.Package, raw []byte) marketproto.PluginVersion {
		t.Helper()
		return versionEntry(pkg, raw, attestPackage(t, online, pkg, none, none, issued), okCompat, none, none, issued)
	}
	plugins := []marketproto.Plugin{
		pluginDoc("acme.rotate", jsplugin.KindQuotaProbe, []string{newFP}, entry(newPkg, newRaw), entry(oldPkg, oldRaw)),
	}
	cloud := newCloud(signCatalog(t, rootPriv, online, 1, 1, 1, issued, exp, plugins, nil))
	cloud.put("packages/"+marketproto.FileSHA256(newRaw)+".xlp", newRaw)
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	base := srv.URL + "/"
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, []string{base}, config.PluginMarketConfig{Enabled: true, IndexSyncHours: 6, SourceURL: base, AutoUpdate: true})
	ctx := context.Background()
	if status, err := svc.SyncOnce(ctx, true); err != nil || status.Error != "" {
		t.Fatalf("sync = %+v, %v", status, err)
	}
	if _, err := mgr.Upload(ctx, "", oldRaw); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetVersion(ctx, "acme.rotate", "1.0.0")
	if err != nil || stored.Signer != oldPkg.Signer {
		t.Fatalf("historical signer = %s err=%v", stored.Signer, err)
	}
	row, err := svc.Install(ctx, "", "acme.rotate", "1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if row.Version != "1.1.0" || row.Signer != newFP || row.Origin != store.JSPluginOriginMarket {
		t.Fatalf("installed version=%s signer=%s origin=%s", row.Version, row.Signer, row.Origin)
	}
	if versionCount(t, repo, "acme.rotate") != 2 {
		t.Fatal("historical version was dropped")
	}
}

func TestMarketInstallIconLegibility(t *testing.T) {
	st := openMarketStore(t)
	repo := store.NewJSPluginRepository(st.DB())
	rootPriv, online := testKeys()
	devKey := fixedKey(0x44)
	devFP := jsplugin.PublicKeyFingerprint(devKey.Public().(ed25519.PublicKey))
	issued, exp := freshWindow()
	okBody := []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"empty"}}`)

	pack := func(id string, glyph color.NRGBA) ([]byte, jsplugin.Package) {
		t.Helper()
		manifest := probeManifest(id, "1.0.0")
		manifest.Icon = "assets/icon.png"
		return packPlugin(t, devKey, manifest, probeSource(id), map[string][]byte{
			"fixtures/ok.json": okBody,
			"assets/icon.png":  transparentGlyphPNG(t, glyph),
		}, true)
	}
	darkRaw, darkPkg := pack("acme.darkicon", color.NRGBA{0, 0, 0, 255})
	lightRaw, lightPkg := pack("acme.lighticon", color.NRGBA{255, 255, 255, 255})

	none := []string{}
	okCompat := compatCurrent()
	entry := func(pkg jsplugin.Package, raw []byte) marketproto.PluginVersion {
		t.Helper()
		att := attestPackage(t, online, pkg, none, none, issued)
		return versionEntry(pkg, raw, att, okCompat, none, none, issued)
	}
	plugins := []marketproto.Plugin{
		pluginDoc("acme.darkicon", jsplugin.KindQuotaProbe, []string{devFP}, entry(darkPkg, darkRaw)),
		pluginDoc("acme.lighticon", jsplugin.KindQuotaProbe, []string{devFP}, entry(lightPkg, lightRaw)),
	}
	cloud := newCloud(signCatalog(t, rootPriv, online, 1, 1, 1, issued, exp, plugins, nil))
	for _, raw := range [][]byte{darkRaw, lightRaw} {
		cloud.put("packages/"+marketproto.FileSHA256(raw)+".xlp", raw)
	}
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	base := srv.URL + "/"
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, []string{base}, config.PluginMarketConfig{Enabled: true, IndexSyncHours: 6, SourceURL: base, AutoUpdate: true})
	ctx := context.Background()
	if status, err := svc.SyncOnce(ctx, true); err != nil || status.Error != "" {
		t.Fatalf("sync = %+v, %v", status, err)
	}

	got := mustInstallCode(t, installErr(svc, "acme.darkicon", "1.0.0"), InstallCodePackageInvalid)
	if !strings.Contains(got.Error(), "深色") {
		t.Fatalf("icon err = %v", got)
	}
	noVersion(t, repo, "acme.darkicon", "1.0.0")

	row, err := svc.Install(ctx, "", "acme.lighticon", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if row.Origin != store.JSPluginOriginMarket {
		t.Fatalf("origin = %q", row.Origin)
	}
}

func transparentGlyphPNG(t *testing.T, glyph color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 16; y < 48; y++ {
		for x := 16; x < 48; x++ {
			img.SetNRGBA(x, y, glyph)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func installErr(svc *Service, id, version string) error {
	_, err := svc.Install(context.Background(), "", id, version)
	return err
}

func mustInstallCode(t *testing.T, err error, code string) *InstallError {
	t.Helper()
	var installErr *InstallError
	if !errors.As(err, &installErr) || installErr.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
	return installErr
}

func noVersion(t *testing.T, repo store.JSPluginRepository, id, version string) {
	t.Helper()
	_, err := repo.GetVersion(context.Background(), id, version)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("version %s@%s = %v", id, version, err)
	}
}

func TestVerifyVersionAttestationTrustCache(t *testing.T) {
	st := openMarketStore(t)
	repo := store.NewJSPluginRepository(st.DB())
	rootPriv, online := testKeys()
	devKey := fixedKey(0x44)
	issued, exp := freshWindow()
	raw, pkg := packPlugin(t, devKey, probeManifest("acme.trust", "1.0.0"), probeSource("acme.trust"), map[string][]byte{
		"fixtures/ok.json": []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"empty"}}`),
	}, true)
	att := attestPackage(t, online, pkg, []string{}, []string{}, issued)
	docs := signCatalog(t, rootPriv, online, 1, 1, 1, issued, exp, nil, nil)
	cloud := newCloud(docs)
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	base := srv.URL + "/"
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, nil, config.PluginMarketConfig{Enabled: true, IndexSyncHours: 6, SourceURL: base})
	ctx := context.Background()
	if _, err := svc.SyncOnce(ctx, true); err != nil {
		t.Fatal(err)
	}

	full, err := repo.GetMarketState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.IndexEnvelope) == 0 {
		t.Fatal("synced state has no index")
	}
	trust, err := repo.GetMarketTrustState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if trust.RootSeq != full.RootSeq || !bytes.Equal(trust.RootEnvelope, full.RootEnvelope) || !bytes.Equal(trust.RevocationsEnvelope, full.RevocationsEnvelope) {
		t.Fatal("trust state dropped root or revocations")
	}
	if len(trust.IndexEnvelope) != 0 || trust.IndexSeq != 0 {
		t.Fatal("trust state included the index")
	}

	attRaw, err := json.Marshal(att)
	if err != nil {
		t.Fatal(err)
	}
	manifestJSON, err := json.Marshal(pkg.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreatePluginVersion(ctx, store.JSPluginVersion{
		PluginID: pkg.Manifest.ID, Version: pkg.Manifest.Version,
		Manifest: store.JSON(manifestJSON), Package: raw, PackageSHA256: pkg.SHA256, Signer: pkg.Signer,
		Status: store.JSPluginStatusVerified, SelfTest: store.JSON(`{"ok":true}`),
		Origin: store.JSPluginOriginMarket, Attestation: store.JSON(attRaw),
	}, store.JSPluginSourceMarket); err != nil {
		t.Fatal(err)
	}
	row, err := repo.GetVersion(ctx, pkg.Manifest.ID, pkg.Manifest.Version)
	if err != nil {
		t.Fatal(err)
	}

	db := st.DB()
	reads := 0
	querySQL := ""
	if err := db.Callback().Query().After("gorm:query").Register("market:count-trust", func(tx *gorm.DB) {
		sql := tx.Statement.SQL.String()
		if strings.Contains(sql, "js_plugin_market_state") {
			reads++
			querySQL = sql
		}
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if !svc.VerifyVersionAttestation(ctx, row) {
			t.Fatalf("call %d: want marketplace", i)
		}
	}
	db.Callback().Query().Remove("market:count-trust")
	if reads != 1 {
		t.Fatalf("trust reads = %d, sql = %s", reads, querySQL)
	}
	if strings.Contains(querySQL, "index_envelope") || strings.Contains(querySQL, "*") {
		t.Fatalf("trust query read the index: %s", querySQL)
	}

	revoked := signCatalog(t, rootPriv, online, 1, 1, 2, issued, exp, nil, []marketproto.RevocationEntry{
		marketRevocation("rev-sec", pkg.Manifest.ID, pkg.Manifest.Version, pkg.SHA256, "security", issued),
	})
	revokedState, err := repo.GetMarketState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	revokedState.RevocationsSeq = 2
	revokedState.RevocationsEnvelope = revoked.revocations
	revokedState.RevocationsPayloadSHA256, err = payloadSHA256(revoked.revocations)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveMarketState(ctx, revokedState); err != nil {
		t.Fatal(err)
	}
	if !svc.VerifyVersionAttestation(ctx, row) {
		t.Fatal("cached trust changed inside the ttl")
	}
	svc.clearTrustView()
	if svc.VerifyVersionAttestation(ctx, row) {
		t.Fatal("cleared cache still ignored the new revocation")
	}

	good, err := repo.GetMarketState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	good.RootEnvelope = []byte("not-an-envelope")
	if err := repo.SaveMarketState(ctx, good); err != nil {
		t.Fatal(err)
	}
	svc.clearTrustView()
	if svc.VerifyVersionAttestation(ctx, row) {
		t.Fatal("bad root was accepted")
	}
	good.RootSeq = full.RootSeq
	good.RootPayloadSHA256 = full.RootPayloadSHA256
	good.RootEnvelope = full.RootEnvelope
	good.RevocationsSeq = full.RevocationsSeq
	good.RevocationsPayloadSHA256 = full.RevocationsPayloadSHA256
	good.RevocationsEnvelope = full.RevocationsEnvelope
	if err := repo.SaveMarketState(ctx, good); err != nil {
		t.Fatal(err)
	}
	if !svc.VerifyVersionAttestation(ctx, row) {
		t.Fatal("root verification failure was cached")
	}

	later := signCatalog(t, rootPriv, online, 3, 3, 3, issued, exp, nil, []marketproto.RevocationEntry{
		marketRevocation("rev-sec", pkg.Manifest.ID, pkg.Manifest.Version, pkg.SHA256, "security", issued),
	})
	cloud.use(later)
	if !svc.VerifyVersionAttestation(ctx, row) {
		t.Fatal("cache cleared before a successful sync")
	}
	if _, err := svc.SyncOnce(ctx, true); err != nil {
		t.Fatal(err)
	}
	if svc.VerifyVersionAttestation(ctx, row) {
		t.Fatal("successful sync left the trust cache in place")
	}
}

func TestStoreInstalledDuplicateKeySameHash(t *testing.T) {
	repo := store.NewJSPluginRepository(openMarketStore(t).DB())
	svc := newTestService(t, repo, nil, nil, config.PluginMarketConfig{Enabled: true})
	devKey := fixedKey(0x46)
	online := fixedKey(0x33)
	issued, _ := freshWindow()
	ctx := context.Background()
	fixture := map[string][]byte{
		"fixtures/ok.json": []byte(`{"name":"ok","ctx":{"siteType":"newapi","baseURL":"https://example.com","credentialType":"api_key"},"responses":[],"expect":{"error":"empty"}}`),
	}

	raw, pkg := packPlugin(t, devKey, probeManifest("acme.dup", "1.0.0"), probeSource("acme.dup"), fixture, true)
	manifestJSON, err := json.Marshal(pkg.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreatePluginVersion(ctx, store.JSPluginVersion{
		PluginID: pkg.Manifest.ID, Version: pkg.Manifest.Version,
		Manifest: store.JSON(manifestJSON), Package: raw, PackageSHA256: pkg.SHA256, Signer: pkg.Signer,
		Status: store.JSPluginStatusVerified, SelfTest: store.JSON(`{"ok":true}`),
		Origin: store.JSPluginOriginManual,
	}, store.JSPluginSourceUploaded); err != nil {
		t.Fatal(err)
	}
	att := attestPackage(t, online, pkg, []string{}, []string{}, issued)
	got, err := svc.adoptDuplicate(ctx, pkg, att)
	if err != nil {
		t.Fatal(err)
	}
	if got.Origin != store.JSPluginOriginMarket || !attestationPresent(got.Attestation) || got.PackageSHA256 != pkg.SHA256 {
		t.Fatalf("adopted row origin=%s sha=%s attestation=%s", got.Origin, got.PackageSHA256, got.Attestation)
	}
	if versionCount(t, repo, pkg.Manifest.ID) != 1 {
		t.Fatal("duplicate key created another version")
	}

	raw2, pkg2 := packPlugin(t, devKey, probeManifest("acme.dup2", "1.0.0"), probeSource("acme.dup2"), fixture, true)
	manifest2, err := json.Marshal(pkg2.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreatePluginVersion(ctx, store.JSPluginVersion{
		PluginID: pkg2.Manifest.ID, Version: pkg2.Manifest.Version,
		Manifest: store.JSON(manifest2), Package: raw2, PackageSHA256: strings.Repeat("ab", 32), Signer: pkg2.Signer,
		Status: store.JSPluginStatusVerified, SelfTest: store.JSON(`{"ok":true}`),
		Origin: store.JSPluginOriginManual,
	}, store.JSPluginSourceUploaded); err != nil {
		t.Fatal(err)
	}
	_, err = svc.adoptDuplicate(ctx, pkg2, attestPackage(t, online, pkg2, []string{}, []string{}, issued))
	var installErr *InstallError
	if !errors.As(err, &installErr) || installErr.Code != InstallCodeVersionConflict {
		t.Fatalf("different hash: %v", err)
	}
	kept, err := repo.GetVersion(ctx, pkg2.Manifest.ID, pkg2.Manifest.Version)
	if err != nil {
		t.Fatal(err)
	}
	if kept.Origin != store.JSPluginOriginManual || attestationPresent(kept.Attestation) || kept.PackageSHA256 == pkg2.SHA256 {
		t.Fatalf("conflict rewrote the row origin=%s sha=%s", kept.Origin, kept.PackageSHA256)
	}
}

func versionCount(t *testing.T, repo store.JSPluginRepository, id string) int {
	t.Helper()
	rows, err := repo.ListVersions(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func pluginSource(t *testing.T, repo store.JSPluginRepository, id string) string {
	t.Helper()
	rows, err := repo.ListPlugins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == id {
			return row.Source
		}
	}
	t.Fatalf("plugin %s not found", id)
	return ""
}

func attestationPresent(raw []byte) bool {
	raw = bytesTrim(raw)
	return len(raw) > 0 && string(raw) != "null"
}

func bytesTrim(raw []byte) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}

func compatCurrent() marketproto.Compat {
	return marketproto.Compat{XLyra: ">=1.0.0", APIVersion: jsplugin.HookAPIVersion, HostAPI: jsplugin.HostAPIVersion}
}

func packPlugin(t *testing.T, dev ed25519.PrivateKey, manifest jsplugin.Manifest, source string, fixtures map[string][]byte, sign bool) ([]byte, jsplugin.Package) {
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

func probeManifest(id, version string) jsplugin.Manifest {
	return jsplugin.Manifest{
		ID: id, Name: "Demo", Version: version, APIVersion: jsplugin.HookAPIVersion, HostAPI: jsplugin.HostAPIVersion,
		XLyra: ">=1.0.0", Kind: jsplugin.KindQuotaProbe, QuotaProbe: jsplugin.QuotaProbeSection{BaseURLMode: "as_is"},
	}
}

func probeSource(id string) string {
	return "export const meta = { apiVersion: 1, id: \"" + id + "\", kind: \"quota_probe\" };\nexport function probe() { return { error: \"empty\" }; }\n"
}

func siteManifest(id, version string, hosts []string) jsplugin.Manifest {
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

func siteSource(id string) string {
	return "export const meta = { apiVersion: 1, id: \"" + id + "\", kind: \"site_provider\" };\n"
}

func attestPackage(t *testing.T, online ed25519.PrivateKey, pkg jsplugin.Package, hosts, perms []string, issued time.Time) marketproto.Envelope {
	t.Helper()
	signer := pkg.Signer
	if signer == "" {
		signer = "SHA256:dW5zaWduZWQ="
	}
	return attestPayload(t, online, marketproto.Attestation{
		V: 1, PluginID: pkg.Manifest.ID, Version: pkg.Manifest.Version, PackageDigest: pkg.SHA256,
		DeveloperFingerprint: signer, Kind: pkg.Manifest.Kind, Hosts: hosts, Permissions: perms,
		IssuedAt: issued.UTC().Format(time.RFC3339),
	})
}

func attestPayload(t *testing.T, online ed25519.PrivateKey, payload marketproto.Attestation) marketproto.Envelope {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return marketproto.Sign(marketproto.PayloadTypeAttestation, raw, online)
}

func versionEntry(pkg jsplugin.Package, raw []byte, att marketproto.Envelope, compat marketproto.Compat, hosts, perms []string, published time.Time) marketproto.PluginVersion {
	return marketproto.PluginVersion{
		Version: pkg.Manifest.Version, PublishedAt: published.UTC().Format(time.RFC3339),
		FileSHA256: marketproto.FileSHA256(raw), PackageDigest: pkg.SHA256, Size: int64(len(raw)),
		Compat: compat, Hosts: hosts, Permissions: perms, Changelog: "notes", Attestation: att,
	}
}

func junkEntry(t *testing.T, online ed25519.PrivateKey, id, fingerprint string, raw []byte, issued time.Time) marketproto.PluginVersion {
	t.Helper()
	digest := strings.Repeat("ab", 32)
	att := attestPayload(t, online, marketproto.Attestation{
		V: 1, PluginID: id, Version: "1.0.0", PackageDigest: digest, DeveloperFingerprint: fingerprint,
		Kind: jsplugin.KindQuotaProbe, Hosts: []string{}, Permissions: []string{}, IssuedAt: issued.UTC().Format(time.RFC3339),
	})
	return marketproto.PluginVersion{
		Version: "1.0.0", PublishedAt: issued.UTC().Format(time.RFC3339),
		FileSHA256: marketproto.FileSHA256(raw), PackageDigest: digest, Size: int64(len(raw)),
		Compat: compatCurrent(), Hosts: []string{}, Permissions: []string{}, Changelog: "junk", Attestation: att,
	}
}

func pluginDoc(id, kind string, fingerprints []string, versions ...marketproto.PluginVersion) marketproto.Plugin {
	copied := append([]marketproto.PluginVersion(nil), versions...)
	sortVersionsNewestFirst(copied)
	ns, _, _ := strings.Cut(id, ".")
	return marketproto.Plugin{
		ID: id, Name: "Demo", Summary: "summary", Kind: kind,
		Developer: marketproto.Developer{Namespace: ns, DisplayName: "Acme", Fingerprints: fingerprints},
		Versions:  copied,
	}
}

func marketRevocation(id, pluginID, version, digest, level string, at time.Time) marketproto.RevocationEntry {
	return marketproto.RevocationEntry{
		ID: id, Target: marketproto.RevocationTarget{Type: "version", PluginID: pluginID, Version: version, PackageDigest: digest},
		Level: level, Reason: "listed", At: at.UTC().Format(time.RFC3339),
	}
}

func signCatalog(t *testing.T, rootPriv, online ed25519.PrivateKey, rootSeq, indexSeq, revSeq int64, generated, expires time.Time, plugins []marketproto.Plugin, entries []marketproto.RevocationEntry) signedDocs {
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
	idx := marketproto.Index{
		V: 1, Seq: indexSeq, GeneratedAt: generated.UTC().Format(time.RFC3339), ExpiresAt: expires.UTC().Format(time.RFC3339), Plugins: plugins,
	}
	rev := marketproto.Revocations{
		V: 1, Seq: revSeq, GeneratedAt: generated.UTC().Format(time.RFC3339), ExpiresAt: expires.UTC().Format(time.RFC3339), Entries: entries,
	}
	return signedDocs{
		root:        signEnv(t, marketproto.PayloadTypeRoot, root, rootPriv),
		index:       signEnv(t, marketproto.PayloadTypeIndex, idx, online),
		revocations: signEnv(t, marketproto.PayloadTypeRevocations, rev, online),
	}
}
