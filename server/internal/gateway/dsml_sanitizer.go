package gateway

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

const (
	sseRepairDSMLTagStripped = "dsml_tag_stripped"
	dsmlMaxPendingTagBytes   = 256
)

// dsmlTagPattern matches leaked DeepSeek Markup Language (DSML) tags such as
// </｜DSML｜invoke>, <｜DSML｜parameter name="...">, </ | DSML | ...>, or
// V4.1 spaced variants like <｜DSML｜ calls>.
var dsmlTagPattern = regexp.MustCompile(`(?i)</?\s*[|\x{FF5C}]\s*DSML(?:\s*[|\x{FF5C}])?[^>]*>`)

// stripDSMLTags removes any complete DSML markup tags from text.
// It returns the sanitized text and whether any tag was removed.
func stripDSMLTags(text string) (string, bool) {
	if text == "" || !containsDSMLHint(text) {
		return text, false
	}
	cleaned := dsmlTagPattern.ReplaceAllString(text, "")
	return cleaned, cleaned != text
}

func containsDSMLHint(text string) bool {
	return strings.IndexByte(text, '<') >= 0 &&
		(strings.Contains(text, "DSML") || strings.Contains(text, "dsml"))
}

// isPotentialDSMLTagPrefix reports whether tail (which must start with '<' and
// contain no '>') could still grow into a complete DSML tag in subsequent chunks.
// Normal text such as "a < b" or "<div>" fails immediately at the pipe check so
// it is never buffered.
func isPotentialDSMLTagPrefix(tail string) bool {
	if len(tail) == 0 || len(tail) > dsmlMaxPendingTagBytes || tail[0] != '<' {
		return false
	}
	rest := tail[1:]
	if strings.HasPrefix(rest, "/") {
		rest = rest[1:]
	}
	rest = strings.TrimLeft(rest, " \t\r\n")
	if rest == "" {
		return true
	}
	switch {
	case strings.HasPrefix(rest, "|"):
		rest = rest[1:]
	case strings.HasPrefix(rest, "\uFF5C"):
		rest = rest[len("\uFF5C"):]
	default:
		return false
	}
	rest = strings.TrimLeft(rest, " \t\r\n")
	if rest == "" {
		return true
	}
	upper := strings.ToUpper(rest)
	if len(upper) <= len("DSML") {
		return strings.HasPrefix("DSML", upper)
	}
	return strings.HasPrefix(upper, "DSML")
}

// dsmlStreamTextFilter strips complete and chunk-split DSML tags from a stream
// of text deltas while holding back only plausible DSML tag prefixes.
type dsmlStreamTextFilter struct {
	pending string
}

func (f *dsmlStreamTextFilter) HasPending() bool {
	return f != nil && f.pending != ""
}

// Feed processes the next delta chunk, returning the safe text to emit and
// whether any DSML tag was stripped.
func (f *dsmlStreamTextFilter) Feed(chunk string) (string, bool) {
	if f == nil {
		return stripDSMLTags(chunk)
	}
	if chunk == "" && f.pending == "" {
		return "", false
	}
	combined := f.pending + chunk
	f.pending = ""

	cleaned, stripped := stripDSMLTags(combined)
	if idx := strings.LastIndexByte(cleaned, '<'); idx >= 0 && strings.IndexByte(cleaned[idx:], '>') < 0 {
		tail := cleaned[idx:]
		if isPotentialDSMLTagPrefix(tail) {
			f.pending = tail
			cleaned = cleaned[:idx]
		}
	}
	return cleaned, stripped
}

// Flush drains any remaining buffered tail at end-of-stream. Unclosed DSML
// fragments that already contain "DSML" after the pipe are discarded; benign
// prefixes like "<" or "</" are emitted as normal text.
func (f *dsmlStreamTextFilter) Flush() (string, bool) {
	if f == nil || f.pending == "" {
		return "", false
	}
	tail := f.pending
	f.pending = ""
	if cleaned, stripped := stripDSMLTags(tail); stripped {
		return cleaned, true
	}
	if containsDSMLHint(tail) && isPotentialDSMLTagPrefix(tail) {
		return "", true
	}
	return tail, false
}

// dsmlCanonicalStreamFilter strips DSML tags from text and reasoning deltas in
// canonical cross-protocol streams, flushing any pending tail before terminal
// events.
type dsmlCanonicalStreamFilter struct {
	text      dsmlStreamTextFilter
	reasoning dsmlStreamTextFilter
}

func newDSMLCanonicalStreamFilter() *dsmlCanonicalStreamFilter {
	return &dsmlCanonicalStreamFilter{}
}

func (f *dsmlCanonicalStreamFilter) Apply(events []canonicalStreamEvent) ([]canonicalStreamEvent, bool) {
	if f == nil || len(events) == 0 {
		return events, false
	}
	out := make([]canonicalStreamEvent, 0, len(events))
	anyStripped := false
	for _, event := range events {
		switch event.Type {
		case canonicalStreamEventTextDelta:
			cleaned, stripped := f.text.Feed(event.Delta)
			if stripped {
				anyStripped = true
			}
			if cleaned == "" && event.Delta != "" {
				continue
			}
			event.Delta = cleaned
			out = append(out, event)
		case canonicalStreamEventReasoningDelta:
			cleaned, stripped := f.reasoning.Feed(event.Delta)
			if stripped {
				anyStripped = true
			}
			if cleaned == "" && event.Delta != "" {
				continue
			}
			event.Delta = cleaned
			out = append(out, event)
		case canonicalStreamEventCompleted, canonicalStreamEventIncomplete:
			flushed, stripped := f.Flush()
			if stripped {
				anyStripped = true
			}
			out = append(out, flushed...)
			out = append(out, event)
		default:
			out = append(out, event)
		}
	}
	return out, anyStripped
}

func (f *dsmlCanonicalStreamFilter) Flush() ([]canonicalStreamEvent, bool) {
	if f == nil {
		return nil, false
	}
	var out []canonicalStreamEvent
	anyStripped := false
	if text, stripped := f.text.Flush(); text != "" || stripped {
		if stripped {
			anyStripped = true
		}
		if text != "" {
			out = append(out, canonicalStreamEvent{
				Type:  canonicalStreamEventTextDelta,
				Delta: text,
			})
		}
	}
	if reasoning, stripped := f.reasoning.Flush(); reasoning != "" || stripped {
		if stripped {
			anyStripped = true
		}
		if reasoning != "" {
			out = append(out, canonicalStreamEvent{
				Type:  canonicalStreamEventReasoningDelta,
				Delta: reasoning,
			})
		}
	}
	return out, anyStripped
}

// sanitizeDSMLInRequestPayload strips leaked DSML tags from assistant-role
// historical messages in outgoing upstream payloads so multi-turn requests do
// not fail with 400 due to polluted assistant content. User/system messages are
// left untouched.
func sanitizeDSMLInRequestPayload(payload map[string]any) {
	if payload == nil {
		return
	}
	if messages, ok := payload["messages"].([]any); ok {
		for _, rawMsg := range messages {
			msg, ok := rawMsg.(map[string]any)
			if !ok {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(anyString(msg["role"])), "assistant") {
				continue
			}
			sanitizeDSMLContentField(msg, "content")
			sanitizeDSMLStringField(msg, "reasoning_content")
			sanitizeDSMLStringField(msg, "thinking")
		}
	}
	if input, ok := payload["input"].([]any); ok {
		for _, rawItem := range input {
			item, ok := rawItem.(map[string]any)
			if !ok {
				continue
			}
			role := strings.TrimSpace(anyString(item["role"]))
			itemType := strings.TrimSpace(anyString(item["type"]))
			if strings.EqualFold(role, "assistant") {
				sanitizeDSMLContentField(item, "content")
			} else if itemType == "reasoning" {
				sanitizeDSMLContentField(item, "content")
				sanitizeDSMLContentField(item, "summary")
			}
		}
	}
}

func sanitizeDSMLStringField(m map[string]any, key string) bool {
	raw, ok := m[key].(string)
	if !ok {
		return false
	}
	cleaned, stripped := stripDSMLTags(raw)
	if stripped {
		m[key] = cleaned
	}
	return stripped
}

func sanitizeDSMLContentField(m map[string]any, key string) bool {
	if sanitizeDSMLStringField(m, key) {
		return true
	}
	parts, ok := m[key].([]any)
	if !ok {
		return false
	}
	changed := false
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			continue
		}
		if sanitizeDSMLStringField(part, "text") {
			changed = true
		}
		if sanitizeDSMLStringField(part, "thinking") {
			changed = true
		}
	}
	return changed
}

// sanitizeDSMLInBufferedResponse strips leaked DSML tags from non-streaming
// JSON responses across OpenAI Chat, Anthropic Messages, and OpenAI Responses
// formats.
func sanitizeDSMLInBufferedResponse(body []byte, contentType string) []byte {
	if len(body) == 0 || !strings.Contains(strings.ToLower(contentType), "json") {
		return body
	}
	if !bytes.Contains(body, []byte("DSML")) && !bytes.Contains(body, []byte("dsml")) {
		return body
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return body
	}
	if !sanitizeDSMLResponseMap(payload) {
		return body
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return encoded
}

func sanitizeDSMLResponseMap(payload map[string]any) bool {
	changed := false
	// OpenAI Chat Completions: choices[].message.{content,reasoning_content,thinking}
	if choices, ok := payload["choices"].([]any); ok {
		for _, rawChoice := range choices {
			choice, ok := rawChoice.(map[string]any)
			if !ok {
				continue
			}
			if msg, ok := choice["message"].(map[string]any); ok {
				if sanitizeDSMLContentField(msg, "content") {
					changed = true
				}
				if sanitizeDSMLStringField(msg, "reasoning_content") {
					changed = true
				}
				if sanitizeDSMLStringField(msg, "thinking") {
					changed = true
				}
			}
		}
	}
	// Anthropic Messages: content[].{text,thinking}
	if sanitizeDSMLContentField(payload, "content") {
		changed = true
	}
	// OpenAI Responses: output[].{content,summary}
	if output, ok := payload["output"].([]any); ok {
		for _, rawItem := range output {
			item, ok := rawItem.(map[string]any)
			if !ok {
				continue
			}
			if sanitizeDSMLContentField(item, "content") {
				changed = true
			}
			if sanitizeDSMLContentField(item, "summary") {
				changed = true
			}
		}
	}
	return changed
}

// sanitizeSSEResponseDeltaDSML applies stateful DSML filtering to text/reasoning
// deltas in Chat, Anthropic, or Responses SSE event payloads.
func sanitizeSSEResponseDeltaDSML(payload map[string]any, textFilter, reasoningFilter *dsmlStreamTextFilter) (changed bool, stripped bool) {
	if payload == nil {
		return false, false
	}
	// OpenAI Chat SSE: choices[].delta.{content,reasoning_content,thinking}
	if choices, ok := payload["choices"].([]any); ok {
		for _, rawChoice := range choices {
			choice, ok := rawChoice.(map[string]any)
			if !ok {
				continue
			}
			delta, ok := choice["delta"].(map[string]any)
			if !ok {
				continue
			}
			if content, ok := delta["content"].(string); ok {
				cleaned, tagStripped := textFilter.Feed(content)
				if tagStripped {
					stripped = true
				}
				if cleaned != content || tagStripped {
					delta["content"] = cleaned
					changed = true
				}
			}
			for _, key := range []string{"reasoning_content", "thinking"} {
				if val, ok := delta[key].(string); ok {
					cleaned, tagStripped := reasoningFilter.Feed(val)
					if tagStripped {
						stripped = true
					}
					if cleaned != val || tagStripped {
						delta[key] = cleaned
						changed = true
					}
				}
			}
		}
	}
	// Anthropic Messages SSE: content_block_delta -> delta.{text,thinking}
	eventType := strings.TrimSpace(anyString(payload["type"]))
	if eventType == "content_block_delta" {
		if delta, ok := payload["delta"].(map[string]any); ok {
			switch strings.TrimSpace(anyString(delta["type"])) {
			case "text_delta":
				if text, ok := delta["text"].(string); ok {
					cleaned, tagStripped := textFilter.Feed(text)
					if tagStripped {
						stripped = true
					}
					if cleaned != text || tagStripped {
						delta["text"] = cleaned
						changed = true
					}
				}
			case "thinking_delta":
				if thinking, ok := delta["thinking"].(string); ok {
					cleaned, tagStripped := reasoningFilter.Feed(thinking)
					if tagStripped {
						stripped = true
					}
					if cleaned != thinking || tagStripped {
						delta["thinking"] = cleaned
						changed = true
					}
				}
			}
		}
	}
	// OpenAI Responses SSE: response.output_text.delta / response.reasoning_text.delta / done / completed
	switch eventType {
	case "response.output_text.delta":
		if delta, ok := payload["delta"].(string); ok {
			cleaned, tagStripped := textFilter.Feed(delta)
			if tagStripped {
				stripped = true
			}
			if cleaned != delta || tagStripped {
				payload["delta"] = cleaned
				changed = true
			}
		}
	case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		if delta, ok := payload["delta"].(string); ok {
			cleaned, tagStripped := reasoningFilter.Feed(delta)
			if tagStripped {
				stripped = true
			}
			if cleaned != delta || tagStripped {
				payload["delta"] = cleaned
				changed = true
			}
		}
	case "response.output_text.done":
		if sanitizeDSMLStringField(payload, "text") {
			changed = true
			stripped = true
		}
	case "response.completed", "response.incomplete":
		if resp, ok := payload["response"].(map[string]any); ok {
			if sanitizeDSMLResponseMap(resp) {
				changed = true
				stripped = true
			}
		}
	}
	return changed, stripped
}

func sanitizeSSEBlockDSML(block []byte, textFilter, reasoningFilter *dsmlStreamTextFilter) ([]byte, bool) {
	if len(block) == 0 {
		return block, false
	}
	if bytes.IndexByte(block, '<') < 0 && !textFilter.HasPending() && !reasoningFilter.HasPending() {
		return block, false
	}
	eventName, data, commentOnly, done, _ := parseSSEEventBlock(block)
	if commentOnly || done || len(data) == 0 {
		return block, false
	}
	value, err := decodeJSONUseNumber(data)
	if err != nil {
		return block, false
	}
	payload, ok := value.(map[string]any)
	if !ok {
		return block, false
	}
	changed, stripped := sanitizeSSEResponseDeltaDSML(payload, textFilter, reasoningFilter)
	if !changed {
		return block, false
	}
	out, encErr := formatRepairedSSE(eventName, payload)
	if encErr != nil {
		return block, false
	}
	return out, stripped
}
