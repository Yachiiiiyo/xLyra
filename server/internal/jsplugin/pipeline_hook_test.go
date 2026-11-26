package jsplugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"xlyra/server/internal/textmatch"
)

func pipelineHookPlugin(t *testing.T, hook string, fixtures []Fixture) *Plugin {
	t.Helper()
	source := pipelineTestSource(hook)
	plugin, err := NewPlugin(pipelineTestManifest(source, func(m *Manifest) {
		m.GatewayPipeline.Types = append(m.GatewayPipeline.Types, PipelineTypeSpec{Type: "PERSON"})
	}), source, fixtures, 1)
	if err != nil {
		t.Fatal(err)
	}
	return plugin
}

func TestInspectSegmentsConvertsUTF16Offsets(t *testing.T) {
	plugin := pipelineHookPlugin(t, `export function inspectSegments(ctx, input) {
  const seg = input.segments[0];
  const f = input.findings[0];
  if (seg.text.slice(f.start, f.end) !== "张三") throw new Error("offsets");
  if (ctx.endpoint !== "/v1/chat/completions" || ctx.binding.id !== "b1" || ctx.binding.params.secret !== true) throw new Error("ctx");
  const i = seg.text.indexOf("李四");
  return { drop: [0], add: [{ segment: seg.id, start: f.start, end: f.end, type: "PERSON" }, { segment: seg.id, start: i, end: i + 2, type: "PERSON", source: "dictionary", score: 0.9 }] };
}`, nil)
	text := "😀a张三，🎉李四"
	start := strings.Index(text, "张三")
	result, err := plugin.InspectPipelineSegments(context.Background(), PipelineInspection{
		Endpoint: "/v1/chat/completions", BindingID: "b1", Params: map[string]any{"secret": true},
		Segments: []PipelineSegment{{ID: "seg_0", Role: "user", Field: "message.text", Text: text}},
		Findings: [][]textmatch.Span{{{Start: start, End: start + len("张三"), Type: "CUSTOM", Source: textmatch.SourceDictionary}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, span := range result.Findings[0] {
		got = append(got, span.Type+":"+text[span.Start:span.End]+":"+span.Source)
	}
	if strings.Join(got, ",") != "PERSON:张三:hook,PERSON:李四:dictionary" || result.Calls != 1 {
		t.Fatalf("findings = %q calls = %d", got, result.Calls)
	}
}

func TestInspectSegmentsBatchesAndBudget(t *testing.T) {
	plugin := pipelineHookPlugin(t, `export function inspectSegments(ctx, input) { return {}; }`, nil)
	big := strings.Repeat("x", 200<<10)
	segments := []PipelineSegment{{ID: "a", Text: big}, {ID: "b", Text: big}, {ID: "c", Text: "small"}, {ID: "d", Text: "small"}}
	result, err := plugin.InspectPipelineSegments(context.Background(), PipelineInspection{Segments: segments, Findings: make([][]textmatch.Span, len(segments))})
	if err != nil || result.Calls != 2 {
		t.Fatalf("calls = %d, %v", result.Calls, err)
	}
	if got := inspectBatches(segments); len(got) != 2 || len(got[0]) != 1 || len(got[1]) != 3 {
		t.Fatalf("batches = %v", got)
	}

	slow := pipelineHookPlugin(t, `export function inspectSegments(ctx, input) { for (;;) {} }`, nil)
	started := time.Now()
	_, err = slow.InspectPipelineSegments(context.Background(), PipelineInspection{
		Segments: []PipelineSegment{{ID: "a", Text: "x"}, {ID: "b", Text: strings.Repeat("y", 300<<10)}},
		Findings: make([][]textmatch.Span, 2),
		Budget:   20 * time.Millisecond,
	})
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("budget: %v after %s", err, time.Since(started))
	}
}

func TestInspectSegmentsRejectNeverQuotesText(t *testing.T) {
	plugin := pipelineHookPlugin(t, `export function inspectSegments(ctx, input) {
  return { reject: { status: 403, code: "Bad Code", message: "found " + input.segments[0].text.slice(0, 5) } };
}`, nil)
	text := "13812345678 is my phone"
	result, err := plugin.InspectPipelineSegments(context.Background(), PipelineInspection{
		Segments: []PipelineSegment{{ID: "seg_0", Text: text}},
		Findings: [][]textmatch.Span{{{Start: 0, End: 5, Type: "PHONE"}}},
	})
	if err != nil || result.Reject == nil {
		t.Fatalf("reject = %#v %v", result.Reject, err)
	}
	if result.Reject.Status != 403 || result.Reject.Code != DefaultPipelineRejectCode || strings.Contains(result.Reject.Message, "13812") {
		t.Fatalf("reject = %#v", result.Reject)
	}
	badStatus := pipelineHookPlugin(t, `export function inspectSegments() { return { reject: { status: 500 } }; }`, nil)
	if _, err := badStatus.InspectPipelineSegments(context.Background(), PipelineInspection{Segments: []PipelineSegment{{ID: "s", Text: "x"}}, Findings: [][]textmatch.Span{nil}}); err == nil {
		t.Fatal("reject.status other than 400 and 403 is a shape error")
	}
}

func TestInspectSegmentsFixtures(t *testing.T) {
	var fixture Fixture
	if err := json.Unmarshal([]byte(`{
  "name": "person", "hook": "inspectSegments",
  "ctx": {"endpoint": "/v1/messages", "model": "m", "inputs": {"secret": true}},
  "input": {"segments": [{"id": "s0", "text": "😀张三", "role": "user", "field": "message.text"}], "findings": []},
  "expect": {"result": {"add": [{"segment": "s0", "start": 2, "end": 4, "type": "PERSON"}]}}
}`), &fixture); err != nil {
		t.Fatal(err)
	}
	hook := `export function inspectSegments(ctx, input) {
  if (ctx.endpoint !== "/v1/messages" || ctx.binding.params.secret !== true) throw new Error("ctx");
  const s = input.segments[0];
  const i = s.text.indexOf("张三");
  return { add: [{ segment: s.id, start: i, end: i + 2, type: "PERSON" }] };
}`
	plugin := pipelineHookPlugin(t, hook, []Fixture{fixture})
	if err := plugin.SelfTest(context.Background()); err != nil {
		t.Fatalf("selftest: %v", err)
	}
	wrong := fixture
	wrong.Expect.Result = map[string]any{"add": []any{}}
	if err := pipelineHookPlugin(t, hook, []Fixture{wrong}).SelfTest(context.Background()); err == nil {
		t.Fatal("a mismatching fixture must fail")
	}
	badOffsets := fixture
	badOffsets.Expect = fixtureExpectation{Error: "character boundaries"}
	surrogate := `export function inspectSegments(ctx, input) { return { add: [{ segment: "s0", start: 1, end: 3, type: "PERSON" }] }; }`
	if err := pipelineHookPlugin(t, surrogate, []Fixture{badOffsets}).SelfTest(context.Background()); err != nil {
		t.Fatalf("a fixture can expect the boundary error: %v", err)
	}
	if err := pipelineHookPlugin(t, hook, nil).SelfTest(context.Background()); err == nil {
		t.Fatal("a pipeline that exports inspectSegments needs samples")
	}
}
