package jsplugin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"xlyra/server/internal/store"
)

// AutomationInputError is a problem with what the admin submitted.
type AutomationInputError struct{ Message string }

func (e *AutomationInputError) Error() string { return e.Message }

func inputErrorf(format string, args ...any) error {
	return &AutomationInputError{Message: fmt.Sprintf(format, args...)}
}

// AutomationBindingInput is what an admin submits to bind an automation plugin:
// for each input the plugin declares, the picked object id (a list of ids when
// the input is multiple) or the parameter value.
type AutomationBindingInput struct {
	Inputs map[string]any
}

// AutomationEntityView is a picked object with its display name.
type AutomationEntityView struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AutomationInputView is one input of a binding as the admin page shows it.
type AutomationInputView struct {
	Name     string                 `json:"name"`
	Entities []AutomationEntityView `json:"entities,omitempty"`
	Value    any                    `json:"value,omitempty"`
}

// AutomationBindingView is a binding as the admin page shows it.
type AutomationBindingView struct {
	ID        string                `json:"id"`
	Version   string                `json:"version"`
	Inputs    []AutomationInputView `json:"inputs"`
	State     map[string]any        `json:"state"`
	CreatedAt string                `json:"created_at"`
}

// AutomationOption is one object an admin may pick for an input. Disabled
// options are shown with the reason they cannot be picked.
type AutomationOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// IconKey names the brand to show next to the object (a site's type, an
	// account's provider); the console picks the logo.
	IconKey  string `json:"icon_key,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	// ReasonCode is stable, for the page to word in its own language; Reason is
	// the same in English.
	ReasonCode string `json:"reason_code,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

func (m *Manager) enabledAutomation(pluginID string) (*Plugin, error) {
	plugin, ok := m.catalog.Registry().ByPluginID(pluginID)
	if !ok || !plugin.Manifest.DeclaresAutomation() {
		return nil, inputErrorf("plugin %q is not an enabled automation plugin", pluginID)
	}
	return plugin, nil
}

func inputTitle(spec AutomationInputSpec) string {
	if title := spec.Title.Default(); title != "" {
		return title
	}
	return spec.Name
}

// pickedIDs reads an object input: one id, or a list of ids.
func pickedIDs(spec AutomationInputSpec, value any) ([]string, error) {
	var ids []string
	switch typed := value.(type) {
	case nil:
	case string:
		if typed != "" {
			ids = []string{typed}
		}
	case []any:
		for _, item := range typed {
			id, ok := item.(string)
			if !ok {
				return nil, inputErrorf("%s: expected ids", inputTitle(spec))
			}
			ids = append(ids, id)
		}
	case []string:
		ids = typed
	default:
		return nil, inputErrorf("%s: expected an id", inputTitle(spec))
	}
	if !spec.Multiple && len(ids) > 1 {
		return nil, inputErrorf("%s: pick one", inputTitle(spec))
	}
	return ids, nil
}

// validateAutomationInput checks the admin's choices against the inputs the
// plugin declares and returns what to store: the picked objects, and the
// parameters as JSON.
func (m *Manager) validateAutomationInput(ctx context.Context, plugin *Plugin, in AutomationBindingInput) (config store.JSON, rows []store.JSPluginBindingInput, err error) {
	params, rows, err := m.validateBindingInputs(ctx, plugin.Manifest.Automation.Inputs, in.Inputs)
	if err != nil {
		return nil, nil, err
	}
	raw, merr := json.Marshal(params)
	if merr != nil {
		return nil, nil, merr
	}
	return raw, rows, nil
}

// CreateAutomation binds an enabled automation plugin.
func (m *Manager) CreateAutomation(ctx context.Context, pluginID string, in AutomationBindingInput) (store.JSPluginBinding, error) {
	plugin, err := m.enabledAutomation(pluginID)
	if err != nil {
		return store.JSPluginBinding{}, err
	}
	config, rows, err := m.validateAutomationInput(ctx, plugin, in)
	if err != nil {
		return store.JSPluginBinding{}, err
	}
	events, _ := json.Marshal(plugin.Manifest.Automation.Subscribes)
	return m.repo.CreateAutomationBinding(ctx, store.JSPluginBinding{
		PluginID: pluginID,
		Version:  plugin.Manifest.Version,
		Kind:     KindAutomation,
		Config:   config,
		Events:   events,
	}, rows)
}

// UpdateAutomation replaces everything an admin picked for a binding.
func (m *Manager) UpdateAutomation(ctx context.Context, pluginID string, id uuid.UUID, in AutomationBindingInput) error {
	plugin, err := m.enabledAutomation(pluginID)
	if err != nil {
		return err
	}
	binding, err := m.repo.GetBinding(ctx, id)
	if err != nil || binding.PluginID != pluginID || binding.ScopeType != store.JSPluginScopeAutomation {
		return gorm.ErrRecordNotFound
	}
	config, rows, err := m.validateAutomationInput(ctx, plugin, in)
	if err != nil {
		return err
	}
	return m.repo.UpdateAutomationBinding(ctx, id, config, rows)
}

// DeleteAutomation removes a binding of this plugin.
func (m *Manager) DeleteAutomation(ctx context.Context, pluginID string, id uuid.UUID) error {
	binding, err := m.repo.GetBinding(ctx, id)
	if err != nil || binding.PluginID != pluginID {
		return gorm.ErrRecordNotFound
	}
	return m.repo.DeleteAutomationBinding(ctx, id)
}

// ListAutomations returns a plugin's bindings with what was picked and the saved state.
func (m *Manager) ListAutomations(ctx context.Context, pluginID string) ([]AutomationBindingView, error) {
	bindings, err := m.repo.ListAutomationBindings(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	out := make([]AutomationBindingView, 0, len(bindings))
	for _, binding := range bindings {
		rows, err := m.repo.ListBindingInputs(ctx, binding.ID)
		if err != nil {
			return nil, err
		}
		inputs, err := m.bindingInputViews(ctx, rows, jsonObject(binding.Config))
		if err != nil {
			return nil, err
		}
		out = append(out, AutomationBindingView{
			ID:        binding.ID.String(),
			Version:   binding.Version,
			Inputs:    inputs,
			State:     jsonObject(binding.State),
			CreatedAt: binding.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return out, nil
}

// AutomationOptions lists the objects an admin may pick for one input of an
// enabled plugin, marking those the input's conditions rule out.
func (m *Manager) AutomationOptions(ctx context.Context, pluginID, inputName string) ([]AutomationOption, error) {
	plugin, err := m.enabledAutomation(pluginID)
	if err != nil {
		return nil, err
	}
	return m.bindingInputOptions(ctx, plugin.Manifest.Automation.Inputs, inputName, nil)
}

// ActionLog returns the latest things a plugin asked xLyra to do.
func (m *Manager) ActionLog(ctx context.Context, pluginID string, limit int) ([]store.JSPluginActionLog, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return m.repo.ListActionLog(ctx, pluginID, limit)
}
