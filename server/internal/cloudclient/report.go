package cloudclient

import (
	"time"

	"xlyra/server/internal/store"
)

// Report is the heartbeat body. The JSON keys are the only fields ever sent.
type Report struct {
	XLyraVersion string       `json:"xlyraVersion"`
	Platform     Platform     `json:"platform"`
	Plugins      []PluginItem `json:"plugins"`
	Counts       Counts       `json:"counts"`
	SentAt       string       `json:"sentAt"`
}

type Platform struct {
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Runtime string `json:"runtime"`
}

type PluginItem struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Status  string `json:"status"`
	Origin  string `json:"origin"`
	Enabled bool   `json:"enabled"`
}

type Counts struct {
	Plugins        int `json:"plugins"`
	EnabledPlugins int `json:"enabledPlugins"`
}

// MapPluginStatus collapses stored version statuses onto the heartbeat vocabulary.
func MapPluginStatus(status string) (string, bool) {
	switch status {
	case store.JSPluginStatusEnabled:
		return "enabled", true
	case store.JSPluginStatusBroken:
		return "broken", false
	default:
		return "disabled", false
	}
}

// MapOrigin maps a plugin source and version origin onto market, manual, or builtin.
func MapOrigin(source, origin string) string {
	if source == "builtin" {
		return "builtin"
	}
	if origin == store.JSPluginOriginMarket {
		return "market"
	}
	return "manual"
}

// SelectPlugins picks one current version per plugin: an enabled version wins,
// otherwise the lexicographically greatest version string (version headers do not carry created_at).
func SelectPlugins(plugins []store.JSPlugin, versions []store.JSPluginVersion) []PluginItem {
	sourceByID := make(map[string]string, len(plugins))
	order := make([]string, 0, len(plugins))
	for _, plugin := range plugins {
		if plugin.ID == "" {
			continue
		}
		if _, ok := sourceByID[plugin.ID]; ok {
			continue
		}
		sourceByID[plugin.ID] = plugin.Source
		order = append(order, plugin.ID)
	}
	chosen := map[string]store.JSPluginVersion{}
	for _, version := range versions {
		if version.PluginID == "" || version.Version == "" {
			continue
		}
		if _, ok := sourceByID[version.PluginID]; !ok {
			continue
		}
		current, ok := chosen[version.PluginID]
		if !ok || betterVersion(version, current) {
			chosen[version.PluginID] = version
		}
	}
	items := make([]PluginItem, 0, len(chosen))
	for _, id := range order {
		version, ok := chosen[id]
		if !ok {
			continue
		}
		status, enabled := MapPluginStatus(version.Status)
		items = append(items, PluginItem{
			ID:      id,
			Version: version.Version,
			Status:  status,
			Origin:  MapOrigin(sourceByID[id], version.Origin),
			Enabled: enabled,
		})
	}
	return items
}

func betterVersion(next, current store.JSPluginVersion) bool {
	nextEnabled := next.Status == store.JSPluginStatusEnabled
	currentEnabled := current.Status == store.JSPluginStatusEnabled
	if nextEnabled != currentEnabled {
		return nextEnabled
	}
	return next.Version > current.Version
}

// BuildReport assembles the heartbeat document. sentAt is RFC3339 UTC.
func BuildReport(version string, platform Platform, plugins []PluginItem, sentAt time.Time) Report {
	if plugins == nil {
		plugins = []PluginItem{}
	}
	enabled := 0
	for _, plugin := range plugins {
		if plugin.Enabled {
			enabled++
		}
	}
	if sentAt.IsZero() {
		sentAt = time.Now()
	}
	return Report{
		XLyraVersion: version,
		Platform:     platform,
		Plugins:      plugins,
		Counts:       Counts{Plugins: len(plugins), EnabledPlugins: enabled},
		SentAt:       sentAt.UTC().Format(time.RFC3339),
	}
}

func applyPollStatus(status string) (next, lastError string, done bool) {
	switch status {
	case "approved":
		return store.CloudStatusBound, "", true
	case "denied":
		return store.CloudStatusUnbound, "配对被拒绝", true
	case "expired":
		return store.CloudStatusUnbound, "配对已过期", true
	default:
		return store.CloudStatusPairing, "", false
	}
}

func nextBackoff(current, min, max time.Duration) time.Duration {
	if min <= 0 {
		min = 30 * time.Second
	}
	if max < min {
		max = min
	}
	if current < min {
		return min
	}
	next := current * 2
	if next > max || next < current {
		return max
	}
	return next
}
