package jsplugin

import (
	"encoding/json"
	"fmt"
	"strings"
)

var probeMethods = map[string]struct{}{
	"GET": {}, "POST": {}, "PUT": {}, "PATCH": {}, "DELETE": {},
}

// TransportProfileSpec is the manifest transport block in the plugin SDK.
// Profile is a built-in TLS fingerprint. bun is not supported by this build.
type TransportProfileSpec struct {
	Profile string `ts:"profile,optional,type=\"claude_code\",doc=Built-in TLS fingerprint. bun is not supported by this build."`
}

// StreamUnwrapSpec pulls the real chunk out of each SSE data payload.
type StreamUnwrapSpec struct {
	Path       string `ts:"path,doc=Dot path with numeric indexes, same syntax as secretPaths."`
	Format     string `ts:"format,optional,type=\"json\" | \"json_string\" | \"raw\",doc=json reads an object or array. json_string parses a JSON string. raw uses the string as the payload. Default is json."`
	StatusPath string `ts:"statusPath,optional,doc=Envelope status path. Checked only when okStatuses is set."`
	OKStatuses []int  `ts:"okStatuses,optional,doc=Allowed envelope statuses. Empty skips the check."`
}

// StreamDoneSpec names extra end-of-stream markers.
type StreamDoneSpec struct {
	DataEquals      []string `ts:"dataEquals,optional,doc=Original data payload equals one of these."`
	UnwrappedEquals []string `ts:"unwrappedEquals,optional,doc=Unwrapped payload equals one of these."`
	Events          []string `ts:"events,optional,doc=SSE event name equals one of these."`
	CloseOnDone     bool     `ts:"closeOnDone,optional,doc=Stop reading the upstream body after a done marker."`
}

// StreamUsageSpec maps token counts onto the standard usage object.
// cost and billable are not applied.
type StreamUsageSpec struct {
	InputTokens     string `ts:"inputTokens,optional,doc=Copied to usage.prompt_tokens when that field is absent."`
	OutputTokens    string `ts:"outputTokens,optional,doc=Copied to usage.completion_tokens when that field is absent."`
	CachedTokens    string `ts:"cachedTokens,optional,doc=Copied to usage.prompt_tokens_details.cached_tokens when that field is absent."`
	ReasoningTokens string `ts:"reasoningTokens,optional,doc=Copied to usage.completion_tokens_details.reasoning_tokens when that field is absent."`
}

// StreamSectionSpec is the declarative SSE transform.
type StreamSectionSpec struct {
	Unwrap *StreamUnwrapSpec `ts:"unwrap,optional"`
	Done   *StreamDoneSpec   `ts:"done,optional"`
	Usage  *StreamUsageSpec  `ts:"usage,optional"`
}

// PluginManifestSpec is the hand-written part of the plugin manifest in the SDK.
type PluginManifestSpec struct {
	APIVersion int                   `ts:"apiVersion"`
	HostAPI    int                   `ts:"hostApi"`
	ID         string                `ts:"id"`
	Name       string                `ts:"name"`
	Version    string                `ts:"version"`
	Kind       string                `ts:"kind,optional,type=PluginKind"`
	Exports    []string              `ts:"exports,optional"`
	Transport  *TransportProfileSpec `ts:"transport,optional"`
	Stream     *StreamSectionSpec    `ts:"stream,optional,doc=Declarative SSE transform. On a protocol plugin this is protocol.stream. On a site type it is siteProvider.protocol.stream."`
}

// ProbeContext is the credential-free object passed to a quota probe hook.
// Account is set only for an account-pool per-account listModels call.
type ProbeContext struct {
	SiteType       string            `ts:"siteType"`
	BaseURL        string            `ts:"baseURL"`
	CredentialType string            `ts:"credentialType"`
	Now            int64             `ts:"now,doc=Unix time in milliseconds."`
	Account        map[string]string `ts:"account,optional,doc=Non-secret account fields for per-account model sync. Includes the resolved region id when the credential does not store region. Omitted for API-key calls."`
	Settings       map[string]any    `ts:"settings,optional,type=Record<string, SettingValue>,doc=Resolved admin settings when manifest declares settings."`
}

// ProbeStep is one finished HTTP exchange, shown back to the plugin.
type ProbeStep struct {
	Request  ProbeRequest  `ts:"request"`
	Response ProbeResponse `ts:"response"`
}

// ProbeRequest is a plugin-authored HTTP request before the host joins the URL.
// OnNonJSON is read only while pollLogin is running. "pending" treats a body
// that is not one JSON object as still waiting. The body is discarded and is
// not shown to the plugin. Every other hook ignores the field and still
// refuses a non-JSON body when secret paths are set.
type ProbeRequest struct {
	Method    string            `ts:"method,type=ProbeMethod"`
	Path      string            `ts:"path,doc=Path under the site base URL. Absolute URLs are rejected."`
	Headers   map[string]string `ts:"headers,optional"`
	Query     map[string]string `ts:"query,optional"`
	Body      any               `ts:"body,optional,type=Json"`
	OnNonJSON string            `ts:"onNonJSON,optional,doc=pollLogin only. pending treats a non-JSON body as still waiting and discards it."`
}

// ProbeResponse is the HTTP result shown to the plugin. JSON is nil when the body is not JSON.
type ProbeResponse struct {
	Status  int               `ts:"status"`
	Headers map[string]string `ts:"headers"`
	Body    string            `ts:"body"`
	JSON    any               `ts:"json,doc=Parsed body, or null when the body is not JSON."`
	Error   string            `ts:"error,optional"`
}

// ProbeEntryKinds are the values of an entry's kind. Empty means unspecified.
var ProbeEntryKinds = []string{"balance", "window", "package"}

// ProbeEntry is one quota window. Field names match the hook contract.
type ProbeEntry struct {
	Label           string   `ts:"label"`
	Kind            string   `ts:"kind,optional,type=ProbeEntryKind"`
	Name            string   `ts:"name,optional"`
	Unit            string   `ts:"unit,optional"`
	Remaining       *float64 `ts:"remaining"`
	Limit           *float64 `ts:"limit"`
	Used            *float64 `ts:"used"`
	Unlimited       bool     `ts:"unlimited,optional"`
	ResetAt         string   `ts:"resetAt,optional"`
	ExpiresAt       string   `ts:"expiresAt,optional"`
	CashBalance     *float64 `ts:"cashBalance"`
	VoucherBalance  *float64 `ts:"voucherBalance"`
	GrantedBalance  *float64 `ts:"grantedBalance"`
	ToppedUpBalance *float64 `ts:"toppedUpBalance"`
}

// ProbeResult is a successful probe payload. Status and fetched_at stay in Go.
type ProbeResult struct {
	Kind        string       `ts:"kind"`
	Plan        string       `ts:"plan,optional"`
	ExpiresAt   string       `ts:"expiresAt,optional"`
	IsAvailable *bool        `ts:"isAvailable"`
	Entries     []ProbeEntry `ts:"entries"`
}

// Decision is one hook return: the next request, the final result, or a probe error.
type Decision struct {
	Request     *ProbeRequest
	Result      *ProbeResult
	Error       string
	Logs        []LogEntry
	DroppedLogs int
}

func (pc ProbeContext) asMap() map[string]any {
	out := map[string]any{
		"siteType":       pc.SiteType,
		"baseURL":        pc.BaseURL,
		"credentialType": pc.CredentialType,
		"now":            pc.Now,
	}
	if len(pc.Account) > 0 {
		out["account"] = pc.Account
	}
	if len(pc.Settings) > 0 {
		out["settings"] = pc.Settings
	}
	return out
}

func stepsAsValue(steps []ProbeStep) []any {
	out := make([]any, len(steps))
	for i, step := range steps {
		headers := step.Response.Headers
		if headers == nil {
			headers = map[string]string{}
		}
		response := map[string]any{
			"status":  step.Response.Status,
			"headers": headers,
			"body":    step.Response.Body,
			"json":    step.Response.JSON,
		}
		if step.Response.Error != "" {
			response["error"] = step.Response.Error
		}
		request := map[string]any{
			"method": step.Request.Method,
			"path":   step.Request.Path,
		}
		if len(step.Request.Headers) > 0 {
			request["headers"] = step.Request.Headers
		}
		if len(step.Request.Query) > 0 {
			request["query"] = step.Request.Query
		}
		if step.Request.Body != nil {
			request["body"] = step.Request.Body
		}
		if step.Request.OnNonJSON != "" {
			request["onNonJSON"] = step.Request.OnNonJSON
		}
		out[i] = map[string]any{"request": request, "response": response}
	}
	return out
}

func decodeDecision(value any) (Decision, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return Decision{}, shapeError("probe return value: expected object")
	}
	for key := range object {
		switch key {
		case "request", "result", "error":
		default:
			return Decision{}, shapeError("probe return value: unknown field %q", key)
		}
	}
	_, hasRequest := object["request"]
	_, hasResult := object["result"]
	_, hasError := object["error"]
	set := 0
	if hasRequest {
		set++
	}
	if hasResult {
		set++
	}
	if hasError {
		set++
	}
	if set != 1 {
		return Decision{}, shapeError("probe return value: expected exactly one of request, result, error")
	}
	if hasError {
		message, ok := object["error"].(string)
		if !ok || strings.TrimSpace(message) == "" {
			return Decision{}, shapeError("error: expected non-empty string")
		}
		return Decision{Error: message}, nil
	}
	if hasRequest {
		request, err := decodeRequest(object["request"])
		if err != nil {
			return Decision{}, err
		}
		return Decision{Request: &request}, nil
	}
	result, err := decodeResult(object["result"])
	if err != nil {
		return Decision{}, err
	}
	return Decision{Result: &result}, nil
}

func decodeRequest(value any) (ProbeRequest, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return ProbeRequest{}, shapeError("request: expected object")
	}
	for key := range object {
		switch key {
		case "method", "path", "headers", "query", "body", "onNonJSON":
		default:
			return ProbeRequest{}, shapeError("request: unknown field %q", key)
		}
	}
	method := "GET"
	if raw, ok := object["method"]; ok {
		text, ok := raw.(string)
		if !ok {
			return ProbeRequest{}, shapeError("request.method: expected string")
		}
		method = strings.ToUpper(strings.TrimSpace(text))
	}
	if _, ok := probeMethods[method]; !ok {
		return ProbeRequest{}, shapeError("request.method: unsupported method %q", method)
	}
	path, ok := object["path"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return ProbeRequest{}, shapeError("request.path: expected string")
	}
	headers, err := stringMap(object["headers"], "request.headers")
	if err != nil {
		return ProbeRequest{}, err
	}
	query, err := stringMap(object["query"], "request.query")
	if err != nil {
		return ProbeRequest{}, err
	}
	body := object["body"]
	if body != nil {
		switch body.(type) {
		case string, map[string]any, []any:
		default:
			return ProbeRequest{}, shapeError("request.body: expected object, array, or string")
		}
	}
	onNonJSON, err := optionalString(object["onNonJSON"], "request.onNonJSON")
	if err != nil {
		return ProbeRequest{}, err
	}
	if onNonJSON != "" && onNonJSON != "pending" {
		return ProbeRequest{}, shapeError("request.onNonJSON: unsupported value %q", onNonJSON)
	}
	return ProbeRequest{Method: method, Path: path, Headers: headers, Query: query, Body: body, OnNonJSON: onNonJSON}, nil
}

func decodeResult(value any) (ProbeResult, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return ProbeResult{}, shapeError("result: expected object")
	}
	for key := range object {
		switch key {
		case "kind", "plan", "expiresAt", "isAvailable", "entries":
		default:
			return ProbeResult{}, shapeError("result: unknown field %q", key)
		}
	}
	kind, ok := object["kind"].(string)
	if !ok || strings.TrimSpace(kind) == "" {
		return ProbeResult{}, shapeError("result.kind: expected string")
	}
	plan, err := optionalString(object["plan"], "result.plan")
	if err != nil {
		return ProbeResult{}, err
	}
	expiresAt, err := optionalString(object["expiresAt"], "result.expiresAt")
	if err != nil {
		return ProbeResult{}, err
	}
	var available *bool
	if raw, ok := object["isAvailable"]; ok && raw != nil {
		value, ok := raw.(bool)
		if !ok {
			return ProbeResult{}, shapeError("result.isAvailable: expected boolean")
		}
		available = &value
	}
	rawEntries, ok := object["entries"]
	if !ok {
		return ProbeResult{}, shapeError("result.entries: expected array")
	}
	items, ok := rawEntries.([]any)
	if !ok {
		return ProbeResult{}, shapeError("result.entries: expected array")
	}
	entries := make([]ProbeEntry, 0, len(items))
	for i, item := range items {
		entry, err := decodeEntry(item, i)
		if err != nil {
			return ProbeResult{}, err
		}
		entries = append(entries, entry)
	}
	return ProbeResult{Kind: kind, Plan: plan, ExpiresAt: expiresAt, IsAvailable: available, Entries: entries}, nil
}

func decodeEntry(value any, index int) (ProbeEntry, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return ProbeEntry{}, shapeError("result.entries[%d]: expected object", index)
	}
	path := fmt.Sprintf("result.entries[%d]", index)
	for key := range object {
		switch key {
		case "label", "kind", "name", "unit", "remaining", "limit", "used", "unlimited", "resetAt", "expiresAt",
			"cashBalance", "voucherBalance", "grantedBalance", "toppedUpBalance":
		default:
			return ProbeEntry{}, shapeError("%s: unknown field %q", path, key)
		}
	}
	label, ok := object["label"].(string)
	if !ok || strings.TrimSpace(label) == "" {
		return ProbeEntry{}, shapeError("%s.label: expected string", path)
	}
	kind, err := optionalString(object["kind"], path+".kind")
	if err != nil {
		return ProbeEntry{}, err
	}
	if kind != "" && !contains(ProbeEntryKinds, kind) {
		return ProbeEntry{}, shapeError("%s.kind: %q is not one of %s", path, kind, strings.Join(ProbeEntryKinds, ","))
	}
	name, err := optionalString(object["name"], path+".name")
	if err != nil {
		return ProbeEntry{}, err
	}
	unit, err := optionalString(object["unit"], path+".unit")
	if err != nil {
		return ProbeEntry{}, err
	}
	resetAt, err := optionalString(object["resetAt"], path+".resetAt")
	if err != nil {
		return ProbeEntry{}, err
	}
	expiresAt, err := optionalString(object["expiresAt"], path+".expiresAt")
	if err != nil {
		return ProbeEntry{}, err
	}
	unlimited := false
	if raw, ok := object["unlimited"]; ok && raw != nil {
		value, ok := raw.(bool)
		if !ok {
			return ProbeEntry{}, shapeError("%s.unlimited: expected boolean", path)
		}
		unlimited = value
	}
	remaining, err := optionalFloat(object["remaining"], path+".remaining")
	if err != nil {
		return ProbeEntry{}, err
	}
	limit, err := optionalFloat(object["limit"], path+".limit")
	if err != nil {
		return ProbeEntry{}, err
	}
	used, err := optionalFloat(object["used"], path+".used")
	if err != nil {
		return ProbeEntry{}, err
	}
	cash, err := optionalFloat(object["cashBalance"], path+".cashBalance")
	if err != nil {
		return ProbeEntry{}, err
	}
	voucher, err := optionalFloat(object["voucherBalance"], path+".voucherBalance")
	if err != nil {
		return ProbeEntry{}, err
	}
	granted, err := optionalFloat(object["grantedBalance"], path+".grantedBalance")
	if err != nil {
		return ProbeEntry{}, err
	}
	topped, err := optionalFloat(object["toppedUpBalance"], path+".toppedUpBalance")
	if err != nil {
		return ProbeEntry{}, err
	}
	return ProbeEntry{
		Label: label, Kind: kind, Name: name, Unit: unit, Remaining: remaining, Limit: limit, Used: used, Unlimited: unlimited,
		ResetAt: resetAt, ExpiresAt: expiresAt, CashBalance: cash, VoucherBalance: voucher, GrantedBalance: granted, ToppedUpBalance: topped,
	}, nil
}

// Account-pool hook result kinds. These are not plugin kinds: a site_provider
// package returns them from startLogin, pollLogin, refreshTokens, and
// summarizeAccount. Token values never appear in these payloads.
const (
	KindAccountLogin   = "account_login"
	KindAccountRefresh = "account_refresh"
	KindAccountSummary = "account_summary"
)

// AccountLoginStatuses are the account_login status values.
var AccountLoginStatuses = []string{"pending", "complete"}

// AccountRefreshStatuses are the account_refresh status values.
// reauth means the grant is dead. A transport failure is not this status.
var AccountRefreshStatuses = []string{"ok", "reauth"}

// AccountSummaryStatuses are the account_summary status values.
// reauth is a successful result that tells the host to mark the credential
// reauth_required. It is not a hook error.
var AccountSummaryStatuses = []string{"ok", "unavailable", "reauth"}

// AccountLoginContext is the credential-free object passed to startLogin and
// pollLogin. It carries login UI parameters only, never tokens.
type AccountLoginContext struct {
	SiteType string            `ts:"siteType"`
	BaseURL  string            `ts:"baseURL"`
	Params   map[string]string `ts:"params,doc=Login parameters from the admin UI. Not credentials."`
	Now      int64             `ts:"now,doc=Unix time in milliseconds."`
	Settings map[string]any    `ts:"settings,optional,type=Record<string, SettingValue>,doc=Resolved admin settings when manifest declares settings."`
}

// AccountLoginResult is the account_login hook result.
// Credential values for secret fields must be {{secret.<field>}} naming a
// SecretBag field. A raw token in this object is rejected.
type AccountLoginResult struct {
	Kind            string            `ts:"kind,doc=Must be account_login." enum:"account_login"`
	Status          string            `ts:"status,type=AccountLoginStatus" enum:"pending,complete"`
	VerificationURL string            `ts:"verificationURL,optional"`
	UserCode        string            `ts:"userCode,optional"`
	Interval        *int              `ts:"interval,optional"`
	Credential      map[string]string `ts:"credential,optional,doc=Secret fields are {{secret.<field>}} bag references. Non-secret fields are plain strings. Token values are rejected." max:"16"`
}

// AccountCredentialContext is the object passed to refreshTokens and
// summarizeAccount. Fields are non-secret credential fields only.
type AccountCredentialContext struct {
	SiteType string            `ts:"siteType"`
	BaseURL  string            `ts:"baseURL"`
	Fields   map[string]string `ts:"fields,doc=Non-secret credential fields only. Access tokens and other secrets are omitted."`
	Now      int64             `ts:"now,doc=Unix time in milliseconds."`
	Settings map[string]any    `ts:"settings,optional,type=Record<string, SettingValue>,doc=Resolved admin settings when manifest declares settings."`
}

// AccountRefreshResult is the account_refresh hook result.
// Credential uses the same {{secret.<field>}} rule as account_login.
type AccountRefreshResult struct {
	Kind       string            `ts:"kind,doc=Must be account_refresh." enum:"account_refresh"`
	Status     string            `ts:"status,type=AccountRefreshStatus" enum:"ok,reauth"`
	Credential map[string]string `ts:"credential,optional,doc=Secret fields are {{secret.<field>}} bag references. Non-secret fields are plain strings. Token values are rejected." max:"16"`
}

// AccountSummaryResult is the account_summary hook result.
// entries use the quota probe entry shape. remark and label must not contain
// secrets; the host scrubs them anyway. label is stored only when
// credential.display lists summary.label.
type AccountSummaryResult struct {
	Kind    string       `ts:"kind,doc=Must be account_summary." enum:"account_summary"`
	Status  string       `ts:"status,type=AccountSummaryStatus" enum:"ok,unavailable,reauth"`
	Remark  string       `ts:"remark,optional"`
	Label   string       `ts:"label,optional,doc=Display text. Stored only when credential.display lists summary.label."`
	Entries []ProbeEntry `ts:"entries"`
}

func optionalString(value any, path string) (string, error) {
	if value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", shapeError("%s: expected string", path)
	}
	return text, nil
}

func optionalFloat(value any, path string) (*float64, error) {
	if value == nil {
		return nil, nil
	}
	switch typed := value.(type) {
	case float64:
		return &typed, nil
	case int64:
		out := float64(typed)
		return &out, nil
	case int:
		out := float64(typed)
		return &out, nil
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return nil, shapeError("%s: expected number", path)
		}
		return &parsed, nil
	default:
		return nil, shapeError("%s: expected number", path)
	}
}

func stringMap(value any, path string) (map[string]string, error) {
	if value == nil {
		return nil, nil
	}
	switch typed := value.(type) {
	case map[string]string:
		return typed, nil
	case map[string]any:
		out := make(map[string]string, len(typed))
		for key, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, shapeError("%s.%s: expected string", path, key)
			}
			out[key] = text
		}
		return out, nil
	default:
		return nil, shapeError("%s: expected object", path)
	}
}

const maxModelMetaKeyBytes = 64
const maxModelMetaBytes = 4 << 10

// modelMetaMap checks one meta object: keys stay short, values are string,
// number, or bool, and the JSON form stays within 4 KiB.
func modelMetaMap(object map[string]any, path string) (map[string]any, error) {
	out := make(map[string]any, len(object))
	for key, item := range object {
		if key == "" || len(key) > maxModelMetaKeyBytes {
			return nil, shapeError("%s: key longer than %d bytes", path, maxModelMetaKeyBytes)
		}
		switch item.(type) {
		case string, bool, float64, int, int64, json.Number:
			out[key] = item
		default:
			return nil, shapeError("%s.%s: expected string, number, or boolean", path, key)
		}
	}
	encoded, err := json.Marshal(out)
	if err != nil || len(encoded) > maxModelMetaBytes {
		return nil, shapeError("%s: longer than %d bytes", path, maxModelMetaBytes)
	}
	return out, nil
}
