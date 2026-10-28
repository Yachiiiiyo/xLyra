package market

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"xlyra/server/internal/auth"
	"xlyra/server/internal/config"
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/marketproto"
	"xlyra/server/internal/store"
	appversion "xlyra/server/internal/version"
)

const (
	UpdateReasonDeveloper   = "developer_changed"
	UpdateReasonKind        = "kind_changed"
	UpdateReasonHosts       = "hosts_added"
	UpdateReasonPermissions = "permissions_added"
	UpdateReasonEvents      = "events_added"
	UpdateReasonMajor       = "major_bump"
	UpdateReasonSiteType    = "site_type_changed"
	UpdateReasonRevoked     = "revoked"
	UpdateReasonDeprecated  = "deprecated"
	UpdateReasonRevocations = "revocations_expired"
	UpdateReasonPricing     = "pricing_parse"
	UpdateReasonPaused      = "paused"
	UpdateReasonGlobalOff   = "global_auto_off"
	UpdateReasonNotEnabled  = "not_enabled"

	reasonNeedPackage       = "need_package"
	reasonHashMismatch      = "hash_mismatch"
	reasonNeedsConfirmation = "needs_confirmation"

	autoRollbackWindow = 24 * time.Hour
	autoRetryWindow    = 6 * time.Hour
	pausedAutoRollback = "auto_rolled_back"

	auditActorAutoUpdate = "system:auto-update"
	auditActorRevocation = "system:revocation"
)

var (
	errAutoHashMismatch      = errors.New(reasonHashMismatch)
	errAutoNeedsConfirmation = errors.New(reasonNeedsConfirmation)
)

// UpdateView is the per-plugin update decision shown on the installed list and the market page.
type UpdateView struct {
	UpdateAvailable bool   `json:"update_available"`
	UpdateVersion   string `json:"update_version,omitempty"`
	Policy          string `json:"update_policy,omitempty"`
	PausedReason    string `json:"paused_reason,omitempty"`
	BlockReason     string `json:"auto_block_reason,omitempty"`
	BlockDetail     string `json:"auto_block_detail,omitempty"`
	SignerRevoked   bool   `json:"signer_revoked,omitempty"`
	AutoRolledBack  bool   `json:"auto_rolled_back,omitempty"`
	AutoOK          bool   `json:"-"`
	NeedsPackage    bool   `json:"-"`
}

// VersionMark is revocation and rollback state for one installed version.
type VersionMark struct {
	Revoked        bool
	Level          string
	Reason         string
	SignerRevoked  bool
	SignerReason   string
	AutoRolledBack bool
}

// InstalledState joins update decisions with per-version revocation marks.
type InstalledState struct {
	Plugins  map[string]UpdateView
	Versions map[string]VersionMark
}

type versionFacts struct {
	Version     string
	Status      string
	Signer      string
	Kind        string
	Hosts       []string
	Permissions []string
	Events      []string
	SiteType    string
	Attested    bool
	Digest      string
}

type candidateFacts struct {
	Version            string
	Fingerprint        string
	Kind               string
	Hosts              []string
	Permissions        []string
	Events             []string
	SiteType           string
	IncompatibleReason string
	SecurityRevoked    bool
	Deprecated         bool
}

func (s *Service) InstalledState(ctx context.Context) (InstalledState, error) {
	out := InstalledState{Plugins: map[string]UpdateView{}, Versions: map[string]VersionMark{}}
	if s == nil {
		return out, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var snap Snapshot
	var locals []store.JSPluginVersion
	var err error
	if s.fixture != nil {
		snap = s.fixture.Snapshot
		locals = append([]store.JSPluginVersion(nil), s.fixture.Locals...)
	} else {
		snap, err = s.Snapshot(ctx)
		if err != nil {
			return InstalledState{}, err
		}
		locals, err = s.repo.ListVersionHeaders(ctx)
		if err != nil {
			return InstalledState{}, err
		}
	}
	subs, rolled, globalAuto, err := s.updateContext(ctx)
	if err != nil {
		return InstalledState{}, err
	}
	xlyra := versionString(s)
	if snap.Index != nil {
		grouped := groupLocals(locals)
		for _, plugin := range snap.Index.Plugins {
			view := describeUpdate(plugin, snap.Revocations, grouped[plugin.ID], subs[plugin.ID], xlyra, snap.RevocationsExpired, globalAuto, rolled[plugin.ID])
			out.Plugins[plugin.ID] = s.applyAutoReason(ctx, plugin.ID, view)
		}
	}
	for _, row := range locals {
		mark := markVersion(snap.Revocations, row, rolled[row.PluginID])
		if !mark.Revoked && !mark.SignerRevoked && !mark.AutoRolledBack {
			continue
		}
		out.Versions[row.PluginID+"@"+row.Version] = mark
	}
	return out, nil
}

func versionString(s *Service) string {
	if s != nil && s.fixture != nil && strings.TrimSpace(s.fixture.XLyraVersion) != "" {
		return strings.TrimSpace(s.fixture.XLyraVersion)
	}
	return appversion.Current().Version
}

func (s *Service) updateContext(ctx context.Context) (map[string]store.JSPluginMarketSubscription, map[string]map[string]struct{}, bool, error) {
	subs := map[string]store.JSPluginMarketSubscription{}
	rolled := map[string]map[string]struct{}{}
	if s != nil && s.fixture != nil {
		for _, row := range s.fixture.Subscriptions {
			subs[row.PluginID] = row
		}
		for pluginID, versions := range s.fixture.RolledBack {
			set := map[string]struct{}{}
			for _, version := range versions {
				set[version] = struct{}{}
			}
			rolled[pluginID] = set
		}
		return subs, rolled, !s.fixture.DisableAutoUpdate, nil
	}
	rows, err := s.repo.ListMarketSubscriptions(ctx)
	if err != nil {
		return nil, nil, false, err
	}
	for _, row := range rows {
		subs[row.PluginID] = row
	}
	backs, err := s.repo.ListRolledBackAutoUpdates(ctx)
	if err != nil {
		return nil, nil, false, err
	}
	for _, row := range backs {
		set := rolled[row.PluginID]
		if set == nil {
			set = map[string]struct{}{}
			rolled[row.PluginID] = set
		}
		set[row.ToVersion] = struct{}{}
	}
	return subs, rolled, config.ReadPluginMarketConfig(s.conf).AutoUpdate, nil
}

func (s *Service) applyPending(ctx context.Context, state store.JSPluginMarketState) error {
	if s == nil {
		return nil
	}
	snap, err := snapshotFrom(state, s.rootKeys, time.Now())
	if err != nil {
		return err
	}
	cfg := config.ReadPluginMarketConfig(s.conf)
	if snap.Revocations != nil && !snap.RevocationsExpired {
		if snap.Index != nil && snap.Root != nil {
			if err := s.backfillAttestations(ctx, snap); err != nil {
				return err
			}
		}
		if err := s.applyRevocations(ctx, snap); err != nil {
			return err
		}
		if snap.Index != nil {
			if err := s.applyAutoUpdates(ctx, snap, cfg.AutoUpdate); err != nil {
				return err
			}
		}
	}
	if snap.Index != nil {
		if err := s.notifyUpdates(ctx, snap, cfg.AutoUpdate); err != nil {
			return err
		}
	}
	return nil
}

func describeUpdate(plugin marketproto.Plugin, rev *marketproto.Revocations, locals []store.JSPluginVersion, sub store.JSPluginMarketSubscription, xlyra string, revocationsExpired, globalAuto bool, rolled map[string]struct{}) UpdateView {
	policy := strings.TrimSpace(sub.UpdatePolicy)
	if policy == "" {
		policy = store.JSPluginUpdatePolicyNotify
	}
	facts := make([]versionFacts, 0, len(locals))
	byVersion := map[string]versionFacts{}
	var signerRevoked bool
	var rolledBack bool
	for _, row := range locals {
		if row.PluginID != "" && row.PluginID != plugin.ID {
			continue
		}
		fact := factsFromRow(row)
		facts = append(facts, fact)
		byVersion[fact.Version] = fact
		if _, ok := rolled[fact.Version]; ok {
			rolledBack = true
		}
		if signerRevokedEffect(rev, row) {
			signerRevoked = true
		}
	}
	view := UpdateView{Policy: policy, PausedReason: strings.TrimSpace(sub.PausedReason), SignerRevoked: signerRevoked, AutoRolledBack: rolledBack}
	if len(facts) == 0 {
		view.Policy = ""
		view.PausedReason = ""
		return view
	}
	cur, ok := currentFacts(facts)
	if !ok {
		return view
	}
	target, _, ok := updateTarget(plugin, rev, xlyra, cur.Version, byVersion)
	if !ok || policy == store.JSPluginUpdatePolicyOff {
		return view
	}
	view.UpdateAvailable = true
	view.UpdateVersion = target.Version
	reason, detail := upgradeBlockReason(cur, target, revocationsExpired)
	switch {
	case reason == reasonNeedPackage:
		view.NeedsPackage = policy == store.JSPluginUpdatePolicyAuto && view.PausedReason == "" && globalAuto && cur.Status == store.JSPluginStatusEnabled
	case reason != "":
		view.BlockReason = reason
		view.BlockDetail = detail
	case policy == store.JSPluginUpdatePolicyAuto && view.PausedReason != "":
		view.BlockReason = UpdateReasonPaused
		view.BlockDetail = view.PausedReason
	case policy == store.JSPluginUpdatePolicyAuto && !globalAuto:
		view.BlockReason = UpdateReasonGlobalOff
	case policy == store.JSPluginUpdatePolicyAuto && cur.Status != store.JSPluginStatusEnabled:
		view.BlockReason = UpdateReasonNotEnabled
	}
	view.AutoOK = policy == store.JSPluginUpdatePolicyAuto && view.BlockReason == "" && !view.NeedsPackage
	return view
}

func currentFacts(facts []versionFacts) (versionFacts, bool) {
	var enabled *versionFacts
	var newest *versionFacts
	for i := range facts {
		fact := facts[i]
		if fact.Status == store.JSPluginStatusEnabled && enabled == nil {
			enabled = &facts[i]
		}
		if newest == nil || versionOlder(newest.Version, fact.Version) {
			newest = &facts[i]
		}
	}
	if enabled != nil {
		return *enabled, true
	}
	if newest != nil {
		return *newest, true
	}
	return versionFacts{}, false
}

func updateTarget(plugin marketproto.Plugin, rev *marketproto.Revocations, xlyra, localVersion string, installed map[string]versionFacts) (candidateFacts, marketproto.PluginVersion, bool) {
	versions := append([]marketproto.PluginVersion(nil), plugin.Versions...)
	sortVersionsNewestFirst(versions)
	for _, ver := range versions {
		if incompatibleReason(ver.Compat, xlyra) != "" {
			continue
		}
		if securityDisabled(rev, plugin.ID, ver) {
			continue
		}
		if !versionOlder(localVersion, ver.Version) {
			continue
		}
		next := candidateFrom(plugin, ver, rev, installed[ver.Version])
		next.IncompatibleReason = ""
		return next, ver, true
	}
	return candidateFacts{}, marketproto.PluginVersion{}, false
}

func candidateFrom(plugin marketproto.Plugin, ver marketproto.PluginVersion, rev *marketproto.Revocations, installed versionFacts) candidateFacts {
	att, ok := attestationBody(ver.Attestation)
	fp := ""
	kind := plugin.Kind
	hosts := ver.Hosts
	perms := ver.Permissions
	var events []string
	if ok {
		if att.DeveloperFingerprint != "" {
			fp = att.DeveloperFingerprint
		}
		if att.Kind != "" {
			kind = att.Kind
		}
	}
	siteType := ""
	if installed.Version != "" {
		if installed.Kind != "" {
			kind = installed.Kind
		}
		if len(installed.Hosts) > 0 || installed.Kind == jsplugin.KindSiteProvider {
			hosts = installed.Hosts
		}
		if len(installed.Permissions) > 0 || installed.Kind == jsplugin.KindAutomation {
			perms = installed.Permissions
		}
		if len(installed.Events) > 0 || installed.Kind == jsplugin.KindAutomation {
			events = append([]string(nil), installed.Events...)
		}
		siteType = installed.SiteType
		if fp == "" {
			fp = installed.Signer
		}
	}
	return candidateFacts{
		Version:            ver.Version,
		Fingerprint:        fp,
		Kind:               kind,
		Hosts:              hosts,
		Permissions:        perms,
		Events:             events,
		SiteType:           siteType,
		IncompatibleReason: incompatibleReason(ver.Compat, ""),
		SecurityRevoked:    securityDisabled(rev, plugin.ID, ver),
		Deprecated:         deprecatedEffect(rev, plugin.ID, ver),
	}
}

func upgradeBlockReason(cur versionFacts, next candidateFacts, revocationsExpired bool) (string, string) {
	if next.IncompatibleReason != "" {
		return next.IncompatibleReason, ""
	}
	if cur.Signer == "" || next.Fingerprint == "" || cur.Signer != next.Fingerprint {
		return UpdateReasonDeveloper, ""
	}
	if cur.Kind == "" || next.Kind == "" || cur.Kind != next.Kind {
		return UpdateReasonKind, ""
	}
	if added := addedStrings(next.Hosts, cur.Hosts); len(added) > 0 {
		return UpdateReasonHosts, strings.Join(added, ", ")
	}
	if added := addedStrings(next.Permissions, cur.Permissions); len(added) > 0 {
		return UpdateReasonPermissions, strings.Join(added, ", ")
	}
	if added := addedStrings(next.Events, cur.Events); len(added) > 0 {
		return UpdateReasonEvents, strings.Join(added, ", ")
	}
	curVer, curErr := marketproto.ParseVersion(cur.Version)
	nextVer, nextErr := marketproto.ParseVersion(next.Version)
	if curErr != nil || nextErr != nil || curVer.Major != nextVer.Major {
		return UpdateReasonMajor, ""
	}
	siteUnknown := false
	if cur.Kind == jsplugin.KindSiteProvider || next.Kind == jsplugin.KindSiteProvider {
		if cur.SiteType == "" || next.SiteType == "" {
			siteUnknown = true
		} else if cur.SiteType != next.SiteType {
			return UpdateReasonSiteType, ""
		}
	}
	if next.SecurityRevoked {
		return UpdateReasonRevoked, ""
	}
	if next.Deprecated {
		return UpdateReasonDeprecated, ""
	}
	if revocationsExpired {
		return UpdateReasonRevocations, ""
	}
	if cur.Kind == jsplugin.KindPricingParse || next.Kind == jsplugin.KindPricingParse {
		return UpdateReasonPricing, ""
	}
	if siteUnknown {
		return reasonNeedPackage, ""
	}
	return "", ""
}

func (s *Service) applyAutoUpdates(ctx context.Context, snap Snapshot, globalAuto bool) error {
	if s == nil || s.runtime == nil || snap.Index == nil || snap.RevocationsExpired || !globalAuto {
		return nil
	}
	locals, err := s.repo.ListVersionHeaders(ctx)
	if err != nil {
		return err
	}
	subs, rolled, _, err := s.updateContext(ctx)
	if err != nil {
		return err
	}
	grouped := groupLocals(locals)
	xlyra := appversion.Current().Version
	for _, plugin := range snap.Index.Plugins {
		view := describeUpdate(plugin, snap.Revocations, grouped[plugin.ID], subs[plugin.ID], xlyra, snap.RevocationsExpired, globalAuto, rolled[plugin.ID])
		if !view.attempt() {
			continue
		}
		if err := s.upgradeOne(ctx, plugin, view); err != nil {
			slog.Warn("plugin market auto update failed", "plugin_id", plugin.ID, "version", view.UpdateVersion, "error", err)
		}
	}
	return nil
}

func (v UpdateView) attempt() bool {
	if v.Policy != store.JSPluginUpdatePolicyAuto || !v.UpdateAvailable || v.BlockReason != "" {
		return false
	}
	return v.AutoOK || v.NeedsPackage
}

func (s *Service) upgradeOne(ctx context.Context, plugin marketproto.Plugin, view UpdateView) error {
	latest, err := s.repo.LatestAutoUpdate(ctx, plugin.ID, view.UpdateVersion)
	if err == nil && latest.Status == store.JSPluginAutoUpdateFailed && time.Since(latest.CreatedAt) < autoRetryWindow {
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	curRow, curFacts, err := s.enabledFacts(ctx, plugin.ID)
	if err != nil {
		return err
	}
	if curFacts.Status != store.JSPluginStatusEnabled {
		return nil
	}
	row, err := s.versionForAuto(ctx, plugin.ID, view.UpdateVersion, listedPackageDigest(plugin.Versions, view.UpdateVersion))
	if err != nil {
		digest := ""
		if errors.Is(err, errAutoHashMismatch) {
			if local, getErr := s.repo.GetVersion(ctx, plugin.ID, view.UpdateVersion); getErr == nil {
				digest = local.PackageSHA256
			}
		}
		s.recordAuto(ctx, plugin.ID, curFacts.Version, view.UpdateVersion, store.JSPluginAutoUpdateFailed, err.Error(), digest)
		if errors.Is(err, errAutoHashMismatch) {
			return nil
		}
		return err
	}
	pkg, err := jsplugin.ReadPackage(row.Package, true)
	if err != nil {
		s.recordAuto(ctx, plugin.ID, curFacts.Version, view.UpdateVersion, store.JSPluginAutoUpdateFailed, err.Error(), row.PackageSHA256)
		return err
	}
	next := candidateFacts{
		Version:     pkg.Manifest.Version,
		Fingerprint: pkg.Signer,
		Kind:        pkg.Manifest.Kind,
		Hosts:       pkg.Manifest.SiteProvider.Hosts,
		Permissions: pkg.Manifest.Automation.Permissions,
		Events:      pkg.Manifest.Automation.Subscribes,
		SiteType:    strings.ToLower(strings.TrimSpace(pkg.Manifest.SiteProvider.SiteType)),
	}
	if reason, detail := upgradeBlockReason(curFacts, next, false); reason != "" && reason != reasonNeedPackage {
		s.audit(ctx, "js_plugin.update_available", plugin.ID, true, map[string]any{
			"actor": auditActorAutoUpdate, "from_version": curFacts.Version, "to_version": view.UpdateVersion,
			"reason": reason, "detail": detail, "package_sha256": row.PackageSHA256,
		})
		_ = s.repo.SetMarketNotifiedVersion(ctx, plugin.ID, view.UpdateVersion)
		return nil
	}
	if err := s.enableVersion(ctx, plugin.ID, row.Version, pkg); err != nil {
		s.recordAuto(ctx, plugin.ID, curRow.Version, row.Version, store.JSPluginAutoUpdateFailed, err.Error(), row.PackageSHA256)
		if errors.Is(err, errAutoNeedsConfirmation) {
			return nil
		}
		return err
	}
	s.recordAuto(ctx, plugin.ID, curRow.Version, row.Version, store.JSPluginAutoUpdateApplied, "", row.PackageSHA256)
	return nil
}

func (s *Service) enabledFacts(ctx context.Context, pluginID string) (store.JSPluginVersion, versionFacts, error) {
	rows, err := s.repo.ListVersions(ctx, pluginID)
	if err != nil {
		return store.JSPluginVersion{}, versionFacts{}, err
	}
	for _, row := range rows {
		if row.Status == store.JSPluginStatusEnabled {
			return row, factsFromRow(row), nil
		}
	}
	return store.JSPluginVersion{}, versionFacts{}, nil
}

func (s *Service) versionForAuto(ctx context.Context, pluginID, version, indexDigest string) (store.JSPluginVersion, error) {
	row, err := s.repo.GetVersion(ctx, pluginID, version)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return store.JSPluginVersion{}, err
	}
	if err == nil && indexDigest != "" && row.PackageSHA256 == indexDigest {
		return row, nil
	}
	installed, installErr := s.Install(ctx, "", pluginID, version)
	if installErr != nil {
		var conflict *InstallError
		if errors.As(installErr, &conflict) && conflict.Code == InstallCodeVersionConflict {
			return store.JSPluginVersion{}, errAutoHashMismatch
		}
		return store.JSPluginVersion{}, installErr
	}
	return installed, nil
}

func listedPackageDigest(versions []marketproto.PluginVersion, version string) string {
	for _, ver := range versions {
		if ver.Version == version {
			return ver.PackageDigest
		}
	}
	return ""
}

func (s *Service) enableVersion(ctx context.Context, pluginID, version string, pkg jsplugin.Package) error {
	row, err := s.repo.GetVersion(ctx, pluginID, version)
	if err != nil {
		return err
	}
	allowed, err := s.autoEnableAllowed(ctx, row)
	if err != nil {
		return err
	}
	if !allowed {
		return errAutoNeedsConfirmation
	}
	opts := jsplugin.EnableOptions{}
	if pkg.Manifest.Kind == jsplugin.KindSiteProvider {
		opts.ConfirmedHosts = append([]string(nil), pkg.Manifest.SiteProvider.Hosts...)
	}
	if pkg.Manifest.Kind == jsplugin.KindAutomation {
		opts.GrantPermissions = append([]string(nil), pkg.Manifest.Automation.Permissions...)
	}
	err = s.runtime.Enable(ctx, pluginID, version, opts)
	var confirm *jsplugin.ConfirmRequiredError
	if errors.As(err, &confirm) {
		return errAutoNeedsConfirmation
	}
	return err
}

func (s *Service) autoEnableAllowed(ctx context.Context, row store.JSPluginVersion) (bool, error) {
	if strings.TrimSpace(row.Signer) != "" {
		trusted, err := s.repo.IsTrustedFingerprint(ctx, row.Signer)
		if err != nil {
			return false, err
		}
		if trusted {
			return true, nil
		}
	}
	if !s.VerifyVersionAttestation(ctx, row) {
		return false, nil
	}
	state, err := s.repo.GetMarketState(ctx)
	if err != nil {
		return false, err
	}
	snap, err := snapshotFrom(state, s.rootKeys, time.Now())
	if err != nil {
		return false, err
	}
	indexed, _, ok := indexedVersion(snap.Index, row.PluginID, row.Version)
	if !ok || indexed.PackageDigest != row.PackageSHA256 {
		return false, nil
	}
	return true, nil
}

func (s *Service) recordAuto(ctx context.Context, pluginID, from, to, status, reason, digest string) {
	if _, err := s.repo.CreateAutoUpdate(ctx, store.JSPluginAutoUpdate{
		PluginID: pluginID, FromVersion: from, ToVersion: to, Status: status, Reason: reason,
	}); err != nil {
		slog.Warn("plugin market auto update record failed", "plugin_id", pluginID, "error", err)
	}
	s.audit(ctx, "js_plugin.auto_update", pluginID, status == store.JSPluginAutoUpdateApplied, map[string]any{
		"actor": auditActorAutoUpdate, "from_version": from, "to_version": to,
		"package_sha256": digest, "status": status, "reason": reason,
	})
}

func (s *Service) notifyUpdates(ctx context.Context, snap Snapshot, globalAuto bool) error {
	if s == nil || snap.Index == nil || s.auditor == nil {
		return nil
	}
	locals, err := s.repo.ListVersionHeaders(ctx)
	if err != nil {
		return err
	}
	subs, rolled, _, err := s.updateContext(ctx)
	if err != nil {
		return err
	}
	grouped := groupLocals(locals)
	xlyra := appversion.Current().Version
	for _, plugin := range snap.Index.Plugins {
		sub := subs[plugin.ID]
		view := describeUpdate(plugin, snap.Revocations, grouped[plugin.ID], sub, xlyra, snap.RevocationsExpired, globalAuto, rolled[plugin.ID])
		if !view.UpdateAvailable || view.AutoOK || view.UpdateVersion == "" || sub.NotifiedVersion == view.UpdateVersion {
			continue
		}
		s.audit(ctx, "js_plugin.update_available", plugin.ID, true, map[string]any{
			"actor": auditActorAutoUpdate, "to_version": view.UpdateVersion,
			"reason": view.BlockReason, "detail": view.BlockDetail,
		})
		if err := s.repo.SetMarketNotifiedVersion(ctx, plugin.ID, view.UpdateVersion); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) audit(ctx context.Context, action, pluginID string, success bool, metadata map[string]any) {
	if s == nil || s.auditor == nil {
		return
	}
	s.auditor.RecordAudit(ctx, auth.AuditInput{
		Actor:        auth.AdminActor{Type: "system"},
		Action:       action,
		ResourceType: "js_plugin",
		ResourceID:   pluginID,
		Success:      success,
		Metadata:     metadata,
	})
}

func (s *Service) RollbackAutoUpdate(ctx context.Context, pluginID, version, breakerReason string) {
	if s == nil || s.runtime == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	row, err := s.repo.FindRecentAppliedAutoUpdate(ctx, pluginID, version, autoRollbackWindow)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			slog.Warn("plugin market auto rollback lookup failed", "plugin_id", pluginID, "error", err)
		}
		return
	}
	if err := s.repo.SetMarketPausedReason(ctx, pluginID, pausedAutoRollback); err != nil {
		slog.Warn("plugin market pause auto update failed", "plugin_id", pluginID, "error", err)
	}
	previous, err := s.repo.GetVersion(ctx, pluginID, row.FromVersion)
	if err != nil {
		_ = s.repo.UpdateAutoUpdateStatus(ctx, row.ID, store.JSPluginAutoUpdateFailed, err.Error())
		s.auditRollback(ctx, pluginID, row.FromVersion, version, false, err.Error())
		return
	}
	pkg, err := jsplugin.ReadPackage(previous.Package, true)
	if err != nil {
		_ = s.repo.UpdateAutoUpdateStatus(ctx, row.ID, store.JSPluginAutoUpdateFailed, err.Error())
		s.auditRollback(ctx, pluginID, row.FromVersion, version, false, err.Error())
		return
	}
	revoked, err := s.rollbackTargetRevoked(ctx, previous)
	if err != nil || revoked {
		reason := UpdateReasonRevoked
		if err != nil {
			reason = err.Error()
		}
		_ = s.repo.UpdateAutoUpdateStatus(ctx, row.ID, store.JSPluginAutoUpdateFailed, reason)
		s.auditRollback(ctx, pluginID, row.FromVersion, version, false, reason)
		return
	}
	if err := s.enableVersion(ctx, pluginID, previous.Version, pkg); err != nil {
		_ = s.repo.UpdateAutoUpdateStatus(ctx, row.ID, store.JSPluginAutoUpdateFailed, err.Error())
		s.auditRollback(ctx, pluginID, row.FromVersion, version, false, err.Error())
		return
	}
	reason := strings.TrimSpace(breakerReason)
	if err := s.repo.UpdateAutoUpdateStatus(ctx, row.ID, store.JSPluginAutoUpdateRolledBack, reason); err != nil {
		slog.Warn("plugin market auto rollback record failed", "plugin_id", pluginID, "error", err)
	}
	s.auditRollback(ctx, pluginID, row.FromVersion, version, true, reason)
}

func (s *Service) rollbackTargetRevoked(ctx context.Context, previous store.JSPluginVersion) (bool, error) {
	snap, err := s.Snapshot(ctx)
	if err != nil {
		return false, err
	}
	for _, effect := range marketproto.MatchRevocations(snap.Revocations, localTarget(previous)) {
		if effect.Action == "disable" {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) applyAutoReason(ctx context.Context, pluginID string, view UpdateView) UpdateView {
	if view.BlockReason != "" || !view.UpdateAvailable || view.UpdateVersion == "" {
		return view
	}
	return autoReason(view, s.recentAutoUpdate(ctx, pluginID, view.UpdateVersion), time.Now())
}

func autoReason(view UpdateView, latest store.JSPluginAutoUpdate, now time.Time) UpdateView {
	if view.BlockReason != "" || !view.UpdateAvailable || view.UpdateVersion == "" {
		return view
	}
	if latest.ToVersion != view.UpdateVersion || latest.Status != store.JSPluginAutoUpdateFailed || latest.Reason != reasonHashMismatch {
		return view
	}
	if latest.CreatedAt.IsZero() || now.Sub(latest.CreatedAt) >= autoRetryWindow {
		return view
	}
	view.BlockReason = reasonHashMismatch
	view.AutoOK = false
	view.NeedsPackage = false
	return view
}

func (s *Service) recentAutoUpdate(ctx context.Context, pluginID, version string) store.JSPluginAutoUpdate {
	if s == nil || pluginID == "" || version == "" {
		return store.JSPluginAutoUpdate{}
	}
	if s.fixture != nil {
		if s.fixture.AutoFailures == nil {
			return store.JSPluginAutoUpdate{}
		}
		return s.fixture.AutoFailures[pluginID]
	}
	row, err := s.repo.LatestAutoUpdate(ctx, pluginID, version)
	if err != nil {
		return store.JSPluginAutoUpdate{}
	}
	return row
}

func (s *Service) auditRollback(ctx context.Context, pluginID, from, to string, success bool, reason string) {
	s.audit(ctx, "js_plugin.auto_rollback", pluginID, success, map[string]any{
		"actor": auditActorAutoUpdate, "from_version": from, "to_version": to, "reason": reason,
	})
}

func (s *Service) backfillAttestations(ctx context.Context, snap Snapshot) error {
	if s == nil || snap.Index == nil || snap.Root == nil || snap.RevocationsExpired {
		return nil
	}
	rows, err := s.repo.ListVersionHeaders(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, row := range rows {
		if localAttested(row) {
			continue
		}
		ver, plugin, ok := indexedDigest(snap.Index, row.PluginID, row.PackageSHA256)
		if !ok || securityDisabled(snap.Revocations, plugin.ID, ver) {
			continue
		}
		full, err := s.repo.GetVersion(ctx, row.PluginID, row.Version)
		if err != nil {
			return err
		}
		if err := s.attachAttestation(ctx, snap, plugin, ver, full, now); err != nil {
			slog.Warn("plugin market attestation backfill skipped", "plugin_id", row.PluginID, "version", row.Version, "error", err)
		}
	}
	return nil
}

func (s *Service) attachAttestation(ctx context.Context, snap Snapshot, plugin marketproto.Plugin, ver marketproto.PluginVersion, row store.JSPluginVersion, now time.Time) error {
	att, err := marketproto.VerifyAttestation(ver.Attestation, snap.Root, now)
	if err != nil {
		return err
	}
	pkg, err := jsplugin.ReadPackage(row.Package, true)
	if err != nil {
		return err
	}
	if err := marketproto.CheckPackage(att, packageFacts(pkg), plugin.Developer.Fingerprints); err != nil {
		return err
	}
	if att.PluginID != row.PluginID || att.Version != row.Version || att.PackageDigest != row.PackageSHA256 {
		return errors.New("attestation does not match the installed package")
	}
	raw, err := json.Marshal(ver.Attestation)
	if err != nil {
		return err
	}
	return s.repo.UpdateVersionAttestation(ctx, row.PluginID, row.Version, store.JSPluginOriginMarket, store.JSON(raw))
}

func indexedDigest(idx *marketproto.Index, pluginID, digest string) (marketproto.PluginVersion, marketproto.Plugin, bool) {
	if idx == nil || digest == "" {
		return marketproto.PluginVersion{}, marketproto.Plugin{}, false
	}
	for _, plugin := range idx.Plugins {
		if plugin.ID != pluginID {
			continue
		}
		for _, ver := range plugin.Versions {
			if ver.PackageDigest == digest {
				return ver, plugin, true
			}
		}
	}
	return marketproto.PluginVersion{}, marketproto.Plugin{}, false
}

func factsFromRow(row store.JSPluginVersion) versionFacts {
	fact := versionFacts{
		Version:  row.Version,
		Status:   row.Status,
		Signer:   strings.TrimSpace(row.Signer),
		Digest:   row.PackageSHA256,
		Attested: localAttested(row),
	}
	if len(row.Manifest) == 0 {
		return fact
	}
	var manifest jsplugin.Manifest
	if err := json.Unmarshal(row.Manifest, &manifest); err != nil {
		return fact
	}
	fact.Kind = manifest.Kind
	fact.Hosts = append([]string(nil), manifest.SiteProvider.Hosts...)
	fact.Permissions = append([]string(nil), manifest.Automation.Permissions...)
	fact.Events = append([]string(nil), manifest.Automation.Subscribes...)
	fact.SiteType = strings.ToLower(strings.TrimSpace(manifest.SiteProvider.SiteType))
	return fact
}

func attestationBody(env marketproto.Envelope) (marketproto.Attestation, bool) {
	if strings.TrimSpace(env.Payload) == "" {
		return marketproto.Attestation{}, false
	}
	raw, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return marketproto.Attestation{}, false
	}
	var att marketproto.Attestation
	if err := json.Unmarshal(raw, &att); err != nil {
		return marketproto.Attestation{}, false
	}
	return att, true
}

func deprecatedEffect(rev *marketproto.Revocations, pluginID string, ver marketproto.PluginVersion) bool {
	for _, effect := range marketproto.MatchRevocations(rev, marketproto.Target{
		PluginID: pluginID, Version: ver.Version, PackageDigest: ver.PackageDigest, Attested: true,
	}) {
		if effect.Action != "notify" {
			continue
		}
		entry, ok := revocationEntry(rev, effect.EntryID)
		if ok && entry.Level == "deprecated" {
			return true
		}
	}
	return false
}

func signerRevokedEffect(rev *marketproto.Revocations, row store.JSPluginVersion) bool {
	if localAttested(row) {
		return false
	}
	for _, effect := range marketproto.MatchRevocations(rev, localTarget(row)) {
		if effect.Action != "notify" {
			continue
		}
		entry, ok := revocationEntry(rev, effect.EntryID)
		if ok && entry.Target.Type == "developerKey" {
			return true
		}
	}
	return false
}

func markVersion(rev *marketproto.Revocations, row store.JSPluginVersion, rolled map[string]struct{}) VersionMark {
	var mark VersionMark
	if _, ok := rolled[row.Version]; ok {
		mark.AutoRolledBack = true
	}
	for _, effect := range marketproto.MatchRevocations(rev, localTarget(row)) {
		entry, ok := revocationEntry(rev, effect.EntryID)
		if !ok {
			continue
		}
		if effect.Action == "notify" && entry.Target.Type == "developerKey" {
			mark.SignerRevoked = true
			mark.SignerReason = entry.Reason
			continue
		}
		if entry.Level != "security" && entry.Level != "deprecated" {
			continue
		}
		if mark.Level == "security" && entry.Level != "security" {
			continue
		}
		mark.Revoked = true
		mark.Level = entry.Level
		mark.Reason = entry.Reason
	}
	return mark
}

func addedStrings(next, cur []string) []string {
	have := map[string]struct{}{}
	for _, item := range cur {
		item = strings.TrimSpace(item)
		if item != "" {
			have[item] = struct{}{}
		}
	}
	seen := map[string]struct{}{}
	var extra []string
	for _, item := range next {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := have[item]; ok {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		extra = append(extra, item)
	}
	sort.Strings(extra)
	return extra
}
