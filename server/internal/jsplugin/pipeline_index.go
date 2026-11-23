package jsplugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"xlyra/server/internal/store"
	"xlyra/server/internal/textmatch"
)

// PipelineBindingStatus is how a gateway pipeline binding stands in a snapshot.
type PipelineBindingStatus string

const (
	// PipelineBindingActive runs the plugin's pipeline.
	PipelineBindingActive PipelineBindingStatus = "active"
	// PipelineBindingUnavailable is a fail_closed binding whose plugin cannot
	// run. Requests with its keys are refused until the plugin is back or the
	// binding is removed.
	PipelineBindingUnavailable PipelineBindingStatus = "unavailable"
	// PipelineBindingBypassed is a fail_open binding whose plugin cannot run.
	// It is not in the hot index; requests pass as if it were not there.
	PipelineBindingBypassed PipelineBindingStatus = "bypassed"
)

// Reasons a binding's plugin cannot run.
const (
	PipelineReasonDisabled      = "disabled"
	PipelineReasonBroken        = "broken"
	PipelineReasonCircuitOpen   = "circuit_open"
	PipelineReasonUninstalled   = "uninstalled"
	PipelineReasonInvalidConfig = "invalid_config"
	PipelineReasonReloadFailed  = "catalog_reload_failed"
)

// PipelineHostOptions are the binding options the host adds for every
// gateway pipeline, whatever the plugin declares as inputs.
type PipelineHostOptions struct {
	PropagateSameValue  bool `json:"propagateSameValue"`
	ScanToolDefinitions bool `json:"scanToolDefinitions"`
	TransformReasoning  bool `json:"transformReasoning"`
}

// PipelinePolicySnapshot keeps the policies a binding was saved under, so a
// fail_closed binding still blocks after its plugin is gone.
type PipelinePolicySnapshot struct {
	FailurePolicy       string `json:"failurePolicy"`
	UnsupportedEndpoint string `json:"unsupportedEndpoint"`
	PluginName          string `json:"pluginName"`
	// InspectRaw records the payload.inspect_raw permission, which keeps
	// raw request text out of logs and captures even while blocked.
	InspectRaw bool `json:"inspectRaw,omitempty"`
}

// PipelineBindingConfig is the config JSON of a gateway pipeline binding.
type PipelineBindingConfig struct {
	Name           string                 `json:"name"`
	Params         map[string]any         `json:"params"`
	HostOptions    PipelineHostOptions    `json:"hostOptions"`
	PolicySnapshot PipelinePolicySnapshot `json:"policySnapshot"`
}

// ResolvedPipelineBinding is one binding ready for the gateway.
type ResolvedPipelineBinding struct {
	BindingID         uuid.UUID
	BindingName       string
	PluginID          string
	PluginName        string
	Version           string
	Status            PipelineBindingStatus
	UnavailableReason string
	// Plugin is set while Status is active.
	Plugin              *Plugin
	Spec                GatewayPipelineSection
	Params              map[string]any
	HostOptions         PipelineHostOptions
	FailurePolicy       string
	UnsupportedEndpoint string
	// InspectRaw is set when the plugin holds payload.inspect_raw.
	InspectRaw bool
	// ConfigHash changes with anything that may change a finding: the plugin
	// package, the binding parameters, the host options and the section.
	ConfigHash string
	// Matcher runs the section's rules, dictionaries and allowlist with the
	// binding's parameters. It is set while Status is active.
	Matcher *textmatch.Matcher
}

// FailClosed reports whether a failure of this binding blocks the request.
func (b *ResolvedPipelineBinding) FailClosed() bool {
	return b != nil && b.FailurePolicy == PipelineFailClosed
}

// PipelineBindingSummary is what an API key page shows about its binding.
type PipelineBindingSummary struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	PluginID          string `json:"plugin_id"`
	PluginName        string `json:"plugin_name"`
	Status            string `json:"status"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// PipelineIndex is the immutable per-snapshot lookup from API key to binding.
type PipelineIndex struct {
	byAPIKey  map[uuid.UUID]*ResolvedPipelineBinding
	summaries map[uuid.UUID]PipelineBindingSummary
	bindings  map[uuid.UUID]*ResolvedPipelineBinding
}

// PipelineByAPIKey is the gateway's one lookup per request. A key without a
// binding, or with a fail_open binding whose plugin cannot run, is absent.
func (s *Snapshot) PipelineByAPIKey(apiKeyID uuid.UUID) (*ResolvedPipelineBinding, bool) {
	if s == nil || s.Pipelines == nil || len(s.Pipelines.byAPIKey) == 0 {
		return nil, false
	}
	binding, ok := s.Pipelines.byAPIKey[apiKeyID]
	return binding, ok
}

// PipelineSummaryForAPIKey describes the binding holding a key, if any,
// including fail_open bindings that are currently bypassed.
func (s *Snapshot) PipelineSummaryForAPIKey(apiKeyID uuid.UUID) (PipelineBindingSummary, bool) {
	if s == nil || s.Pipelines == nil {
		return PipelineBindingSummary{}, false
	}
	summary, ok := s.Pipelines.summaries[apiKeyID]
	return summary, ok
}

// PipelineBinding returns a binding of the snapshot by id.
func (s *Snapshot) PipelineBinding(bindingID uuid.UUID) (*ResolvedPipelineBinding, bool) {
	if s == nil || s.Pipelines == nil {
		return nil, false
	}
	binding, ok := s.Pipelines.bindings[bindingID]
	return binding, ok
}

func decodePipelineBindingConfig(raw store.JSON) PipelineBindingConfig {
	var cfg PipelineBindingConfig
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cfg)
	}
	if cfg.Params == nil {
		cfg.Params = map[string]any{}
	}
	return cfg
}

// pipelineConfigHash fingerprints everything that can change a finding.
func pipelineConfigHash(plugin *Plugin, params map[string]any, options PipelineHostOptions, spec GatewayPipelineSection) string {
	payload := struct {
		PluginID string                 `json:"pluginId"`
		Version  string                 `json:"version"`
		Package  string                 `json:"package"`
		Source   string                 `json:"source"`
		Params   map[string]any         `json:"params"`
		Options  PipelineHostOptions    `json:"options"`
		Spec     GatewayPipelineSection `json:"spec"`
	}{Params: params, Options: options, Spec: spec}
	if plugin != nil {
		payload.PluginID = plugin.Manifest.ID
		payload.Version = plugin.Manifest.Version
		payload.Package = plugin.PackageSHA256()
		payload.Source = hashSource(plugin.source)
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// pipelineVersionReason says why a binding's plugin is not in the registry.
func pipelineVersionReason(states []store.JSPluginVersionState, pluginID, version string) string {
	installed := false
	for _, state := range states {
		if state.PluginID != pluginID {
			continue
		}
		installed = true
		if state.Version != version {
			continue
		}
		switch state.Status {
		case store.JSPluginStatusEnabled:
			// Enabled but not a gateway pipeline any more.
			return PipelineReasonInvalidConfig
		case store.JSPluginStatusBroken:
			return PipelineReasonBroken
		case store.JSPluginStatusDisabled:
			if _, tripped := jsonObject(state.SelfTest)["breaker"]; tripped {
				return PipelineReasonCircuitOpen
			}
		}
		return PipelineReasonDisabled
	}
	if installed {
		return PipelineReasonDisabled
	}
	return PipelineReasonUninstalled
}

// buildPipelineIndex resolves every gateway pipeline binding against the
// registry of a new generation. A binding whose plugin cannot run stays in the
// hot index as unavailable when it was saved fail_closed.
func (m *Manager) buildPipelineIndex(ctx context.Context, registry *Registry, forcedReason string) (*PipelineIndex, error) {
	bindings, err := m.repo.ListBindingsByScopeType(ctx, store.JSPluginScopeGatewayPipeline)
	if err != nil {
		return nil, err
	}
	index := &PipelineIndex{
		byAPIKey:  map[uuid.UUID]*ResolvedPipelineBinding{},
		summaries: map[uuid.UUID]PipelineBindingSummary{},
		bindings:  map[uuid.UUID]*ResolvedPipelineBinding{},
	}
	if len(bindings) == 0 {
		return index, nil
	}
	inputs, err := m.repo.ListBindingInputsByScopeType(ctx, store.JSPluginScopeGatewayPipeline)
	if err != nil {
		return nil, err
	}
	keysByBinding := map[uuid.UUID][]uuid.UUID{}
	for _, input := range inputs {
		if input.EntityType != EntityAPIKey {
			continue
		}
		id, err := uuid.Parse(input.EntityID)
		if err != nil {
			continue
		}
		keysByBinding[input.BindingID] = append(keysByBinding[input.BindingID], id)
	}
	var states []store.JSPluginVersionState
	statesLoaded := false
	for _, binding := range bindings {
		cfg := decodePipelineBindingConfig(binding.Config)
		resolved := &ResolvedPipelineBinding{
			BindingID:           binding.ID,
			BindingName:         cfg.Name,
			PluginID:            binding.PluginID,
			PluginName:          cfg.PolicySnapshot.PluginName,
			Version:             binding.Version,
			Params:              cfg.Params,
			HostOptions:         cfg.HostOptions,
			FailurePolicy:       cfg.PolicySnapshot.FailurePolicy,
			UnsupportedEndpoint: cfg.PolicySnapshot.UnsupportedEndpoint,
			InspectRaw:          cfg.PolicySnapshot.InspectRaw,
		}
		var plugin *Plugin
		if forcedReason == "" && registry != nil {
			if candidate, ok := registry.ByPluginID(binding.PluginID); ok && candidate.Manifest.DeclaresGatewayPipeline() {
				plugin = candidate
			}
		}
		switch {
		case plugin != nil:
			active, err := ResolvePipelineBinding(plugin, binding.ID, cfg.Name, cfg.Params, cfg.HostOptions)
			if err != nil {
				slog.Warn("gateway pipeline binding cannot be compiled", "binding_id", binding.ID.String(), "plugin_id", binding.PluginID, "error", err)
				resolved.Status = PipelineBindingUnavailable
				resolved.UnavailableReason = PipelineReasonInvalidConfig
				resolved.PluginName = pluginDisplayName(plugin.Manifest)
				break
			}
			resolved = active
		case forcedReason != "":
			resolved.UnavailableReason = forcedReason
		default:
			if !statesLoaded {
				states, err = m.repo.ListVersionStates(ctx)
				if err != nil {
					return nil, err
				}
				statesLoaded = true
			}
			resolved.UnavailableReason = pipelineVersionReason(states, binding.PluginID, binding.Version)
		}
		if resolved.Status != PipelineBindingActive {
			resolved.Status = PipelineBindingUnavailable
			if !resolved.FailClosed() {
				resolved.Status = PipelineBindingBypassed
			}
		}
		if resolved.PluginName == "" {
			resolved.PluginName = binding.PluginID
		}
		index.bindings[binding.ID] = resolved
		summary := PipelineBindingSummary{
			ID: binding.ID.String(), Name: resolved.BindingName, PluginID: resolved.PluginID,
			PluginName: resolved.PluginName, Status: string(resolved.Status), UnavailableReason: resolved.UnavailableReason,
		}
		for _, keyID := range keysByBinding[binding.ID] {
			if _, taken := index.summaries[keyID]; taken {
				continue
			}
			index.summaries[keyID] = summary
			if resolved.Status != PipelineBindingBypassed {
				index.byAPIKey[keyID] = resolved
			}
		}
	}
	return index, nil
}

// ResolvePipelineBinding prepares an active binding of an enabled plugin:
// policies from its section and a compiled matcher, so no request compiles
// an expression or a dictionary. Dry runs and fixtures use it without a
// database.
func ResolvePipelineBinding(plugin *Plugin, bindingID uuid.UUID, name string, params map[string]any, options PipelineHostOptions) (*ResolvedPipelineBinding, error) {
	if plugin == nil || !plugin.Manifest.DeclaresGatewayPipeline() {
		return nil, fmt.Errorf("plugin has no gatewayPipeline section")
	}
	if params == nil {
		params = map[string]any{}
	}
	spec := *plugin.Manifest.GatewayPipeline
	matcher, err := CompilePipelineMatcher(spec, params)
	if err != nil {
		return nil, err
	}
	return &ResolvedPipelineBinding{
		BindingID:           bindingID,
		BindingName:         name,
		PluginID:            plugin.Manifest.ID,
		PluginName:          pluginDisplayName(plugin.Manifest),
		Version:             plugin.Manifest.Version,
		Status:              PipelineBindingActive,
		Plugin:              plugin,
		Spec:                spec,
		Params:              params,
		HostOptions:         options,
		FailurePolicy:       spec.FailurePolicyOrDefault(),
		UnsupportedEndpoint: spec.UnsupportedEndpointOrDefault(),
		InspectRaw:          contains(plugin.Manifest.Permissions, PermissionPayloadInspectRaw),
		ConfigHash:          pipelineConfigHash(plugin, params, options, spec),
		Matcher:             matcher,
	}, nil
}

// NewPipelineIndex indexes bindings by API key. Bypassed bindings are listed
// in the summaries only.
func NewPipelineIndex(byAPIKey map[uuid.UUID]*ResolvedPipelineBinding) *PipelineIndex {
	index := &PipelineIndex{
		byAPIKey:  map[uuid.UUID]*ResolvedPipelineBinding{},
		summaries: map[uuid.UUID]PipelineBindingSummary{},
		bindings:  map[uuid.UUID]*ResolvedPipelineBinding{},
	}
	for keyID, binding := range byAPIKey {
		if binding == nil {
			continue
		}
		index.bindings[binding.BindingID] = binding
		index.summaries[keyID] = PipelineBindingSummary{
			ID: binding.BindingID.String(), Name: binding.BindingName, PluginID: binding.PluginID,
			PluginName: binding.PluginName, Status: string(binding.Status), UnavailableReason: binding.UnavailableReason,
		}
		if binding.Status != PipelineBindingBypassed {
			index.byAPIKey[keyID] = binding
		}
	}
	return index
}

// CompilePipelineMatcher builds the native matcher of a section with the
// parameters of one binding.
func CompilePipelineMatcher(section GatewayPipelineSection, params map[string]any) (*textmatch.Matcher, error) {
	spec := textmatch.Spec{Types: section.TypeNames(), Disabled: map[string]bool{}}
	for _, typ := range section.Types {
		if typ.EnabledBy == "" {
			continue
		}
		if on, ok := params[typ.EnabledBy].(bool); ok && !on {
			spec.Disabled[typ.Type] = true
		}
	}
	for _, rule := range section.Rules {
		spec.Rules = append(spec.Rules, textmatch.RuleSpec{
			Type: rule.Type, Pattern: rule.Pattern, Group: rule.Group, Boundary: rule.Boundary, Validate: rule.Validate,
			MinEntropy: rule.MinEntropy, ContextAny: rule.ContextAny, ContextWindow: rule.ContextWindow,
		})
	}
	for _, dict := range section.Dictionaries {
		text, _ := params[dict.Param].(string)
		spec.Dictionaries = append(spec.Dictionaries, textmatch.DictionarySpec{Type: dict.Type, CaseSensitive: dict.CaseSensitive, Text: text})
	}
	if section.Allowlist != nil {
		spec.Allowlist, _ = params[section.Allowlist.Param].(string)
		spec.AllowlistCaseSensitive = section.Allowlist.CaseSensitive
	}
	return textmatch.Compile(spec)
}

// blockPipelinesWithoutIndex installs an index of fail_closed bindings, all
// unavailable, when no generation has built one yet, so a failed first reload
// does not let bound keys through untransformed.
func (m *Manager) blockPipelinesWithoutIndex(ctx context.Context) {
	if m == nil || m.catalog == nil {
		return
	}
	current := m.catalog.Current()
	if current != nil && current.Pipelines != nil {
		return
	}
	index, err := m.buildPipelineIndex(ctx, nil, PipelineReasonReloadFailed)
	if err != nil {
		slog.Warn("gateway pipeline index unavailable after failed reload", "error", err)
		return
	}
	next := &Snapshot{Pipelines: index}
	if current != nil {
		next.Gen = current.Gen
		next.Registry = current.Registry
	}
	if next.Registry == nil {
		next.Registry = NewRegistry()
	}
	m.catalog.Replace(next)
}

func pluginDisplayName(manifest Manifest) string {
	if manifest.Name != "" {
		return manifest.Name
	}
	return manifest.ID
}
