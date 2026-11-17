package gateway

import (
	"errors"
	"io"
	"strings"
	"testing"

	"xlyra/server/internal/jsplugin"
)

type closeSpy struct {
	io.Reader
	closed bool
}

func (c *closeSpy) Close() error {
	c.closed = true
	return nil
}

func readAllString(t *testing.T, r io.Reader) string {
	t.Helper()
	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestStreamUnwrapFormats(t *testing.T) {
	t.Parallel()
	src := strings.Join([]string{
		`data: {"body":{"id":"a"},"statusCode":200}`,
		``,
		`data: {"body":"{\"id\":\"b\"}","statusCode":200}`,
		``,
		`data: {"body":"raw-text","statusCode":200}`,
		``,
		``,
	}, "\n")
	section := &jsplugin.StreamSection{Unwrap: &jsplugin.StreamUnwrap{Path: "body", Format: "json", StatusPath: "statusCode", OKStatuses: []int{200}}}
	got := readAllString(t, newStreamTransformer(io.NopCloser(strings.NewReader(src)), section))
	if !strings.Contains(got, `data: {"id":"a"}`) {
		t.Fatalf("json unwrap = %s", got)
	}

	stringSection := &jsplugin.StreamSection{Unwrap: &jsplugin.StreamUnwrap{Path: "body", Format: "json_string"}}
	got = readAllString(t, newStreamTransformer(io.NopCloser(strings.NewReader("data: {\"body\":\"{\\\"id\\\":\\\"b\\\"}\"}\n\n")), stringSection))
	if !strings.Contains(got, `data: {"id":"b"}`) {
		t.Fatalf("json_string = %s", got)
	}
	rawSection := &jsplugin.StreamSection{Unwrap: &jsplugin.StreamUnwrap{Path: "body", Format: "raw"}}
	got = readAllString(t, newStreamTransformer(io.NopCloser(strings.NewReader("data: {\"body\":\"raw-text\"}\n\n")), rawSection))
	if !strings.Contains(got, "data: raw-text\n") {
		t.Fatalf("raw = %s", got)
	}
}

func TestStreamUnwrapDoneMarkersAndClose(t *testing.T) {
	t.Parallel()
	body := "data: [DONE]\n\ndata: {\"id\":1}\n\n"
	src := &closeSpy{Reader: strings.NewReader(body)}
	section := &jsplugin.StreamSection{Done: &jsplugin.StreamDone{DataEquals: []string{"[DONE]"}, CloseOnDone: true}}
	tr := newStreamTransformer(src, section)
	got := readAllString(t, tr)
	if !strings.Contains(got, "data: [DONE]\n") || strings.Contains(got, `"id"`) {
		t.Fatalf("body = %q closed=%v", got, src.closed)
	}
	if !src.closed {
		t.Fatal("upstream was not closed")
	}

	eventBody := "event: finish\ndata: {\"id\":1}\n\ndata: later\n\n"
	got = readAllString(t, newStreamTransformer(io.NopCloser(strings.NewReader(eventBody)), &jsplugin.StreamSection{
		Done: &jsplugin.StreamDone{Events: []string{"finish"}, CloseOnDone: true},
	}))
	if strings.Contains(got, "later") {
		t.Fatalf("event done = %s", got)
	}

	unwrapped := "data: {\"body\":\"[DONE]\"}\n\ndata: {\"body\":{\"id\":2}}\n\n"
	got = readAllString(t, newStreamTransformer(io.NopCloser(strings.NewReader(unwrapped)), &jsplugin.StreamSection{
		Unwrap: &jsplugin.StreamUnwrap{Path: "body", Format: "raw"},
		Done:   &jsplugin.StreamDone{UnwrappedEquals: []string{"[DONE]"}, CloseOnDone: true},
	}))
	if !strings.Contains(got, "data: [DONE]\n") || strings.Contains(got, `"id"`) {
		t.Fatalf("unwrapped done = %s", got)
	}
}

func TestStreamUnwrapUsageDoesNotOverwrite(t *testing.T) {
	t.Parallel()
	payload := `{"usage":{"in":3,"out":4,"prompt_tokens":9,"cached":1,"reason":2}}`
	body := "data: " + payload + "\n\n"
	section := &jsplugin.StreamSection{Usage: &jsplugin.StreamUsage{
		InputTokens: "usage.in", OutputTokens: "usage.out", CachedTokens: "usage.cached", ReasoningTokens: "usage.reason",
	}}
	got := readAllString(t, newStreamTransformer(io.NopCloser(strings.NewReader(body)), section))
	if !strings.Contains(got, `"prompt_tokens":9`) || !strings.Contains(got, `"completion_tokens":4`) {
		t.Fatalf("usage = %s", got)
	}
	if !strings.Contains(got, `"cached_tokens":1`) || !strings.Contains(got, `"reasoning_tokens":2`) {
		t.Fatalf("details = %s", got)
	}
}

func TestStreamUnwrapMalformedAndComments(t *testing.T) {
	t.Parallel()
	var lines []string
	for i := 0; i < 9; i++ {
		lines = append(lines, "data: not-json", "")
	}
	_, err := io.ReadAll(newStreamTransformer(io.NopCloser(strings.NewReader(strings.Join(lines, "\n"))), &jsplugin.StreamSection{
		Unwrap: &jsplugin.StreamUnwrap{Path: "body"},
	}))
	if !errors.Is(err, errUpstreamStreamMalformed) {
		t.Fatalf("err = %v", err)
	}

	reset := "data: nope\n\n: ping\n\ndata: {\"body\":{\"id\":1}}\n\n"
	got := readAllString(t, newStreamTransformer(io.NopCloser(strings.NewReader(reset)), &jsplugin.StreamSection{
		Unwrap: &jsplugin.StreamUnwrap{Path: "body"},
	}))
	if !strings.Contains(got, `data: {"id":1}`) {
		t.Fatalf("reset = %s", got)
	}

	plain := "data: hello\n\n"
	got = readAllString(t, newStreamTransformer(io.NopCloser(strings.NewReader(plain)), nil))
	if got != "data: hello\n\n" {
		t.Fatalf("passthrough = %q", got)
	}
}

func TestStreamUnwrapStatusError(t *testing.T) {
	t.Parallel()
	body := "data: {\"body\":{\"id\":1},\"statusCode\":500}\n\n"
	_, err := io.ReadAll(newStreamTransformer(io.NopCloser(strings.NewReader(body)), &jsplugin.StreamSection{
		Unwrap: &jsplugin.StreamUnwrap{Path: "body", StatusPath: "statusCode", OKStatuses: []int{200}},
	}))
	if !errors.Is(err, errUpstreamStreamStatus) {
		t.Fatalf("err = %v", err)
	}
}

func TestTransformStreamChainAndDrop(t *testing.T) {
	t.Parallel()
	src1 := `export const meta = { apiVersion: 1, id: "acme.s1" };
export function transformStream(ctx, ev) {
	if (ev.data === "drop-me") return { drop: true };
	return { event: "chunk", data: ev.data + "|p1" };
}
`
	p1, err := jsplugin.NewPlugin(jsplugin.Manifest{
		ID: "acme.s1", Name: "s1", Version: "1.0.0", APIVersion: 1, HostAPI: 2,
		SHA256: map[string]string{"plugin.js": jsplugin.HashSource(src1)},
	}, src1, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	src2 := `export const meta = { apiVersion: 1, id: "acme.s2" };
export function transformStream(ctx, ev) {
	return { data: ev.data + "|p2:" + ctx.model, done: ev.data.indexOf("stop") === 0 };
}
`
	p2, err := jsplugin.NewPlugin(jsplugin.Manifest{
		ID: "acme.s2", Name: "s2", Version: "1.0.0", APIVersion: 1, HostAPI: 2,
		SHA256: map[string]string{"plugin.js": jsplugin.HashSource(src2)},
	}, src2, nil, 1)
	if err != nil {
		t.Fatal(err)
	}

	raw := strings.Join([]string{
		"data: hello",
		"",
		"data: drop-me",
		"",
		"data: stop-now",
		"",
		"data: unreachable",
		"",
		"",
	}, "\n")
	tr := newStreamTransformer(io.NopCloser(strings.NewReader(raw)), nil)
	tr.hooks = []*jsplugin.Plugin{p1, p2}
	tr.streamCtx = jsplugin.StreamTransformContext{SiteType: "openai", Model: "gpt-4o", Protocol: "openai"}
	got := readAllString(t, tr)
	if !strings.Contains(got, "event: chunk\ndata: hello|p1|p2:gpt-4o\n\n") {
		t.Fatalf("first event missing: %q", got)
	}
	if strings.Contains(got, "drop-me") {
		t.Fatalf("dropped event leaked: %q", got)
	}
	if !strings.Contains(got, "data: stop-now|p1|p2:gpt-4o\n\n") {
		t.Fatalf("stop event missing: %q", got)
	}
	if strings.Contains(got, "unreachable") {
		t.Fatalf("events after done should not be emitted: %q", got)
	}
}
