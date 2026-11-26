package textpipeline

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/textmatch"
	"xlyra/server/internal/textxform"
)

// Error codes of the gateway pipeline. They name the pipeline, never a plugin
// or what it is used for.
const (
	CodePipelineUnavailable = "plugin_pipeline_unavailable"
	CodePipelineFailed      = "plugin_pipeline_failed"
	CodeUnsupportedEndpoint = "plugin_unsupported_endpoint"
	CodePayloadTooLarge     = "plugin_payload_too_large"
)

// Outcomes of a run, as recorded in metrics.
const (
	OutcomeTransformed = "transformed"
	OutcomeInspected   = "inspected"
	OutcomeRejected    = "rejected"
	OutcomeFailed      = "failed"
	OutcomeBypassed    = "bypassed"
	OutcomeUnavailable = "unavailable"
	OutcomeUnsupported = "unsupported_endpoint"
	OutcomeTooLarge    = "payload_too_large"
)

// DefaultCache is shared by every binding of the process; keys include the
// binding's config hash, so a changed binding or plugin never reads old entries.
var DefaultCache = textmatch.NewSegmentCache(textmatch.DefaultCacheBytes)

// Metrics is the only record of a run: counts and ids, never text or placeholders.
type Metrics struct {
	PluginID  string         `json:"plugin_id"`
	BindingID string         `json:"binding_id"`
	Outcome   string         `json:"outcome"`
	Counts    map[string]int `json:"counts,omitempty"`
	Segments  int            `json:"segments"`
	Cached    int            `json:"cached"`
	HookCalls int            `json:"hook_calls,omitempty"`
	LatencyMS int64          `json:"latency_ms"`
}

// Failure is a client-facing refusal: its message never quotes request text.
type Failure struct {
	Status  int
	Code    string
	Message string
}

// Request is what a run works on. Payload is never changed in place.
type Request struct {
	Endpoint string
	Model    string
	Payload  map[string]any
}

// Options are the request-independent parts of a run.
type Options struct {
	// Cache defaults to DefaultCache; dry runs pass their own to see every step.
	Cache *textmatch.SegmentCache
	// NoCache skips the cache altogether.
	NoCache bool
	// Session is reused when set, so one request keeps one mapping.
	Session *textxform.Session
	Breaker *jsplugin.Breaker
}

// Result is what a run decided.
type Result struct {
	// Payload is the transformed copy, set only when Changed.
	Payload map[string]any
	Changed bool
	// Session maps placeholders back; set for token_placeholder runs.
	Session *textxform.Session
	// Failure refuses the request.
	Failure *Failure
	// Bypassed is a fail_open run that failed: send the original request.
	Bypassed bool
	// ErrorKind names a failure for logs; it never carries text.
	ErrorKind string
	Metrics   Metrics
	// Segments, Findings and Applied describe the run for dry runs.
	Segments []Segment
	Findings [][]textmatch.Span
	Applied  [][]textxform.AppliedSpan
}

type runError struct{ kind string }

func (e *runError) Error() string { return "gateway pipeline: " + e.kind }

// ErrorKind names an error for logs without its text, which may come from
// plugin code that saw the request.
func ErrorKind(err error) string {
	var re *runError
	if errors.As(err, &re) {
		return re.kind
	}
	var call *jsplugin.CallError
	if errors.As(err, &call) {
		return call.Kind
	}
	switch {
	case errors.Is(err, jsplugin.ErrPipelineBudgetExceeded):
		return "budget_exceeded"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	return "internal"
}

// Run applies a binding to one request: refuse it when the binding is
// unavailable or the endpoint is not supported (and rejected), extract its
// segments, find what to transform, and transform a copy. A failure follows
// the binding's failure policy: fail_closed refuses with 503, fail_open
// marks the run bypassed.
func Run(ctx context.Context, binding *jsplugin.ResolvedPipelineBinding, req Request, opts Options) (result Result) {
	started := time.Now()
	result.Metrics = Metrics{PluginID: binding.PluginID, BindingID: binding.BindingID.String()}
	defer func() {
		result.Metrics.LatencyMS = time.Since(started).Milliseconds()
	}()
	if binding.Status != jsplugin.PipelineBindingActive {
		result.Metrics.Outcome = OutcomeUnavailable
		result.Failure = &Failure{Status: http.StatusServiceUnavailable, Code: CodePipelineUnavailable, Message: "the gateway pipeline bound to this API key is unavailable"}
		return result
	}
	if !SegmentCodecSupports(req.Endpoint) {
		result.Metrics.Outcome = OutcomeUnsupported
		if binding.UnsupportedEndpoint == jsplugin.PipelineUnsupportedReject {
			result.Failure = &Failure{Status: http.StatusBadRequest, Code: CodeUnsupportedEndpoint, Message: "this endpoint is not supported by the gateway pipeline bound to this API key"}
		}
		return result
	}
	err := run(ctx, binding, req, opts, &result)
	if err == nil {
		return result
	}
	result.ErrorKind = ErrorKind(err)
	result.Metrics.Outcome = OutcomeFailed
	result.Payload, result.Changed, result.Session = nil, false, nil
	if binding.FailClosed() {
		result.Failure = &Failure{Status: http.StatusServiceUnavailable, Code: CodePipelineFailed, Message: "the gateway pipeline bound to this API key failed"}
		return result
	}
	result.Bypassed = true
	result.Metrics.Outcome = OutcomeBypassed
	return result
}

func run(ctx context.Context, binding *jsplugin.ResolvedPipelineBinding, req Request, opts Options, result *Result) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &runError{kind: "panic"}
		}
	}()
	if binding.Matcher == nil || binding.Plugin == nil {
		return &runError{kind: "not_compiled"}
	}
	payload := DeepCopyJSONMap(req.Payload)
	doc, err := ExtractSegments(req.Endpoint, payload, SegmentCodecOptions{
		ScanToolDefinitions: binding.HostOptions.ScanToolDefinitions,
		MaxInspectBytes:     binding.Spec.MaxInspectBytesOrDefault(),
	})
	if errors.Is(err, ErrPayloadTooLarge) {
		result.Metrics.Outcome = OutcomeTooLarge
		result.Failure = &Failure{Status: http.StatusRequestEntityTooLarge, Code: CodePayloadTooLarge, Message: fmt.Sprintf("the text of this request exceeds the gateway pipeline limit of %d bytes", binding.Spec.MaxInspectBytesOrDefault())}
		return nil
	}
	if err != nil {
		return &runError{kind: "extract_failed"}
	}
	result.Metrics.Segments = len(doc.Segments)
	result.Segments = append([]Segment(nil), doc.Segments...)
	findings, reject, err := inspect(ctx, binding, req, doc, opts, &result.Metrics)
	if err != nil {
		return err
	}
	result.Findings = findings
	if reject != nil {
		result.Metrics.Outcome = OutcomeRejected
		result.Failure = &Failure{Status: reject.Status, Code: reject.Code, Message: reject.Message}
		return nil
	}
	if binding.Spec.Transform.ModeOrDefault() != jsplugin.PipelineTransformTokenPlaceholder {
		result.Metrics.Counts = countSelected(binding.Matcher, findings)
		result.Metrics.Outcome = OutcomeInspected
		return nil
	}
	session := opts.Session
	if session == nil {
		session = textxform.NewSession()
	}
	texts := make([]string, len(doc.Segments))
	for i, seg := range doc.Segments {
		texts[i] = seg.Text
	}
	applied := textxform.Apply(session, texts, findings, textxform.Options{
		Types:              binding.Spec.TypeNames(),
		PropagateSameValue: binding.HostOptions.PropagateSameValue,
		PropagateMinLength: binding.Spec.Transform.PropagateMinLengthOrDefault(),
	})
	result.Applied = applied.Applied
	counts := map[string]int{}
	changed := false
	for i, spans := range applied.Applied {
		for _, span := range spans {
			counts[span.Type]++
		}
		changed = changed || applied.Changed[i]
	}
	if len(counts) > 0 {
		result.Metrics.Counts = counts
	}
	session.Reversible = binding.Spec.Transform.IsReversible()
	session.RestoreReasoning = binding.HostOptions.TransformReasoning
	result.Session = session
	if !changed {
		result.Metrics.Outcome = OutcomeInspected
		return nil
	}
	hint := ""
	if binding.Spec.Transform.SystemHint != "" && session.HasReplacements() {
		hint = binding.Spec.Transform.SystemHint
	}
	ApplySegmentReplacements(doc, applied.Texts, hint)
	result.Payload = payload
	result.Changed = true
	result.Metrics.Outcome = OutcomeTransformed
	return nil
}

// inspect returns the final findings of every segment: from the cache when it
// knows the segment, otherwise from the native matcher and the plugin's
// inspectSegments hook, which are then cached.
func inspect(ctx context.Context, binding *jsplugin.ResolvedPipelineBinding, req Request, doc *SegmentDocument, opts Options, metrics *Metrics) ([][]textmatch.Span, *jsplugin.InspectSegmentsReject, error) {
	cache := opts.Cache
	if cache == nil && !opts.NoCache {
		cache = DefaultCache
	}
	findings := make([][]textmatch.Span, len(doc.Segments))
	keys := make([]string, len(doc.Segments))
	var pending []int
	for i, seg := range doc.Segments {
		if cache != nil {
			keys[i] = textmatch.CacheKey(seg.Text, seg.Role, seg.Field, binding.ConfigHash)
			if cached, ok := cache.Get(keys[i]); ok {
				findings[i] = spansFromCache(cached)
				metrics.Cached++
				continue
			}
		}
		findings[i] = binding.Matcher.Match(seg.Text)
		pending = append(pending, i)
	}
	if len(pending) == 0 {
		return findings, nil, nil
	}
	if binding.Plugin.HasHook(jsplugin.HookInspectSegments) {
		inspection := jsplugin.PipelineInspection{
			Endpoint:  req.Endpoint,
			Model:     req.Model,
			BindingID: binding.BindingID.String(),
			Params:    binding.Params,
			Segments:  make([]jsplugin.PipelineSegment, len(pending)),
			Findings:  make([][]textmatch.Span, len(pending)),
			Keep:      binding.Matcher.Keep,
			Budget:    time.Duration(binding.Spec.TotalTimeoutMsOrDefault()) * time.Millisecond,
			Breaker:   opts.Breaker,
		}
		for n, index := range pending {
			seg := doc.Segments[index]
			inspection.Segments[n] = jsplugin.PipelineSegment{ID: seg.ID, Role: seg.Role, Field: seg.Field, Text: seg.Text}
			inspection.Findings[n] = findings[index]
		}
		reviewed, err := binding.Plugin.InspectPipelineSegments(ctx, inspection)
		metrics.HookCalls = reviewed.Calls
		if err != nil {
			return nil, nil, err
		}
		if reviewed.Reject != nil {
			return nil, reviewed.Reject, nil
		}
		for n, index := range pending {
			findings[index] = reviewed.Findings[n]
		}
	}
	if cache != nil {
		for _, index := range pending {
			cache.Put(keys[index], spansToCache(findings[index]))
		}
	}
	return findings, nil, nil
}

func countSelected(matcher *textmatch.Matcher, findings [][]textmatch.Span) map[string]int {
	counts := map[string]int{}
	for _, spans := range findings {
		for _, span := range textxform.SelectSpans(spans, matcher.TypeRank, nil) {
			counts[span.Type]++
		}
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}

func spansFromCache(cached []textmatch.CachedSpan) []textmatch.Span {
	out := make([]textmatch.Span, len(cached))
	for i, span := range cached {
		out[i] = textmatch.Span{Start: span.Start, End: span.End, Type: span.Type, Source: span.Source, Score: span.Score}
	}
	return out
}

func spansToCache(spans []textmatch.Span) []textmatch.CachedSpan {
	out := make([]textmatch.CachedSpan, len(spans))
	for i, span := range spans {
		out[i] = textmatch.CachedSpan{Start: span.Start, End: span.End, Type: span.Type, Source: span.Source, Score: span.Score}
	}
	return out
}
