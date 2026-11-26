package textpipeline

import (
	"context"
	"testing"

	"xlyra/server/internal/jsplugin"
)

func TestGatewayPipelineEndToEndFixture(t *testing.T) {
	source := `export const meta = { apiVersion: 1, id: "acme.e2e" };
export function inspectSegments(ctx, input) {
  const drop = [];
  for (let i = 0; i < input.findings.length; i++) {
    const f = input.findings[i];
    const seg = input.segments.find((s) => s.id === f.segment);
    if (f.source === "dictionary" && seg && seg.text.slice(f.start, f.end) === "李四") {
      drop.push(i);
    }
  }
  return { drop };
}`
	manifest := jsplugin.Manifest{
		ID:          "acme.e2e",
		Name:        "E2E Pipeline",
		Version:     "1.0.0",
		APIVersion:  1,
		HostAPI:     2,
		Permissions: []string{jsplugin.PermissionPayloadInspectRaw},
		GatewayPipeline: &jsplugin.GatewayPipelineSection{
			Scope:               jsplugin.PipelineScopeAPIKey,
			Exclusive:           true,
			FailurePolicy:       jsplugin.PipelineFailClosed,
			UnsupportedEndpoint: jsplugin.PipelineUnsupportedReject,
			Transform:           jsplugin.PipelineTransformSpec{Mode: jsplugin.PipelineTransformTokenPlaceholder},
			Types: []jsplugin.PipelineTypeSpec{
				{Type: "PHONE"},
				{Type: "PERSON"},
			},
			Rules: []jsplugin.PipelineRuleSpec{
				{Type: "PHONE", Pattern: `1[3-9]\d{9}`, Boundary: "digit"},
			},
			Dictionaries: []jsplugin.PipelineDictSpec{
				{Param: "names", Type: "PERSON"},
			},
			Inputs: []jsplugin.AutomationInputSpec{
				{Name: "keys", Type: jsplugin.EntityAPIKey, Multiple: true},
				{Name: "names", Type: "string", Control: "textarea", Default: "张三\n李四"},
			},
		},
		SHA256: map[string]string{"plugin.js": jsplugin.HashSource(source)},
	}

	sseFixture := jsplugin.Fixture{
		Name:     "sse-split-restore",
		Hook:     jsplugin.HookGatewayPipelineFixture,
		Endpoint: "/v1/chat/completions",
		Payload: map[string]any{
			"model": "gpt-4o",
			"messages": []any{
				map[string]any{"role": "user", "content": "联系张三 13812345678，忽略李四"},
			},
		},
		Upstream: &jsplugin.FixturePipelineUpstream{
			SSEChunks: []string{
				"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"你好 [[PER\"}}]}\n\n",
				"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"SON_1]] 电话 [[PHO\"}}]}\n\n",
				"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"NE_1]]\"}}]}\n\n",
				"data: [DONE]\n\n",
			},
		},
		Expect: jsplugin.Fixture{}.Expect,
	}
	sseFixture.Expect.UpstreamPayload = map[string]any{
		"model": "gpt-4o",
		"messages": []any{
			map[string]any{"role": "user", "content": "联系[[PERSON_1]] [[PHONE_1]]，忽略李四"},
		},
	}
	sseFixture.Expect.DownstreamResponse = []any{
		map[string]any{"choices": []any{map[string]any{"index": float64(0), "delta": map[string]any{"content": "你好 "}}}},
		map[string]any{"choices": []any{map[string]any{"index": float64(0), "delta": map[string]any{"content": "张三 电话 "}}}},
		map[string]any{"choices": []any{map[string]any{"index": float64(0), "delta": map[string]any{"content": "13812345678"}}}},
	}

	unsupportedFixture := jsplugin.Fixture{
		Name:     "unsupported-endpoint",
		Hook:     jsplugin.HookGatewayPipelineFixture,
		Endpoint: "/v1/audio/speech",
		Payload:  map[string]any{"model": "tts-1", "input": "hello"},
	}
	unsupportedFixture.Expect.Status = 400
	unsupportedFixture.Expect.ErrorCode = CodeUnsupportedEndpoint

	plugin, err := jsplugin.NewPlugin(manifest, source, []jsplugin.Fixture{sseFixture, unsupportedFixture}, 1)
	if err != nil {
		t.Fatalf("NewPlugin: %v", err)
	}
	if err := plugin.SelfTest(context.Background()); err != nil {
		t.Fatalf("SelfTest failed: %v", err)
	}
}
