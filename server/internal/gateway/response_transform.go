package gateway

import (
	"context"
	"strconv"
	"strings"

	"xlyra/server/internal/textpipeline"
	"xlyra/server/internal/textxform"
)

// streamTextTransformer rewrites a stream of text deltas, holding back only
// a tail that may still grow into something it must rewrite. DSML stripping
// and placeholder restoring share it.
type streamTextTransformer interface {
	Feed(chunk string) (emit string, changed bool)
	Flush() (emit string, changed bool)
	HasPending() bool
}

var (
	_ streamTextTransformer = (*dsmlStreamTextFilter)(nil)
	_ streamTextTransformer = (*textpipeline.TokenRestoreFilter)(nil)
)

// restoreTokensInBufferedResponse restores the placeholders of the request's
// session in a whole response body. It runs after usage was read from the
// upstream body, so billing counts what the upstream saw.
func restoreTokensInBufferedResponse(ctx context.Context, body []byte, contentType string) []byte {
	return textpipeline.RestoreBody(textxform.RestoringSessionFromContext(ctx), body, contentType)
}

// newResponseStreamTransformState restores the request's placeholders in an
// SSE stream; nil when there is nothing to restore.
func newResponseStreamTransformState(ctx context.Context) *textpipeline.StreamRestorer {
	return textpipeline.NewStreamRestorer(textxform.RestoringSessionFromContext(ctx))
}

// canonicalTokenRestoreFilter restores placeholders in canonical stream
// events before an encoder turns them into the downstream protocol. Each tool
// call keeps its own channel; reasoning is restored only when the binding
// asks for it.
type canonicalTokenRestoreFilter struct {
	session   *textxform.Session
	reasoning bool
	text      textpipeline.TokenRestoreFilter
	thinking  textpipeline.TokenRestoreFilter
	tools     map[string]*textpipeline.TokenRestoreFilter
	toolOrder []string
	lastTool  map[string]canonicalStreamEvent
	lastText  canonicalStreamEvent
	lastThink canonicalStreamEvent
}

func newCanonicalTokenRestoreFilter(ctx context.Context) *canonicalTokenRestoreFilter {
	session := textxform.RestoringSessionFromContext(ctx)
	if session == nil {
		return nil
	}
	return &canonicalTokenRestoreFilter{
		session: session, reasoning: session.RestoreReasoning,
		text:     textpipeline.TokenRestoreFilter{Session: session},
		thinking: textpipeline.TokenRestoreFilter{Session: session},
		tools:    map[string]*textpipeline.TokenRestoreFilter{},
		lastTool: map[string]canonicalStreamEvent{},
	}
}

// toolKey keys a tool call by call id, which the encoders key on, or by index
// when a decoder leaves the id out.
func toolKey(event canonicalStreamEvent) string {
	if id := strings.TrimSpace(event.ToolCallID); id != "" {
		return "id:" + id
	}
	return "index:" + strconv.Itoa(event.ToolCallIndex)
}

func (f *canonicalTokenRestoreFilter) tool(key string) *textpipeline.TokenRestoreFilter {
	filter := f.tools[key]
	if filter == nil {
		filter = &textpipeline.TokenRestoreFilter{Session: f.session, JSONEscape: true}
		f.tools[key] = filter
		f.toolOrder = append(f.toolOrder, key)
	}
	return filter
}

// Apply restores text, tool argument and reasoning deltas, and drains every
// channel before a terminal event. A failure keeps the events as they were.
func (f *canonicalTokenRestoreFilter) Apply(events []canonicalStreamEvent) (out []canonicalStreamEvent) {
	if f == nil || len(events) == 0 {
		return events
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			out = events
		}
	}()
	out = make([]canonicalStreamEvent, 0, len(events))
	for _, event := range events {
		switch event.Type {
		case canonicalStreamEventTextDelta:
			emit, _ := f.text.Feed(event.Delta)
			f.lastText = event
			if emit == "" && event.Delta != "" {
				continue
			}
			event.Delta = emit
		case canonicalStreamEventReasoningDelta:
			if f.reasoning {
				emit, _ := f.thinking.Feed(event.Delta)
				f.lastThink = event
				if emit == "" && event.Delta != "" {
					continue
				}
				event.Delta = emit
			}
		case canonicalStreamEventToolCallDelta:
			key := toolKey(event)
			if event.ToolCallArgumentsDelta != "" || f.tool(key).HasPending() {
				emit, _ := f.tool(key).Feed(event.ToolCallArgumentsDelta)
				f.lastTool[key] = event
				event.ToolCallArgumentsDelta = emit
			}
		case canonicalStreamEventCompleted, canonicalStreamEventIncomplete:
			out = append(out, f.Flush()...)
		case canonicalStreamEventError:
			event.ErrorMessage, _ = textpipeline.RestoreTokens(event.ErrorMessage, f.session, false)
		case canonicalStreamEventAdvanced:
			if event.AdvancedPayload != nil {
				textpipeline.RestoreJSONValue(event.AdvancedPayload, f.session, f.reasoning)
			}
		}
		out = append(out, event)
	}
	return out
}

// Flush drains every channel into delta events.
func (f *canonicalTokenRestoreFilter) Flush() []canonicalStreamEvent {
	if f == nil {
		return nil
	}
	var out []canonicalStreamEvent
	if text, _ := f.text.Flush(); text != "" {
		event := f.lastText
		event.Type = canonicalStreamEventTextDelta
		event.Delta = text
		out = append(out, event)
	}
	if text, _ := f.thinking.Flush(); text != "" {
		event := f.lastThink
		event.Type = canonicalStreamEventReasoningDelta
		event.Delta = text
		out = append(out, event)
	}
	for _, key := range f.toolOrder {
		if text, _ := f.tools[key].Flush(); text != "" {
			event := f.lastTool[key]
			event.Type = canonicalStreamEventToolCallDelta
			event.ToolCallArgumentsDelta = text
			out = append(out, event)
		}
	}
	return out
}
