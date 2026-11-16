package jsplugin

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	maxManifestSettings       = 32
	maxSettingStringBytes     = 1024
	maxSettingPatternBytes    = 256
	maxSettingOptions         = 32
	SettingTypeString         = "string"
	SettingTypeNumber         = "number"
	SettingTypeBoolean        = "boolean"
)

var settingKeyPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)

// SettingSpec declares one admin-configurable constant in a plugin manifest.
type SettingSpec struct {
	Key         string        `json:"key"`
	Type        string        `json:"type"`
	Default     any           `json:"default"`
	Pattern     string        `json:"pattern,omitempty"`
	Min         *float64      `json:"min,omitempty"`
	Max         *float64      `json:"max,omitempty"`
	Options     []string      `json:"options,omitempty"`
	Label       LocalizedText `json:"label,omitempty"`
	Description LocalizedText `json:"description,omitempty"`
}

func validateSettingSpecs(specs []SettingSpec) error {
	if len(specs) == 0 {
		return nil
	}
	if len(specs) > maxManifestSettings {
		return fmt.Errorf("settings must declare at most %d entries", maxManifestSettings)
	}
	seen := make(map[string]struct{}, len(specs))
	for i, spec := range specs {
		path := fmt.Sprintf("settings[%d]", i)
		if !settingKeyPattern.MatchString(spec.Key) {
			return fmt.Errorf("%s.key %q is invalid", path, spec.Key)
		}
		if _, dup := seen[spec.Key]; dup {
			return fmt.Errorf("%s.key: duplicate key %q", path, spec.Key)
		}
		seen[spec.Key] = struct{}{}
		if err := spec.Label.validate(); err != nil {
			return fmt.Errorf("%s.label: %w", path, err)
		}
		if err := spec.Description.validate(); err != nil {
			return fmt.Errorf("%s.description: %w", path, err)
		}
		if err := validateSettingSpecConstraints(spec, path); err != nil {
			return err
		}
		if spec.Default == nil {
			return fmt.Errorf("%s.default is required", path)
		}
		if _, err := normalizeSettingValue(spec, spec.Default, path+".default"); err != nil {
			return err
		}
	}
	return nil
}

func validateSettingSpecConstraints(spec SettingSpec, path string) error {
	switch spec.Type {
	case SettingTypeString:
		if spec.Min != nil || spec.Max != nil {
			return fmt.Errorf("%s: min and max are only supported for number settings", path)
		}
		if len(spec.Pattern) > maxSettingPatternBytes {
			return fmt.Errorf("%s.pattern exceeds %d bytes", path, maxSettingPatternBytes)
		}
		if spec.Pattern != "" {
			if _, err := regexp.Compile(spec.Pattern); err != nil {
				return fmt.Errorf("%s.pattern: %w", path, err)
			}
		}
		if len(spec.Options) > maxSettingOptions {
			return fmt.Errorf("%s.options exceeds %d items", path, maxSettingOptions)
		}
		for j, opt := range spec.Options {
			if opt == "" || len(opt) > maxSettingStringBytes {
				return fmt.Errorf("%s.options[%d] is invalid", path, j)
			}
		}
	case SettingTypeNumber:
		if spec.Pattern != "" || len(spec.Options) > 0 {
			return fmt.Errorf("%s: pattern and options are only supported for string settings", path)
		}
		if spec.Min != nil && (math.IsNaN(*spec.Min) || math.IsInf(*spec.Min, 0)) {
			return fmt.Errorf("%s.min must be finite", path)
		}
		if spec.Max != nil && (math.IsNaN(*spec.Max) || math.IsInf(*spec.Max, 0)) {
			return fmt.Errorf("%s.max must be finite", path)
		}
		if spec.Min != nil && spec.Max != nil && *spec.Min > *spec.Max {
			return fmt.Errorf("%s: min must not exceed max", path)
		}
	case SettingTypeBoolean:
		if spec.Pattern != "" || len(spec.Options) > 0 || spec.Min != nil || spec.Max != nil {
			return fmt.Errorf("%s: boolean settings do not accept constraints", path)
		}
	default:
		return fmt.Errorf("%s.type %q is not supported", path, spec.Type)
	}
	return nil
}

func normalizeSettingValue(spec SettingSpec, raw any, path string) (any, error) {
	switch spec.Type {
	case SettingTypeString:
		text, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("%s: expected string", path)
		}
		if len(text) > maxSettingStringBytes || !utf8.ValidString(text) {
			return nil, fmt.Errorf("%s: exceeds %d valid utf-8 bytes", path, maxSettingStringBytes)
		}
		if len(spec.Options) > 0 && !contains(spec.Options, text) {
			return nil, fmt.Errorf("%s: %q is not one of [%s]", path, text, strings.Join(spec.Options, ", "))
		}
		if spec.Pattern != "" {
			re, err := regexp.Compile(spec.Pattern)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			if !re.MatchString(text) {
				return nil, fmt.Errorf("%s: value does not match pattern %q", path, spec.Pattern)
			}
		}
		return text, nil
	case SettingTypeNumber:
		num, ok := asFloat(raw)
		if !ok || math.IsNaN(num) || math.IsInf(num, 0) {
			return nil, fmt.Errorf("%s: expected finite number", path)
		}
		if spec.Min != nil && num < *spec.Min {
			return nil, fmt.Errorf("%s: must be at least %v", path, *spec.Min)
		}
		if spec.Max != nil && num > *spec.Max {
			return nil, fmt.Errorf("%s: must be at most %v", path, *spec.Max)
		}
		return num, nil
	case SettingTypeBoolean:
		flag, ok := raw.(bool)
		if !ok {
			return nil, fmt.Errorf("%s: expected boolean", path)
		}
		return flag, nil
	default:
		return nil, fmt.Errorf("%s: unsupported setting type %q", path, spec.Type)
	}
}

// ValidateSettingValues validates admin-supplied setting overrides against specs
// and returns the normalized map. Unknown keys are rejected.
func ValidateSettingValues(specs []SettingSpec, values map[string]any) (map[string]any, error) {
	if len(values) == 0 {
		return map[string]any{}, nil
	}
	byKey := make(map[string]SettingSpec, len(specs))
	for _, spec := range specs {
		byKey[spec.Key] = spec
	}
	out := make(map[string]any, len(values))
	for key, raw := range values {
		spec, ok := byKey[key]
		if !ok {
			return nil, fmt.Errorf("unknown setting %q", key)
		}
		if raw == nil {
			continue
		}
		normalized, err := normalizeSettingValue(spec, raw, "settings."+key)
		if err != nil {
			return nil, err
		}
		out[key] = normalized
	}
	return out, nil
}

// ResolveSettings merges manifest defaults, global overrides, and site overrides.
// Invalid or undeclared override keys stored from an older version are skipped
// so a version upgrade never crashes runtime execution.
func ResolveSettings(specs []SettingSpec, layers ...map[string]any) map[string]any {
	if len(specs) == 0 {
		return nil
	}
	out := make(map[string]any, len(specs))
	byKey := make(map[string]SettingSpec, len(specs))
	for _, spec := range specs {
		byKey[spec.Key] = spec
		if val, err := normalizeSettingValue(spec, spec.Default, "default"); err == nil {
			out[spec.Key] = val
		}
	}
	for _, layer := range layers {
		for key, raw := range layer {
			spec, ok := byKey[key]
			if !ok || raw == nil {
				continue
			}
			if val, err := normalizeSettingValue(spec, raw, key); err == nil {
				out[key] = val
			}
		}
	}
	return out
}

func decodeStoredSettingsJSON(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}
