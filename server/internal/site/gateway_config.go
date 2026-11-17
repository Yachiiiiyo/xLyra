package site

import (
	"encoding/json"
	"fmt"
	"strings"

	"xlyra/server/internal/jsplugin"
)

const (
	ResponsesToolPolicyPassthrough   = "passthrough"
	ResponsesToolPolicyCompatibility = "compatibility"

	ResponsesHostedToolImageGeneration = "image_generation"

	QuotaProbeTypeSub2API  = "sub2api"
	QuotaProbeTypeNewAPI   = "newapi"
	QuotaProbeTypeXLyra    = "xlyra"
	QuotaProbeTypeKimi     = "kimi"
	QuotaProbeTypeGLM      = "glm"
	QuotaProbeTypeDeepSeek = "deepseek"
	QuotaProbeTypeMoonshot = "moonshot"

	QuotaProbePluginPrefix = "plugin:"
)

type GatewayConfig struct {
	RequestTimeoutMS               *int     `json:"request_timeout_ms,omitempty"`
	ConnectTimeoutMS               *int     `json:"connect_timeout_ms,omitempty"`
	ResponseHeaderTimeoutMS        *int     `json:"response_header_timeout_ms,omitempty"`
	MaxConcurrency                 *int     `json:"max_concurrency,omitempty"`
	MaxModelConcurrency            *int     `json:"max_model_concurrency,omitempty"`
	MaxCredentialConcurrency       *int     `json:"max_credential_concurrency,omitempty"`
	MaxIdleConns                   *int     `json:"max_idle_conns,omitempty"`
	MaxIdleConnsPerHost            *int     `json:"max_idle_conns_per_host,omitempty"`
	MaxConnsPerHost                *int     `json:"max_conns_per_host,omitempty"`
	IdleConnTimeoutMS              *int     `json:"idle_conn_timeout_ms,omitempty"`
	ResponsesToolPolicy            string   `json:"responses_tool_policy,omitempty"`
	DisabledResponsesTools         []string `json:"disabled_responses_tools,omitempty"`
	ResponsesImageGenerationPolicy string   `json:"responses_image_generation_policy,omitempty"`
	ImpersonateCodexClient         *bool    `json:"impersonate_codex_client,omitempty"`
	ImpersonateClaudeCodeClient    *bool    `json:"impersonate_claude_code_client,omitempty"`
	QuotaProbe                     *string  `json:"quota_probe,omitempty"`
	// Plugins maps a per-site plugin kind (model_list, credential_check,
	// error_classifier, pricing_parse) to the plugin id an admin bound to this site.
	// Kept so sites saved before plugin_ids still load; new writes clear it.
	Plugins map[string]string `json:"plugins,omitempty"`
	// PluginIDs is the ordered list of plugins mounted on the site. The first
	// plugin that exports a hook wins. A non-nil empty slice clears the list.
	PluginIDs []string `json:"plugin_ids,omitempty"`
	// PluginSettings maps a plugin id to per-site setting overrides merged over
	// the plugin's global settings and manifest defaults.
	PluginSettings map[string]map[string]any `json:"plugin_settings,omitempty"`
}

// SitePluginKinds are the plugin kinds that take effect per site.
var SitePluginKinds = []string{
	jsplugin.KindModelList, jsplugin.KindCredentialCheck, jsplugin.KindErrorClassifier, jsplugin.KindPricingParse,
}

// normalizeSitePlugins keeps the known kinds with a usable plugin id and drops
// the rest, so a stale or hand-edited entry can never break reading the config.
func normalizeSitePlugins(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, kind := range SitePluginKinds {
		id := strings.TrimSpace(in[kind])
		if id == "" || strings.ContainsAny(id, " \t\r\n") {
			continue
		}
		out[kind] = id
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizePluginIDs keeps usable plugin ids, in order, without duplicates or
// blanks. The id rules match normalizeSitePlugins.
func normalizePluginIDs(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		id := strings.TrimSpace(raw)
		if id == "" || strings.ContainsAny(id, " \t\r\n") {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeSitePluginSettings(in map[string]map[string]any) map[string]map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := map[string]map[string]any{}
	for pluginID, rawSettings := range in {
		id := strings.TrimSpace(pluginID)
		if id == "" || strings.ContainsAny(id, " \t\r\n") || len(rawSettings) == 0 {
			continue
		}
		entries := map[string]any{}
		for key, val := range rawSettings {
			k := strings.TrimSpace(key)
			if k == "" || val == nil {
				continue
			}
			switch val.(type) {
			case string, bool, float64, float32, int, int64, int32, json.Number:
				entries[k] = val
			}
		}
		if len(entries) > 0 {
			out[id] = entries
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SitePluginSettings returns the per-site setting overrides for pluginID, if any.
func SitePluginSettings(cfg *GatewayConfig, pluginID string) map[string]any {
	if cfg == nil || len(cfg.PluginSettings) == 0 {
		return nil
	}
	return cfg.PluginSettings[strings.TrimSpace(pluginID)]
}

func NormalizeQuotaProbeType(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "", "none":
		return "", nil
	case QuotaProbeTypeSub2API:
		return QuotaProbeTypeSub2API, nil
	case QuotaProbeTypeNewAPI:
		return QuotaProbeTypeNewAPI, nil
	case QuotaProbeTypeXLyra:
		return QuotaProbeTypeXLyra, nil
	case QuotaProbeTypeKimi:
		return QuotaProbeTypeKimi, nil
	case QuotaProbeTypeGLM:
		return QuotaProbeTypeGLM, nil
	case QuotaProbeTypeMoonshot:
		return QuotaProbeTypeMoonshot, nil
	case QuotaProbeTypeDeepSeek:
		return QuotaProbeTypeDeepSeek, nil
	default:
		if strings.HasPrefix(value, QuotaProbePluginPrefix) {
			id := strings.TrimSpace(strings.TrimPrefix(value, QuotaProbePluginPrefix))
			if id == "" || strings.Contains(id, " ") {
				return "", fmt.Errorf("unsupported quota_probe type %q", value)
			}
			return QuotaProbePluginPrefix + id, nil
		}
		return "", fmt.Errorf("unsupported quota_probe type %q", value)
	}
}

func QuotaProbeTypeFromConfig(cfg *GatewayConfig) string {
	if cfg == nil || cfg.QuotaProbe == nil {
		return ""
	}
	normalized, err := NormalizeQuotaProbeType(*cfg.QuotaProbe)
	if err != nil {
		return ""
	}
	return normalized
}

func NormalizeGatewayConfig(input *GatewayConfig) (*GatewayConfig, error) {
	if input == nil {
		return nil, nil
	}

	normalized := *input
	normalizeLegacyResponsesToolConfig(&normalized)
	if normalized.ResponsesToolPolicy != "" {
		normalized.ResponsesToolPolicy = NormalizeResponsesToolPolicy(normalized.ResponsesToolPolicy)
	}
	hasDisabledResponsesTools := normalized.DisabledResponsesTools != nil
	normalized.DisabledResponsesTools = NormalizeDisabledResponsesTools(normalized.DisabledResponsesTools)
	if hasDisabledResponsesTools && normalized.DisabledResponsesTools == nil {
		normalized.DisabledResponsesTools = []string{}
	}
	if normalized.QuotaProbe != nil {
		probeType, err := NormalizeQuotaProbeType(*normalized.QuotaProbe)
		if err != nil {
			return nil, err
		}
		normalized.QuotaProbe = &probeType
	}
	normalized.Plugins = normalizeSitePlugins(normalized.Plugins)
	normalized.PluginIDs = normalizePluginIDs(normalized.PluginIDs)
	normalized.PluginSettings = normalizeSitePluginSettings(normalized.PluginSettings)
	for _, field := range []struct {
		name  string
		value *int
	}{
		{name: "request_timeout_ms", value: normalized.RequestTimeoutMS},
		{name: "connect_timeout_ms", value: normalized.ConnectTimeoutMS},
		{name: "response_header_timeout_ms", value: normalized.ResponseHeaderTimeoutMS},
		{name: "max_concurrency", value: normalized.MaxConcurrency},
		{name: "max_model_concurrency", value: normalized.MaxModelConcurrency},
		{name: "max_credential_concurrency", value: normalized.MaxCredentialConcurrency},
		{name: "max_idle_conns", value: normalized.MaxIdleConns},
		{name: "max_idle_conns_per_host", value: normalized.MaxIdleConnsPerHost},
		{name: "max_conns_per_host", value: normalized.MaxConnsPerHost},
		{name: "idle_conn_timeout_ms", value: normalized.IdleConnTimeoutMS},
	} {
		if field.value != nil && *field.value <= 0 {
			return nil, fmt.Errorf("%s must be greater than 0", field.name)
		}
	}

	return &normalized, nil
}

func NormalizeResponsesToolPolicy(value string) string {
	switch strings.TrimSpace(value) {
	case ResponsesToolPolicyCompatibility:
		return ResponsesToolPolicyCompatibility
	default:
		return ResponsesToolPolicyPassthrough
	}
}

func NormalizeDisabledResponsesTools(values []string) []string {
	seen := map[string]struct{}{}
	tools := make([]string, 0, len(values))
	for _, value := range values {
		tool := normalizeResponsesHostedTool(value)
		if tool == "" {
			continue
		}
		if _, ok := seen[tool]; ok {
			continue
		}
		seen[tool] = struct{}{}
		tools = append(tools, tool)
	}
	if len(tools) == 0 {
		return nil
	}
	return tools
}

func ResponsesToolDisabled(cfg *GatewayConfig, tool string) bool {
	if cfg == nil || NormalizeResponsesToolPolicy(cfg.ResponsesToolPolicy) != ResponsesToolPolicyCompatibility {
		return false
	}
	tool = normalizeResponsesHostedTool(tool)
	if tool == "" {
		return false
	}
	for _, item := range NormalizeDisabledResponsesTools(cfg.DisabledResponsesTools) {
		if item == tool {
			return true
		}
	}
	return false
}

func normalizeResponsesHostedTool(value string) string {
	switch strings.TrimSpace(value) {
	case ResponsesHostedToolImageGeneration:
		return ResponsesHostedToolImageGeneration
	default:
		return ""
	}
}

func normalizeLegacyResponsesToolConfig(cfg *GatewayConfig) {
	switch strings.TrimSpace(cfg.ResponsesImageGenerationPolicy) {
	case "strip_auto_tool", "disabled":
		if cfg.ResponsesToolPolicy == "" {
			cfg.ResponsesToolPolicy = ResponsesToolPolicyCompatibility
		}
		cfg.DisabledResponsesTools = append(cfg.DisabledResponsesTools, ResponsesHostedToolImageGeneration)
	}
	cfg.ResponsesImageGenerationPolicy = ""
}

func GatewayConfigFromSiteMeta(raw []byte) *GatewayConfig {
	if len(raw) == 0 {
		return nil
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}

	value, ok := root["gateway"]
	if !ok || len(value) == 0 || string(value) == "null" {
		return nil
	}

	var cfg GatewayConfig
	if err := json.Unmarshal(value, &cfg); err != nil {
		return nil
	}

	normalized, err := NormalizeGatewayConfig(&cfg)
	if err != nil {
		return nil
	}
	return normalized
}

func MergeSiteGatewayConfig(existingRaw []byte, gateway *GatewayConfig) ([]byte, error) {
	if gateway == nil {
		if len(existingRaw) == 0 {
			return []byte(`{}`), nil
		}
		return existingRaw, nil
	}

	normalized, err := NormalizeGatewayConfig(gateway)
	if err != nil {
		return nil, err
	}
	// Normalizing drops empty plugin ids, but in a patch an empty id means
	// "remove this binding", so the merge must see it.
	normalized.Plugins = gateway.Plugins
	// Normalizing turns an empty list into nil, which would look like "leave the
	// list alone". A patch distinguishes nil (untouched) from empty (clear).
	if gateway.PluginIDs != nil {
		normalized.PluginIDs = gateway.PluginIDs
	}
	normalized.PluginSettings = gateway.PluginSettings

	root := map[string]any{}
	if len(existingRaw) > 0 {
		_ = json.Unmarshal(existingRaw, &root)
	}
	if root == nil {
		root = map[string]any{}
	}
	existing := GatewayConfigFromSiteMeta(existingRaw)
	root["gateway"] = mergeGatewayConfig(existing, normalized)

	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("marshal site meta: %w", err)
	}
	return encoded, nil
}

func RequestHeadersFromSiteMeta(raw []byte) map[string]string {
	if len(raw) == 0 {
		return nil
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}

	value, ok := root["request_headers"]
	if !ok || len(value) == 0 || string(value) == "null" {
		return nil
	}

	var items []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(value, &items); err != nil {
		return nil
	}

	result := make(map[string]string, len(items))
	for _, item := range items {
		if item.Key != "" {
			result[item.Key] = item.Value
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func mergeGatewayConfig(existing *GatewayConfig, patch *GatewayConfig) *GatewayConfig {
	if patch == nil {
		return existing
	}
	if existing == nil {
		patch.Plugins = normalizeSitePlugins(patch.Plugins)
		patch.PluginIDs = normalizePluginIDs(patch.PluginIDs)
		if patch.PluginIDs != nil {
			clearLegacyPluginBindings(patch)
		}
		patch.PluginSettings = normalizeSitePluginSettings(patch.PluginSettings)
		return patch
	}
	merged := *existing
	if patch.RequestTimeoutMS != nil {
		merged.RequestTimeoutMS = patch.RequestTimeoutMS
	}
	if patch.ConnectTimeoutMS != nil {
		merged.ConnectTimeoutMS = patch.ConnectTimeoutMS
	}
	if patch.ResponseHeaderTimeoutMS != nil {
		merged.ResponseHeaderTimeoutMS = patch.ResponseHeaderTimeoutMS
	}
	if patch.MaxConcurrency != nil {
		merged.MaxConcurrency = patch.MaxConcurrency
	}
	if patch.MaxModelConcurrency != nil {
		merged.MaxModelConcurrency = patch.MaxModelConcurrency
	}
	if patch.MaxCredentialConcurrency != nil {
		merged.MaxCredentialConcurrency = patch.MaxCredentialConcurrency
	}
	if patch.MaxIdleConns != nil {
		merged.MaxIdleConns = patch.MaxIdleConns
	}
	if patch.MaxIdleConnsPerHost != nil {
		merged.MaxIdleConnsPerHost = patch.MaxIdleConnsPerHost
	}
	if patch.MaxConnsPerHost != nil {
		merged.MaxConnsPerHost = patch.MaxConnsPerHost
	}
	if patch.IdleConnTimeoutMS != nil {
		merged.IdleConnTimeoutMS = patch.IdleConnTimeoutMS
	}
	if patch.ResponsesToolPolicy != "" {
		merged.ResponsesToolPolicy = NormalizeResponsesToolPolicy(patch.ResponsesToolPolicy)
	}
	if patch.DisabledResponsesTools != nil {
		merged.DisabledResponsesTools = NormalizeDisabledResponsesTools(patch.DisabledResponsesTools)
	}
	if patch.ImpersonateCodexClient != nil {
		merged.ImpersonateCodexClient = patch.ImpersonateCodexClient
	}
	if patch.ImpersonateClaudeCodeClient != nil {
		merged.ImpersonateClaudeCodeClient = patch.ImpersonateClaudeCodeClient
	}
	if patch.Plugins != nil {
		merged.Plugins = map[string]string{}
		for kind, id := range existing.Plugins {
			merged.Plugins[kind] = id
		}
		for _, kind := range SitePluginKinds {
			id, present := patch.Plugins[kind]
			if !present {
				continue
			}
			if strings.TrimSpace(id) == "" {
				delete(merged.Plugins, kind)
			} else {
				merged.Plugins[kind] = strings.TrimSpace(id)
			}
		}
		merged.Plugins = normalizeSitePlugins(merged.Plugins)
	}
	if patch.PluginIDs != nil {
		merged.PluginIDs = normalizePluginIDs(patch.PluginIDs)
		clearLegacyPluginBindings(&merged)
	}
	if patch.QuotaProbe != nil {
		if value, err := NormalizeQuotaProbeType(*patch.QuotaProbe); err == nil {
			if value == "" {
				merged.QuotaProbe = nil
			} else {
				merged.QuotaProbe = &value
			}
		}
	}
	if patch.PluginSettings != nil {
		merged.PluginSettings = map[string]map[string]any{}
		for id, vals := range existing.PluginSettings {
			copied := make(map[string]any, len(vals))
			for k, v := range vals {
				copied[k] = v
			}
			merged.PluginSettings[id] = copied
		}
		for id, patchVals := range patch.PluginSettings {
			pluginID := strings.TrimSpace(id)
			if pluginID == "" {
				continue
			}
			if len(patchVals) == 0 {
				delete(merged.PluginSettings, pluginID)
				continue
			}
			target := merged.PluginSettings[pluginID]
			if target == nil {
				target = map[string]any{}
			}
			for k, v := range patchVals {
				if v == nil {
					delete(target, k)
				} else {
					target[k] = v
				}
			}
			merged.PluginSettings[pluginID] = target
		}
		merged.PluginSettings = normalizeSitePluginSettings(merged.PluginSettings)
	}
	return &merged
}

// clearLegacyPluginBindings drops the old per-kind map once plugin_ids is the
// source of truth, and drops a plugin: quota probe so it is not read twice.
// A built-in probe type stays.
func clearLegacyPluginBindings(cfg *GatewayConfig) {
	if cfg == nil {
		return
	}
	cfg.Plugins = nil
	if cfg.QuotaProbe == nil {
		return
	}
	value := strings.TrimSpace(*cfg.QuotaProbe)
	if strings.HasPrefix(value, QuotaProbePluginPrefix) {
		cfg.QuotaProbe = nil
	}
}

// SitePluginIDs returns the plugins mounted on a site, in the order a hook
// search uses: plugin_ids, then a legacy plugin: quota probe, then the legacy
// plugins map in SitePluginKinds order. Duplicates are dropped. A nil config
// returns nil.
func SitePluginIDs(cfg *GatewayConfig) []string {
	if cfg == nil {
		return nil
	}
	var ids []string
	ids = append(ids, cfg.PluginIDs...)
	if probe := QuotaProbeTypeFromConfig(cfg); strings.HasPrefix(probe, QuotaProbePluginPrefix) {
		ids = append(ids, strings.TrimPrefix(probe, QuotaProbePluginPrefix))
	}
	for _, kind := range SitePluginKinds {
		if id := strings.TrimSpace(cfg.Plugins[kind]); id != "" {
			ids = append(ids, id)
		}
	}
	return normalizePluginIDs(ids)
}
