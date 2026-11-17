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

type candidateClassifier struct {
	plugin *jsplugin.Plugin
	id     string
	hook   string
}

// applyErrorClassifierPlugin lets the error_classifier plugins bound to the
// candidate, and then the site type's classify hook, correct the default
// classification of an upstream error.
//
// The plugin sees only the status and the code, type and message the default
// classifier already read from the body. It can pick a class and nothing else:
// cooldown length, retry timing and routing are still computed here from the
// class. Plugins run in priority order (mounted plugin_ids first, then the
// site type base hook); the first known class wins, while "unknown", an
// unrecognized class, or a hook failure continues to the next plugin and
// finally leaves the default classification untouched.
func applyErrorClassifierPlugin(candidate routeengine.Candidate, statusCode int, failure upstream.Failure) upstream.Failure {
	classifiers := classifyPlugins(candidate)
	if len(classifiers) == 0 {
		return failure
	}
	classifyCtx := jsplugin.ErrorClassifyContext{SiteType: candidate.Site.SiteType}
	input := jsplugin.ErrorClassifyInput{
		Status:  statusCode,
		Code:    failure.Code,
		Type:    failure.Type,
		Message: clipMessage(failure.Message, errorClassifierMessageLimit),
	}
	for _, item := range classifiers {
		ctx, cancel := context.WithTimeout(context.Background(), errorClassifierWait)
		callCtx := withCandidatePluginContext(ctx, candidate, item.plugin)
		verdict, err := item.plugin.CallErrorClassifyHook(callCtx, item.hook, classifyCtx, input)
		cancel()
		if breaker := jsplugin.DefaultCatalog().Breaker(); breaker != nil {
			breaker.RecordProtocolCall(item.plugin, item.hook, err != nil, isInterruption(err))
		}
		if err != nil {
			slog.Default().Warn("error_classifier plugin failed", "plugin_id", item.id, "site_id", candidate.Site.ID, "error", err)
			continue
		}
		switch upstream.FailureClass(verdict.Class) {
		case upstream.FailureLimited, upstream.FailureSubscriptionLimit, upstream.FailureTransient, upstream.FailureCredentialInvalid:
			failure.Class = upstream.FailureClass(verdict.Class)
			failure.Reason = strings.TrimSpace(verdict.Reason)
			return failure
		}
	}
	return failure
}

// classifyPlugins resolves the ordered classifiers for one candidate. Mounted
// plugins that export classifyError/classify and do not declare a site type
// run first in PluginIDs order, followed by a plugin site type's classify hook
// as the fallback. Built-in site types (no "/") have no base hook.
func classifyPlugins(candidate routeengine.Candidate) []candidateClassifier {
	registry := jsplugin.DefaultCatalog().Registry()
	if registry == nil {
		return nil
	}
	var out []candidateClassifier
	seen := map[string]bool{}
	for _, pluginID := range candidate.Site.PluginIDs {
		if seen[pluginID] {
			continue
		}
		seen[pluginID] = true
		plugin, ok := registry.ByPluginID(pluginID)
		if !ok || plugin == nil || plugin.Manifest.DeclaresSiteType() {
			continue
		}
		if plugin.HasHook("classifyError") || plugin.HasHook("classify") {
			out = append(out, candidateClassifier{plugin: plugin, id: pluginID, hook: "classifyError"})
		}
	}
	if strings.Contains(candidate.Site.SiteType, "/") {
		if plugin, ok := sitepkg.ProviderHookPlugin(registry, candidate.Site.SiteType, "classify"); ok && !seen[plugin.Manifest.ID] {
			out = append(out, candidateClassifier{plugin: plugin, id: plugin.Manifest.ID, hook: "classify"})
		}
	}
	return out
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
