package jsplugin

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The gatewayPipeline section lets a plugin inspect and transform the text of
// downstream requests for the API keys an admin binds it to, and restore the
// transformed text in responses. The host does the protocol work; the plugin
// declares rules, dictionaries and policies, and may add an inspectSegments hook.
const (
	// PermissionPayloadInspectRaw lets the pipeline and its hook read the
	// request text before any transform. It needs a trusted signature and rules
	// out every way to send or keep that text.
	PermissionPayloadInspectRaw = "payload.inspect_raw"
	// HookInspectSegments reviews the extracted segments and the native findings.
	HookInspectSegments = "inspectSegments"

	PipelineScopeAPIKey = "api_key"

	PipelineFailOpen   = "fail_open"
	PipelineFailClosed = "fail_closed"

	PipelineUnsupportedPass   = "pass"
	PipelineUnsupportedReject = "reject"

	PipelineTransformTokenPlaceholder = "token_placeholder"
	PipelineTransformNone             = "none"

	DefaultPipelineMaxInspectBytes    = 8 << 20
	MaxPipelineMaxInspectBytes        = 16 << 20
	DefaultPipelineTotalTimeoutMs     = 1000
	MaxPipelineTotalTimeoutMs         = 10000
	DefaultPipelinePropagateMinLength = 2
	MaxPipelineSystemHintBytes        = 1024
	DefaultPipelineContextWindow      = 20

	maxPipelineTypes        = 32
	maxPipelineRules        = 64
	maxPipelineRulePattern  = 1024
	maxPipelineDictionaries = 8
	maxPipelineContextWords = 32
	maxPipelineContextWord  = 64
	maxPipelineContextRunes = 256
	maxPipelineMinLength    = 64
)

// Rule boundaries and validators the native matcher implements.
var (
	PipelineBoundaries = []string{"none", "digit", "alnum", "word"}
	PipelineValidators = []string{"luhn", "cn_id_card", "ipv4", "ipv6", "email"}
)

var pipelineTypePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,23}$`)

// GatewayPipelineSection is the gatewayPipeline manifest block.
type GatewayPipelineSection struct {
	Scope               string                 `json:"scope" ts:"scope,type=\"api_key\",doc=What an admin binds the pipeline to. Only api_key for now."`
	Exclusive           bool                   `json:"exclusive,omitempty" ts:"exclusive,optional,doc=Each API key holds at most one gateway pipeline binding."`
	FailurePolicy       string                 `json:"failurePolicy,omitempty" ts:"failurePolicy,optional,type=\"fail_open\" | \"fail_closed\",doc=fail_open (default) skips the pipeline when it cannot run. fail_closed rejects the request with 503 instead."`
	UnsupportedEndpoint string                 `json:"unsupportedEndpoint,omitempty" ts:"unsupportedEndpoint,optional,type=\"pass\" | \"reject\",doc=What a bound key gets on an endpoint the host cannot extract text from. pass (default) or reject with 400 plugin_unsupported_endpoint."`
	MaxInspectBytes     int                    `json:"maxInspectBytes,omitempty" ts:"maxInspectBytes,optional,doc=Largest total of extracted text per request. Default 8 MiB, at most 16 MiB. Larger requests get 413 plugin_payload_too_large."`
	TotalTimeoutMs      int                    `json:"totalTimeoutMs,omitempty" ts:"totalTimeoutMs,optional,doc=Budget of all inspectSegments calls of one request. Default 1000, at most 10000."`
	Transform           PipelineTransformSpec  `json:"transform,omitzero" ts:"transform,optional"`
	Types               []PipelineTypeSpec     `json:"types,omitempty" ts:"types,optional"`
	Rules               []PipelineRuleSpec     `json:"rules,omitempty" ts:"rules,optional"`
	Dictionaries        []PipelineDictSpec     `json:"dictionaries,omitempty" ts:"dictionaries,optional"`
	Allowlist           *PipelineAllowlistSpec `json:"allowlist,omitempty" ts:"allowlist,optional"`
	Inputs              []AutomationInputSpec  `json:"inputs" ts:"inputs,type=JsonObject[],doc=Binding form inputs, written like automation inputs. Exactly one input has type api_key."`
	Form                AutomationForm         `json:"form,omitzero" ts:"form,optional,type=JsonObject"`
}

// PipelineTransformSpec says what happens to the findings.
type PipelineTransformSpec struct {
	Mode               string `json:"mode,omitempty" ts:"mode,optional,type=\"token_placeholder\" | \"none\",doc=token_placeholder replaces each finding with [[TYPE_N]]. none (default) leaves the text alone."`
	Reversible         *bool  `json:"reversible,omitempty" ts:"reversible,optional,doc=Restore placeholders in responses. Defaults to true with token_placeholder."`
	PropagateMinLength int    `json:"propagateMinLength,omitempty" ts:"propagateMinLength,optional,doc=Shortest value, in characters, replaced again wherever else it appears in the request. Default 2."`
	SystemHint         string `json:"systemHint,omitempty" ts:"systemHint,optional,doc=Appended to the system prompt when the request got at least one new placeholder. At most 1024 bytes."`
}

// PipelineTypeSpec declares one finding type. Earlier types win overlaps of equal length.
type PipelineTypeSpec struct {
	Type      string        `json:"type" ts:"type"`
	Label     LocalizedText `json:"label,omitempty" ts:"label,optional,type=string | Record<string, string>"`
	EnabledBy string        `json:"enabledBy,omitempty" ts:"enabledBy,optional,doc=A boolean input that turns this type on and off."`
}

// PipelineRuleSpec is one RE2 rule run by the host.
type PipelineRuleSpec struct {
	Type          string   `json:"type" ts:"type"`
	Pattern       string   `json:"pattern" ts:"pattern,doc=Go RE2 syntax: linear time, no backreferences or lookaround."`
	Group         int      `json:"group,omitempty" ts:"group,optional,doc=Capture group that is the finding. 0 is the whole match."`
	Boundary      string   `json:"boundary,omitempty" ts:"boundary,optional,type=\"none\" | \"digit\" | \"alnum\" | \"word\""`
	Validate      string   `json:"validate,omitempty" ts:"validate,optional,type=\"luhn\" | \"cn_id_card\" | \"ipv4\" | \"ipv6\" | \"email\""`
	MinEntropy    float64  `json:"minEntropy,omitempty" ts:"minEntropy,optional,doc=Least Shannon entropy in bits per character."`
	ContextAny    []string `json:"contextAny,omitempty" ts:"contextAny,optional,doc=Keep the finding only when one of these words is near it (case-insensitive)."`
	ContextWindow int      `json:"contextWindow,omitempty" ts:"contextWindow,optional,doc=Characters searched on each side for contextAny. Default 20."`
}

// PipelineDictSpec matches the lines of a textarea input as literal words.
type PipelineDictSpec struct {
	Param         string `json:"param" ts:"param,doc=A string input. One word per line; word<TAB>TYPE overrides the type."`
	Type          string `json:"type" ts:"type"`
	CaseSensitive bool   `json:"caseSensitive,omitempty" ts:"caseSensitive,optional"`
}

// PipelineAllowlistSpec names a textarea input of values never reported.
type PipelineAllowlistSpec struct {
	Param         string `json:"param" ts:"param"`
	CaseSensitive bool   `json:"caseSensitive,omitempty" ts:"caseSensitive,optional"`
}

// DeclaresGatewayPipeline reports whether the manifest has a gatewayPipeline section.
func (m Manifest) DeclaresGatewayPipeline() bool {
	return m.GatewayPipeline != nil
}

// RequiresTrustedSignature reports whether enabling the package needs a
// trusted signature or a marketplace attestation that ConfirmUntrusted cannot
// replace: a site type, plain credentials, or raw request text.
func RequiresTrustedSignature(manifest Manifest) bool {
	return manifest.DeclaresSiteType() ||
		contains(manifest.Permissions, "secret.use") ||
		contains(manifest.Permissions, PermissionPayloadInspectRaw)
}

// FailurePolicyOrDefault returns the declared policy, fail_open when unset.
func (s GatewayPipelineSection) FailurePolicyOrDefault() string {
	if s.FailurePolicy == "" {
		return PipelineFailOpen
	}
	return s.FailurePolicy
}

// UnsupportedEndpointOrDefault returns the declared policy, pass when unset.
func (s GatewayPipelineSection) UnsupportedEndpointOrDefault() string {
	if s.UnsupportedEndpoint == "" {
		return PipelineUnsupportedPass
	}
	return s.UnsupportedEndpoint
}

// MaxInspectBytesOrDefault returns the extracted text limit.
func (s GatewayPipelineSection) MaxInspectBytesOrDefault() int {
	if s.MaxInspectBytes <= 0 {
		return DefaultPipelineMaxInspectBytes
	}
	return s.MaxInspectBytes
}

// TotalTimeoutMsOrDefault returns the inspectSegments budget of one request.
func (s GatewayPipelineSection) TotalTimeoutMsOrDefault() int {
	if s.TotalTimeoutMs <= 0 {
		return DefaultPipelineTotalTimeoutMs
	}
	return s.TotalTimeoutMs
}

// ModeOrDefault returns the transform mode, none when unset.
func (t PipelineTransformSpec) ModeOrDefault() string {
	if t.Mode == "" {
		return PipelineTransformNone
	}
	return t.Mode
}

// IsReversible reports whether placeholders are restored in responses.
func (t PipelineTransformSpec) IsReversible() bool {
	if t.ModeOrDefault() != PipelineTransformTokenPlaceholder {
		return false
	}
	return t.Reversible == nil || *t.Reversible
}

// PropagateMinLengthOrDefault returns the shortest value replaced again elsewhere.
func (t PipelineTransformSpec) PropagateMinLengthOrDefault() int {
	if t.PropagateMinLength <= 0 {
		return DefaultPipelinePropagateMinLength
	}
	return t.PropagateMinLength
}

// TypeNames lists the declared types in declaration order.
func (s GatewayPipelineSection) TypeNames() []string {
	out := make([]string, 0, len(s.Types))
	for _, spec := range s.Types {
		out = append(out, spec.Type)
	}
	return out
}

// APIKeyInput returns the input that picks the bound API keys.
func (s GatewayPipelineSection) APIKeyInput() (AutomationInputSpec, bool) {
	for _, input := range s.Inputs {
		if input.Type == EntityAPIKey {
			return input, true
		}
	}
	return AutomationInputSpec{}, false
}

func validateGatewayPipelineSection(section GatewayPipelineSection) error {
	if section.Scope != PipelineScopeAPIKey {
		return fmt.Errorf("gatewayPipeline.scope must be %q", PipelineScopeAPIKey)
	}
	switch section.FailurePolicy {
	case "", PipelineFailOpen, PipelineFailClosed:
	default:
		return fmt.Errorf("gatewayPipeline.failurePolicy %q is not supported (use %s or %s)", section.FailurePolicy, PipelineFailOpen, PipelineFailClosed)
	}
	switch section.UnsupportedEndpoint {
	case "", PipelineUnsupportedPass, PipelineUnsupportedReject:
	default:
		return fmt.Errorf("gatewayPipeline.unsupportedEndpoint %q is not supported (use %s or %s)", section.UnsupportedEndpoint, PipelineUnsupportedPass, PipelineUnsupportedReject)
	}
	if section.MaxInspectBytes < 0 || section.MaxInspectBytes > MaxPipelineMaxInspectBytes {
		return fmt.Errorf("gatewayPipeline.maxInspectBytes must be between 1 and %d", MaxPipelineMaxInspectBytes)
	}
	if section.TotalTimeoutMs < 0 || section.TotalTimeoutMs > MaxPipelineTotalTimeoutMs {
		return fmt.Errorf("gatewayPipeline.totalTimeoutMs must be between 1 and %d", MaxPipelineTotalTimeoutMs)
	}
	if err := validatePipelineTransform(section.Transform); err != nil {
		return err
	}
	inputs, err := validatePipelineInputs(section)
	if err != nil {
		return err
	}
	if err := validatePipelineForm(section.Form); err != nil {
		return err
	}
	declared, err := validatePipelineTypes(section.Types, inputs)
	if err != nil {
		return err
	}
	if len(section.Types) == 0 && (len(section.Rules) > 0 || len(section.Dictionaries) > 0) {
		return fmt.Errorf("gatewayPipeline.types must declare the types its rules and dictionaries report")
	}
	if section.Transform.ModeOrDefault() == PipelineTransformTokenPlaceholder && len(section.Types) == 0 {
		return fmt.Errorf("gatewayPipeline.types must declare at least one type for token_placeholder")
	}
	if len(section.Rules) > maxPipelineRules {
		return fmt.Errorf("gatewayPipeline.rules has more than %d entries", maxPipelineRules)
	}
	for i, rule := range section.Rules {
		if err := validatePipelineRule(rule, declared); err != nil {
			return fmt.Errorf("gatewayPipeline.rules[%d]: %w", i, err)
		}
	}
	if len(section.Dictionaries) > maxPipelineDictionaries {
		return fmt.Errorf("gatewayPipeline.dictionaries has more than %d entries", maxPipelineDictionaries)
	}
	for i, dict := range section.Dictionaries {
		if err := requirePipelineParam(inputs, dict.Param, "string"); err != nil {
			return fmt.Errorf("gatewayPipeline.dictionaries[%d].param: %w", i, err)
		}
		if !declared[dict.Type] {
			return fmt.Errorf("gatewayPipeline.dictionaries[%d].type %q is not declared in gatewayPipeline.types", i, dict.Type)
		}
	}
	if section.Allowlist != nil {
		if err := requirePipelineParam(inputs, section.Allowlist.Param, "string"); err != nil {
			return fmt.Errorf("gatewayPipeline.allowlist.param: %w", err)
		}
	}
	return nil
}

func validatePipelineTransform(spec PipelineTransformSpec) error {
	switch spec.Mode {
	case "", PipelineTransformNone, PipelineTransformTokenPlaceholder:
	default:
		return fmt.Errorf("gatewayPipeline.transform.mode %q is not supported (use %s or %s)", spec.Mode, PipelineTransformTokenPlaceholder, PipelineTransformNone)
	}
	if spec.ModeOrDefault() != PipelineTransformTokenPlaceholder {
		if spec.Reversible != nil && *spec.Reversible {
			return fmt.Errorf("gatewayPipeline.transform.reversible needs mode %q", PipelineTransformTokenPlaceholder)
		}
		if spec.SystemHint != "" {
			return fmt.Errorf("gatewayPipeline.transform.systemHint needs mode %q", PipelineTransformTokenPlaceholder)
		}
	}
	if spec.PropagateMinLength < 0 || spec.PropagateMinLength > maxPipelineMinLength {
		return fmt.Errorf("gatewayPipeline.transform.propagateMinLength must be between 1 and %d", maxPipelineMinLength)
	}
	if len(spec.SystemHint) > MaxPipelineSystemHintBytes || !utf8.ValidString(spec.SystemHint) {
		return fmt.Errorf("gatewayPipeline.transform.systemHint must be valid UTF-8 of at most %d bytes", MaxPipelineSystemHintBytes)
	}
	for _, r := range spec.SystemHint {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return fmt.Errorf("gatewayPipeline.transform.systemHint must not contain control characters")
		}
	}
	return nil
}

func validatePipelineInputs(section GatewayPipelineSection) (map[string]AutomationInputSpec, error) {
	if len(section.Inputs) > MaxAutomationInputs {
		return nil, fmt.Errorf("gatewayPipeline.inputs has more than %d entries", MaxAutomationInputs)
	}
	inputs := make(map[string]AutomationInputSpec, len(section.Inputs))
	apiKeys := 0
	for i := range section.Inputs {
		input := &section.Inputs[i]
		if !inputNamePattern.MatchString(input.Name) {
			return nil, fmt.Errorf("gatewayPipeline.inputs[%d].name %q must start with a lowercase letter and contain only letters and digits", i, input.Name)
		}
		if _, dup := inputs[input.Name]; dup {
			return nil, fmt.Errorf("gatewayPipeline.inputs: duplicate name %q", input.Name)
		}
		for label, text := range map[string]LocalizedText{
			"title": input.Title, "description": input.Description, "placeholder": input.Placeholder, "emptyText": input.EmptyText,
		} {
			if err := text.validate(); err != nil {
				return nil, fmt.Errorf("gatewayPipeline.inputs.%s.%s: %w", input.Name, label, err)
			}
		}
		if input.EventSubject {
			return nil, fmt.Errorf("gatewayPipeline.inputs.%s: eventSubject is for automation inputs", input.Name)
		}
		if err := validateInputSpec(input); err != nil {
			return nil, fmt.Errorf("gatewayPipeline.inputs.%s: %w", input.Name, err)
		}
		if input.IsEntity() && input.Type != EntityAPIKey {
			return nil, fmt.Errorf("gatewayPipeline.inputs.%s: type %q cannot be picked for a gateway pipeline", input.Name, input.Type)
		}
		if input.Type == EntityAPIKey {
			apiKeys++
			if input.Requires != "" {
				return nil, fmt.Errorf("gatewayPipeline.inputs.%s: requires does not apply to a gateway pipeline", input.Name)
			}
		}
		inputs[input.Name] = *input
	}
	if section.Scope == PipelineScopeAPIKey && apiKeys != 1 {
		return nil, fmt.Errorf("gatewayPipeline.inputs must have exactly one input of type %q", EntityAPIKey)
	}
	return inputs, nil
}

func validatePipelineForm(form AutomationForm) error {
	for label, text := range map[string]LocalizedText{
		"form.title": form.Title, "form.description": form.Description,
		"form.addLabel": form.AddLabel, "form.emptyText": form.EmptyText,
	} {
		if err := text.validate(); err != nil {
			return fmt.Errorf("gatewayPipeline.%s: %w", label, err)
		}
	}
	return nil
}

func validatePipelineTypes(types []PipelineTypeSpec, inputs map[string]AutomationInputSpec) (map[string]bool, error) {
	if len(types) > maxPipelineTypes {
		return nil, fmt.Errorf("gatewayPipeline.types has more than %d entries", maxPipelineTypes)
	}
	declared := make(map[string]bool, len(types))
	for i, spec := range types {
		if !pipelineTypePattern.MatchString(spec.Type) {
			return nil, fmt.Errorf("gatewayPipeline.types[%d].type %q must match %s", i, spec.Type, pipelineTypePattern.String())
		}
		if declared[spec.Type] {
			return nil, fmt.Errorf("gatewayPipeline.types: duplicate type %q", spec.Type)
		}
		declared[spec.Type] = true
		if err := spec.Label.validate(); err != nil {
			return nil, fmt.Errorf("gatewayPipeline.types.%s.label: %w", spec.Type, err)
		}
		if spec.EnabledBy != "" {
			if err := requirePipelineParam(inputs, spec.EnabledBy, "boolean"); err != nil {
				return nil, fmt.Errorf("gatewayPipeline.types.%s.enabledBy: %w", spec.Type, err)
			}
		}
	}
	return declared, nil
}

func validatePipelineRule(rule PipelineRuleSpec, declared map[string]bool) error {
	if !declared[rule.Type] {
		return fmt.Errorf("type %q is not declared in gatewayPipeline.types", rule.Type)
	}
	if rule.Pattern == "" || len(rule.Pattern) > maxPipelineRulePattern {
		return fmt.Errorf("pattern must be 1..%d bytes", maxPipelineRulePattern)
	}
	compiled, err := regexp.Compile(rule.Pattern)
	if err != nil {
		return fmt.Errorf("pattern: %w", err)
	}
	if rule.Group < 0 || rule.Group > compiled.NumSubexp() {
		return fmt.Errorf("group %d is out of range (the pattern has %d groups)", rule.Group, compiled.NumSubexp())
	}
	if rule.Boundary != "" && !contains(PipelineBoundaries, rule.Boundary) {
		return fmt.Errorf("boundary %q is not supported (use one of: %s)", rule.Boundary, strings.Join(PipelineBoundaries, ", "))
	}
	if rule.Validate != "" && !contains(PipelineValidators, rule.Validate) {
		return fmt.Errorf("validate %q is not supported (use one of: %s)", rule.Validate, strings.Join(PipelineValidators, ", "))
	}
	if rule.MinEntropy < 0 || rule.MinEntropy > 32 {
		return fmt.Errorf("minEntropy must be between 0 and 32")
	}
	if len(rule.ContextAny) > maxPipelineContextWords {
		return fmt.Errorf("contextAny has more than %d words", maxPipelineContextWords)
	}
	for _, word := range rule.ContextAny {
		if strings.TrimSpace(word) == "" || len(word) > maxPipelineContextWord {
			return fmt.Errorf("contextAny words must be 1..%d bytes", maxPipelineContextWord)
		}
	}
	if rule.ContextWindow < 0 || rule.ContextWindow > maxPipelineContextRunes {
		return fmt.Errorf("contextWindow must be between 1 and %d", maxPipelineContextRunes)
	}
	if rule.ContextWindow > 0 && len(rule.ContextAny) == 0 {
		return fmt.Errorf("contextWindow needs contextAny")
	}
	return nil
}

func requirePipelineParam(inputs map[string]AutomationInputSpec, name, paramType string) error {
	input, ok := inputs[name]
	if !ok {
		return fmt.Errorf("%q is not an input of gatewayPipeline", name)
	}
	if input.Type != paramType {
		return fmt.Errorf("input %q must have type %q", name, paramType)
	}
	return nil
}

// validatePipelinePermissions keeps raw request text away from every channel
// that could send or keep it.
func validatePipelinePermissions(manifest Manifest) error {
	if !contains(manifest.Permissions, PermissionPayloadInspectRaw) {
		return nil
	}
	if !manifest.DeclaresGatewayPipeline() {
		return fmt.Errorf("permission %s needs a gatewayPipeline section", PermissionPayloadInspectRaw)
	}
	for _, perm := range manifest.Permissions {
		if perm == PermissionStorage || perm == "http" || perm == "schedule" || strings.HasPrefix(perm, "net:") {
			return fmt.Errorf("permission %s cannot be combined with %s", PermissionPayloadInspectRaw, perm)
		}
	}
	if automationSectionPresent(manifest) {
		return fmt.Errorf("permission %s cannot be combined with an automation section", PermissionPayloadInspectRaw)
	}
	if !manifest.Schedule.Empty() {
		return fmt.Errorf("permission %s cannot be combined with a schedule section", PermissionPayloadInspectRaw)
	}
	return nil
}

// pipelineForbiddenHooks may not be exported by a plugin that reads raw text.
var pipelineForbiddenHooks = []string{"handle", "onEvent", HookOnSchedule}

// validatePipelineProgram checks the hooks of a gatewayPipeline plugin. It may
// export inspectSegments and nothing else, since every other hook belongs to
// a scope the pipeline binding does not reach.
func validatePipelineProgram(manifest Manifest, program *program) error {
	if contains(manifest.Permissions, PermissionPayloadInspectRaw) {
		for _, name := range pipelineForbiddenHooks {
			if _, ok := program.hooks[name]; ok {
				return fmt.Errorf("export %s cannot be combined with permission %s", name, PermissionPayloadInspectRaw)
			}
		}
	}
	if _, ok := program.hooks[HookInspectSegments]; ok {
		if !manifest.DeclaresGatewayPipeline() {
			return fmt.Errorf("export %s requires a \"gatewayPipeline\" section", HookInspectSegments)
		}
		if !contains(manifest.Permissions, PermissionPayloadInspectRaw) {
			return fmt.Errorf("export %s requires permission %s", HookInspectSegments, PermissionPayloadInspectRaw)
		}
	}
	if !manifest.DeclaresGatewayPipeline() {
		return nil
	}
	for _, name := range program.knownHooks() {
		if name != HookInspectSegments {
			return fmt.Errorf("a gatewayPipeline plugin can only export %s, not %s", HookInspectSegments, name)
		}
	}
	return nil
}
