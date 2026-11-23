package jsplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"xlyra/server/internal/store"
)

// ReasonAlreadyBound marks an object another binding of an exclusive scope holds.
const ReasonAlreadyBound = "already_bound"

const maxPipelineBindingName = 80

// validateBindingInputs checks an admin's choices against declared inputs and
// returns the parameters and the picked objects to store. Automation and
// gateway pipeline bindings share it.
func (m *Manager) validateBindingInputs(ctx context.Context, specs []AutomationInputSpec, values map[string]any) (map[string]any, []store.JSPluginBindingInput, error) {
	declared := map[string]bool{}
	for _, spec := range specs {
		declared[spec.Name] = true
	}
	for name := range values {
		if !declared[name] {
			return nil, nil, inputErrorf("%q is not an input of this plugin", name)
		}
	}

	params := map[string]any{}
	var rows []store.JSPluginBindingInput
	seen := map[[2]string]string{}
	for _, spec := range specs {
		value := values[spec.Name]
		if !spec.IsEntity() {
			if value == nil {
				if spec.Default != nil {
					params[spec.Name] = spec.Default
				} else if spec.IsRequired() {
					return nil, nil, inputErrorf("%s is required", inputTitle(spec))
				}
				continue
			}
			if cerr := checkParam(spec, value); cerr != nil {
				return nil, nil, inputErrorf("%s: %s", inputTitle(spec), cerr.Error())
			}
			params[spec.Name] = value
			continue
		}

		ids, perr := pickedIDs(spec, value)
		if perr != nil {
			return nil, nil, perr
		}
		if len(ids) == 0 {
			if spec.IsRequired() {
				return nil, nil, inputErrorf("pick at least one for %s", inputTitle(spec))
			}
			continue
		}
		kind := lookupEntity(spec.Type)
		unique := make([]string, 0, len(ids))
		for _, id := range ids {
			if _, perr := uuid.Parse(id); perr != nil {
				return nil, nil, inputErrorf("%s: %q is not a uuid", inputTitle(spec), id)
			}
			unique = append(unique, id)
		}
		found, rerr := kind.Resolve(ctx, m.db, unique)
		if rerr != nil {
			return nil, nil, rerr
		}
		byID := map[string]AutomationEntity{}
		for _, entity := range found {
			byID[entity.ID] = entity
		}
		for _, id := range unique {
			entity, ok := byID[id]
			if !ok {
				return nil, nil, inputErrorf("%s: some objects were not found", inputTitle(spec))
			}
			if _, reason := kind.Check(entity, spec); reason != "" {
				return nil, nil, inputErrorf("%s: %q: %s", inputTitle(spec), entity.Name, reason)
			}
			key := [2]string{spec.Type, id}
			if previous, dup := seen[key]; dup {
				return nil, nil, inputErrorf("%q is picked for both %s and %s", entity.Name, previous, inputTitle(spec))
			}
			seen[key] = inputTitle(spec)
			rows = append(rows, store.JSPluginBindingInput{
				InputName: spec.Name, EntityType: spec.Type, EntityID: id, EventSubject: spec.EventSubject,
			})
		}
	}
	if len(rows) > MaxAutomationEntities {
		return nil, nil, inputErrorf("at most %d objects can be picked in all", MaxAutomationEntities)
	}
	return params, rows, nil
}

// bindingInputOptions lists the objects an admin may pick for one declared
// input. extra may rule out more objects with a reason code and a sentence.
func (m *Manager) bindingInputOptions(ctx context.Context, specs []AutomationInputSpec, inputName string, extra func(AutomationEntity) (code, reason string)) ([]AutomationOption, error) {
	for _, spec := range specs {
		if spec.Name != inputName {
			continue
		}
		kind := lookupEntity(spec.Type)
		if kind == nil {
			return nil, inputErrorf("%q is a parameter, not a list of objects", inputName)
		}
		entities, err := kind.List(ctx, m.db)
		if err != nil {
			return nil, err
		}
		options := make([]AutomationOption, 0, len(entities))
		for _, entity := range entities {
			code, reason := kind.Check(entity, spec)
			if reason == "" && extra != nil {
				code, reason = extra(entity)
			}
			options = append(options, AutomationOption{
				ID: entity.ID, Name: entity.Name, Description: kind.Describe(entity), IconKey: kind.iconKey(entity),
				Disabled: reason != "", ReasonCode: code, Reason: reason,
			})
		}
		return options, nil
	}
	return nil, inputErrorf("%q is not an input of this plugin", inputName)
}

// ---- gateway pipeline bindings ----

// PipelineBindingInput is what an admin submits to bind a gateway pipeline.
// HostOptions holds the host options to change; the rest keep their defaults.
type PipelineBindingInput struct {
	Name        string
	Inputs      map[string]any
	HostOptions map[string]any
}

// PipelineHostOptionSpec describes one host option the binding form shows.
type PipelineHostOptionSpec struct {
	Key     string `json:"key"`
	Type    string `json:"type"`
	Default any    `json:"default"`
}

// PipelineBindingView is a gateway pipeline binding as the admin page shows it.
type PipelineBindingView struct {
	ID                string                 `json:"id"`
	Version           string                 `json:"version"`
	Name              string                 `json:"name"`
	Inputs            []AutomationInputView  `json:"inputs"`
	HostOptions       PipelineHostOptions    `json:"host_options"`
	Policy            PipelinePolicySnapshot `json:"policy"`
	Status            string                 `json:"status"`
	UnavailableReason string                 `json:"unavailable_reason,omitempty"`
	CreatedAt         string                 `json:"created_at"`
}

// PipelineBindingInUseError means a plugin, or the version its bindings
// follow, cannot be removed while fail_closed gateway pipeline bindings use it.
type PipelineBindingInUseError struct {
	PluginID string
	Count    int
}

func (e *PipelineBindingInUseError) Error() string {
	return fmt.Sprintf("plugin %q still has %d fail_closed gateway pipeline binding(s); remove them first", e.PluginID, e.Count)
}

// PipelineHostOptionSpecs lists, in a fixed order, the host options a binding
// of this section can set.
func PipelineHostOptionSpecs(section GatewayPipelineSection) []PipelineHostOptionSpec {
	var out []PipelineHostOptionSpec
	if section.Transform.ModeOrDefault() == PipelineTransformTokenPlaceholder {
		out = append(out, PipelineHostOptionSpec{Key: "propagateSameValue", Type: "boolean", Default: true})
	}
	out = append(out, PipelineHostOptionSpec{Key: "scanToolDefinitions", Type: "boolean", Default: false})
	if section.Transform.IsReversible() {
		out = append(out, PipelineHostOptionSpec{Key: "transformReasoning", Type: "boolean", Default: false})
	}
	return out
}

// ResolvePipelineHostOptions applies an admin's changes to the defaults.
func ResolvePipelineHostOptions(section GatewayPipelineSection, values map[string]any) (PipelineHostOptions, error) {
	specs := PipelineHostOptionSpecs(section)
	options := PipelineHostOptions{}
	byKey := map[string]PipelineHostOptionSpec{}
	for _, spec := range specs {
		byKey[spec.Key] = spec
		if spec.Key == "propagateSameValue" {
			options.PropagateSameValue = true
		}
	}
	for key, value := range values {
		spec, ok := byKey[key]
		if !ok {
			return PipelineHostOptions{}, inputErrorf("%q is not an option of this pipeline", key)
		}
		if value == nil {
			continue
		}
		switch spec.Type {
		case "boolean":
			flag, ok := value.(bool)
			if !ok {
				return PipelineHostOptions{}, inputErrorf("%s: expected boolean", key)
			}
			switch key {
			case "propagateSameValue":
				options.PropagateSameValue = flag
			case "scanToolDefinitions":
				options.ScanToolDefinitions = flag
			case "transformReasoning":
				options.TransformReasoning = flag
			}
		}
	}
	return options, nil
}

func (m *Manager) enabledPipeline(pluginID string) (*Plugin, error) {
	plugin, ok := m.catalog.Registry().ByPluginID(pluginID)
	if !ok || !plugin.Manifest.DeclaresGatewayPipeline() {
		return nil, inputErrorf("plugin %q is not an enabled gateway pipeline plugin", pluginID)
	}
	return plugin, nil
}

func (m *Manager) pipelineBindingConfig(ctx context.Context, plugin *Plugin, in PipelineBindingInput) (store.JSON, []store.JSPluginBindingInput, error) {
	section := *plugin.Manifest.GatewayPipeline
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = pluginDisplayName(plugin.Manifest)
	}
	if utf8.RuneCountInString(name) > maxPipelineBindingName {
		return nil, nil, inputErrorf("name: at most %d characters", maxPipelineBindingName)
	}
	params, rows, err := m.validateBindingInputs(ctx, section.Inputs, in.Inputs)
	if err != nil {
		return nil, nil, err
	}
	if _, err := CompilePipelineMatcher(section, params); err != nil {
		return nil, nil, inputErrorf("%s", err.Error())
	}
	options, err := ResolvePipelineHostOptions(section, in.HostOptions)
	if err != nil {
		return nil, nil, err
	}
	raw, err := json.Marshal(PipelineBindingConfig{
		Name:        name,
		Params:      params,
		HostOptions: options,
		PolicySnapshot: pipelinePolicySnapshot(plugin.Manifest),
	})
	if err != nil {
		return nil, nil, err
	}
	return raw, rows, nil
}

func (m *Manager) pipelineConflictError(err error) error {
	var conflict *store.BindingEntityConflictError
	if !errors.As(err, &conflict) {
		return err
	}
	name := decodePipelineBindingConfig(conflict.Binding.Config).Name
	if name == "" {
		name = conflict.Binding.ID.String()
	}
	label := conflict.EntityID
	if found, rerr := resolveAPIKeys(context.Background(), m.db, []string{conflict.EntityID}); rerr == nil && len(found) == 1 {
		label = found[0].Name
	}
	return inputErrorf("API key %q is already bound to gateway pipeline %q", label, name)
}

// CreatePipelineBinding binds an enabled gateway pipeline plugin to API keys.
func (m *Manager) CreatePipelineBinding(ctx context.Context, pluginID string, in PipelineBindingInput) (store.JSPluginBinding, error) {
	plugin, err := m.enabledPipeline(pluginID)
	if err != nil {
		return store.JSPluginBinding{}, err
	}
	config, rows, err := m.pipelineBindingConfig(ctx, plugin, in)
	if err != nil {
		return store.JSPluginBinding{}, err
	}
	binding, err := m.repo.CreateScopedBinding(ctx, store.JSPluginBinding{
		PluginID:  pluginID,
		Version:   plugin.Manifest.Version,
		Kind:      "",
		ScopeType: store.JSPluginScopeGatewayPipeline,
		Config:    config,
	}, rows, EntityAPIKey)
	if err != nil {
		return store.JSPluginBinding{}, m.pipelineConflictError(err)
	}
	if err := m.publishCatalog(ctx); err != nil {
		return binding, err
	}
	return binding, nil
}

// UpdatePipelineBinding replaces the name, inputs and host options of a binding.
func (m *Manager) UpdatePipelineBinding(ctx context.Context, pluginID string, id uuid.UUID, in PipelineBindingInput) error {
	plugin, err := m.enabledPipeline(pluginID)
	if err != nil {
		return err
	}
	binding, err := m.repo.GetBinding(ctx, id)
	if err != nil || binding.PluginID != pluginID || binding.ScopeType != store.JSPluginScopeGatewayPipeline {
		return gorm.ErrRecordNotFound
	}
	config, rows, err := m.pipelineBindingConfig(ctx, plugin, in)
	if err != nil {
		return err
	}
	if err := m.repo.UpdateScopedBinding(ctx, pluginID, id, store.JSPluginScopeGatewayPipeline, config, rows, EntityAPIKey); err != nil {
		return m.pipelineConflictError(err)
	}
	return m.publishCatalog(ctx)
}

// DeletePipelineBinding removes a binding. It works while the plugin is
// disabled, which is how an admin releases keys a fail_closed binding blocks.
func (m *Manager) DeletePipelineBinding(ctx context.Context, pluginID string, id uuid.UUID) error {
	if err := m.repo.DeleteScopedBinding(ctx, pluginID, id, store.JSPluginScopeGatewayPipeline); err != nil {
		return err
	}
	return m.publishCatalog(ctx)
}

// ListPipelineBindings returns a plugin's gateway pipeline bindings with the
// state the current snapshot gives them.
func (m *Manager) ListPipelineBindings(ctx context.Context, pluginID string) ([]PipelineBindingView, error) {
	bindings, err := m.repo.ListPluginBindingsByScope(ctx, pluginID, store.JSPluginScopeGatewayPipeline)
	if err != nil {
		return nil, err
	}
	snap := m.catalog.Current()
	out := make([]PipelineBindingView, 0, len(bindings))
	for _, binding := range bindings {
		cfg := decodePipelineBindingConfig(binding.Config)
		rows, err := m.repo.ListBindingInputs(ctx, binding.ID)
		if err != nil {
			return nil, err
		}
		inputs, err := m.bindingInputViews(ctx, rows, cfg.Params)
		if err != nil {
			return nil, err
		}
		view := PipelineBindingView{
			ID:          binding.ID.String(),
			Version:     binding.Version,
			Name:        cfg.Name,
			Inputs:      inputs,
			HostOptions: cfg.HostOptions,
			Policy:      cfg.PolicySnapshot,
			Status:      string(PipelineBindingBypassed),
			CreatedAt:   binding.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		}
		if resolved, ok := snap.PipelineBinding(binding.ID); ok {
			view.Status = string(resolved.Status)
			view.UnavailableReason = resolved.UnavailableReason
		}
		out = append(out, view)
	}
	return out, nil
}

// bindingInputViews shows a binding's picked objects with their names and its parameters.
func (m *Manager) bindingInputViews(ctx context.Context, rows []store.JSPluginBindingInput, params map[string]any) ([]AutomationInputView, error) {
	idsByType := map[string][]string{}
	for _, row := range rows {
		idsByType[row.EntityType] = append(idsByType[row.EntityType], row.EntityID)
	}
	names := map[[2]string]string{}
	for kind, ids := range idsByType {
		known := lookupEntity(kind)
		if known == nil {
			continue
		}
		entities, err := known.Resolve(ctx, m.db, ids)
		if err != nil {
			return nil, err
		}
		for _, entity := range entities {
			names[[2]string{entity.Type, entity.ID}] = entity.Name
		}
	}
	byName := map[string]*AutomationInputView{}
	var order []string
	view := func(name string) *AutomationInputView {
		if byName[name] == nil {
			byName[name] = &AutomationInputView{Name: name}
			order = append(order, name)
		}
		return byName[name]
	}
	for _, row := range rows {
		v := view(row.InputName)
		v.Entities = append(v.Entities, AutomationEntityView{Type: row.EntityType, ID: row.EntityID, Name: names[[2]string{row.EntityType, row.EntityID}]})
	}
	for name, value := range params {
		view(name).Value = value
	}
	sort.Strings(order)
	inputs := make([]AutomationInputView, 0, len(order))
	for _, name := range order {
		inputs = append(inputs, *byName[name])
	}
	return inputs, nil
}

// PipelineInputOptions lists the objects an admin may pick for one input of
// an enabled gateway pipeline plugin. Keys another binding holds are marked
// already_bound; bindingID is the binding being edited, whose keys stay free.
func (m *Manager) PipelineInputOptions(ctx context.Context, pluginID, inputName string, bindingID uuid.UUID) ([]AutomationOption, error) {
	plugin, err := m.enabledPipeline(pluginID)
	if err != nil {
		return nil, err
	}
	owners, err := m.repo.EntityBindingOwners(ctx, store.JSPluginScopeGatewayPipeline, EntityAPIKey)
	if err != nil {
		return nil, err
	}
	return m.bindingInputOptions(ctx, plugin.Manifest.GatewayPipeline.Inputs, inputName, func(entity AutomationEntity) (string, string) {
		if entity.Type != EntityAPIKey {
			return "", ""
		}
		if owner, taken := owners[entity.ID]; taken && owner != bindingID {
			return ReasonAlreadyBound, "already bound to another gateway pipeline"
		}
		return "", ""
	})
}

// failClosedPipelineBindings counts a plugin's fail_closed pipeline bindings,
// only those following version when it is set.
func (m *Manager) failClosedPipelineBindings(ctx context.Context, pluginID, version string) (int, error) {
	bindings, err := m.repo.ListPluginBindingsByScope(ctx, pluginID, store.JSPluginScopeGatewayPipeline)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, binding := range bindings {
		if version != "" && binding.Version != version {
			continue
		}
		if decodePipelineBindingConfig(binding.Config).PolicySnapshot.FailurePolicy == PipelineFailClosed {
			count++
		}
	}
	return count, nil
}

func (m *Manager) ensurePipelineBindingsReleased(ctx context.Context, pluginID, version string) error {
	count, err := m.failClosedPipelineBindings(ctx, pluginID, version)
	if err != nil {
		return err
	}
	if count > 0 {
		return &PipelineBindingInUseError{PluginID: pluginID, Count: count}
	}
	return nil
}

// refreshPipelinePolicies rewrites the policy snapshot of a plugin's bindings
// after a version is enabled, so it matches the version they now follow.
func (m *Manager) refreshPipelinePolicies(ctx context.Context, manifest Manifest) error {
	if !manifest.DeclaresGatewayPipeline() {
		return nil
	}
	bindings, err := m.repo.ListPluginBindingsByScope(ctx, manifest.ID, store.JSPluginScopeGatewayPipeline)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		cfg := decodePipelineBindingConfig(binding.Config)
		next := pipelinePolicySnapshot(manifest)
		if cfg.PolicySnapshot == next {
			continue
		}
		cfg.PolicySnapshot = next
		raw, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		if err := m.repo.SetBindingConfig(ctx, binding.ID, raw); err != nil {
			return err
		}
	}
	return nil
}

func pipelinePolicySnapshot(manifest Manifest) PipelinePolicySnapshot {
	section := *manifest.GatewayPipeline
	return PipelinePolicySnapshot{
		FailurePolicy:       section.FailurePolicyOrDefault(),
		UnsupportedEndpoint: section.UnsupportedEndpointOrDefault(),
		PluginName:          pluginDisplayName(manifest),
		InspectRaw:          contains(manifest.Permissions, PermissionPayloadInspectRaw),
	}
}
