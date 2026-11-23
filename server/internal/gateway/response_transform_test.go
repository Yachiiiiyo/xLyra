package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"xlyra/server/internal/textxform"
)

func restoreTestContext(reasoning bool) context.Context {
	session := textxform.NewSession()
	session.Reversible = true
	session.RestoreReasoning = reasoning
	session.Allocate("PHONE", "13812345678")
	session.Allocate("NAME", `张"三"`+"\n")
	return textxform.WithSession(context.Background(), session)
}

// gatewaySSEText joins one string field of every data event that has it.
func gatewaySSEText(t *testing.T, stream string, extract func(map[string]any) (string, bool)) string {
	t.Helper()
	var b strings.Builder
	for _, block := range strings.Split(stream, "\n\n") {
		_, data, _, done, _ := parseSSEEventBlock([]byte(block + "\n\n"))
		if done || len(data) == 0 {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("bad event %q: %v", block, err)
		}
		if text, ok := extract(payload); ok {
			b.WriteString(text)
		}
	}
	return b.String()
}

func TestRestoreGeminiPassthroughStream(t *testing.T) {
	body := "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello [[PHO\"}]}}]}\r\n\r\n" +
		"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"musing [[PHONE_1]]\",\"thought\":true},{\"text\":\"NE_1]] [\"},{\"functionCall\":{\"name\":\"f\",\"args\":{\"who\":\"[[NAME_1]]\"}}}]},\"finishReason\":\"STOP\"}]}\r\n\r\n"
	recorder := httptest.NewRecorder()
	_, _, err := proxyUpstreamStreamWithInspector(restoreTestContext(false), recorder, gatewayStreamTestResponse(body), time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := recorder.Body.String()
	text := gatewaySSEText(t, strings.ReplaceAll(out, "\r\n", "\n"), func(p map[string]any) (string, bool) {
		var b strings.Builder
		for _, part := range p["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any) {
			partMap := part.(map[string]any)
			if thought, _ := partMap["thought"].(bool); thought {
				continue
			}
			if text, ok := partMap["text"].(string); ok {
				b.WriteString(text)
			}
		}
		return b.String(), true
	})
	if text != "hello 13812345678 [" {
		t.Fatalf("gemini text = %q\n%s", text, out)
	}
	if !strings.Contains(out, `"who":"张\"三\"\n"`) || !strings.Contains(out, `"text":"musing [[PHONE_1]]"`) {
		t.Fatalf("gemini args and thoughts:\n%s", out)
	}
	plain := httptest.NewRecorder()
	if _, _, err := proxyUpstreamStreamWithInspector(context.Background(), plain, gatewayStreamTestResponse(body), time.Now(), nil); err != nil || plain.Body.String() != body {
		t.Fatalf("without a session the passthrough is byte for byte: %v", err)
	}
}

func TestCanonicalTokenRestoreFilter(t *testing.T) {
	filter := newCanonicalTokenRestoreFilter(restoreTestContext(false))
	events := filter.Apply([]canonicalStreamEvent{
		{Type: canonicalStreamEventTextDelta, Delta: "a [[PHO"},
		{Type: canonicalStreamEventToolCallDelta, ToolCallID: "call_1", ToolCallIndex: 0, ToolCallName: "f", ToolCallArgumentsDelta: `{"q":"[[NA`},
		{Type: canonicalStreamEventTextDelta, Delta: "NE_1]] b ["},
		{Type: canonicalStreamEventReasoningDelta, Delta: "[[PHONE_1]]"},
		{Type: canonicalStreamEventToolCallDelta, ToolCallID: "call_1", ToolCallIndex: 0, ToolCallArgumentsDelta: `ME_1]]"`},
		{Type: canonicalStreamEventCompleted},
	})
	var text, args strings.Builder
	completedAt := -1
	for i, event := range events {
		switch event.Type {
		case canonicalStreamEventTextDelta:
			if completedAt >= 0 {
				t.Fatal("text after completed")
			}
			text.WriteString(event.Delta)
		case canonicalStreamEventToolCallDelta:
			if event.ToolCallID != "call_1" {
				t.Fatalf("tool event lost its id: %#v", event)
			}
			args.WriteString(event.ToolCallArgumentsDelta)
		case canonicalStreamEventReasoningDelta:
			if event.Delta != "[[PHONE_1]]" {
				t.Fatalf("reasoning restored by default: %q", event.Delta)
			}
		case canonicalStreamEventCompleted:
			completedAt = i
		}
	}
	if text.String() != "a 13812345678 b [" || args.String() != `{"q":"张\"三\"\n"` || completedAt != len(events)-1 {
		t.Fatalf("text = %q args = %q events = %#v", text.String(), args.String(), events)
	}
	if newCanonicalTokenRestoreFilter(context.Background()) != nil {
		t.Fatal("no session, no filter")
	}
}

func TestRestoreResponsesWebSocketStreamAndContinuation(t *testing.T) {
	conn := &capturedResponsesWebSocket{}
	wsState := responsesWebSocketState{maxBytes: 1 << 20}
	initialInput := []any{
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "call 13812345678"}}},
	}
	writer := newResponsesWebSocketResponseWriter(context.Background(), conn, &sync.Mutex{}, &wsState, initialInput, map[string]any{}, "req_ws_1")

	body := "" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"reach [[PHO\"}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"NE_1]]\"}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":1,\"delta\":\"{\\\"name\\\":\\\"[[NA\"}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":1,\"delta\":\"ME_1]]\\\"}\"}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"reach [[PHONE_1]]\"}]}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ws_1\",\"status\":\"completed\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"reach [[PHONE_1]]\"}]}]}}\n\n"

	if _, _, err := proxyResponsesStreamPassthrough(restoreTestContext(false), writer, gatewayStreamTestResponse(body), time.Now()); err != nil {
		t.Fatalf("proxyResponsesStreamPassthrough: %v", err)
	}
	if err := writer.finalize("req_ws_1"); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	var deltaText, deltaArgs strings.Builder
	sawCompleted := false
	for _, msg := range conn.messages {
		var payload map[string]any
		if err := json.Unmarshal(msg, &payload); err != nil {
			t.Fatalf("invalid ws json %q: %v", msg, err)
		}
		switch payload["type"] {
		case "response.output_text.delta":
			if s, ok := payload["delta"].(string); ok {
				deltaText.WriteString(s)
			}
		case "response.function_call_arguments.delta":
			if s, ok := payload["delta"].(string); ok {
				deltaArgs.WriteString(s)
			}
		case "response.completed":
			sawCompleted = true
			if !strings.Contains(string(msg), "reach 13812345678") {
				t.Fatalf("completed frame did not restore placeholder: %s", msg)
			}
		}
	}
	if !sawCompleted || deltaText.String() != "reach 13812345678" {
		t.Fatalf("ws restored text = %q completed=%v", deltaText.String(), sawCompleted)
	}
	var parsedArgs map[string]string
	if err := json.Unmarshal([]byte(deltaArgs.String()), &parsedArgs); err != nil || parsedArgs["name"] != `张"三"`+"\n" {
		t.Fatalf("ws tool args invalid JSON %q: %v %#v", deltaArgs.String(), err, parsedArgs)
	}

	// Next turn via previous_response_id expands the restored history and re-applies ingress pipeline.
	expanded, hadPrev, _, err := wsState.expand(map[string]any{
		"previous_response_id": "resp_ws_1",
		"input":                []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "confirm"}}}},
	})
	if err != nil || !hadPrev {
		t.Fatalf("expand turn 2: hadPrev=%v err=%v", hadPrev, err)
	}
	apiKeyID := uuid.New()
	plugin := pipelineGatewayPlugin(t, "", nil)
	installPipeline(t, apiKeyID, activePipelineBinding(t, plugin, map[string]any{"flag": true}))
	turn2Payload := responsesWebSocketExecutionPayload(map[string]any{"model": "gpt-4o"}, nil, expanded)
	turn2Req := gatewayRequest{DownstreamPath: gatewayEndpointResponses, RequestedModel: "gpt-4o", Stream: true, Payload: turn2Payload}
	h := Handler{}
	_, failure := h.applyGatewayIngressPipeline(context.Background(), apiKeyID, &turn2Req)
	if failure != nil {
		t.Fatalf("turn 2 ingress pipeline failed: %+v", failure)
	}
	encoded := pipelineJSON(t, turn2Req.Payload)
	if strings.Contains(encoded, "13812345678") || !strings.Contains(encoded, "[[PHONE_1]]") {
		t.Fatalf("turn 2 payload was not re-redacted: %s", encoded)
	}
}

