package textpipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"xlyra/server/internal/textxform"
)

func restoreTestSession(reasoning bool) *textxform.Session {
	session := textxform.NewSession()
	session.Reversible = true
	session.RestoreReasoning = reasoning
	session.Allocate("PHONE", "13812345678")
	session.Allocate("NAME", `张"三"`+"\n")
	return session
}

func runSSERestore(t *testing.T, session *textxform.Session, blocks ...string) string {
	t.Helper()
	restorer := NewStreamRestorer(session)
	var out strings.Builder
	for _, block := range blocks {
		for _, piece := range restorer.Apply([]byte(block)) {
			out.Write(piece)
		}
	}
	for _, piece := range restorer.Flush() {
		out.Write(piece)
	}
	return out.String()
}

// sseText joins one string field of every data event that has it.
func sseText(t *testing.T, stream string, extract func(map[string]any) (string, bool)) string {
	t.Helper()
	var b strings.Builder
	for _, block := range strings.Split(stream, "\n\n") {
		_, data, _, done := parseSSEEvent([]byte(block + "\n\n"))
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

func TestPotentialTokenPrefix(t *testing.T) {
	cases := map[string]bool{
		"[": true, "[[": true, "[[P": true, "[[PHONE": true, "[[PHONE_": true, "[[PHONE_1": true, "[[PHONE_12]": true,
		"[[ID_CARD_": true, "[[ID_CARD_3": true, "[[A": true, "[[PHONE_0": true,
		"[0": false, "[0]": false, "[DONE]": false, "[[foo": false, "[[A]": false, "[[PHONE_0]": false, "[[PHONE_1]]": false,
		"[[" + strings.Repeat("A", 25): false, "[ [": false, "[[PHONE_1 ": false,
	}
	for tail, want := range cases {
		if got := IsPotentialTokenPrefix(tail); got != want {
			t.Fatalf("IsPotentialTokenPrefix(%q) = %v", tail, got)
		}
	}
}

func TestTokenRestoreStreamFilterHoldsOnlyPrefixes(t *testing.T) {
	session := restoreTestSession(false)
	filter := &TokenRestoreFilter{Session: session}
	var out strings.Builder
	for _, chunk := range []string{"call [[PHO", "NE_", "1]] now", " [0] and [DONE] [[foo]] [[PHONE_9]] [[NAME_1]]"} {
		emit, _ := filter.Feed(chunk)
		out.WriteString(emit)
	}
	tail, _ := filter.Flush()
	out.WriteString(tail)
	if got := out.String(); got != `call 13812345678 now [0] and [DONE] [[foo]] [[PHONE_9]] 张"三"`+"\n" {
		t.Fatalf("restored = %q", got)
	}
	immediate := &TokenRestoreFilter{Session: session}
	if emit, _ := immediate.Feed("list [0]"); emit != "list [0]" || immediate.HasPending() {
		t.Fatalf("[0] must not wait: %q", emit)
	}
	if emit, _ := immediate.Feed("x [["); emit != "x " || !immediate.HasPending() {
		t.Fatalf("[[ waits: %q", emit)
	}
	if emit, _ := immediate.Feed("lowercase"); emit != "[[lowercase" || immediate.HasPending() {
		t.Fatalf("a broken prefix is released at once: %q", emit)
	}
	escaped := &TokenRestoreFilter{Session: session, JSONEscape: true}
	first, _ := escaped.Feed(`{"name":"[[NA`)
	second, _ := escaped.Feed(`ME_1]]"}`)
	var decoded map[string]string
	if err := json.Unmarshal([]byte(first+second), &decoded); err != nil || decoded["name"] != `张"三"`+"\n" {
		t.Fatalf("escaped arguments %q: %v", first+second, err)
	}
	dangling := &TokenRestoreFilter{Session: session}
	dangling.Feed("end [[PH")
	if tail, _ := dangling.Flush(); tail != "[[PH" {
		t.Fatalf("flush releases the raw tail: %q", tail)
	}
}

func TestRestoreBufferedResponses(t *testing.T) {
	session := restoreTestSession(false)
	chat := `{"choices":[{"message":{"role":"assistant","content":"call [[PHONE_1]]","reasoning_content":"think [[PHONE_1]]","tool_calls":[{"function":{"name":"f","arguments":"{\"who\":\"[[NAME_1]]\"}"}}]}}],"usage":{"prompt_tokens":1}}`
	out := RestoreBody(session, []byte(chat), "application/json")
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	message := decoded["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if message["content"] != "call 13812345678" || message["reasoning_content"] != "think [[PHONE_1]]" {
		t.Fatalf("message = %#v", message)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(message["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"].(string)), &args); err != nil || args["who"] != `张"三"`+"\n" {
		t.Fatalf("arguments are not valid JSON: %v %#v", err, args)
	}
	reasoningOn := RestoreBody(restoreTestSession(true), []byte(chat), "application/json")
	if !strings.Contains(string(reasoningOn), "think 13812345678") {
		t.Fatalf("reasoning restore = %s", reasoningOn)
	}
	anthropic := `{"content":[{"type":"thinking","thinking":"[[PHONE_1]]","signature":"sig"},{"type":"text","text":"[[PHONE_1]]"},{"type":"tool_use","input":{"q":"[[NAME_1]]"}}]}`
	out = RestoreBody(session, []byte(anthropic), "application/json")
	if !strings.Contains(string(out), `"thinking":"[[PHONE_1]]"`) || !strings.Contains(string(out), `"text":"13812345678"`) || !strings.Contains(string(out), `"q":"张\"三\"\n"`) {
		t.Fatalf("anthropic = %s", out)
	}
	if got := RestoreBody(session, []byte("upstream said [[PHONE_1]]"), "text/plain"); string(got) != "upstream said 13812345678" {
		t.Fatalf("text body = %s", got)
	}
	audio := []byte("RIFF[[PHONE_1]]")
	if got := RestoreBody(session, audio, "audio/mpeg"); string(got) != string(audio) {
		t.Fatal("binary bodies are untouched")
	}
	if got := RestoreBody(nil, []byte(chat), "application/json"); string(got) != chat {
		t.Fatal("no session, no change")
	}
	irreversible := restoreTestSession(false)
	irreversible.Reversible = false
	if got := RestoreBody(textxform.RestoringSessionFromContext(textxform.WithSession(context.Background(), irreversible)), []byte(chat), "application/json"); string(got) != chat {
		t.Fatal("an irreversible transform is never restored")
	}
}

func TestRestoreChatSSE(t *testing.T) {
	stream := runSSERestore(t, restoreTestSession(false),
		`data: {"id":"c","choices":[{"index":0,"delta":{"content":"call [[PHO"}}]}`+"\n\n",
		`data: {"id":"c","choices":[{"index":0,"delta":{"content":"NE_1]] x [["}}]}`+"\n\n",
		`data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":\"[[NAME"}}]}}]}`+"\n\n",
		`data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"_1]]\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n",
		`data: {"id":"c","choices":[{"index":0,"delta":{"reasoning_content":"[[PHONE_1]]"}}]}`+"\n\n",
		"data: [DONE]\n\n",
	)
	content := sseText(t, stream, func(p map[string]any) (string, bool) {
		delta, _ := p["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
		text, ok := delta["content"].(string)
		return text, ok
	})
	if content != "call 13812345678 x [[" {
		t.Fatalf("content = %q\n%s", content, stream)
	}
	args := sseText(t, stream, func(p map[string]any) (string, bool) {
		delta, _ := p["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
		calls, _ := delta["tool_calls"].([]any)
		if len(calls) == 0 {
			return "", false
		}
		return calls[0].(map[string]any)["function"].(map[string]any)["arguments"].(string), true
	})
	var decoded map[string]string
	if err := json.Unmarshal([]byte(args), &decoded); err != nil || decoded["a"] != `张"三"`+"\n" {
		t.Fatalf("arguments %q: %v", args, err)
	}
	if !strings.Contains(stream, `"reasoning_content":"[[PHONE_1]]"`) {
		t.Fatal("reasoning stays as is by default")
	}
	if strings.Index(stream, "[DONE]") < strings.LastIndex(stream, `"content":"[["`) {
		t.Fatalf("held text must come before [DONE]:\n%s", stream)
	}
}

func TestRestoreAnthropicSSE(t *testing.T) {
	stream := runSSERestore(t, restoreTestSession(false),
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi [[PH\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"[[PHONE_1]]\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ONE_1]] [\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"n\\\":\\\"[[NAME_1]]\\\"}\"}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	)
	text := sseText(t, stream, func(p map[string]any) (string, bool) {
		delta, _ := p["delta"].(map[string]any)
		if stringField(delta["type"]) != "text_delta" {
			return "", false
		}
		return delta["text"].(string), true
	})
	if text != "hi 13812345678 [" {
		t.Fatalf("text = %q\n%s", text, stream)
	}
	if strings.Index(stream, `"text":"["`) > strings.Index(stream, "content_block_stop") {
		t.Fatalf("held text must come before its content_block_stop:\n%s", stream)
	}
	if !strings.Contains(stream, `"thinking":"[[PHONE_1]]"`) {
		t.Fatal("thinking stays as is by default")
	}
	partial := sseText(t, stream, func(p map[string]any) (string, bool) {
		delta, _ := p["delta"].(map[string]any)
		text, ok := delta["partial_json"].(string)
		return text, ok
	})
	var decoded map[string]string
	if err := json.Unmarshal([]byte(partial), &decoded); err != nil || decoded["n"] != `张"三"`+"\n" {
		t.Fatalf("partial json %q: %v", partial, err)
	}
	withThinking := runSSERestore(t, restoreTestSession(true),
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"[[PHONE_1]]\"}}\n\n")
	if !strings.Contains(withThinking, `"thinking":"13812345678"`) {
		t.Fatalf("thinking restore when enabled: %s", withThinking)
	}
}

func TestRestoreResponsesSSE(t *testing.T) {
	stream := runSSERestore(t, restoreTestSession(false),
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"m\",\"output_index\":0,\"content_index\":0,\"delta\":\"a [[PHONE\"}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"m\",\"output_index\":0,\"content_index\":0,\"delta\":\"_1]] b [[\"}\n\n",
		"event: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"item_id\":\"m\",\"output_index\":0,\"content_index\":0,\"text\":\"a [[PHONE_1]] b [[\"}\n\n",
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":1,\"delta\":\"{\\\"x\\\":\\\"[[NAME_1]]\\\"}\"}\n\n",
		"event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"output_index\":2,\"summary_index\":0,\"delta\":\"[[PHONE_1]]\"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"[[PHONE_1]]\"}]},{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"a [[PHONE_1]]\"}]},{\"type\":\"function_call\",\"arguments\":\"{\\\"x\\\":\\\"[[NAME_1]]\\\"}\"}]}}\n\n",
	)
	deltas := sseText(t, stream, func(p map[string]any) (string, bool) {
		if p["type"] != "response.output_text.delta" {
			return "", false
		}
		return p["delta"].(string), true
	})
	if deltas != "a 13812345678 b [[" {
		t.Fatalf("deltas = %q\n%s", deltas, stream)
	}
	if strings.Index(stream, `"delta":"[["`) > strings.Index(stream, "response.output_text.done") {
		t.Fatalf("held text must come before output_text.done:\n%s", stream)
	}
	if !strings.Contains(stream, `"text":"a 13812345678 b [["`) || !strings.Contains(stream, `"delta":"[[PHONE_1]]"`) {
		t.Fatalf("done text restored, reasoning kept:\n%s", stream)
	}
	var completed map[string]any
	for _, block := range strings.Split(stream, "\n\n") {
		if strings.Contains(block, "response.completed") {
			_, data, _, _ := parseSSEEvent([]byte(block + "\n\n"))
			_ = json.Unmarshal(data, &completed)
		}
	}
	output := completed["response"].(map[string]any)["output"].([]any)
	if output[0].(map[string]any)["summary"].([]any)[0].(map[string]any)["text"] != "[[PHONE_1]]" ||
		output[1].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] != "a 13812345678" {
		t.Fatalf("completed output = %#v", output)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(output[2].(map[string]any)["arguments"].(string)), &args); err != nil || args["x"] != `张"三"`+"\n" {
		t.Fatalf("completed arguments: %v", err)
	}
}

func TestRetokenizeBridgeInput(t *testing.T) {
	session := restoreTestSession(false)
	items := []any{
		map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "call 13812345678 or " + `张"三"` + "\n"}}},
		map[string]any{"type": "function_call_output", "output": "revised: 13812345678", "encrypted_content": "13812345678"},
	}
	out := Retokenize(items, session).([]any)
	if text := out[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]; text != "call [[PHONE_1]] or [[NAME_1]]" {
		t.Fatalf("text = %q", text)
	}
	if output := out[1].(map[string]any); output["output"] != "revised: [[PHONE_1]]" || output["encrypted_content"] != "13812345678" {
		t.Fatalf("output = %#v", output)
	}
}

