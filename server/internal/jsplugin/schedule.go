package jsplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"xlyra/server/internal/httpclient"
)

const (
	HookOnSchedule          = "onSchedule"
	PermissionSchedule      = "schedule"
	PermissionHTTP          = "http"
	defaultScheduleMinutes  = 60
	scheduleHTTPRequestWait = 20 * time.Second
)

// ScheduleSection is the optional top-level schedule block in a manifest.
type ScheduleSection struct {
	EveryMinutes int           `json:"everyMinutes,omitempty"`
	Label        LocalizedText `json:"label,omitempty"`
}

// Empty reports whether no schedule field is set.
func (s ScheduleSection) Empty() bool {
	return s.EveryMinutes == 0 && len(s.Label) == 0
}

// ScheduleContext is the object passed to onSchedule.
type ScheduleContext struct {
	SiteType string            `ts:"siteType,optional"`
	BaseURL  string            `ts:"baseURL,optional"`
	Fields   map[string]string `ts:"fields,optional,doc=Non-secret account fields when running for an account pool."`
	Account  map[string]string `ts:"account,optional"`
	Now      int64             `ts:"now,doc=Unix time in milliseconds."`
	Settings map[string]any    `ts:"settings,optional,type=Record<string, SettingValue>,doc=Resolved admin settings when manifest declares settings."`
}

func (sc ScheduleContext) asMap() map[string]any {
	out := map[string]any{
		"now": sc.Now,
	}
	if sc.SiteType != "" {
		out["siteType"] = sc.SiteType
	}
	if sc.BaseURL != "" {
		out["baseURL"] = sc.BaseURL
	}
	if len(sc.Fields) > 0 {
		out["fields"] = sc.Fields
	}
	if len(sc.Account) > 0 {
		out["account"] = sc.Account
	}
	if len(sc.Settings) > 0 {
		out["settings"] = sc.Settings
	}
	return out
}

// ScheduleDecision is one step return of onSchedule: the next HTTP request,
// a final result object, or an error string.
type ScheduleDecision struct {
	Request     *ProbeRequest
	Result      map[string]any
	Error       string
	Logs        []LogEntry
	DroppedLogs int
}

// EffectiveScheduleMinutes returns the declared schedule interval in minutes,
// or the default 60 minutes when the plugin has the schedule permission.
func (m Manifest) EffectiveScheduleMinutes() int {
	if m.Schedule.EveryMinutes >= MinScheduleMinutes {
		return m.Schedule.EveryMinutes
	}
	if contains(m.Permissions, PermissionSchedule) {
		return defaultScheduleMinutes
	}
	return 0
}

// AllowsSchedule reports whether the manifest declares the schedule permission.
func (m Manifest) AllowsSchedule() bool {
	return contains(m.Permissions, PermissionSchedule)
}

// AllowsHTTP reports whether the manifest declares the http permission.
func (m Manifest) AllowsHTTP() bool {
	return contains(m.Permissions, PermissionHTTP)
}

// AllowedNetHosts returns the host patterns from net:<host> permissions and,
// for a site_provider, siteProvider.hosts.
func (m Manifest) AllowedNetHosts() []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(host string) {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" {
			return
		}
		if _, ok := seen[host]; ok {
			return
		}
		seen[host] = struct{}{}
		out = append(out, host)
	}
	for _, perm := range m.Permissions {
		if host, ok := strings.CutPrefix(perm, "net:"); ok {
			add(host)
		}
	}
	if m.DeclaresSiteType() {
		for _, host := range m.SiteProvider.Hosts {
			add(host)
		}
	}
	return out
}

func validateManifestSchedule(manifest Manifest) error {
	if manifest.Schedule.Empty() {
		return nil
	}
	if !manifest.AllowsSchedule() {
		return fmt.Errorf("schedule requires %q in permissions", PermissionSchedule)
	}
	if manifest.Schedule.EveryMinutes < MinScheduleMinutes || manifest.Schedule.EveryMinutes > MaxScheduleMinutes {
		return fmt.Errorf("schedule.everyMinutes must be between %d and %d", MinScheduleMinutes, MaxScheduleMinutes)
	}
	if err := manifest.Schedule.Label.validate(); err != nil {
		return fmt.Errorf("schedule.label: %w", err)
	}
	return nil
}

func validateProgramScheduleHook(manifest Manifest, p *program) error {
	if p == nil {
		return nil
	}
	if _, has := p.hooks[HookOnSchedule]; !has {
		return nil
	}
	if !manifest.AllowsSchedule() {
		return fmt.Errorf("export %s requires %q in permissions", HookOnSchedule, PermissionSchedule)
	}
	return nil
}

// CallSchedule invokes onSchedule once with the 1s scheduleHookTimeout budget.
func (p *Plugin) CallSchedule(ctx context.Context, ctxMap map[string]any, steps []ProbeStep) (ScheduleDecision, error) {
	targetPool := p.poolForHook(HookOnSchedule)
	if p == nil || targetPool == nil {
		return ScheduleDecision{}, fmt.Errorf("plugin is not loaded")
	}
	started := time.Now()
	session, err := targetPool.Acquire(ctx)
	if err != nil {
		return ScheduleDecision{}, annotate(p, err, HookOnSchedule, len(steps), time.Since(started).Milliseconds())
	}
	hook, ok := p.program.hooks[HookOnSchedule]
	if !ok {
		targetPool.Release(session, true)
		return ScheduleDecision{}, fmt.Errorf("%s hook is missing", HookOnSchedule)
	}
	ctxArg := p.injectContextSettings(ctx, ctxMap)
	out, logs, dropped, discard, callErr := session.callWithEnv(ctx, scheduleHookTimeout, hook, p.buildCallEnv(ctx), []any{ctxArg, stepsAsValue(steps)})
	targetPool.Release(session, discard || fatalRuntime(callErr))
	duration := time.Since(started).Milliseconds()
	if callErr != nil {
		return ScheduleDecision{Logs: logs, DroppedLogs: dropped}, annotate(p, callErr, HookOnSchedule, len(steps), duration)
	}
	decision, err := decodeScheduleDecision(out)
	if err != nil {
		return ScheduleDecision{Logs: logs, DroppedLogs: dropped}, annotate(p, err, HookOnSchedule, len(steps), duration)
	}
	decision.Logs = logs
	decision.DroppedLogs = dropped
	return decision, nil
}

func decodeScheduleDecision(value any) (ScheduleDecision, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return ScheduleDecision{}, shapeError("onSchedule return value: expected object")
	}
	if rawErr, hasErr := object["error"]; hasErr && rawErr != nil {
		if len(object) > 1 {
			return ScheduleDecision{}, shapeError("onSchedule return value: error cannot be combined with other fields")
		}
		msg, ok := rawErr.(string)
		if !ok || strings.TrimSpace(msg) == "" {
			return ScheduleDecision{}, shapeError("error: expected non-empty string")
		}
		return ScheduleDecision{Error: msg}, nil
	}
	if rawReq, hasReq := object["request"]; hasReq && rawReq != nil {
		if len(object) > 1 {
			return ScheduleDecision{}, shapeError("onSchedule return value: request cannot be combined with other fields")
		}
		req, err := decodeRequest(rawReq)
		if err != nil {
			return ScheduleDecision{}, err
		}
		return ScheduleDecision{Request: &req}, nil
	}
	if inner, ok := object["result"].(map[string]any); ok {
		return ScheduleDecision{Result: inner}, nil
	}
	return ScheduleDecision{Result: object}, nil
}

// DriveSchedule runs a stepped onSchedule call for a plugin using the generic
// http + net:<host> outbound capability. When the plugin does not request any
// HTTP step, http permission is not required.
func (p *Plugin) DriveSchedule(ctx context.Context, client *http.Client, scheduleCtx ScheduleContext) (result map[string]any, err error) {
	defer func() {
		if breaker := DefaultCatalog().Breaker(); breaker != nil && p != nil {
			breaker.RecordProbeResult(p, HookOnSchedule, err == nil)
		}
	}()
	if p == nil {
		return nil, errors.New("plugin is required")
	}
	if !p.HasHook(HookOnSchedule) {
		return nil, fmt.Errorf("%s hook is missing", HookOnSchedule)
	}
	if !p.Manifest.AllowsSchedule() {
		return nil, permissionDeniedError("schedule permission is required")
	}
	if scheduleCtx.Now == 0 {
		scheduleCtx.Now = time.Now().UnixMilli()
	}
	if strings.TrimSpace(scheduleCtx.BaseURL) == "" {
		_, defaultBase := p.Manifest.BaseURLSettings()
		scheduleCtx.BaseURL = strings.TrimRight(strings.TrimSpace(defaultBase), "/")
	}
	ctxMap := scheduleCtx.asMap()
	allowedHosts := p.Manifest.AllowedNetHosts()
	var steps []ProbeStep
	for step := 0; step < MaxProbeSteps; step++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		decision, callErr := p.CallSchedule(ctx, ctxMap, steps)
		logs, dropped := decision.Logs, decision.DroppedLogs
		if ce, ok := callErr.(*CallError); ok && len(logs) == 0 {
			logs, dropped = ce.Logs, ce.DroppedLogs
		}
		EmitLogs(ctx, p, HookOnSchedule, step, logs, dropped)
		if callErr != nil {
			return nil, callErr
		}
		if decision.Error != "" {
			return nil, fmt.Errorf("plugin %s: %s", p.Manifest.ID, decision.Error)
		}
		if decision.Request == nil {
			return decision.Result, nil
		}
		if step == MaxProbeSteps-1 {
			return nil, fmt.Errorf("plugin %s exceeded %d steps", p.Manifest.ID, MaxProbeSteps)
		}
		if !p.Manifest.AllowsHTTP() {
			return nil, permissionDeniedError("http permission is required")
		}
		resp, reqErr := doScheduleHTTP(ctx, client, scheduleCtx.BaseURL, allowedHosts, *decision.Request)
		if reqErr != nil {
			return nil, reqErr
		}
		steps = append(steps, ProbeStep{Request: *decision.Request, Response: resp})
	}
	return nil, fmt.Errorf("plugin %s exceeded %d steps", p.Manifest.ID, MaxProbeSteps)
}

// ResolveScheduleURL resolves a plugin request path against base, or parses an
// explicit http(s) URL when path starts with https:// or http://.
func ResolveScheduleURL(base, path string, query map[string]string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if strings.HasPrefix(trimmed, "https://") || strings.HasPrefix(trimmed, "http://") {
		parsed, err := url.Parse(trimmed)
		if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
			return "", fmt.Errorf("invalid request URL")
		}
		if parsed.User != nil {
			return "", fmt.Errorf("url userinfo is not allowed")
		}
		if parsed.Fragment != "" {
			return "", fmt.Errorf("url fragment is not allowed")
		}
		values := parsed.Query()
		for key, value := range query {
			values.Set(key, value)
		}
		parsed.RawQuery = values.Encode()
		return parsed.String(), nil
	}
	return ResolveProbeURL(base, path, query)
}

// CheckPluginNetHost verifies that rawURL's hostname is permitted by allowedHosts
// (matched with httpclient.MatchHostPattern) and uses https (or http for a
// loopback host explicitly listed in allowedHosts).
func CheckPluginNetHost(allowedHosts []string, rawURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Hostname() == "" || parsed.Scheme == "" || parsed.User != nil {
		return &CallError{Kind: KindURLRejected, Detail: "url rejected"}
	}
	host := strings.ToLower(parsed.Hostname())
	matched := false
	for _, allowed := range allowedHosts {
		if httpclient.MatchHostPattern(allowed, host) {
			matched = true
			break
		}
	}
	if !matched {
		return &CallError{Kind: KindURLRejected, Detail: fmt.Sprintf("host %q is not allowed", host)}
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return nil
	case "http":
		if host == "127.0.0.1" || host == "::1" || host == "localhost" {
			return nil
		}
	}
	return &CallError{Kind: KindURLRejected, Detail: fmt.Sprintf("host %q must use https", host)}
}

func doScheduleHTTP(ctx context.Context, client *http.Client, baseURL string, allowedHosts []string, request ProbeRequest) (ProbeResponse, error) {
	headers, err := FilterRequestHeaders(request.Headers)
	if err != nil {
		return ProbeResponse{}, &CallError{Kind: KindShape, Detail: err.Error()}
	}
	target, err := ResolveScheduleURL(baseURL, request.Path, request.Query)
	if err != nil {
		return ProbeResponse{}, &CallError{Kind: KindURLRejected, Detail: err.Error()}
	}
	if err := CheckPluginNetHost(allowedHosts, target); err != nil {
		return ProbeResponse{}, err
	}
	var body io.Reader
	if request.Method != http.MethodGet && request.Method != http.MethodHead && request.Body != nil {
		var encoded []byte
		switch typed := request.Body.(type) {
		case string:
			encoded = []byte(typed)
		default:
			encoded, err = json.Marshal(typed)
			if err != nil {
				return ProbeResponse{}, err
			}
		}
		if len(encoded) > MaxProbeRequestBody {
			return ProbeResponse{}, &CallError{Kind: KindShape, Detail: "request body is too large"}
		}
		body = bytes.NewReader(encoded)
		if _, ok := request.Body.(string); !ok {
			if _, exists := headers["Content-Type"]; !exists {
				headers["Content-Type"] = "application/json"
			}
		}
	}
	reqCtx, cancel := context.WithTimeout(ctx, scheduleHTTPRequestWait)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(reqCtx, request.Method, target, body)
	if err != nil {
		return ProbeResponse{}, err
	}
	for key, value := range headers {
		httpReq.Header.Set(key, value)
	}
	if httpReq.Header.Get("Accept") == "" {
		httpReq.Header.Set("Accept", "application/json")
	}
	httpClient := http.Client{Timeout: scheduleHTTPRequestWait, Transport: httpclient.NewGuardedTransport(30 * time.Second)}
	if client != nil {
		httpClient = *client
		if httpClient.Timeout <= 0 {
			httpClient.Timeout = scheduleHTTPRequestWait
		}
		if httpClient.Transport == nil {
			httpClient.Transport = httpclient.NewGuardedTransport(30 * time.Second)
		}
	}
	httpClient.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return CheckPluginNetHost(allowedHosts, next.URL.String())
	}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		var callErr *CallError
		if errors.As(err, &callErr) {
			return ProbeResponse{}, callErr
		}
		if ctx.Err() != nil {
			return ProbeResponse{}, ctx.Err()
		}
		return ProbeResponse{Status: 0, Error: err.Error()}, nil
	}
	defer func() { _ = resp.Body.Close() }()
	buf, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeResponseBody+1))
	if err != nil {
		return ProbeResponse{}, err
	}
	if len(buf) > maxProbeResponseBody {
		return ProbeResponse{}, &CallError{Kind: KindResponseTooLarge, Detail: "response is too large"}
	}
	respHeaders := FilterResponseHeaders(resp.Header)
	var payload any
	if json.Unmarshal(buf, &payload) != nil {
		payload = nil
		if request.OnNonJSON == "ignore" || request.OnNonJSON == "pending" {
			return ProbeResponse{Status: resp.StatusCode, Headers: respHeaders}, nil
		}
	}
	return ProbeResponse{
		Status:  resp.StatusCode,
		Headers: respHeaders,
		Body:    string(buf),
		JSON:    payload,
	}, nil
}
