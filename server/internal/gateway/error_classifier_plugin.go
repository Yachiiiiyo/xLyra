package gateway

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"xlyra/server/internal/jsplugin"
	routeengine "xlyra/server/internal/router"
	sitepkg "xlyra/server/internal/site"
	"xlyra/server/internal/upstream"
)

const (
	errorClassifierMessageLimit = 1024
	// errorClassifierWait bounds how long a request waits for a plugin runtime;
	// the hook itself is limited to 50 ms by the runtime.
	errorClassifierWait = 250 * time.Millisecond
)

// applyErrorClassifierPlugin lets the error_classifier bound to the candidate,
// or the site type's classify hook, correct the default classification of an
// upstream error.
//
// The plugin sees only the status and the code, type and message the default
// classifier already read from the body. It can pick a class and nothing else:
// cooldown length, retry timing and routing are still computed here from the
// class. A verdict of "unknown", an unrecognized class, a missing plugin or
// any failure leaves the default classification untouched. A mounted plugin
// that exports classify wins over the site type hook, including on a built-in site.
func applyErrorClassifierPlugin(candidate routeengine.Candidate, statusCode int, failure upstream.Failure) upstream.Failure {
	plugin, pluginID, hook := classifyPlugin(candidate)
	if plugin == nil {
		return failure
	}
	ctx, cancel := context.WithTimeout(context.Background(), errorClassifierWait)
	defer cancel()
	classifyCtx := jsplugin.ErrorClassifyContext{SiteType: candidate.Site.SiteType}
	input := jsplugin.ErrorClassifyInput{
		Status:  statusCode,
		Code:    failure.Code,
		Type:    failure.Type,
		Message: clipMessage(failure.Message, errorClassifierMessageLimit),
	}
	callCtx := withCandidatePluginContext(ctx, candidate, plugin)
	verdict, err := plugin.CallErrorClassifyHook(callCtx, hook, classifyCtx, input)
	if breaker := jsplugin.DefaultCatalog().Breaker(); breaker != nil {
		breaker.RecordProtocolCall(plugin, hook, err != nil, isInterruption(err))
	}
	if err != nil {
		slog.Default().Warn("error_classifier plugin failed", "plugin_id", pluginID, "site_id", candidate.Site.ID, "error", err)
		return failure
	}
	switch upstream.FailureClass(verdict.Class) {
	case upstream.FailureLimited, upstream.FailureSubscriptionLimit, upstream.FailureTransient, upstream.FailureCredentialInvalid:
		failure.Class = upstream.FailureClass(verdict.Class)
		failure.Reason = strings.TrimSpace(verdict.Reason)
		return failure
	default:
		return failure
	}
}

// classifyPlugin resolves the classifier for one candidate. The first mounted
// plugin that exports classify and does not declare a site type wins, including
// on a built-in site. Otherwise a plugin site type's classify hook is used.
// Built-in site types (no "/") have no base hook. The third result is the hook
// name actually called.
func classifyPlugin(candidate routeengine.Candidate) (*jsplugin.Plugin, string, string) {
	if registry := jsplugin.DefaultCatalog().Registry(); registry != nil {
		for _, pluginID := range candidate.Site.PluginIDs {
			plugin, ok := registry.ByPluginID(pluginID)
			if !ok || plugin == nil || plugin.Manifest.DeclaresSiteType() {
				continue
			}
			if plugin.HasHook("classifyError") || plugin.HasHook("classify") {
				return plugin, pluginID, "classifyError"
			}
		}
	}
	if !strings.Contains(candidate.Site.SiteType, "/") {
		return nil, "", ""
	}
	plugin, ok := sitepkg.ProviderHookPlugin(jsplugin.DefaultCatalog().Registry(), candidate.Site.SiteType, "classify")
	if !ok {
		return nil, "", ""
	}
	return plugin, plugin.Manifest.ID, "classify"
}

func isInterruption(err error) bool {
	call, ok := err.(*jsplugin.CallError)
	return ok && (call.Kind == jsplugin.KindTimeout || call.Kind == jsplugin.KindHeapLimit)
}

// clipMessage cuts a message to at most limit bytes without splitting a character.
func clipMessage(message string, limit int) string {
	if len(message) <= limit {
		return message
	}
	cut := message[:limit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}
