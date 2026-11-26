package textpipeline

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func decodeTestPayload(t *testing.T, raw string) map[string]any {
	t.Helper()
	value, err := decodeJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return value.(map[string]any)
}

func segmentSummary(doc *SegmentDocument) []string {
	out := make([]string, 0, len(doc.Segments))
	for _, seg := range doc.Segments {
		out = append(out, seg.Role+"|"+seg.Field+"|"+seg.Text)
	}
	return out
}

// tagAll replaces every segment with <TEXT>, to see where each one is written back.
func tagAll(doc *SegmentDocument, hint string) {
	texts := make([]string, len(doc.Segments))
	for i, seg := range doc.Segments {
		texts[i] = "<" + seg.Text + ">"
	}
	ApplySegmentReplacements(doc, texts, hint)
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := MarshalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSegmentCodecChatCompletions(t *testing.T) {
	payload := decodeTestPayload(t, `{
		"model": "gpt", "stream": true, "max_tokens": 10,
		"response_format": {"type": "json_schema", "json_schema": {"description": "keep"}},
		"messages": [
			{"role": "developer", "content": "be nice"},
			{"role": "user", "content": [{"type": "text", "text": "hi 13812345678"}, {"type": "image_url", "image_url": {"url": "data:x"}}]},
			{"role": "assistant", "content": null, "tool_calls": [
				{"id": "c1", "type": "function", "function": {"name": "f", "arguments": "{\"q\":\"a \\\"quoted\\\"\\nline\",\"n\":1.50,\"list\":[\"x\",2]}"}},
				{"id": "c2", "type": "function", "function": {"name": "g", "arguments": "not json"}}
			]},
			{"role": "tool", "tool_call_id": "c1", "content": "result text"}
		],
		"tools": [{"type": "function", "function": {"name": "f", "description": "search the web", "parameters": {"type": "object", "properties": {"q": {"type": "string", "description": "query"}}}}}]
	}`)
	doc, err := ExtractSegments(EndpointChatCompletions, payload, SegmentCodecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"system|message.text|be nice",
		"user|message.text|hi 13812345678",
		"assistant|tool.arguments|x",
		"assistant|tool.arguments|a \"quoted\"\nline",
		"assistant|tool.arguments|not json",
		"tool|tool.result|result text",
	}
	if got := segmentSummary(doc); !reflect.DeepEqual(got, want) {
		t.Fatalf("segments:\n%q\nwant\n%q", got, want)
	}
	if !doc.Segments[2].IsJSONString || doc.Segments[4].IsJSONString {
		t.Fatal("only leaves of parsed JSON strings are marked")
	}
	tagAll(doc, "HINT")
	messages := payload["messages"].([]any)
	if messages[0].(map[string]any)["content"] != "<be nice>\n\nHINT" {
		t.Fatalf("hint = %#v", messages[0])
	}
	args := messages[2].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"].(string)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(args), &decoded); err != nil {
		t.Fatalf("arguments are not JSON any more: %q", args)
	}
	if decoded["q"] != "<a \"quoted\"\nline>" || decoded["list"].([]any)[0] != "<x>" || !strings.Contains(args, `"n":1.50`) {
		t.Fatalf("arguments = %s", args)
	}
	if got := messages[2].(map[string]any)["tool_calls"].([]any)[1].(map[string]any)["function"].(map[string]any)["arguments"]; got != "<not json>" {
		t.Fatalf("plain arguments = %#v", got)
	}
	if payload["model"] != "gpt" || payload["stream"] != true || mustJSON(t, payload["response_format"]) != `{"json_schema":{"description":"keep"},"type":"json_schema"}` {
		t.Fatal("control fields must not change")
	}
	if url := messages[1].(map[string]any)["content"].([]any)[1].(map[string]any)["image_url"].(map[string]any)["url"]; url != "data:x" {
		t.Fatal("images are not text")
	}

	withTools := decodeTestPayload(t, `{"messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"f","description":"search","parameters":{"type":"object","properties":{"q":{"type":"string","description":"query","enum":["a"]}}}}}]}`)
	doc, err = ExtractSegments(EndpointChatCompletions, withTools, SegmentCodecOptions{ScanToolDefinitions: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := segmentSummary(doc); !reflect.DeepEqual(got, []string{"user|message.text|x", "system|tool.definition|search", "system|tool.definition|query", "system|tool.definition|a"}) {
		t.Fatalf("tool definitions = %q", got)
	}
	tagAll(doc, "HINT")
	if first := withTools["messages"].([]any)[0].(map[string]any); first["role"] != "system" || first["content"] != "HINT" {
		t.Fatalf("hint without a system message = %#v", first)
	}
	if typ := withTools["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)["type"]; typ != "object" {
		t.Fatal("schema keywords must not change")
	}
}

func TestSegmentCodecResponses(t *testing.T) {
	payload := decodeTestPayload(t, `{
		"model": "gpt", "instructions": "sys",
		"input": [
			{"role": "user", "content": [{"type": "input_text", "text": "hello"}, {"type": "input_image", "image_url": "data:y"}]},
			{"type": "reasoning", "summary": [{"type": "summary_text", "text": "think"}], "encrypted_content": "enc"},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "answer"}]},
			{"type": "function_call", "call_id": "c", "name": "f", "arguments": "{\"path\":\"/tmp/a\"}"},
			{"type": "function_call_output", "call_id": "c", "output": "file body"},
			{"type": "input_file", "file_data": "zzz"}
		]
	}`)
	doc, err := ExtractSegments(EndpointResponses, payload, SegmentCodecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"system|instructions|sys", "user|message.text|hello", "assistant|message.text|answer", "assistant|tool.arguments|/tmp/a", "tool|tool.result|file body"}
	if got := segmentSummary(doc); !reflect.DeepEqual(got, want) {
		t.Fatalf("segments = %q", got)
	}
	tagAll(doc, "HINT")
	if payload["instructions"] != "<sys>\n\nHINT" {
		t.Fatalf("instructions = %#v", payload["instructions"])
	}
	reasoning := payload["input"].([]any)[1].(map[string]any)
	if reasoning["encrypted_content"] != "enc" || reasoning["summary"].([]any)[0].(map[string]any)["text"] != "think" {
		t.Fatal("reasoning items must not change")
	}
	if args := payload["input"].([]any)[3].(map[string]any)["arguments"]; args != `{"path":"</tmp/a>"}` {
		t.Fatalf("arguments = %#v", args)
	}

	simple := decodeTestPayload(t, `{"model":"gpt","input":"just text"}`)
	doc, _ = ExtractSegments(EndpointResponses, simple, SegmentCodecOptions{})
	tagAll(doc, "HINT")
	if simple["input"] != "<just text>" || simple["instructions"] != "HINT" {
		t.Fatalf("string input = %#v", simple)
	}
}

func TestSegmentCodecAnthropicKeepsThinking(t *testing.T) {
	payload := decodeTestPayload(t, `{
		"model": "claude", "max_tokens": 100,
		"system": [{"type": "text", "text": "sys", "cache_control": {"type": "ephemeral"}}],
		"messages": [
			{"role": "user", "content": "plain"},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "secret thoughts 13812345678", "signature": "sig"},
				{"type": "redacted_thinking", "data": "abc"},
				{"type": "text", "text": "visible"},
				{"type": "tool_use", "id": "t", "name": "f", "input": {"q": "find", "opts": {"deep": ["x"]}, "n": 3}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "t", "content": [{"type": "text", "text": "found"}, {"type": "image", "source": {"data": "img"}}]},
				{"type": "document", "source": {"data": "doc"}}
			]}
		]
	}`)
	doc, err := ExtractSegments(EndpointMessages, payload, SegmentCodecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"system|instructions|sys", "user|message.text|plain", "assistant|message.text|visible", "assistant|tool.arguments|x", "assistant|tool.arguments|find", "tool|tool.result|found"}
	if got := segmentSummary(doc); !reflect.DeepEqual(got, want) {
		t.Fatalf("segments = %q", got)
	}
	before := mustJSON(t, payload["messages"].([]any)[1].(map[string]any)["content"].([]any)[:2])
	tagAll(doc, "HINT")
	if after := mustJSON(t, payload["messages"].([]any)[1].(map[string]any)["content"].([]any)[:2]); after != before {
		t.Fatalf("thinking changed: %s", after)
	}
	system := payload["system"].([]any)
	if len(system) != 2 || system[1].(map[string]any)["text"] != "HINT" || system[0].(map[string]any)["text"] != "<sys>" {
		t.Fatalf("system = %s", mustJSON(t, system))
	}
	input := payload["messages"].([]any)[1].(map[string]any)["content"].([]any)[3].(map[string]any)["input"].(map[string]any)
	if input["q"] != "<find>" || input["n"].(json.Number).String() != "3" {
		t.Fatalf("tool input = %#v", input)
	}

	plain := decodeTestPayload(t, `{"model":"claude","messages":[{"role":"user","content":"x"}]}`)
	doc, _ = ExtractSegments(EndpointMessages, plain, SegmentCodecOptions{})
	tagAll(doc, "HINT")
	if plain["system"] != "HINT" {
		t.Fatalf("system without one = %#v", plain["system"])
	}
}

func TestSegmentCodecGemini(t *testing.T) {
	payload := decodeTestPayload(t, `{
		"systemInstruction": {"parts": [{"text": "sys"}]},
		"contents": [
			{"role": "user", "parts": [{"text": "hello"}, {"inlineData": {"mimeType": "image/png", "data": "AAA"}}]},
			{"role": "model", "parts": [{"text": "musing", "thought": true}, {"functionCall": {"name": "f", "args": {"city": "上海"}}, "thoughtSignature": "sig"}]},
			{"role": "user", "parts": [{"functionResponse": {"name": "f", "response": {"result": "sunny"}}}]}
		]
	}`)
	doc, err := ExtractSegments(EndpointGeminiGenerate, payload, SegmentCodecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"system|instructions|sys", "user|message.text|hello", "assistant|tool.arguments|上海", "tool|tool.result|sunny"}
	if got := segmentSummary(doc); !reflect.DeepEqual(got, want) {
		t.Fatalf("segments = %q", got)
	}
	tagAll(doc, "HINT")
	parts := payload["systemInstruction"].(map[string]any)["parts"].([]any)
	if len(parts) != 2 || parts[1].(map[string]any)["text"] != "HINT" {
		t.Fatalf("system parts = %s", mustJSON(t, parts))
	}
	model := payload["contents"].([]any)[1].(map[string]any)["parts"].([]any)
	if model[0].(map[string]any)["text"] != "musing" || model[1].(map[string]any)["thoughtSignature"] != "sig" {
		t.Fatal("thoughts and signatures must not change")
	}
	if data := payload["contents"].([]any)[0].(map[string]any)["parts"].([]any)[1].(map[string]any)["inlineData"].(map[string]any)["data"]; data != "AAA" {
		t.Fatal("inline data is not text")
	}
	bare := decodeTestPayload(t, `{"contents":[{"parts":[{"text":"x"}]}]}`)
	doc, _ = ExtractSegments(EndpointGeminiGenerate, bare, SegmentCodecOptions{})
	tagAll(doc, "HINT")
	if mustJSON(t, bare["systemInstruction"]) != `{"parts":[{"text":"HINT"}]}` {
		t.Fatalf("new system instruction = %s", mustJSON(t, bare["systemInstruction"]))
	}
}

func TestSegmentCodecEmbeddingsImagesAndLimits(t *testing.T) {
	embeddings := decodeTestPayload(t, `{"model":"e","input":["a",[1,2],"b"]}`)
	doc, err := ExtractSegments(EndpointEmbeddings, embeddings, SegmentCodecOptions{})
	if err != nil || !reflect.DeepEqual(segmentSummary(doc), []string{"user|embedding.input|a", "user|embedding.input|b"}) {
		t.Fatalf("embeddings = %q %v", segmentSummary(doc), err)
	}
	tagAll(doc, "HINT")
	if mustJSON(t, embeddings) != `{"input":["<a>",[1,2],"<b>"],"model":"e"}` {
		t.Fatalf("embeddings after = %s", mustJSON(t, embeddings))
	}
	for _, path := range []string{EndpointImagesGenerations, EndpointImagesEdits} {
		images := map[string]any{"model": "img", "prompt": "a cat", "image": "binary"}
		doc, err := ExtractSegments(path, images, SegmentCodecOptions{})
		if err != nil || len(doc.Segments) != 1 || doc.Segments[0].Field != SegmentFieldPrompt {
			t.Fatalf("%s = %#v %v", path, doc, err)
		}
		tagAll(doc, "HINT")
		if images["prompt"] != "<a cat>" || images["image"] != "binary" || len(images) != 3 {
			t.Fatalf("%s after = %#v", path, images)
		}
	}
	for _, path := range []string{"/v1/audio/speech", "/v1/plugins/acme", "/v1/systemone"} {
		if _, err := ExtractSegments(path, map[string]any{"input": "x"}, SegmentCodecOptions{}); !errors.Is(err, ErrUnsupportedEndpoint) {
			t.Fatalf("%s: %v", path, err)
		}
	}
	large := map[string]any{"model": "m", "messages": []any{map[string]any{"role": "user", "content": strings.Repeat("x", 600)}, map[string]any{"role": "user", "content": strings.Repeat("y", 600)}}}
	if _, err := ExtractSegments(EndpointChatCompletions, large, SegmentCodecOptions{MaxInspectBytes: 1000}); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("limit: %v", err)
	}
	doc, err = ExtractSegments(EndpointChatCompletions, large, SegmentCodecOptions{MaxInspectBytes: 1200})
	if err != nil || doc.TotalBytes() != 1200 {
		t.Fatalf("at the limit: %v %d", err, doc.TotalBytes())
	}
	ApplySegmentReplacements(doc, nil, "")
	if large["messages"].([]any)[0].(map[string]any)["content"] != strings.Repeat("x", 600) {
		t.Fatal("no replacements leave the payload alone")
	}
}
