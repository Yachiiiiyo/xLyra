package jsplugin

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"xlyra/server/internal/textmatch"
)

// Limits of the inspectSegments hook.
const (
	// maxInspectBatchBytes is the most segment text sent in one call. A
	// larger segment is sent alone.
	maxInspectBatchBytes = 256 << 10
	// maxInspectRejectMessage bounds the message of a reject.
	maxInspectRejectMessage = 512
	// DefaultPipelineRejectCode is the error code of a reject that names none.
	DefaultPipelineRejectCode    = "plugin_payload_rejected"
	defaultPipelineRejectMessage = "the request was rejected by a gateway pipeline"
)

// FindingSources are the values of SegmentFinding.Source.
var FindingSources = []string{textmatch.SourceRule, textmatch.SourceDictionary}

var pipelineRejectCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ErrPipelineBudgetExceeded means inspectSegments ran out of the request budget.
var ErrPipelineBudgetExceeded = errors.New("inspectSegments exceeded the pipeline time budget")

// InspectSegmentsContext is the ctx argument of inspectSegments.
type InspectSegmentsContext struct {
	Endpoint string                 `ts:"endpoint,doc=Downstream path, such as /v1/chat/completions."`
	Model    string                 `ts:"model,doc=Model the client asked for."`
	Binding  InspectSegmentsBinding `ts:"binding"`
	Settings map[string]any         `ts:"settings,optional,type=Record<string, SettingValue>,doc=Resolved admin settings when manifest declares settings."`
}

// InspectSegmentsBinding is the binding a request runs under.
type InspectSegmentsBinding struct {
	ID     string         `ts:"id"`
	Params map[string]any `ts:"params,type=Record<string, SettingValue>,doc=The parameters an admin set on the binding."`
}

// SegmentItem is one piece of request text.
type SegmentItem struct {
	ID    string `ts:"id"`
	Text  string `ts:"text"`
	Role  string `ts:"role,optional,doc=system, user, assistant or tool."`
	Field string `ts:"field,doc=instructions, message.text, tool.arguments, tool.result, tool.definition, prompt or embedding.input."`
}

// SegmentFinding is one typed range of a segment, in UTF-16 code units.
type SegmentFinding struct {
	Segment string  `ts:"segment,doc=SegmentItem.id."`
	Start   int     `ts:"start,doc=UTF-16 code unit index, inclusive."`
	End     int     `ts:"end,doc=UTF-16 code unit index, exclusive."`
	Type    string  `ts:"type,doc=A type declared in gatewayPipeline.types."`
	Source  string  `ts:"source,optional,type=FindingSource"`
	Score   float64 `ts:"score,optional"`
}

// InspectSegmentsInput is the input argument of inspectSegments.
type InspectSegmentsInput struct {
	Segments []SegmentItem    `ts:"segments"`
	Findings []SegmentFinding `ts:"findings,doc=What the host rules and dictionaries found in these segments."`
}

// InspectSegmentsReject stops the request.
type InspectSegmentsReject struct {
	Status  int    `ts:"status,optional,type=400 | 403,doc=Default 400."`
	Code    string `ts:"code,optional,doc=Error code. Default plugin_payload_rejected."`
	Message string `ts:"message,optional,doc=Shown to the client. Never quote the segment text."`
}

// InspectSegmentsResult is what inspectSegments returns.
type InspectSegmentsResult struct {
	Add    []SegmentFinding       `ts:"add,optional,doc=More findings."`
	Drop   []int                  `ts:"drop,optional,doc=Indexes into input.findings to discard."`
	Reject *InspectSegmentsReject `ts:"reject,optional,doc=Reject the request instead of sending it."`
}

// PipelineSegment is a segment as the gateway extracted it.
type PipelineSegment struct {
	ID    string
	Role  string
	Field string
	Text  string
}

// PipelineInspection is one request's call of inspectSegments over the
// segments the cache did not know.
type PipelineInspection struct {
	Endpoint  string
	Model     string
	BindingID string
	Params    map[string]any
	Segments  []PipelineSegment
	// Findings holds the native findings of each segment, in bytes.
	Findings [][]textmatch.Span
	// Keep filters added findings (type switches and allowlist); nil keeps all.
	Keep func(typ, value string) bool
	// Budget is the time all calls of the request may take.
	Budget time.Duration
	// Breaker records each call, when set.
	Breaker *Breaker
}

// PipelineInspectionResult holds the reviewed findings of each segment, in
// bytes, or the reject that stops the request.
type PipelineInspectionResult struct {
	Findings [][]textmatch.Span
	Reject   *InspectSegmentsReject
	Calls    int
}

// InspectPipelineSegments runs inspectSegments over the segments in batches
// of at most 256 KiB of text. It always drops the plugin's logs: the hook
// sees request text. An error leaves the failure policy to the caller, and
// never carries segment text.
func (p *Plugin) InspectPipelineSegments(ctx context.Context, in PipelineInspection) (PipelineInspectionResult, error) {
	result := PipelineInspectionResult{Findings: make([][]textmatch.Span, len(in.Segments))}
	for i := range in.Segments {
		if i < len(in.Findings) {
			result.Findings[i] = append([]textmatch.Span(nil), in.Findings[i]...)
		}
	}
	if p == nil || !p.HasHook(HookInspectSegments) || len(in.Segments) == 0 {
		return result, nil
	}
	if p.Manifest.GatewayPipeline == nil {
		return result, fmt.Errorf("plugin has no gatewayPipeline section")
	}
	declared := map[string]bool{}
	for _, typ := range p.Manifest.GatewayPipeline.Types {
		declared[typ.Type] = true
	}
	budget := in.Budget
	if budget <= 0 {
		budget = time.Duration(DefaultPipelineTotalTimeoutMs) * time.Millisecond
	}
	deadline := time.Now().Add(budget)
	ctx = WithDiscardLogs(ctx)
	hookCtx := InspectSegmentsContext{
		Endpoint: in.Endpoint,
		Model:    in.Model,
		Binding:  InspectSegmentsBinding{ID: in.BindingID, Params: in.Params},
	}
	for _, batch := range inspectBatches(in.Segments) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return result, ErrPipelineBudgetExceeded
		}
		reject, err := p.inspectBatch(ctx, hookCtx, in, batch, declared, min(remaining, protocolHookTimeout), deadline, &result)
		result.Calls++
		if in.Breaker != nil {
			in.Breaker.RecordProtocolCall(p, HookInspectSegments, err != nil, isCallInterruption(err))
		}
		if err != nil {
			return result, err
		}
		if reject != nil {
			result.Reject = reject
			return result, nil
		}
	}
	return result, nil
}

func isCallInterruption(err error) bool {
	var call *CallError
	if errors.As(err, &call) {
		return call.Kind == KindTimeout || call.Kind == KindHeapLimit
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// inspectBatches groups segment indexes so each group holds at most
// maxInspectBatchBytes of text, except a single larger segment.
func inspectBatches(segments []PipelineSegment) [][]int {
	var out [][]int
	var current []int
	size := 0
	for i, seg := range segments {
		if len(current) > 0 && size+len(seg.Text) > maxInspectBatchBytes {
			out = append(out, current)
			current, size = nil, 0
		}
		current = append(current, i)
		size += len(seg.Text)
	}
	if len(current) > 0 {
		out = append(out, current)
	}
	return out
}

type batchFinding struct {
	segment int
	span    textmatch.Span
}

func (p *Plugin) inspectBatch(ctx context.Context, hookCtx InspectSegmentsContext, in PipelineInspection, batch []int, declared map[string]bool, timeout time.Duration, deadline time.Time, result *PipelineInspectionResult) (*InspectSegmentsReject, error) {
	input := InspectSegmentsInput{Segments: make([]SegmentItem, 0, len(batch)), Findings: []SegmentFinding{}}
	byID := map[string]int{}
	var flat []batchFinding
	maps := make(map[int]*utf16Map, len(batch))
	for _, index := range batch {
		seg := in.Segments[index]
		byID[seg.ID] = index
		input.Segments = append(input.Segments, SegmentItem{ID: seg.ID, Text: seg.Text, Role: seg.Role, Field: seg.Field})
		spans := result.Findings[index]
		offsets := make([]int, 0, 2*len(spans))
		for _, span := range spans {
			offsets = append(offsets, span.Start, span.End)
		}
		conv := newUTF16Map(seg.Text, offsets)
		maps[index] = conv
		for _, span := range spans {
			start, okStart := conv.toUTF16(span.Start)
			end, okEnd := conv.toUTF16(span.End)
			if !okStart || !okEnd {
				return nil, fmt.Errorf("segment %s: a finding is not on a character boundary", seg.ID)
			}
			flat = append(flat, batchFinding{segment: index, span: span})
			input.Findings = append(input.Findings, SegmentFinding{Segment: seg.ID, Start: start, End: end, Type: span.Type, Source: span.Source, Score: span.Score})
		}
	}
	callCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	out, _, _, err := p.callHook(callCtx, HookInspectSegments, timeout, 0, encodeValue(hookCtx), encodeValue(input))
	if err != nil {
		return nil, err
	}
	decoded, err := decodeInspectSegments(out)
	if err != nil {
		return nil, annotate(p, err, HookInspectSegments, 0, 0)
	}
	if decoded.Reject != nil {
		return normalizeInspectReject(*decoded.Reject, in, batch), nil
	}
	dropped := make([]bool, len(flat))
	for _, idx := range decoded.Drop {
		if idx < 0 || idx >= len(flat) {
			return nil, annotate(p, shapeError("drop: index %d is out of range", idx), HookInspectSegments, 0, 0)
		}
		dropped[idx] = true
	}
	added := make([]batchFinding, 0, len(decoded.Add))
	for i, finding := range decoded.Add {
		index, ok := byID[finding.Segment]
		if !ok {
			return nil, annotate(p, shapeError("add[%d]: segment is not in this call", i), HookInspectSegments, 0, 0)
		}
		if !declared[finding.Type] {
			return nil, annotate(p, shapeError("add[%d]: type %q is not declared", i, finding.Type), HookInspectSegments, 0, 0)
		}
		conv := maps[index].withUTF16(in.Segments[index].Text, finding.Start, finding.End)
		start, okStart := conv.toBytes(finding.Start)
		end, okEnd := conv.toBytes(finding.End)
		if finding.Start < 0 || finding.End <= finding.Start || !okStart || !okEnd {
			return nil, annotate(p, shapeError("add[%d]: start and end must be ordered UTF-16 indexes on character boundaries", i), HookInspectSegments, 0, 0)
		}
		source := finding.Source
		if source == "" {
			source = "hook"
		}
		added = append(added, batchFinding{segment: index, span: textmatch.Span{Start: start, End: end, Type: finding.Type, Source: source, Score: finding.Score}})
	}
	for _, index := range batch {
		result.Findings[index] = nil
	}
	for i, finding := range flat {
		if !dropped[i] {
			result.Findings[finding.segment] = append(result.Findings[finding.segment], finding.span)
		}
	}
	for _, finding := range added {
		text := in.Segments[finding.segment].Text
		if in.Keep != nil && !in.Keep(finding.span.Type, text[finding.span.Start:finding.span.End]) {
			continue
		}
		result.Findings[finding.segment] = append(result.Findings[finding.segment], finding.span)
	}
	return nil, nil
}

// normalizeInspectReject fills defaults and replaces a message that quotes
// any finding or a whole segment of the batch, so a reject never echoes text.
func normalizeInspectReject(reject InspectSegmentsReject, in PipelineInspection, batch []int) *InspectSegmentsReject {
	out := reject
	if out.Status != 403 {
		out.Status = 400
	}
	if out.Code == "" || !pipelineRejectCodePattern.MatchString(out.Code) {
		out.Code = DefaultPipelineRejectCode
	}
	message := strings.TrimSpace(out.Message)
	if message == "" || len(message) > maxInspectRejectMessage || !utf8.ValidString(message) || quotesSegments(message, in, batch) {
		message = defaultPipelineRejectMessage
	}
	out.Message = message
	return &out
}

func quotesSegments(message string, in PipelineInspection, batch []int) bool {
	for _, index := range batch {
		text := in.Segments[index].Text
		if len(text) >= 3 && strings.Contains(message, text) {
			return true
		}
		for _, span := range in.Findings[index] {
			if value := text[span.Start:span.End]; len(value) >= 3 && strings.Contains(message, value) {
				return true
			}
		}
	}
	return false
}

func decodeInspectSegments(value any) (InspectSegmentsResult, error) {
	var out InspectSegmentsResult
	if value == nil {
		return out, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return out, shapeError("result: expected an object")
	}
	for key := range object {
		switch key {
		case "add", "drop", "reject":
		default:
			return out, shapeError("result: unknown field %q", key)
		}
	}
	if raw, ok := object["add"]; ok && raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return out, shapeError("add: expected an array")
		}
		for i, item := range list {
			finding, ok := item.(map[string]any)
			if !ok {
				return out, shapeError("add[%d]: expected an object", i)
			}
			segment, _ := finding["segment"].(string)
			typ, _ := finding["type"].(string)
			start, okStart := exactInt(finding["start"])
			end, okEnd := exactInt(finding["end"])
			if segment == "" || typ == "" || !okStart || !okEnd {
				return out, shapeError("add[%d]: segment, start, end and type are required", i)
			}
			entry := SegmentFinding{Segment: segment, Start: start, End: end, Type: typ}
			if source, ok := finding["source"].(string); ok {
				if !slices.Contains(FindingSources, source) {
					return out, shapeError("add[%d]: unknown source %q", i, source)
				}
				entry.Source = source
			}
			if score, ok := asFloat(finding["score"]); ok {
				entry.Score = score
			}
			out.Add = append(out.Add, entry)
		}
	}
	if raw, ok := object["drop"]; ok && raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return out, shapeError("drop: expected an array")
		}
		for i, item := range list {
			idx, ok := exactInt(item)
			if !ok {
				return out, shapeError("drop[%d]: expected an integer", i)
			}
			out.Drop = append(out.Drop, idx)
		}
	}
	if raw, ok := object["reject"]; ok && raw != nil {
		reject, ok := raw.(map[string]any)
		if !ok {
			return out, shapeError("reject: expected an object")
		}
		entry := InspectSegmentsReject{}
		if status, ok := reject["status"]; ok && status != nil {
			code, ok := exactInt(status)
			if !ok || (code != 400 && code != 403) {
				return out, shapeError("reject.status must be 400 or 403")
			}
			entry.Status = code
		}
		entry.Code, _ = reject["code"].(string)
		entry.Message, _ = reject["message"].(string)
		out.Reject = &entry
	}
	return out, nil
}

func exactInt(value any) (int, bool) {
	number, ok := asFloat(value)
	if !ok || number != float64(int(number)) {
		return 0, false
	}
	return int(number), true
}

// utf16Map converts the offsets a call needs between UTF-8 bytes and UTF-16
// code units in one pass over the text, without an index of every character.
type utf16Map struct {
	byteToUnit map[int]int
	unitToByte map[int]int
}

func newUTF16Map(text string, byteOffsets []int) *utf16Map {
	m := &utf16Map{byteToUnit: map[int]int{}, unitToByte: map[int]int{}}
	if len(byteOffsets) == 0 {
		return m
	}
	want := map[int]bool{}
	for _, offset := range byteOffsets {
		want[offset] = true
	}
	units := 0
	for offset, r := range text {
		if want[offset] {
			m.byteToUnit[offset] = units
		}
		units += utf16Width(r)
	}
	if want[len(text)] {
		m.byteToUnit[len(text)] = units
	}
	return m
}

// withUTF16 adds the conversions of two UTF-16 indexes.
func (m *utf16Map) withUTF16(text string, starts ...int) *utf16Map {
	want := map[int]bool{}
	for _, unit := range starts {
		if _, ok := m.unitToByte[unit]; !ok {
			want[unit] = true
		}
	}
	if len(want) == 0 {
		return m
	}
	units := 0
	for offset, r := range text {
		if want[units] {
			m.unitToByte[units] = offset
		}
		units += utf16Width(r)
	}
	if want[units] {
		m.unitToByte[units] = len(text)
	}
	return m
}

func (m *utf16Map) toUTF16(offset int) (int, bool) {
	unit, ok := m.byteToUnit[offset]
	return unit, ok
}

func (m *utf16Map) toBytes(unit int) (int, bool) {
	offset, ok := m.unitToByte[unit]
	return offset, ok
}

func utf16Width(r rune) int {
	if r >= 0x10000 && r <= utf8.MaxRune {
		return 2
	}
	return 1
}

// runInspectSegmentsFixture calls inspectSegments with a fixture's input,
// checks the result the way a request would, and compares it with
// expect.result or expect.error. ctx.inputs are the binding parameters.
func (p *Plugin) runInspectSegmentsFixture(ctx context.Context, fixture Fixture) error {
	var in InspectSegmentsInput
	if err := decodeFixtureInput(fixture.Input, &in); err != nil {
		return err
	}
	params := fixture.Ctx.Inputs
	if params == nil {
		params = map[string]any{}
	}
	hookCtx := InspectSegmentsContext{
		Endpoint: fixture.Ctx.Endpoint,
		Model:    fixture.Ctx.Model,
		Binding:  InspectSegmentsBinding{ID: fixture.Ctx.BindingID, Params: params},
	}
	out, _, _, err := p.callHook(WithDiscardLogs(ctx), HookInspectSegments, protocolHookTimeout, 0, encodeValue(hookCtx), encodeValue(in))
	var result InspectSegmentsResult
	if err == nil {
		result, err = decodeInspectSegments(out)
	}
	if err == nil {
		err = p.checkInspectResult(in, result)
	}
	if err != nil {
		err = annotate(p, err, HookInspectSegments, 0, 0)
	}
	return compareOneShot(encodeValue(result), err, fixture)
}

// checkInspectResult applies the request-time checks to a hook result.
func (p *Plugin) checkInspectResult(in InspectSegmentsInput, result InspectSegmentsResult) error {
	declared := map[string]bool{}
	if p.Manifest.GatewayPipeline != nil {
		for _, typ := range p.Manifest.GatewayPipeline.Types {
			declared[typ.Type] = true
		}
	}
	texts := map[string]string{}
	for _, seg := range in.Segments {
		texts[seg.ID] = seg.Text
	}
	for _, idx := range result.Drop {
		if idx < 0 || idx >= len(in.Findings) {
			return shapeError("drop: index %d is out of range", idx)
		}
	}
	for i, finding := range result.Add {
		text, ok := texts[finding.Segment]
		if !ok {
			return shapeError("add[%d]: segment is not in this call", i)
		}
		if !declared[finding.Type] {
			return shapeError("add[%d]: type %q is not declared", i, finding.Type)
		}
		conv := newUTF16Map(text, nil).withUTF16(text, finding.Start, finding.End)
		_, okStart := conv.toBytes(finding.Start)
		_, okEnd := conv.toBytes(finding.End)
		if finding.Start < 0 || finding.End <= finding.Start || !okStart || !okEnd {
			return shapeError("add[%d]: start and end must be ordered UTF-16 indexes on character boundaries", i)
		}
	}
	return nil
}
