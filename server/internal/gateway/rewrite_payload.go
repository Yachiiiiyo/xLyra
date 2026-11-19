package gateway

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"

	"xlyra/server/internal/jsplugin"
	routeengine "xlyra/server/internal/router"
	sitepkg "xlyra/server/internal/site"
)

// applyProviderRewrite runs rewritePayload on the upstream body. The site
// type's own hook runs first, then each mounted plugin that exports the hook,
// in plugin_ids order. A built-in site type has no base hook, so only its
// mounted plugins run. The hooks run before billing, parameter checks, stream
// detection, and client impersonation, so they never see an account UUID
// written into the body. Plugin authors must not log user messages; the host
// does not record the payload.
func applyProviderRewrite(ctx context.Context, candidate routeengine.Candidate, protocol gatewayProtocolAdapter, payload map[string]any) (map[string]any, error) {
	plugins := rewritePayloadPlugins(candidate)
	if len(plugins) == 0 {
		return payload, nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	for _, plugin := range plugins {
		modelValue, hasModel := payloadField(payload, "model")
		streamValue, hasStream := payloadField(payload, "stream")
		rewritten, err := plugin.CallRewritePayload(withCandidatePluginContext(waitCtx, candidate, plugin), "rewritePayload", jsplugin.RewritePayloadContext{
			SiteType: candidate.Site.SiteType,
			Model:    rewriteModelName(candidate, payload),
			Stream:   rewriteStreamFlag(payload),
			Protocol: rewriteProtocolName(protocol),
		}, payload)
		if err == nil {
			err = immutableRewriteFields(rewritten, hasModel, modelValue, hasStream, streamValue)
		}
		if breaker := jsplugin.DefaultCatalog().Breaker(); breaker != nil {
			breaker.RecordProtocolCall(plugin, "rewritePayload", err != nil, isInterruption(err))
		}
		if err != nil {
			return nil, fmt.Errorf("plugin %s: %s", plugin.Manifest.ID, err.Error())
		}
		payload = rewritten
	}
	return payload, nil
}

// rewritePayloadPlugins is the site type's hook first, then the mounted
// plugins that export rewritePayload and do not declare a site type. A plugin
// id is used once, and a mounted copy of the base is not run again.
func rewritePayloadPlugins(candidate routeengine.Candidate) []*jsplugin.Plugin {
	registry := jsplugin.DefaultCatalog().Registry()
	if registry == nil {
		return nil
	}
	var out []*jsplugin.Plugin
	seen := map[string]bool{}
	if plugin, ok := sitepkg.ProviderHookPlugin(registry, candidate.Site.SiteType, "rewritePayload"); ok {
		seen[plugin.Manifest.ID] = true
		out = append(out, plugin)
	}
	for _, id := range candidate.Site.PluginIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		plugin, ok := registry.ByPluginID(id)
		if !ok || plugin == nil || plugin.Manifest.DeclaresSiteType() || !plugin.HasHook("rewritePayload") {
			continue
		}
		out = append(out, plugin)
	}
	return out
}

// withCandidatePluginContext attaches the site's setting overrides and, when
// the plugin declared storage, a site-scoped KV backend.
func withCandidatePluginContext(ctx context.Context, candidate routeengine.Candidate, plugin *jsplugin.Plugin) context.Context {
	if plugin == nil {
		return ctx
	}
	ctx = jsplugin.WithSiteSettings(ctx, candidate.Site.PluginSettings[plugin.Manifest.ID])
	if plugin.AllowsStorage() && candidate.Site.ID != uuid.Nil {
		ctx = jsplugin.WithStorageScope(ctx, jsplugin.StorageScopeSite, candidate.Site.ID.String())
	}
	return ctx
}

func rewriteModelName(candidate routeengine.Candidate, payload map[string]any) string {
	if payload != nil {
		if model, ok := payload["model"].(string); ok {
			return model
		}
	}
	return candidate.Model.UpstreamName
}

func rewriteStreamFlag(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	stream, _ := payload["stream"].(bool)
	return stream
}

func payloadField(payload map[string]any, key string) (any, bool) {
	if payload == nil {
		return nil, false
	}
	value, ok := payload[key]
	return value, ok
}

// immutableRewriteFields rejects a rewrite that adds, removes, or changes model
// or stream. Billing, routing, and the stream decision depend on those keys.
func immutableRewriteFields(payload map[string]any, hasModel bool, model any, hasStream bool, stream any) error {
	if err := sameRewriteField(payload, "model", hasModel, model); err != nil {
		return err
	}
	return sameRewriteField(payload, "stream", hasStream, stream)
}

func sameRewriteField(payload map[string]any, key string, present bool, want any) error {
	got, ok := payloadField(payload, key)
	if ok != present || (present && !reflect.DeepEqual(got, want)) {
		return fmt.Errorf("rewritePayload changed %q", key)
	}
	return nil
}

// rewriteProtocolName is the canonical protocol of the upstream body.
func rewriteProtocolName(protocol gatewayProtocolAdapter) string {
	switch protocol.(type) {
	case openAIChatProtocolAdapter, mimoAudioSpeechProtocolAdapter:
		return string(canonicalProtocolOpenAIChat)
	case openAIResponsesProtocolAdapter:
		return string(canonicalProtocolOpenAIResponses)
	case openAIImagesProtocolAdapter, grokImagesProtocolAdapter:
		return string(canonicalProtocolOpenAIImages)
	case openAIEmbeddingsProtocolAdapter:
		return string(canonicalProtocolOpenAIEmbeddings)
	case openAIAudioSpeechProtocolAdapter:
		return string(canonicalProtocolOpenAIAudioSpeech)
	case anthropicMessagesProtocolAdapter, *providerAnthropicMessagesProtocolAdapter:
		return string(canonicalProtocolAnthropicMessages)
	case *googleProtocolAdapter:
		return string(canonicalProtocolGoogleGemini)
	case codexProtocolAdapter:
		return string(canonicalProtocolCodexResponses)
	case antigravityProtocolAdapter:
		return string(canonicalProtocolAntigravity)
	case *grokResponsesProtocolAdapter:
		return string(canonicalProtocolGrok)
	default:
		if protocol == nil {
			return ""
		}
		return protocol.ProtocolName()
	}
}
