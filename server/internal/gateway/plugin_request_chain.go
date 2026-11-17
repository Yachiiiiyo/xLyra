package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"xlyra/server/internal/jsplugin"
	routeengine "xlyra/server/internal/router"
	sitepkg "xlyra/server/internal/site"
)

// pluginTransportProfile picks the TLS fingerprint for one candidate.
// An explicit impersonate switch wins, then the first mounted plugin that
// declares transport.profile and does not declare a site type, then the site
// type plugin. Empty means the default handshake.
func pluginTransportProfile(candidate routeengine.Candidate, cfg *sitepkg.GatewayConfig) string {
	if cfg != nil && cfg.ImpersonateOpencodeClient != nil && *cfg.ImpersonateOpencodeClient {
		return "bun"
	}
	if cfg != nil && cfg.ImpersonateClaudeCodeClient != nil && *cfg.ImpersonateClaudeCodeClient {
		return "claude_code"
	}
	registry := jsplugin.DefaultCatalog().Registry()
	if registry == nil {
		return ""
	}
	for _, id := range candidate.Site.PluginIDs {
		plugin, ok := registry.ByPluginID(id)
		if !ok || plugin == nil || plugin.Manifest.DeclaresSiteType() {
			continue
		}
		if profile := plugin.Manifest.TransportProfile(); profile != "" {
			return profile
		}
	}
	if item, ok := sitepkg.LookupPluginSiteType(candidate.Site.SiteType); ok && item.Enabled && item.PluginID != "" {
		if plugin, ok := registry.ByPluginID(item.PluginID); ok && plugin != nil && plugin.Manifest.DeclaresSiteType() {
			if profile := plugin.Manifest.TransportProfile(); profile != "" {
				return profile
			}
		}
	}
	return ""
}

// requestChainPlugins is the site type plugin first, then mounted plugins that
// do not declare a site type, in PluginIDs order and without duplicates.
func requestChainPlugins(candidate routeengine.Candidate) []*jsplugin.Plugin {
	registry := jsplugin.DefaultCatalog().Registry()
	if registry == nil {
		return nil
	}
	var out []*jsplugin.Plugin
	seen := map[string]struct{}{}
	add := func(plugin *jsplugin.Plugin) {
		if plugin == nil {
			return
		}
		id := plugin.Manifest.ID
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, plugin)
	}
	if item, ok := sitepkg.LookupPluginSiteType(candidate.Site.SiteType); ok && item.Enabled && item.PluginID != "" {
		if plugin, ok := registry.ByPluginID(item.PluginID); ok && plugin != nil && plugin.Manifest.DeclaresSiteType() {
			add(plugin)
		}
	}
	for _, id := range candidate.Site.PluginIDs {
		plugin, ok := registry.ByPluginID(id)
		if !ok || plugin == nil || plugin.Manifest.DeclaresSiteType() {
			continue
		}
		add(plugin)
	}
	return out
}

// applyPluginSignChain runs signRequest on each plugin in the request chain
// that exports it. A rewrite changes the request the next plugin sees. A sign
// result is allowed only when the plugin declares secret.use. The returned
// body is the body later signing and logs must use.
func applyPluginSignChain(ctx context.Context, req *http.Request, candidate routeengine.Candidate, request gatewayRequest, protocol gatewayProtocolAdapter, body []byte, upstreamKey string, headerVars map[string]string, meta map[string]any, sessionSecret []byte) ([]byte, error) {
	if req == nil {
		return body, nil
	}
	for _, plugin := range requestChainPlugins(candidate) {
		if !plugin.HasHook(jsplugin.HookSignRequest) {
			continue
		}
		next, err := applyOnePluginSign(ctx, plugin, req, candidate, request, protocol, body, upstreamKey, headerVars, meta, sessionSecret)
		recordSignCall(plugin, err)
		if err != nil {
			return body, fmt.Errorf("plugin %s: %s", plugin.Manifest.ID, err.Error())
		}
		body = next
	}
	return body, nil
}

func applyOnePluginSign(ctx context.Context, plugin *jsplugin.Plugin, req *http.Request, candidate routeengine.Candidate, request gatewayRequest, protocol gatewayProtocolAdapter, body []byte, upstreamKey string, headerVars map[string]string, meta map[string]any, sessionSecret []byte) ([]byte, error) {
	if len(body) > jsplugin.MaxSignBodyBytes {
		return body, fmt.Errorf("signRequest: request body exceeds %d bytes", jsplugin.MaxSignBodyBytes)
	}
	flat := make(map[string]string, len(req.Header))
	for name, values := range req.Header {
		flat[name] = strings.Join(values, ", ")
	}
	visible, err := jsplugin.FilterRequestHeaders(flat)
	if err != nil {
		return body, fmt.Errorf("signRequest: %w", err)
	}
	callCtx := withCandidatePluginContext(ctx, candidate, plugin)
	usesSecret := containsPermission(plugin.Manifest.Permissions, "secret.use")
	if usesSecret {
		callCtx = jsplugin.WithDiscardLogs(callCtx)
	}
	waitCtx, cancel := context.WithTimeout(callCtx, 100*time.Millisecond)
	defer cancel()
	signed, logs, dropped, err := plugin.CallSignRequest(waitCtx, signBuildContext(plugin, candidate, request, protocol, upstreamKey, headerVars, meta, sessionSecret, usesSecret), jsplugin.ProtocolSignInput{
		Method:  req.Method,
		URL:     req.URL.String(),
		Headers: visible,
		Body:    string(body),
	})
	jsplugin.EmitLogs(callCtx, plugin, jsplugin.HookSignRequest, 0, logs, dropped)
	if err != nil {
		return body, fmt.Errorf("signRequest: %w", err)
	}
	switch signed.Mode {
	case "rewrite":
		return applySignRewrite(req, signed, body)
	case "sign":
		if !usesSecret {
			return body, fmt.Errorf("signRequest sign mode needs secret.use")
		}
		signature, err := signString(signed, upstreamKey)
		if err != nil {
			return body, fmt.Errorf("signRequest: %w", err)
		}
		for name, value := range signed.Headers {
			req.Header.Set(name, value)
		}
		req.Header.Set(signed.Header, signed.Prefix+signature)
		return body, nil
	default:
		return body, fmt.Errorf("signRequest: unknown mode %q", signed.Mode)
	}
}

func applySignRewrite(req *http.Request, signed jsplugin.ProtocolSignResult, body []byte) ([]byte, error) {
	for name, value := range signed.Headers {
		req.Header.Set(name, value)
	}
	if len(signed.Query) > 0 {
		query := req.URL.Query()
		for key, value := range signed.Query {
			query.Set(key, value)
		}
		req.URL.RawQuery = query.Encode()
	}
	if signed.Body != nil {
		next := []byte(*signed.Body)
		if err := sameSignBodyFields(body, next); err != nil {
			return body, err
		}
		req.Body = io.NopCloser(bytes.NewReader(next))
		req.ContentLength = int64(len(next))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(next)), nil
		}
		return next, nil
	}
	return body, nil
}

func sameSignBodyFields(before, after []byte) error {
	var previous map[string]any
	var next map[string]any
	if err := json.Unmarshal(before, &previous); err != nil || previous == nil {
		return nil
	}
	if err := json.Unmarshal(after, &next); err != nil || next == nil {
		return nil
	}
	model, hasModel := payloadField(previous, "model")
	stream, hasStream := payloadField(previous, "stream")
	if err := immutableRewriteFields(next, hasModel, model, hasStream, stream); err != nil {
		return fmt.Errorf("signRequest %s", strings.TrimPrefix(err.Error(), "rewritePayload "))
	}
	return nil
}

func signBuildContext(plugin *jsplugin.Plugin, candidate routeengine.Candidate, request gatewayRequest, protocol gatewayProtocolAdapter, upstreamKey string, headerVars map[string]string, meta map[string]any, sessionSecret []byte, usesSecret bool) jsplugin.ProtocolBuildContext {
	build := jsplugin.ProtocolBuildContext{
		Candidate: jsplugin.ProtocolCandidateContext{
			SiteType:      candidate.Site.SiteType,
			BaseURL:       candidate.Site.BaseURL,
			UpstreamName:  candidate.Model.UpstreamName,
			UpstreamModel: candidate.Model.UpstreamName,
		},
		Model:      candidate.Model.UpstreamName,
		Stream:     request.Stream,
		Protocol:   rewriteProtocolName(protocol),
		Now:        time.Now().UnixMilli(),
		SessionKey: gatewaySessionKey(request, candidate.Site.ID, plugin.Manifest.ID, sessionSecret),
		ModelMeta:  meta,
	}
	if usesSecret {
		build.Credential = signCredential(upstreamKey, headerVars)
	}
	return build
}

func signCredential(upstreamKey string, headerVars map[string]string) map[string]string {
	if headerVars == nil {
		return map[string]string{"api_key": upstreamKey}
	}
	out := make(map[string]string, len(headerVars))
	for key, value := range headerVars {
		name, ok := strings.CutPrefix(key, "cred.")
		if !ok || name == "" {
			continue
		}
		out[name] = value
	}
	return out
}

func recordSignCall(plugin *jsplugin.Plugin, err error) {
	breaker := jsplugin.DefaultCatalog().Breaker()
	if breaker == nil || plugin == nil {
		return
	}
	breaker.RecordProtocolCall(plugin, jsplugin.HookSignRequest, err != nil, isInterruption(err))
}

func containsPermission(permissions []string, want string) bool {
	for _, item := range permissions {
		if item == want {
			return true
		}
	}
	return false
}
