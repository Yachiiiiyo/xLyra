package cloudclient

import (
	"encoding/json"
	"time"

	"xlyra/server/internal/store"
)

// Report is the heartbeat body. The JSON keys are the only fields ever sent.
// Devices is omitted entirely when the instance has no device source; an empty
// slice is sent as [] so the Hub drops devices that are no longer active.
type Report struct {
	XLyraVersion string       `json:"xlyraVersion"`
	Platform     Platform     `json:"platform"`
	Plugins      []PluginItem `json:"plugins"`
	Counts       Counts       `json:"counts"`
	SentAt       string       `json:"sentAt"`
	Devices      []DeviceItem `json:"devices,omitempty"`
	// withDevices 为真时即使清单为空也输出 "devices":[]。
	// 没有数据源时保持缺省, Hub 不会动已有设备。
	withDevices bool `json:"-"`
}

// DeviceItem is one row of the heartbeat device list. Ref is the device id.
type DeviceItem struct {
	Ref            string `json:"ref"`
	Name           string `json:"name"`
	Platform       string `json:"platform"`
	LastActiveAt   string `json:"lastActiveAt"`
	PushRegistered bool   `json:"pushRegistered"`
	PushEnv        string `json:"pushEnv"`
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

// MarshalJSON 在有设备数据源时保留空的 devices 数组。
// encoding/json 会把空切片当成 omitempty 丢掉, Hub 就分不清「没有数据源」和「一台都没有」。
func (r Report) MarshalJSON() ([]byte, error) {
	payload := map[string]any{
		"xlyraVersion": r.XLyraVersion,
		"platform":     r.Platform,
		"plugins":      r.Plugins,
		"counts":       r.Counts,
		"sentAt":       r.SentAt,
	}
	if r.withDevices {
		devices := r.Devices
		if devices == nil {
			devices = []DeviceItem{}
		}
		payload["devices"] = devices
	}
	return json.Marshal(payload)
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
