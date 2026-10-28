package market

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/marketproto"
	"xlyra/server/internal/store"
)

func TestUpgradeBlockReasons(t *testing.T) {
	base := func() (versionFacts, candidateFacts) {
		cur := versionFacts{
			Version: "1.2.3", Status: store.JSPluginStatusEnabled, Signer: "SHA256:dev",
			Kind: jsplugin.KindQuotaProbe,
		}
		next := candidateFacts{Version: "1.4.0", Fingerprint: "SHA256:dev", Kind: jsplugin.KindQuotaProbe}
		return cur, next
	}
	cases := []struct {
		name    string
		mutate  func(cur *versionFacts, next *candidateFacts)
		expired bool
		reason  string
		detail  string
	}{
		{name: "compatible", mutate: func(_ *versionFacts, next *candidateFacts) {
			next.IncompatibleReason = reasonXLyra
		}, reason: reasonXLyra},
		{name: "developer", mutate: func(_ *versionFacts, next *candidateFacts) {
			next.Fingerprint = "SHA256:other"
		}, reason: UpdateReasonDeveloper},
		{name: "kind", mutate: func(_ *versionFacts, next *candidateFacts) {
			next.Kind = jsplugin.KindProtocol
		}, reason: UpdateReasonKind},
		{name: "hosts", mutate: func(cur *versionFacts, next *candidateFacts) {
			cur.Hosts = []string{"api.example.com"}
			next.Hosts = []string{"api.example.com", "api2.foo.com"}
		}, reason: UpdateReasonHosts, detail: "api2.foo.com"},
		{name: "permissions", mutate: func(cur *versionFacts, next *candidateFacts) {
			cur.Permissions = []string{"notify"}
			next.Permissions = []string{"http", "notify"}
		}, reason: UpdateReasonPermissions, detail: "http"},
		{name: "events", mutate: func(cur *versionFacts, next *candidateFacts) {
			cur.Events = []string{"schedule.tick"}
			next.Events = []string{"oauth.quota_synced", "schedule.tick"}
		}, reason: UpdateReasonEvents, detail: "oauth.quota_synced"},
		{name: "major", mutate: func(_ *versionFacts, next *candidateFacts) {
			next.Version = "2.0.0"
		}, reason: UpdateReasonMajor},
		{name: "site type", mutate: func(cur *versionFacts, next *candidateFacts) {
			cur.Kind = jsplugin.KindSiteProvider
			next.Kind = jsplugin.KindSiteProvider
			cur.SiteType = "acme.demo/example"
			next.SiteType = "acme.demo/other"
		}, reason: UpdateReasonSiteType},
		{name: "revoked", mutate: func(_ *versionFacts, next *candidateFacts) {
			next.SecurityRevoked = true
		}, reason: UpdateReasonRevoked},
		{name: "revocations expired", expired: true, reason: UpdateReasonRevocations},
		{name: "pricing", mutate: func(cur *versionFacts, next *candidateFacts) {
			cur.Kind = jsplugin.KindPricingParse
			next.Kind = jsplugin.KindPricingParse
		}, reason: UpdateReasonPricing},
		{name: "deprecated", mutate: func(_ *versionFacts, next *candidateFacts) {
			next.Deprecated = true
		}, reason: UpdateReasonDeprecated},
		{name: "subset hosts allowed", mutate: func(cur *versionFacts, next *candidateFacts) {
			cur.Hosts = []string{"api.example.com", "files.example.com"}
			next.Hosts = []string{"api.example.com"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cur, next := base()
			if tc.mutate != nil {
				tc.mutate(&cur, &next)
			}
			reason, detail := upgradeBlockReason(cur, next, tc.expired)
			if reason != tc.reason || detail != tc.detail {
				t.Fatalf("reason = %q %q, want %q %q", reason, detail, tc.reason, tc.detail)
			}
		})
	}
	cur, next := base()
	if reason, detail := upgradeBlockReason(cur, next, false); reason != "" || detail != "" {
		t.Fatalf("safe upgrade blocked: %s %s", reason, detail)
	}
}

func TestDescribeUpdatePolicy(t *testing.T) {
	plugin := marketproto.Plugin{
		ID: "acme.demo", Kind: jsplugin.KindQuotaProbe,
		Versions: []marketproto.PluginVersion{{
			Version: "1.4.0", Compat: compatCurrent(), Hosts: []string{}, Permissions: []string{},
			Attestation: attestationEnvelope(t, "SHA256:dev", jsplugin.KindQuotaProbe),
		}},
	}
	manifest, err := json.Marshal(jsplugin.Manifest{ID: "acme.demo", Version: "1.2.3", Kind: jsplugin.KindQuotaProbe})
	if err != nil {
		t.Fatal(err)
	}
	local := []store.JSPluginVersion{{
		PluginID: "acme.demo", Version: "1.2.3", Status: store.JSPluginStatusEnabled,
		Signer: "SHA256:dev", Manifest: manifest,
	}}
	off := describeUpdate(plugin, nil, local, store.JSPluginMarketSubscription{UpdatePolicy: store.JSPluginUpdatePolicyOff}, "1.15.1", false, true, nil)
	if off.UpdateAvailable || off.AutoOK || off.Policy != store.JSPluginUpdatePolicyOff {
		t.Fatalf("off = %+v", off)
	}
	notify := describeUpdate(plugin, nil, local, store.JSPluginMarketSubscription{UpdatePolicy: store.JSPluginUpdatePolicyNotify}, "1.15.1", false, true, nil)
	if !notify.UpdateAvailable || notify.UpdateVersion != "1.4.0" || notify.AutoOK || notify.BlockReason != "" {
		t.Fatalf("notify = %+v", notify)
	}
	auto := describeUpdate(plugin, nil, local, store.JSPluginMarketSubscription{UpdatePolicy: store.JSPluginUpdatePolicyAuto}, "1.15.1", false, true, nil)
	if !auto.AutoOK || auto.BlockReason != "" {
		t.Fatalf("auto = %+v", auto)
	}
	paused := describeUpdate(plugin, nil, local, store.JSPluginMarketSubscription{UpdatePolicy: store.JSPluginUpdatePolicyAuto, PausedReason: pausedAutoRollback}, "1.15.1", false, true, nil)
	if paused.AutoOK || paused.BlockReason != UpdateReasonPaused {
		t.Fatalf("paused = %+v", paused)
	}
	globalOff := describeUpdate(plugin, nil, local, store.JSPluginMarketSubscription{UpdatePolicy: store.JSPluginUpdatePolicyAuto}, "1.15.1", false, false, nil)
	if globalOff.AutoOK || globalOff.BlockReason != UpdateReasonGlobalOff || !globalOff.UpdateAvailable {
		t.Fatalf("global off = %+v", globalOff)
	}
	expired := describeUpdate(plugin, nil, local, store.JSPluginMarketSubscription{UpdatePolicy: store.JSPluginUpdatePolicyAuto}, "1.15.1", true, true, nil)
	if expired.AutoOK || expired.BlockReason != UpdateReasonRevocations || !expired.UpdateAvailable {
		t.Fatalf("expired = %+v", expired)
	}
}

func TestDescribeUpdateBlocksAddedEvents(t *testing.T) {
	plugin := marketproto.Plugin{
		ID: "acme.auto", Kind: jsplugin.KindAutomation,
		Versions: []marketproto.PluginVersion{{
			Version: "1.4.0", Compat: compatCurrent(), Hosts: []string{}, Permissions: []string{},
			Attestation: attestationEnvelope(t, "SHA256:dev", jsplugin.KindAutomation),
		}},
	}
	oldRaw, err := json.Marshal(jsplugin.Manifest{
		ID: "acme.auto", Version: "1.2.3", Kind: jsplugin.KindAutomation,
		Automation: jsplugin.AutomationSection{Subscribes: []string{"schedule.tick"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	newRaw, err := json.Marshal(jsplugin.Manifest{
		ID: "acme.auto", Version: "1.4.0", Kind: jsplugin.KindAutomation,
		Automation: jsplugin.AutomationSection{Subscribes: []string{"oauth.quota_synced", "schedule.tick"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	local := []store.JSPluginVersion{
		{PluginID: "acme.auto", Version: "1.2.3", Status: store.JSPluginStatusEnabled, Signer: "SHA256:dev", Manifest: oldRaw},
		{PluginID: "acme.auto", Version: "1.4.0", Status: store.JSPluginStatusDisabled, Signer: "SHA256:dev", Manifest: newRaw},
	}
	view := describeUpdate(plugin, nil, local, store.JSPluginMarketSubscription{UpdatePolicy: store.JSPluginUpdatePolicyAuto}, "1.15.1", false, true, nil)
	if view.AutoOK || !view.UpdateAvailable || view.BlockReason != UpdateReasonEvents || view.BlockDetail != "oauth.quota_synced" {
		t.Fatalf("events = %+v", view)
	}
}

func TestAutoReasonHashMismatchWindow(t *testing.T) {
	now := time.Now()
	base := UpdateView{UpdateAvailable: true, UpdateVersion: "1.4.0", AutoOK: true}
	latest := store.JSPluginAutoUpdate{
		ToVersion: "1.4.0", Status: store.JSPluginAutoUpdateFailed, Reason: reasonHashMismatch, CreatedAt: now.Add(-time.Hour),
	}
	got := autoReason(base, latest, now)
	if got.BlockReason != reasonHashMismatch || got.AutoOK || got.NeedsPackage {
		t.Fatalf("inside window = %+v", got)
	}
	stale := latest
	stale.CreatedAt = now.Add(-autoRetryWindow - time.Second)
	if got := autoReason(base, stale, now); got.BlockReason != "" || !got.AutoOK {
		t.Fatalf("outside window = %+v", got)
	}
	other := latest
	other.Reason = reasonNeedsConfirmation
	if got := autoReason(base, other, now); got.BlockReason != "" {
		t.Fatalf("other reason = %+v", got)
	}
	blocked := base
	blocked.BlockReason = UpdateReasonPermissions
	blocked.AutoOK = false
	if got := autoReason(blocked, latest, now); got.BlockReason != UpdateReasonPermissions {
		t.Fatalf("kept reason = %+v", got)
	}
}

func TestInstalledListReportsHashMismatch(t *testing.T) {
	plugin := marketproto.Plugin{
		ID: "acme.demo", Kind: jsplugin.KindQuotaProbe,
		Versions: []marketproto.PluginVersion{{
			Version: "1.4.0", Compat: compatCurrent(), Hosts: []string{}, Permissions: []string{},
			Attestation: attestationEnvelope(t, "SHA256:dev", jsplugin.KindQuotaProbe),
		}},
	}
	manifest, err := json.Marshal(jsplugin.Manifest{ID: "acme.demo", Version: "1.2.3", Kind: jsplugin.KindQuotaProbe})
	if err != nil {
		t.Fatal(err)
	}
	local := []store.JSPluginVersion{{
		PluginID: "acme.demo", Version: "1.2.3", Status: store.JSPluginStatusEnabled,
		Signer: "SHA256:dev", Manifest: manifest,
	}}
	failure := store.JSPluginAutoUpdate{
		PluginID: "acme.demo", ToVersion: "1.4.0", Status: store.JSPluginAutoUpdateFailed,
		Reason: reasonHashMismatch, CreatedAt: time.Now(),
	}
	svc := &Service{fixture: &CatalogFixture{
		Snapshot:      Snapshot{Index: &marketproto.Index{Plugins: []marketproto.Plugin{plugin}}},
		Locals:        local,
		XLyraVersion:  "1.15.1",
		Subscriptions: []store.JSPluginMarketSubscription{{PluginID: "acme.demo", UpdatePolicy: store.JSPluginUpdatePolicyAuto}},
		AutoFailures:  map[string]store.JSPluginAutoUpdate{"acme.demo": failure},
	}}
	state, err := svc.InstalledState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	view := state.Plugins["acme.demo"]
	if view.BlockReason != reasonHashMismatch || view.AutoOK || !view.UpdateAvailable {
		t.Fatalf("installed = %+v", view)
	}
	cat, err := svc.List(context.Background())
	if err != nil || len(cat.Plugins) != 1 || cat.Plugins[0].AutoBlockReason != reasonHashMismatch {
		t.Fatalf("catalog = %+v err=%v", cat.Plugins, err)
	}
}

func attestationEnvelope(t *testing.T, fingerprint, kind string) marketproto.Envelope {
	t.Helper()
	raw, err := json.Marshal(marketproto.Attestation{V: 1, DeveloperFingerprint: fingerprint, Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	return marketproto.Envelope{Payload: base64.StdEncoding.EncodeToString(raw)}
}
