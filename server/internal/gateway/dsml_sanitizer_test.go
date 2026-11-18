package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStripDSMLTags(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		want     string
		stripped bool
	}{
		{
			name:     "fullwidth_closing_tag",
			input:    "Done.</｜DSML｜invoke></｜DSML｜function_calls>",
			want:     "Done.",
			stripped: true,
		},
		{
			name:     "halfwidth_spaced_closing_tag",
			input:    "Result: </ | DSML | parameter>ok",
			want:     "Result: ok",
			stripped: true,
		},
		{
			name:     "v41_spaced_opening_tag",
			input:    `<｜DSML｜ invoke name="read_file">text`,
			want:     "text",
			stripped: true,
		},
		{
			name:     "normal_html_and_math_untouched",
			input:    "if a < b && x > y { return <div>ok</div> }",
			want:     "if a < b && x > y { return <div>ok</div> }",
			stripped: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, stripped := stripDSMLTags(tc.input)
			if got != tc.want || stripped != tc.stripped {
				t.Fatalf("stripDSMLTags(%q) = (%q, %v), want (%q, %v)", tc.input, got, stripped, tc.want, tc.stripped)
			}
		})
	}
}

func TestDSMLStreamTextFilterAcrossChunks(t *testing.T) {
	var f dsmlStreamTextFilter

	out1, s1 := f.Feed("Hello </｜DSML")
	if out1 != "Hello " || s1 || !f.HasPending() {
		t.Fatalf("chunk1 = (%q, %v, pending=%v), want (%q, false, true)", out1, s1, f.HasPending(), "Hello ")
	}
	out2, s2 := f.Feed("｜invoke")
	if out2 != "" || s2 || !f.HasPending() {
		t.Fatalf("chunk2 = (%q, %v, pending=%v), want (%q, false, true)", out2, s2, f.HasPending(), "")
	}
	out3, s3 := f.Feed("> world")
	if out3 != " world" || !s3 || f.HasPending() {
		t.Fatalf("chunk3 = (%q, %v, pending=%v), want (%q, true, false)", out3, s3, f.HasPending(), " world")
	}

	// Normal '<' must not be buffered.
	out4, s4 := f.Feed(" a < b")
	if out4 != " a < b" || s4 || f.HasPending() {
		t.Fatalf("normal '<' = (%q, %v, pending=%v), want (%q, false, false)", out4, s4, f.HasPending(), " a < b")
	}

	// Trailing benign '<' flushes on EOF.
	out5, _ := f.Feed(" end <")
	if out5 != " end " || !f.HasPending() {
		t.Fatalf("trailing '<' = (%q, pending=%v), want (%q, true)", out5, f.HasPending(), " end ")
	}
	flushed, sf := f.Flush()
	if flushed != "<" || sf {
		t.Fatalf("Flush() = (%q, %v), want (%q, false)", flushed, sf, "<")
	}

	// Unclosed DSML fragment at EOF is discarded.
	_, _ = f.Feed("ok</｜DSML｜parameter")
	flushed2, sf2 := f.Flush()
	if flushed2 != "" || !sf2 {
		t.Fatalf("Flush(unclosed DSML) = (%q, %v), want (%q, true)", flushed2, sf2, "")
	}
}

func TestSanitizeDSMLInRequestPayloadOnlyTouchesAssistant(t *testing.T) {
	payload := map[string]any{
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "Why did it output </｜DSML｜invoke>?",
			},
			map[string]any{
				"role":              "assistant",
				"content":           "Sure.</｜DSML｜invoke>",
				"reasoning_content": "thinking</ | DSML | parameter>",
			},
		},
		"input": []any{
			map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "input_text", "text": "keep </｜DSML｜invoke>"}},
			},
			map[string]any{
				"role":    "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "clean</｜DSML｜invoke>"}},
			},
		},
	}

	sanitizeDSMLInRequestPayload(payload)

	msgs := payload["messages"].([]any)
	if got := msgs[0].(map[string]any)["content"]; got != "Why did it output </｜DSML｜invoke>?" {
		t.Fatalf("user message mutated: %q", got)
	}
	asst := msgs[1].(map[string]any)
	if asst["content"] != "Sure." || asst["reasoning_content"] != "thinking" {
		t.Fatalf("assistant message not sanitized: %#v", asst)
	}

	inputs := payload["input"].([]any)
	if got := inputs[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]; got != "keep </｜DSML｜invoke>" {
		t.Fatalf("user input item mutated: %q", got)
	}
	if got := inputs[1].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]; got != "clean" {
		t.Fatalf("assistant input item not sanitized: %q", got)
	}
}

func TestSanitizeDSMLInBufferedResponse(t *testing.T) {
	raw := []byte(`{"id":"chatcmpl_1","choices":[{"index":0,"message":{"role":"assistant","content":"Answer.</｜DSML｜invoke>"}}]}`)
	got := sanitizeDSMLInBufferedResponse(raw, "application/json")
	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal sanitized response: %v", err)
	}
	content := parsed["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"]
	if content != "Answer." {
		t.Fatalf("buffered content = %q, want %q", content, "Answer.")
	}
}

func TestProxyUpstreamStreamStripsSplitDSMLTags(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"id":"1","choices":[{"index":0,"delta":{"content":"Hi </｜DSML"}}]}`,
		``,
		`data: {"id":"1","choices":[{"index":0,"delta":{"content":"｜invoke> there"}}]}`,
		``,
		`data: {"id":"1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	rec := httptest.NewRecorder()
	capture, started, err := proxyUpstreamStream(context.Background(), rec, gatewayStreamTestResponse(sse), time.Now())
	if err != nil {
		t.Fatalf("proxyUpstreamStream error: %v", err)
	}
	if !started {
		t.Fatal("expected stream to start")
	}
	body := rec.Body.String()
	if strings.Contains(body, "DSML") {
		t.Fatalf("leaked DSML tag in downstream SSE: %s", body)
	}
	if !strings.Contains(body, `"content":"Hi "`) || !strings.Contains(body, `"content":" there"`) {
		t.Fatalf("unexpected downstream SSE body: %s", body)
	}
	foundRepair := false
	for _, r := range capture.sseRepairs {
		if r == sseRepairDSMLTagStripped {
			foundRepair = true
			break
		}
	}
	if !foundRepair {
		t.Fatalf("expected %q in sseRepairs, got %v", sseRepairDSMLTagStripped, capture.sseRepairs)
	}
}

func TestDSMLCanonicalStreamFilter(t *testing.T) {
	f := newDSMLCanonicalStreamFilter()
	events, stripped1 := f.Apply([]canonicalStreamEvent{
		{Type: canonicalStreamEventTextDelta, Delta: "Hello </｜DSML"},
		{Type: canonicalStreamEventTextDelta, Delta: "｜invoke>"},
		{Type: canonicalStreamEventCompleted, FinishReason: "stop"},
	})
	if !stripped1 {
		t.Fatal("expected DSML tag to be stripped")
	}
	var text strings.Builder
	for _, ev := range events {
		if ev.Type == canonicalStreamEventTextDelta {
			text.WriteString(ev.Delta)
		}
	}
	if text.String() != "Hello " {
		t.Fatalf("canonical filtered text = %q, want %q", text.String(), "Hello ")
	}
}
