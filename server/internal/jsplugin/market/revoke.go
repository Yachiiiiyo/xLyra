package market

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"gorm.io/gorm"

	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/marketproto"
	"xlyra/server/internal/store"
)

func (s *Service) applyRevocations(ctx context.Context, snap Snapshot) error {
	if s == nil || s.runtime == nil || snap.Revocations == nil || snap.RevocationsExpired {
		return nil
	}
	rows, err := s.repo.ListVersionHeaders(ctx)
	if err != nil {
		return err
	}
	subs, err := s.repo.ListMarketSubscriptions(ctx)
	if err != nil {
		return err
	}
	known := map[string]store.JSPluginMarketSubscription{}
	for _, sub := range subs {
		known[sub.PluginID] = sub
	}
	disabled := map[string]struct{}{}
	for _, row := range rows {
		effect, ok := disableEffect(snap.Revocations, row)
		if !ok || row.Status != store.JSPluginStatusEnabled {
			known[row.PluginID] = s.noteRevocation(ctx, snap.Revocations, row, known[row.PluginID])
			continue
		}
		if _, done := disabled[row.PluginID]; done {
			known[row.PluginID] = s.noteRevocation(ctx, snap.Revocations, row, known[row.PluginID])
			continue
		}
		if err := s.runtime.Disable(ctx, row.PluginID); err != nil {
			slog.Warn("plugin market revocation disable failed", "plugin_id", row.PluginID, "version", row.Version, "error", err)
			continue
		}
		disabled[row.PluginID] = struct{}{}
		meta := map[string]any{
			"actor": auditActorRevocation, "entry_id": effect.EntryID, "version": row.Version,
			"package_sha256": row.PackageSHA256, "action": "disable",
		}
		if effect.RollbackTo != "" {
			rolled, rollErr := s.rollbackRevoked(ctx, snap, row, effect.RollbackTo)
			meta["rollback_to"] = effect.RollbackTo
			meta["rollback_ok"] = rollErr == nil && rolled
			if rollErr != nil {
				meta["rollback_error"] = rollErr.Error()
			}
		}
		s.audit(ctx, "js_plugin.revoke", row.PluginID, true, meta)
		known[row.PluginID] = s.noteRevocation(ctx, snap.Revocations, row, known[row.PluginID])
	}
	return nil
}

func disableEffect(rev *marketproto.Revocations, row store.JSPluginVersion) (marketproto.Effect, bool) {
	var chosen marketproto.Effect
	found := false
	for _, effect := range marketproto.MatchRevocations(rev, localTarget(row)) {
		if effect.Action != "disable" {
			continue
		}
		if !found || (chosen.RollbackTo == "" && effect.RollbackTo != "") {
			chosen = effect
			found = true
		}
	}
	return chosen, found
}

func (s *Service) noteRevocation(ctx context.Context, rev *marketproto.Revocations, row store.JSPluginVersion, sub store.JSPluginMarketSubscription) store.JSPluginMarketSubscription {
	if s == nil || s.auditor == nil {
		return sub
	}
	for _, effect := range marketproto.MatchRevocations(rev, localTarget(row)) {
		if effect.Action != "notify" {
			continue
		}
		if revocationNotified(sub.NotifiedRevocations, effect.EntryID) {
			continue
		}
		entry, _ := revocationEntry(rev, effect.EntryID)
		s.audit(ctx, "js_plugin.revoke", row.PluginID, true, map[string]any{
			"actor": auditActorRevocation, "entry_id": effect.EntryID, "version": row.Version,
			"package_sha256": row.PackageSHA256, "action": "notify", "level": entry.Level,
			"target": entry.Target.Type, "reason": entry.Reason,
		})
		if err := s.repo.RememberMarketRevocation(ctx, row.PluginID, effect.EntryID); err != nil {
			slog.Warn("plugin market revocation notice failed", "plugin_id", row.PluginID, "error", err)
			continue
		}
		sub.NotifiedRevocations = appendRevocationNotice(sub.NotifiedRevocations, effect.EntryID)
	}
	return sub
}

func (s *Service) rollbackRevoked(ctx context.Context, snap Snapshot, revoked store.JSPluginVersion, rollbackTo string) (bool, error) {
	rollbackTo = strings.TrimSpace(rollbackTo)
	if rollbackTo == "" || rollbackTo == revoked.Version {
		return false, nil
	}
	indexed, _, indexedOK := indexedVersion(snap.Index, revoked.PluginID, rollbackTo)
	if indexedOK && securityDisabled(snap.Revocations, revoked.PluginID, indexed) {
		return false, errors.New("rollback target is revoked")
	}
	digest := ""
	if indexedOK {
		digest = indexed.PackageDigest
	}
	local, getErr := s.repo.GetVersion(ctx, revoked.PluginID, rollbackTo)
	if getErr != nil && !errors.Is(getErr, gorm.ErrRecordNotFound) {
		return false, getErr
	}
	if (getErr != nil || digest == "" || local.PackageSHA256 != digest) && snap.RevocationsExpired {
		return false, errors.New("revocation list has expired")
	}
	row, err := s.versionForAuto(ctx, revoked.PluginID, rollbackTo, digest)
	if err != nil {
		return false, err
	}
	fullRevoked, err := s.repo.GetVersion(ctx, revoked.PluginID, revoked.Version)
	if err != nil {
		return false, err
	}
	oldPkg, err := jsplugin.ReadPackage(fullRevoked.Package, true)
	if err != nil {
		return false, err
	}
	nextPkg, err := jsplugin.ReadPackage(row.Package, true)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(nextPkg.Signer) == "" || nextPkg.Signer != oldPkg.Signer {
		return false, errors.New("rollback developer does not match")
	}
	if oldPkg.Manifest.Kind == "" || nextPkg.Manifest.Kind == "" || oldPkg.Manifest.Kind != nextPkg.Manifest.Kind {
		return false, errors.New("rollback changes kind")
	}
	if oldPkg.Manifest.Kind == jsplugin.KindSiteProvider || nextPkg.Manifest.Kind == jsplugin.KindSiteProvider {
		oldType := strings.ToLower(strings.TrimSpace(oldPkg.Manifest.SiteProvider.SiteType))
		nextType := strings.ToLower(strings.TrimSpace(nextPkg.Manifest.SiteProvider.SiteType))
		if oldType != nextType {
			return false, errors.New("rollback changes site type")
		}
	}
	if added := addedStrings(nextPkg.Manifest.SiteProvider.Hosts, oldPkg.Manifest.SiteProvider.Hosts); len(added) > 0 {
		return false, errors.New("rollback adds hosts")
	}
	if added := addedStrings(nextPkg.Manifest.Automation.Permissions, oldPkg.Manifest.Automation.Permissions); len(added) > 0 {
		return false, errors.New("rollback adds permissions")
	}
	if err := s.enableVersion(ctx, revoked.PluginID, row.Version, nextPkg); err != nil {
		return false, err
	}
	return true, nil
}

func indexedVersion(idx *marketproto.Index, pluginID, version string) (marketproto.PluginVersion, marketproto.Plugin, bool) {
	if idx == nil {
		return marketproto.PluginVersion{}, marketproto.Plugin{}, false
	}
	for _, plugin := range idx.Plugins {
		if plugin.ID != pluginID {
			continue
		}
		for _, ver := range plugin.Versions {
			if ver.Version == version {
				return ver, plugin, true
			}
		}
	}
	return marketproto.PluginVersion{}, marketproto.Plugin{}, false
}

func revocationNotified(list, entryID string) bool {
	entryID = strings.TrimSpace(entryID)
	if entryID == "" {
		return true
	}
	for _, item := range strings.Split(list, ",") {
		if strings.TrimSpace(item) == entryID {
			return true
		}
	}
	return false
}

func appendRevocationNotice(list, entryID string) string {
	if revocationNotified(list, entryID) {
		return list
	}
	if strings.TrimSpace(list) == "" {
		return entryID
	}
	return list + "," + entryID
}
