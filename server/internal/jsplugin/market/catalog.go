package market

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/marketproto"
	"xlyra/server/internal/store"
	"xlyra/server/internal/version"
)

const (
	localNotInstalled = "not_installed"
	localInstalled    = "installed"
	localEnabled      = "enabled"

	reasonXLyra      = "xlyra_version"
	reasonAPIVersion = "api_version"
	reasonHostAPI    = "host_api"
)

// ErrNotFound is returned when the cached index has no plugin with that id.
var ErrNotFound = errors.New("plugin market plugin not found")

// CatalogFixture replaces the signed cache and local versions. Tests use it
// to exercise the read API without a database.
type CatalogFixture struct {
	Snapshot           Snapshot
	Enabled            bool
	RootKeysConfigured bool
	SourceURL          string
	IndexSyncedAt      *time.Time
	LastError          string
	Locals             []store.JSPluginVersion
	XLyraVersion       string
	Subscriptions      []store.JSPluginMarketSubscription
	RolledBack         map[string][]string
	AutoFailures       map[string]store.JSPluginAutoUpdate
	DisableAutoUpdate  bool
}

func (s *Service) SetCatalogFixture(f *CatalogFixture) {
	s.fixture = f
}

type DeveloperView struct {
	Namespace   string `json:"namespace"`
	DisplayName string `json:"display_name"`
}

type CompatView struct {
	XLyra      string `json:"xlyra"`
	APIVersion int    `json:"api_version"`
	HostAPI    int    `json:"host_api"`
}

type RevocationView struct {
	Revoked bool   `json:"revoked"`
	Reason  string `json:"reason,omitempty"`
	Level   string `json:"level,omitempty"`
}

type VersionView struct {
	Version            string         `json:"version"`
	PublishedAt        string         `json:"published_at"`
	Changelog          string         `json:"changelog"`
	Size               int64          `json:"size"`
	Compat             CompatView     `json:"compat"`
	Hosts              []string       `json:"hosts"`
	Permissions        []string       `json:"permissions"`
	Compatible         bool           `json:"compatible"`
	IncompatibleReason string         `json:"incompatible_reason,omitempty"`
	Revocation         RevocationView `json:"revocation"`
}

type PluginView struct {
	ID                      string        `json:"id"`
	Name                    string        `json:"name"`
	Summary                 string        `json:"summary"`
	Kind                    string        `json:"kind"`
	Developer               DeveloperView `json:"developer"`
	Homepage                string        `json:"homepage,omitempty"`
	IconURL                 string        `json:"icon_url"`
	LatestCompatibleVersion string        `json:"latest_compatible_version,omitempty"`
	HasCompatibleVersion    bool          `json:"has_compatible_version"`
	IncompatibleReason      string        `json:"incompatible_reason,omitempty"`
	LocalStatus             string        `json:"local_status"`
	LocalVersion            string        `json:"local_version,omitempty"`
	UpdateAvailable         bool          `json:"update_available"`
	UpdateVersion           string        `json:"update_version,omitempty"`
	UpdatePolicy            string        `json:"update_policy,omitempty"`
	PausedReason            string        `json:"paused_reason,omitempty"`
	AutoBlockReason         string        `json:"auto_block_reason,omitempty"`
	AutoBlockDetail         string        `json:"auto_block_detail,omitempty"`
	SignerRevoked           bool          `json:"signer_revoked,omitempty"`
	AutoRolledBack          bool          `json:"auto_rolled_back,omitempty"`
	HasRevokedVersion       bool          `json:"has_revoked_version"`
	Versions                []VersionView `json:"versions,omitempty"`
}

type SnapshotView struct {
	IndexExpired       bool       `json:"index_expired"`
	RevocationsExpired bool       `json:"revocations_expired"`
	IndexSyncedAt      *time.Time `json:"index_synced_at"`
	LastError          string     `json:"last_error"`
	SourceURL          string     `json:"source_url"`
	RootKeysConfigured bool       `json:"root_keys_configured"`
	Enabled            bool       `json:"enabled"`
}

type Catalog struct {
	Plugins  []PluginView `json:"plugins"`
	Snapshot SnapshotView `json:"snapshot"`
}

func (s *Service) List(ctx context.Context) (Catalog, error) {
	snap, meta, locals, xlyra, err := s.loadCatalog(ctx)
	if err != nil {
		return Catalog{}, err
	}
	subs, rolled, globalAuto, err := s.updateContext(ctx)
	if err != nil {
		return Catalog{}, err
	}
	cat := buildCatalog(snap, meta, locals, xlyra, subs, rolled, globalAuto)
	for i := range cat.Plugins {
		cat.Plugins[i].AutoBlockReason = s.applyAutoReason(ctx, cat.Plugins[i].ID, UpdateView{
			UpdateAvailable: cat.Plugins[i].UpdateAvailable,
			UpdateVersion:   cat.Plugins[i].UpdateVersion,
			BlockReason:     cat.Plugins[i].AutoBlockReason,
			BlockDetail:     cat.Plugins[i].AutoBlockDetail,
		}).BlockReason
	}
	return cat, nil
}

func (s *Service) Get(ctx context.Context, id string) (PluginView, error) {
	id = strings.TrimSpace(id)
	snap, _, locals, xlyra, err := s.loadCatalog(ctx)
	if err != nil {
		return PluginView{}, err
	}
	if snap.Index == nil || id == "" {
		return PluginView{}, ErrNotFound
	}
	subs, rolled, globalAuto, err := s.updateContext(ctx)
	if err != nil {
		return PluginView{}, err
	}
	grouped := groupLocals(locals)
	for _, plugin := range snap.Index.Plugins {
		if plugin.ID != id {
			continue
		}
		item := buildPlugin(plugin, snap.Revocations, grouped[plugin.ID], xlyra, true, subs[plugin.ID], rolled[plugin.ID], snap.RevocationsExpired, globalAuto)
		item.AutoBlockReason = s.applyAutoReason(ctx, item.ID, UpdateView{
			UpdateAvailable: item.UpdateAvailable,
			UpdateVersion:   item.UpdateVersion,
			BlockReason:     item.AutoBlockReason,
			BlockDetail:     item.AutoBlockDetail,
		}).BlockReason
		return item, nil
	}
	return PluginView{}, ErrNotFound
}

func (s *Service) loadCatalog(ctx context.Context) (Snapshot, SnapshotView, []store.JSPluginVersion, string, error) {
	if s.fixture != nil {
		xlyra := strings.TrimSpace(s.fixture.XLyraVersion)
		if xlyra == "" {
			xlyra = version.Current().Version
		}
		meta := SnapshotView{
			IndexExpired:       s.fixture.Snapshot.IndexExpired,
			RevocationsExpired: s.fixture.Snapshot.RevocationsExpired,
			IndexSyncedAt:      s.fixture.IndexSyncedAt,
			LastError:          s.fixture.LastError,
			SourceURL:          s.fixture.SourceURL,
			RootKeysConfigured: s.fixture.RootKeysConfigured,
			Enabled:            s.fixture.Enabled,
		}
		return s.fixture.Snapshot, meta, append([]store.JSPluginVersion(nil), s.fixture.Locals...), xlyra, nil
	}
	snap, err := s.Snapshot(ctx)
	if err != nil {
		return Snapshot{}, SnapshotView{}, nil, "", err
	}
	state, err := s.repo.GetMarketState(ctx)
	if err != nil {
		return Snapshot{}, SnapshotView{}, nil, "", err
	}
	locals, err := s.repo.ListVersionHeaders(ctx)
	if err != nil {
		return Snapshot{}, SnapshotView{}, nil, "", err
	}
	cfg := s.cfg
	meta := SnapshotView{
		IndexExpired:       snap.IndexExpired,
		RevocationsExpired: snap.RevocationsExpired,
		IndexSyncedAt:      state.IndexSyncedAt,
		LastError:          state.LastError,
		SourceURL:          state.SourceURL,
		RootKeysConfigured: len(s.rootKeys) > 0,
		Enabled:            cfg.Enabled,
	}
	return snap, meta, locals, version.Current().Version, nil
}

func buildCatalog(snap Snapshot, meta SnapshotView, locals []store.JSPluginVersion, xlyra string, subs map[string]store.JSPluginMarketSubscription, rolled map[string]map[string]struct{}, globalAuto bool) Catalog {
	meta.IndexExpired = snap.IndexExpired
	meta.RevocationsExpired = snap.RevocationsExpired
	out := Catalog{Plugins: []PluginView{}, Snapshot: meta}
	if snap.Index == nil {
		return out
	}
	grouped := groupLocals(locals)
	for _, plugin := range snap.Index.Plugins {
		out.Plugins = append(out.Plugins, buildPlugin(plugin, snap.Revocations, grouped[plugin.ID], xlyra, false, subs[plugin.ID], rolled[plugin.ID], snap.RevocationsExpired, globalAuto))
	}
	return out
}

func buildPlugin(plugin marketproto.Plugin, rev *marketproto.Revocations, locals []store.JSPluginVersion, xlyra string, withVersions bool, sub store.JSPluginMarketSubscription, rolled map[string]struct{}, revocationsExpired, globalAuto bool) PluginView {
	versions := append([]marketproto.PluginVersion(nil), plugin.Versions...)
	sortVersionsNewestFirst(versions)
	views := make([]VersionView, 0, len(versions))
	for _, ver := range versions {
		views = append(views, buildVersion(plugin.ID, ver, rev, xlyra))
	}
	latestCompatible := ""
	for _, view := range views {
		if view.Compatible {
			latestCompatible = view.Version
			break
		}
	}
	reason := ""
	if latestCompatible == "" && len(views) > 0 {
		reason = views[0].IncompatibleReason
	}
	status, localVersion, revoked := summarizeLocal(plugin.ID, locals, rev)
	item := PluginView{
		ID:                      plugin.ID,
		Name:                    plugin.Name,
		Summary:                 plugin.Summary,
		Kind:                    plugin.Kind,
		Developer:               DeveloperView{Namespace: plugin.Developer.Namespace, DisplayName: plugin.Developer.DisplayName},
		Homepage:                plugin.Homepage,
		IconURL:                 marketIconURL(plugin.Icon),
		LatestCompatibleVersion: latestCompatible,
		HasCompatibleVersion:    latestCompatible != "",
		IncompatibleReason:      reason,
		LocalStatus:             status,
		LocalVersion:            localVersion,
		HasRevokedVersion:       revoked,
	}
	decision := describeUpdate(plugin, rev, locals, sub, xlyra, revocationsExpired, globalAuto, rolled)
	item.UpdateAvailable = decision.UpdateAvailable
	item.UpdateVersion = decision.UpdateVersion
	item.UpdatePolicy = decision.Policy
	item.PausedReason = decision.PausedReason
	item.AutoBlockReason = decision.BlockReason
	item.AutoBlockDetail = decision.BlockDetail
	item.SignerRevoked = decision.SignerRevoked
	item.AutoRolledBack = decision.AutoRolledBack
	if withVersions {
		if views == nil {
			views = []VersionView{}
		}
		item.Versions = views
	}
	return item
}

func buildVersion(pluginID string, ver marketproto.PluginVersion, rev *marketproto.Revocations, xlyra string) VersionView {
	reason := incompatibleReason(ver.Compat, xlyra)
	return VersionView{
		Version:     ver.Version,
		PublishedAt: ver.PublishedAt,
		Changelog:   ver.Changelog,
		Size:        ver.Size,
		Compat: CompatView{
			XLyra:      ver.Compat.XLyra,
			APIVersion: ver.Compat.APIVersion,
			HostAPI:    ver.Compat.HostAPI,
		},
		Hosts:              stringList(ver.Hosts),
		Permissions:        stringList(ver.Permissions),
		Compatible:         reason == "",
		IncompatibleReason: reason,
		Revocation:         versionRevocation(rev, pluginID, ver),
	}
}

func incompatibleReason(compat marketproto.Compat, xlyra string) string {
	if compat.APIVersion != jsplugin.HookAPIVersion {
		return reasonAPIVersion
	}
	if compat.HostAPI > jsplugin.HostAPIVersion || compat.HostAPI < 1 {
		return reasonHostAPI
	}
	if err := jsplugin.VersionAllowed(compat.XLyra, xlyra); err != nil {
		return reasonXLyra
	}
	return ""
}

func summarizeLocal(pluginID string, rows []store.JSPluginVersion, rev *marketproto.Revocations) (status, localVersion string, revoked bool) {
	if len(rows) == 0 {
		return localNotInstalled, "", false
	}
	enabled := ""
	highest := ""
	for _, row := range rows {
		if row.PluginID != pluginID {
			continue
		}
		if row.Status == store.JSPluginStatusEnabled && enabled == "" {
			enabled = row.Version
		}
		if highest == "" || versionOlder(highest, row.Version) {
			highest = row.Version
		}
		if !revoked && len(marketproto.MatchRevocations(rev, localTarget(row))) > 0 {
			revoked = true
		}
	}
	if enabled == "" && highest == "" {
		return localNotInstalled, "", false
	}
	if enabled != "" {
		return localEnabled, enabled, revoked
	}
	return localInstalled, highest, revoked
}

func localTarget(row store.JSPluginVersion) marketproto.Target {
	return marketproto.Target{
		PluginID:          row.PluginID,
		Version:           row.Version,
		PackageDigest:     row.PackageSHA256,
		SignerFingerprint: row.Signer,
		Attested:          localAttested(row),
	}
}

func localAttested(row store.JSPluginVersion) bool {
	if row.Origin == store.JSPluginOriginMarket {
		return true
	}
	raw := bytes.TrimSpace(row.Attestation)
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null"))
}

func versionRevocation(rev *marketproto.Revocations, pluginID string, ver marketproto.PluginVersion) RevocationView {
	effects := marketproto.MatchRevocations(rev, marketproto.Target{
		PluginID:      pluginID,
		Version:       ver.Version,
		PackageDigest: ver.PackageDigest,
		Attested:      true,
	})
	if len(effects) == 0 {
		return RevocationView{}
	}
	best := effects[0]
	bestLevel := revocationLevel(rev, best.EntryID)
	for _, effect := range effects[1:] {
		level := revocationLevel(rev, effect.EntryID)
		if level == "security" && bestLevel != "security" {
			best = effect
			bestLevel = level
		}
	}
	entry, ok := revocationEntry(rev, best.EntryID)
	if !ok {
		return RevocationView{Revoked: true, Level: bestLevel}
	}
	return RevocationView{Revoked: true, Reason: entry.Reason, Level: entry.Level}
}

func revocationLevel(rev *marketproto.Revocations, id string) string {
	entry, ok := revocationEntry(rev, id)
	if !ok {
		return ""
	}
	return entry.Level
}

func revocationEntry(rev *marketproto.Revocations, id string) (marketproto.RevocationEntry, bool) {
	if rev == nil {
		return marketproto.RevocationEntry{}, false
	}
	for _, entry := range rev.Entries {
		if entry.ID == id {
			return entry, true
		}
	}
	return marketproto.RevocationEntry{}, false
}

func groupLocals(rows []store.JSPluginVersion) map[string][]store.JSPluginVersion {
	out := map[string][]store.JSPluginVersion{}
	for _, row := range rows {
		out[row.PluginID] = append(out[row.PluginID], row)
	}
	return out
}

func sortVersionsNewestFirst(versions []marketproto.PluginVersion) {
	sort.SliceStable(versions, func(i, j int) bool {
		return versionOlder(versions[j].Version, versions[i].Version)
	})
}

func versionOlder(left, right string) bool {
	l, err := marketproto.ParseVersion(strings.TrimSpace(left))
	if err != nil {
		return false
	}
	r, err := marketproto.ParseVersion(strings.TrimSpace(right))
	if err != nil {
		return false
	}
	return marketproto.Compare(l, r) < 0
}

func stringList(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	return append([]string(nil), in...)
}
