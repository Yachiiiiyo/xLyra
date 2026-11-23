package gateway

import (
	"context"

	"github.com/google/uuid"

	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/textpipeline"
	"xlyra/server/internal/textxform"
)

const pipelineFailureStage = "gateway_pipeline"

type gatewayPipelineState struct {
	metrics textpipeline.Metrics
	// protectRaw is set when the plugin may read raw request text: nothing
	// about the inbound body may be kept or echoed.
	protectRaw bool
}

type gatewayPipelineStateKey struct{}

func withGatewayPipelineState(ctx context.Context, state *gatewayPipelineState) context.Context {
	return context.WithValue(ctx, gatewayPipelineStateKey{}, state)
}

func gatewayPipelineStateFrom(ctx context.Context) *gatewayPipelineState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(gatewayPipelineStateKey{}).(*gatewayPipelineState)
	return state
}

// gatewayPipelineProtectsRaw reports whether the request's inbound text must
// not be stored or echoed.
func gatewayPipelineProtectsRaw(ctx context.Context) bool {
	state := gatewayPipelineStateFrom(ctx)
	return state != nil && state.protectRaw
}

// applyGatewayIngressPipeline runs the gateway pipeline bound to the API key
// once per request, after decoding and before model mapping and routing. A
// key without a binding costs one map lookup. On success the request carries
// the transformed payload, and ctx the session that restores responses. A
// request made for another one, such as an image bridge call, reuses its
// session.
func (h Handler) applyGatewayIngressPipeline(ctx context.Context, apiKeyID uuid.UUID, request *gatewayRequest) (context.Context, *chatFailure) {
	binding, ok := jsplugin.DefaultCatalog().Current().PipelineByAPIKey(apiKeyID)
	if !ok {
		return ctx, nil
	}
	result := textpipeline.Run(ctx, binding, textpipeline.Request{
		Endpoint: request.DownstreamPath,
		Model:    request.RequestedModel,
		Payload:  request.Payload,
	}, textpipeline.Options{
		Session: textxform.SessionFromContext(ctx),
		Breaker: jsplugin.DefaultCatalog().Breaker(),
	})
	ctx = withGatewayPipelineState(ctx, &gatewayPipelineState{metrics: result.Metrics, protectRaw: binding.InspectRaw})
	if result.ErrorKind != "" && h.logger != nil {
		h.logger.WarnContext(ctx, "gateway pipeline failed", "scope", "gateway", "binding_id", result.Metrics.BindingID, "plugin_id", binding.PluginID, "error_kind", result.ErrorKind, "fail_closed", binding.FailClosed())
	}
	if result.Failure != nil {
		return ctx, &chatFailure{
			status: result.Failure.Status, code: result.Failure.Code, message: result.Failure.Message,
			requestedModel: request.RequestedModel, stream: request.Stream, stage: pipelineFailureStage,
		}
	}
	if result.Session != nil {
		ctx = textxform.WithSession(ctx, result.Session)
	}
	if result.Changed {
		request.Payload = result.Payload
		rebuildCanonicalRequest(request)
	}
	return ctx, nil
}

// rebuildCanonicalRequest derives the cross-protocol form again from the
// transformed payload, the way the endpoint decoded it.
func rebuildCanonicalRequest(request *gatewayRequest) {
	model := request.RequestedModel
	switch request.DownstreamPath {
	case gatewayEndpointChatCompletions:
		if canonical, err := canonicalRequestFromOpenAIChatPayload(request.Payload, model); err == nil {
			request.Canonical = &canonical
		} else {
			request.Canonical = nil
		}
	case gatewayEndpointResponses:
		if canonical, err := canonicalRequestFromOpenAIResponsesPayload(request.Payload, model); err == nil {
			request.Canonical = &canonical
		} else {
			request.Canonical = nil
		}
	case gatewayEndpointMessages:
		if canonical, err := canonicalRequestFromAnthropicMessagesPayload(request.Payload, model); err == nil {
			request.Canonical = &canonical
		}
	case gatewayEndpointGeminiGenerate:
		if canonical, err := canonicalRequestFromGoogleGeminiPayload(request.Payload, model, request.Stream); err == nil {
			request.Canonical = &canonical
		}
	case gatewayEndpointImagesGenerations:
		canonical := canonicalRequestFromOpenAIImagesPayload(request.Payload, model)
		request.Canonical = &canonical
	case gatewayEndpointImagesEdits:
		canonical := canonicalRequestFromOpenAIImagesPayloadForPath(request.Payload, model, gatewayEndpointImagesEdits)
		request.Canonical = &canonical
	}
}
