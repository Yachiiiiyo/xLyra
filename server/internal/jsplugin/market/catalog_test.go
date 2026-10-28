package market

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"crypto/ed25519"

	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/marketproto"
	"xlyra/server/internal/store"
)

func TestCatalogCompatLocalAndRevocation(t *testing.T) {
	synced := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	ok := marketproto.Compat{XLyra: ">=1.0.0", APIVersion: jsplugin.HookAPIVersion, HostAPI: jsplugin.HostAPIVersion}
	svc := catalogService(t, CatalogFixture{
		Enabled:            true,
		RootKeysConfigured: true,
		SourceURL:          "https://market.example/v1/",
		IndexSyncedAt:      &synced,
		LastError:          "previous",
		XLyraVersion:       "1.15.1",
		Snapshot: Snapshot{
			IndexExpired:       true,
			RevocationsExpired: true,
			Index: &marketproto.Index{Plugins: []marketproto.Plugin{
				{
					ID: "acme.update", Name: "Update", Summary: "has a newer build", Kind: "quota_probe",
					Developer: marketproto.Developer{Namespace: "acme", DisplayName: "Acme"},
					Homepage:  "https://acme.example",
					Versions: []marketproto.PluginVersion{
						ver("1.0.0", ok, "digest-old", []string{"api.example.com"}, []string{"notify"}),
						ver("1.2.0", ok, "digest-new", nil, nil),
					},
				},
				{
					ID: "acme.api", Name: "API", Summary: "bad api", Kind: "protocol",
					Versions: []marketproto.PluginVersion{
						ver("1.0.0", marketproto.Compat{APIVersion: jsplugin.HookAPIVersion + 1, HostAPI: jsplugin.HostAPIVersion}, "d", nil, nil),
					},
				},
				{
					ID: "acme.host", Name: "Host", Summary: "bad host", Kind: "automation",
					Versions: []marketproto.PluginVersion{
						ver("1.0.0", marketproto.Compat{APIVersion: jsplugin.HookAPIVersion, HostAPI: jsplugin.HostAPIVersion + 1}, "d", nil, nil),
						ver("0.9.0", marketproto.Compat{APIVersion: jsplugin.HookAPIVersion, HostAPI: 0}, "d0", nil, nil),
					},
				},
				{
					ID: "acme.xlyra", Name: "Range", Summary: "too new", Kind: "quota_probe",
					Versions: []marketproto.PluginVersion{
						ver("2.0.0", marketproto.Compat{XLyra: ">=9.0.0", APIVersion: jsplugin.HookAPIVersion, HostAPI: jsplugin.HostAPIVersion}, "d2", nil, nil),
						ver("1.0.0", ok, "d1", nil, nil),
					},
				},
				{
					ID: "acme.installed", Name: "Installed", Summary: "not enabled", Kind: "quota_probe",
					Versions: []marketproto.PluginVersion{ver("1.4.0", ok, "inst", nil, nil), ver("1.0.0", ok, "inst-old", nil, nil)},
				},
				{
					ID: "acme.current", Name: "Current", Summary: "same version", Kind: "quota_probe",
					Versions: []marketproto.PluginVersion{ver("1.2.0", ok, "cur", nil, nil)},
				},
				{
					ID: "acme.newer", Name: "Newer local", Summary: "local is ahead", Kind: "quota_probe",
					Versions: []marketproto.PluginVersion{ver("1.0.0", ok, "mkt", nil, nil)},
				},
				{
					ID: "acme.revoked", Name: "Revoked", Summary: "local digest revoked", Kind: "quota_probe",
					Versions: []marketproto.PluginVersion{
						ver("1.1.0", ok, "market-digest", nil, nil),
						ver("1.0.0", ok, "local-digest", nil, nil),
					},
				},
				{
					ID: "acme.plugin", Name: "Plugin revoke", Summary: "whole plugin", Kind: "quota_probe",
					Versions: []marketproto.PluginVersion{ver("1.0.0", ok, "plugin-digest", nil, nil)},
				},
				{
					ID: "acme.key", Name: "Key", Summary: "developer key", Kind: "quota_probe",
					Versions: []marketproto.PluginVersion{ver("1.0.0", ok, "key-digest", nil, nil)},
				},
			}},
			Revocations: &marketproto.Revocations{Entries: []marketproto.RevocationEntry{
				{ID: "r-local", Level: "security", Reason: "bad package", Target: marketproto.RevocationTarget{Type: "version", PackageDigest: "local-digest"}},
				{ID: "r-plugin", Level: "deprecated", Reason: "replaced", Target: marketproto.RevocationTarget{Type: "plugin", PluginID: "acme.plugin"}},
				{ID: "r-key", Level: "security", Reason: "leaked key", Target: marketproto.RevocationTarget{Type: "developerKey", Fingerprint: "fp-manual"}},
			}},
		},
		Locals: []store.JSPluginVersion{
			{PluginID: "acme.update", Version: "1.0.0", Status: store.JSPluginStatusEnabled, PackageSHA256: "digest-old"},
			{PluginID: "acme.installed", Version: "1.0.0", Status: store.JSPluginStatusVerified, PackageSHA256: "inst-old"},
			{PluginID: "acme.installed", Version: "1.4.0", Status: store.JSPluginStatusDisabled, PackageSHA256: "inst"},
			{PluginID: "acme.current", Version: "1.2.0", Status: store.JSPluginStatusEnabled, PackageSHA256: "cur"},
			{PluginID: "acme.newer", Version: "3.0.0", Status: store.JSPluginStatusEnabled, PackageSHA256: "ahead"},
			{PluginID: "acme.revoked", Version: "1.0.0", Status: store.JSPluginStatusEnabled, PackageSHA256: "local-digest", Origin: store.JSPluginOriginManual},
			{PluginID: "acme.plugin", Version: "1.0.0", Status: store.JSPluginStatusEnabled, PackageSHA256: "plugin-digest", Origin: store.JSPluginOriginMarket},
			{PluginID: "acme.key", Version: "9.0.0", Status: store.JSPluginStatusVerified, PackageSHA256: "manual-digest", Signer: "fp-manual"},
		},
	})

	cat, err := svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cat.Plugins == nil {
		t.Fatal("plugins is nil")
	}
	if !cat.Snapshot.IndexExpired || !cat.Snapshot.RevocationsExpired || cat.Snapshot.LastError != "previous" || !cat.Snapshot.Enabled || !cat.Snapshot.RootKeysConfigured {
		t.Fatalf("snapshot = %+v", cat.Snapshot)
	}
	if cat.Snapshot.IndexSyncedAt == nil || !cat.Snapshot.IndexSyncedAt.Equal(synced) || cat.Snapshot.SourceURL != "https://market.example/v1/" {
		t.Fatalf("snapshot times = %+v", cat.Snapshot)
	}

	byID := map[string]PluginView{}
	for _, item := range cat.Plugins {
		if item.Versions != nil {
			t.Fatalf("%s list included versions", item.ID)
		}
		byID[item.ID] = item
	}

	update := byID["acme.update"]
	if !update.HasCompatibleVersion || update.LatestCompatibleVersion != "1.2.0" || update.IncompatibleReason != "" {
		t.Fatalf("update compat = %+v", update)
	}
	if update.LocalStatus != localEnabled || update.LocalVersion != "1.0.0" || !update.UpdateAvailable || update.HasRevokedVersion {
		t.Fatalf("update local = %+v", update)
	}
	if update.Developer.Namespace != "acme" || update.Developer.DisplayName != "Acme" || update.Homepage != "https://acme.example" {
		t.Fatalf("update meta = %+v", update)
	}

	if byID["acme.api"].HasCompatibleVersion || byID["acme.api"].IncompatibleReason != reasonAPIVersion {
		t.Fatalf("api = %+v", byID["acme.api"])
	}
	host := byID["acme.host"]
	if host.HasCompatibleVersion || host.IncompatibleReason != reasonHostAPI || host.LatestCompatibleVersion != "" {
		t.Fatalf("host = %+v", host)
	}
	rng := byID["acme.xlyra"]
	if !rng.HasCompatibleVersion || rng.LatestCompatibleVersion != "1.0.0" || rng.IncompatibleReason != "" || rng.UpdateAvailable {
		t.Fatalf("xlyra mix = %+v", rng)
	}
	installed := byID["acme.installed"]
	if installed.LocalStatus != localInstalled || installed.LocalVersion != "1.4.0" || installed.UpdateAvailable {
		t.Fatalf("installed = %+v", installed)
	}
	if byID["acme.current"].UpdateAvailable || byID["acme.current"].LocalStatus != localEnabled {
		t.Fatalf("current = %+v", byID["acme.current"])
	}
	if byID["acme.newer"].UpdateAvailable || byID["acme.newer"].LocalVersion != "3.0.0" {
		t.Fatalf("newer = %+v", byID["acme.newer"])
	}
	if !byID["acme.revoked"].HasRevokedVersion || byID["acme.revoked"].LocalStatus != localEnabled {
		t.Fatalf("revoked flag = %+v", byID["acme.revoked"])
	}
	if !byID["acme.plugin"].HasRevokedVersion {
		t.Fatalf("plugin revoke = %+v", byID["acme.plugin"])
	}
	if !byID["acme.key"].HasRevokedVersion || byID["acme.key"].LocalStatus != localInstalled {
		t.Fatalf("key revoke = %+v", byID["acme.key"])
	}
	if byID["acme.api"].LocalStatus != localNotInstalled {
		t.Fatalf("missing local = %+v", byID["acme.api"])
	}

	detail, err := svc.Get(context.Background(), "acme.revoked")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Versions) != 2 || detail.Versions[0].Version != "1.1.0" || detail.Versions[1].Version != "1.0.0" {
		t.Fatalf("versions = %+v", detail.Versions)
	}
	if detail.Versions[0].Revocation.Revoked {
		t.Fatalf("market digest should not inherit the local digest revocation: %+v", detail.Versions[0].Revocation)
	}
	rev := detail.Versions[1].Revocation
	if !rev.Revoked || rev.Level != "security" || rev.Reason != "bad package" {
		t.Fatalf("local matching market version = %+v", rev)
	}
	if detail.Versions[1].Hosts == nil || detail.Versions[0].Permissions == nil {
		t.Fatal("empty host or permission list is nil")
	}
	if !detail.Versions[0].Compatible || detail.Versions[0].Changelog != "notes" {
		t.Fatalf("version body = %+v", detail.Versions[0])
	}

	pluginDetail, err := svc.Get(context.Background(), "acme.plugin")
	if err != nil {
		t.Fatal(err)
	}
	if !pluginDetail.Versions[0].Revocation.Revoked || pluginDetail.Versions[0].Revocation.Level != "deprecated" || pluginDetail.Versions[0].Revocation.Reason != "replaced" {
		t.Fatalf("plugin-level revocation = %+v", pluginDetail.Versions[0].Revocation)
	}

	keyDetail, err := svc.Get(context.Background(), "acme.key")
	if err != nil {
		t.Fatal(err)
	}
	if keyDetail.Versions[0].Revocation.Revoked {
		t.Fatalf("attested market version matched a developer key revocation: %+v", keyDetail.Versions[0].Revocation)
	}

	hostDetail, err := svc.Get(context.Background(), "acme.host")
	if err != nil {
		t.Fatal(err)
	}
	if hostDetail.Versions[0].Version != "1.0.0" || hostDetail.Versions[0].IncompatibleReason != reasonHostAPI || hostDetail.Versions[0].Compatible {
		t.Fatalf("host detail = %+v", hostDetail.Versions[0])
	}
	if hostDetail.Versions[1].IncompatibleReason != reasonHostAPI {
		t.Fatalf("host 0 = %+v", hostDetail.Versions[1])
	}

	apiDetail, err := svc.Get(context.Background(), "acme.api")
	if err != nil {
		t.Fatal(err)
	}
	if apiDetail.Versions[0].IncompatibleReason != reasonAPIVersion {
		t.Fatalf("api detail = %+v", apiDetail.Versions[0])
	}

	onlyNew, err := svc.Get(context.Background(), "acme.xlyra")
	if err != nil {
		t.Fatal(err)
	}
	if onlyNew.Versions[0].Version != "2.0.0" || onlyNew.Versions[0].IncompatibleReason != reasonXLyra || onlyNew.Versions[0].Compatible {
		t.Fatalf("xlyra detail = %+v", onlyNew.Versions[0])
	}
	if !onlyNew.Versions[1].Compatible {
		t.Fatalf("older xlyra = %+v", onlyNew.Versions[1])
	}
}

func TestCatalogEmptyCacheAndNotFound(t *testing.T) {
	svc := catalogService(t, CatalogFixture{Enabled: true, RootKeysConfigured: false, LastError: "未配置市场根公钥"})
	cat, err := svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cat.Plugins == nil || len(cat.Plugins) != 0 {
		t.Fatalf("plugins = %#v", cat.Plugins)
	}
	if cat.Snapshot.RootKeysConfigured || cat.Snapshot.LastError != "未配置市场根公钥" {
		t.Fatalf("snapshot = %+v", cat.Snapshot)
	}
	if _, err := svc.Get(context.Background(), "acme.update"); err != ErrNotFound {
		t.Fatalf("missing cache get = %v", err)
	}

	emptyIndex := catalogService(t, CatalogFixture{
		XLyraVersion: "1.15.1",
		Snapshot:     Snapshot{Index: &marketproto.Index{}},
	})
	cat, err = emptyIndex.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cat.Plugins == nil || len(cat.Plugins) != 0 {
		t.Fatalf("empty index plugins = %#v", cat.Plugins)
	}
	if _, err := emptyIndex.Get(context.Background(), "missing"); err != ErrNotFound {
		t.Fatalf("missing id = %v", err)
	}
	if _, err := emptyIndex.Get(context.Background(), "  "); err != ErrNotFound {
		t.Fatalf("blank id = %v", err)
	}
}

func TestCatalogDevBuildAllowsXLyraRange(t *testing.T) {
	okAPI := marketproto.Compat{XLyra: ">=99.0.0", APIVersion: jsplugin.HookAPIVersion, HostAPI: jsplugin.HostAPIVersion}
	svc := catalogService(t, CatalogFixture{
		XLyraVersion: "dev",
		Snapshot: Snapshot{Index: &marketproto.Index{Plugins: []marketproto.Plugin{{
			ID: "acme.dev", Name: "Dev", Kind: "quota_probe",
			Versions: []marketproto.PluginVersion{ver("1.0.0", okAPI, "d", nil, nil)},
		}}}},
	})
	cat, err := svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Plugins) != 1 || !cat.Plugins[0].HasCompatibleVersion || cat.Plugins[0].IncompatibleReason != "" {
		t.Fatalf("dev = %+v", cat.Plugins)
	}
}

func TestCatalogIconURL(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	ok := marketproto.Compat{XLyra: ">=1.0.0", APIVersion: jsplugin.HookAPIVersion, HostAPI: jsplugin.HostAPIVersion}
	version := ver("1.0.0", ok, "digest", nil, nil)
	svc := catalogService(t, CatalogFixture{
		Enabled:      true,
		XLyraVersion: "1.15.1",
		Snapshot: Snapshot{Index: &marketproto.Index{Plugins: []marketproto.Plugin{
			{
				ID: "acme.icon", Name: "Icon", Summary: "has an icon", Kind: "quota_probe",
				Icon:     &marketproto.Icon{Path: "icons/" + sum + ".png", SHA256: sum},
				Versions: []marketproto.PluginVersion{version},
			},
			{
				ID: "acme.plain", Name: "Plain", Summary: "no icon", Kind: "quota_probe",
				Versions: []marketproto.PluginVersion{version},
			},
			{
				ID: "acme.bad", Name: "Bad", Summary: "bad hash", Kind: "quota_probe",
				Icon:     &marketproto.Icon{Path: "icons/NOPE.png", SHA256: "NOPE"},
				Versions: []marketproto.PluginVersion{version},
			},
			{
				ID: "acme.upper", Name: "Upper", Summary: "uppercase hash", Kind: "quota_probe",
				Icon:     &marketproto.Icon{Path: "icons/" + strings.ToUpper(sum) + ".png", SHA256: strings.ToUpper(sum)},
				Versions: []marketproto.PluginVersion{version},
			},
		}}},
	})
	cat, err := svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]PluginView{}
	for _, item := range cat.Plugins {
		byID[item.ID] = item
	}
	want := "/api/v1/plugin-market/icons/" + sum
	if byID["acme.icon"].IconURL != want {
		t.Fatalf("icon url = %q", byID["acme.icon"].IconURL)
	}
	if byID["acme.plain"].IconURL != "" || byID["acme.bad"].IconURL != "" || byID["acme.upper"].IconURL != "" {
		t.Fatalf("empty urls = %q %q %q", byID["acme.plain"].IconURL, byID["acme.bad"].IconURL, byID["acme.upper"].IconURL)
	}
	detail, err := svc.Get(context.Background(), "acme.icon")
	if err != nil {
		t.Fatal(err)
	}
	if detail.IconURL != want {
		t.Fatalf("detail icon url = %q", detail.IconURL)
	}
	raw, err := json.Marshal(detail.Versions)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "icon_url") {
		t.Fatalf("version view includes icon_url: %s", raw)
	}
}

func catalogService(t *testing.T, fixture CatalogFixture) *Service {
	t.Helper()
	svc, err := NewService(store.JSPluginRepository{}, nil, nil, []ed25519.PublicKey{}, []string{})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetCatalogFixture(&fixture)
	return svc
}

func ver(version string, compat marketproto.Compat, digest string, hosts, permissions []string) marketproto.PluginVersion {
	return marketproto.PluginVersion{
		Version:       version,
		PublishedAt:   "2026-10-01T00:00:00Z",
		Changelog:     "notes",
		Size:          12,
		Compat:        compat,
		Hosts:         hosts,
		Permissions:   permissions,
		PackageDigest: digest,
	}
}
