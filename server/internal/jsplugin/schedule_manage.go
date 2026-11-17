package jsplugin

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"xlyra/server/internal/store"
)

// PluginScheduleView describes a plugin's periodic onSchedule configuration and toggle state.
type PluginScheduleView struct {
	Supported    bool          `json:"supported"`
	Enabled      bool          `json:"enabled"`
	EveryMinutes int           `json:"every_minutes,omitempty"`
	Label        LocalizedText `json:"label,omitempty"`
	NextTickAt   *string       `json:"next_tick_at,omitempty"`
}

// GetPluginSchedule returns the schedule configuration and current toggle state for one plugin.
func (m *Manager) GetPluginSchedule(ctx context.Context, pluginID string) (PluginScheduleView, error) {
	if m == nil {
		return PluginScheduleView{}, errors.New("js plugin manager is not available")
	}
	if _, err := m.repo.GetPlugin(ctx, pluginID); err != nil {
		return PluginScheduleView{}, err
	}
	versions, err := m.repo.ListVersions(ctx, pluginID)
	if err != nil {
		return PluginScheduleView{}, err
	}
	selected, ok := SelectPluginVersion(versions)
	if !ok {
		return PluginScheduleView{}, gorm.ErrRecordNotFound
	}
	manifest, err := ParseManifest(selected.Manifest)
	if err != nil {
		return PluginScheduleView{}, err
	}
	hasHook := contains(manifest.Exports, HookOnSchedule)
	if plugin, loaded := m.catalog.Registry().ByPluginID(pluginID); loaded && plugin != nil {
		hasHook = plugin.HasHook(HookOnSchedule)
		manifest = plugin.Manifest
	}
	if !manifest.AllowsSchedule() && !hasHook && manifest.Schedule.Empty() {
		return PluginScheduleView{Supported: false}, nil
	}
	view := PluginScheduleView{
		Supported:    true,
		EveryMinutes: manifest.EffectiveScheduleMinutes(),
		Label:        manifest.Schedule.Label,
	}
	binding, err := m.repo.GetScheduleBinding(ctx, pluginID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return view, nil
		}
		return PluginScheduleView{}, err
	}
	view.Enabled = scheduleBindingEnabled(binding)
	if binding.NextTickAt != nil && !binding.NextTickAt.IsZero() {
		formatted := binding.NextTickAt.UTC().Format(time.RFC3339)
		view.NextTickAt = &formatted
	}
	return view, nil
}

// UpdatePluginSchedule enables or disables the periodic onSchedule runner for an enabled plugin.
// Enabling writes NextTickAt so the automation loop picks it up on the next poll.
func (m *Manager) UpdatePluginSchedule(ctx context.Context, pluginID string, enabled bool) (PluginScheduleView, error) {
	if m == nil {
		return PluginScheduleView{}, errors.New("js plugin manager is not available")
	}
	plugin, ok := m.catalog.Registry().ByPluginID(pluginID)
	if !ok || plugin == nil {
		return PluginScheduleView{}, inputErrorf("plugin %q is not enabled", pluginID)
	}
	if !plugin.HasHook(HookOnSchedule) || !plugin.Manifest.AllowsSchedule() {
		return PluginScheduleView{}, inputErrorf("plugin %q does not declare a schedule", pluginID)
	}
	version, err := m.repo.GetVersion(ctx, pluginID, plugin.Manifest.Version)
	if err != nil {
		return PluginScheduleView{}, err
	}
	granted := grantedSet(version.GrantedPermissions)
	if !granted[PermissionSchedule] {
		return PluginScheduleView{}, inputErrorf("schedule permission is not granted for plugin %q", pluginID)
	}
	var nextTick *time.Time
	if enabled {
		now := time.Now().UTC()
		nextTick = &now
	}
	if _, err := m.repo.UpsertScheduleBinding(ctx, pluginID, plugin.Manifest.Version, enabled, nextTick); err != nil {
		return PluginScheduleView{}, err
	}
	if enabled {
		select {
		case m.wakeup <- struct{}{}:
		default:
		}
	}
	return m.GetPluginSchedule(ctx, pluginID)
}

func scheduleBindingEnabled(binding store.JSPluginBinding) bool {
	if binding.NextTickAt == nil {
		return false
	}
	var cfg struct {
		Enabled bool `json:"enabled"`
	}
	if len(binding.Config) > 0 && json.Unmarshal(binding.Config, &cfg) == nil {
		return cfg.Enabled
	}
	return true
}
