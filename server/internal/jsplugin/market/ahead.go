package market

import (
	"context"
	"strings"

	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/marketproto"
	"xlyra/server/internal/store"
)

// CachedIndex returns the signed index when one is loaded.
// The boolean is false when there is no index to compare against.
func (s *Service) CachedIndex(ctx context.Context) (*marketproto.Index, bool) {
	if s == nil {
		return nil, false
	}
	if s.fixture != nil {
		if s.fixture.Snapshot.Index == nil {
			return nil, false
		}
		return s.fixture.Snapshot.Index, true
	}
	snap, err := s.Snapshot(ctx)
	if err != nil || snap.Index == nil {
		return nil, false
	}
	return snap.Index, true
}

// Ahead reports whether local is newer than the plugin's newest indexed version.
// A ready index that does not list the plugin is ahead, with an empty market version.
func Ahead(local string, index *marketproto.Index, pluginID string) (ahead bool, marketVersion string) {
	if index == nil {
		return false, ""
	}
	var versions []marketproto.PluginVersion
	found := false
	for _, plugin := range index.Plugins {
		if plugin.ID != pluginID {
			continue
		}
		found = true
		versions = plugin.Versions
		break
	}
	if !found {
		return true, ""
	}
	marketVersion = newestIndexedVersion(versions)
	if marketVersion == "" {
		return true, ""
	}
	return versionGreater(local, marketVersion), marketVersion
}

// CandidateVersion is the installed version compared with the market.
// An enabled version wins; otherwise the highest MAJOR.MINOR.PATCH wins.
func CandidateVersion(versions []store.JSPluginVersion) (store.JSPluginVersion, bool) {
	var enabled store.JSPluginVersion
	var hasEnabled bool
	var best store.JSPluginVersion
	var hasBest bool
	for _, row := range versions {
		ver := strings.TrimSpace(row.Version)
		if _, err := marketproto.ParseVersion(ver); err != nil {
			continue
		}
		if row.Status == store.JSPluginStatusEnabled && (!hasEnabled || versionGreater(ver, enabled.Version)) {
			enabled = row
			hasEnabled = true
		}
		if !hasBest || versionGreater(ver, best.Version) {
			best = row
			hasBest = true
		}
	}
	if hasEnabled {
		return enabled, true
	}
	if hasBest {
		return best, true
	}
	return jsplugin.SelectPluginVersion(versions)
}

func newestIndexedVersion(versions []marketproto.PluginVersion) string {
	best := ""
	for _, item := range versions {
		ver := strings.TrimSpace(item.Version)
		if ver == "" {
			continue
		}
		if _, err := marketproto.ParseVersion(ver); err != nil {
			continue
		}
		if best == "" || versionGreater(ver, best) {
			best = ver
		}
	}
	return best
}

func versionGreater(local, market string) bool {
	left, err := marketproto.ParseVersion(strings.TrimSpace(local))
	if err != nil {
		return false
	}
	right, err := marketproto.ParseVersion(strings.TrimSpace(market))
	if err != nil {
		return false
	}
	return marketproto.Compare(left, right) > 0
}
