package textpipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"

	"xlyra/server/internal/textmatch"
	"xlyra/server/internal/textxform"
)

// restoreFailures counts restore failures. A failure keeps the placeholders,
// so a response is never cut short or restored halfway.
var restoreFailures atomic.Int64

// RestoreFailures reports how many restores failed since the process started.
func RestoreFailures() int64 { return restoreFailures.Load() }

func recordRestoreFailure(recovered any) {
	restoreFailures.Add(1)
	slog.Default().Warn("gateway pipeline restore failed; placeholders kept", "scope", "gateway", "kind", fmt.Sprintf("%T", recovered))
}

// IsPotentialTokenPrefix reports whether tail, which starts with '[', can
// still grow into a [[TYPE_N]] placeholder. Anything that breaks the form,
// such as "[0]" or "[[foo", is released at once.
func IsPotentialTokenPrefix(tail string) bool {
	if tail == "" || tail[0] != '[' || len(tail) > textxform.MaxPlaceholderLen {
		return false
	}
	if len(tail) == 1 {
		return true
	}
	if tail[1] != '[' {
		return false
	}
	rest := tail[2:]
	closing := false
	if strings.HasSuffix(rest, "]") {
		rest = rest[:len(rest)-1]
		closing = true
	}
	if strings.ContainsRune(rest, ']') {
		return false
	}
	if rest == "" {
		return !closing
	}
	if rest[0] < 'A' || rest[0] > 'Z' {
		return false
	}
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	if !closing && len(rest) <= 24 {
		return true
	}
	for i := len(rest) - 1; i >= 1; i-- {
		if rest[i] != '_' || i > 24 {
			continue
		}
		number := rest[i+1:]
		if number == "" {
			if !closing {
				return true
			}
			continue
		}
		if number[0] < '1' || number[0] > '9' {
			continue
		}
		digits := true
		for j := 1; j < len(number); j++ {
			if number[j] < '0' || number[j] > '9' {
				digits = false
				break
			}
		}
		if digits {
			return true
		}
	}
	return false
}

// potentialTokenStart returns where a tail that may grow into a placeholder
// begins, or len(text) when there is none.
func potentialTokenStart(text string) int {
	from := len(text) - textxform.MaxPlaceholderLen
	if from < 0 {
		from = 0
	}
	for i := from; i < len(text); i++ {
		if text[i] == '[' && IsPotentialTokenPrefix(text[i:]) {
			return i
		}
	}
	return len(text)
}

// RestoreTokens replaces the placeholders of session in text with their
// originals, JSON-escaped when the text sits inside a JSON string.
func RestoreTokens(text string, session *textxform.Session, jsonEscape bool) (string, bool) {
	if session == nil || !strings.Contains(text, "[[") {
		return text, false
	}
	changed := false
	out := textxform.PlaceholderPattern.ReplaceAllStringFunc(text, func(token string) string {
		original, ok := session.Lookup(token)
		if !ok {
			return token
		}
		changed = true
		if jsonEscape {
			return jsonStringEscape(original)
		}
		return original
	})
	return out, changed
}

// jsonStringEscape escapes text for the inside of a JSON string literal.
func jsonStringEscape(text string) string {
	var b strings.Builder
	b.Grow(len(text) + 8)
	for _, r := range text {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// TokenRestoreFilter restores placeholders in one channel of deltas. A
// placeholder split across chunks is held back, at most MaxPlaceholderLen
// bytes, until it completes or proves not to be one.
type TokenRestoreFilter struct {
	Session    *textxform.Session
	JSONEscape bool
	pending    string
}

func (f *TokenRestoreFilter) HasPending() bool {
	return f != nil && f.pending != ""
}

func (f *TokenRestoreFilter) Feed(chunk string) (string, bool) {
	if f.pending == "" && !strings.Contains(chunk, "[") {
		return chunk, false
	}
	combined := f.pending + chunk
	cut := potentialTokenStart(combined)
	f.pending = combined[cut:]
	out, _ := RestoreTokens(combined[:cut], f.Session, f.JSONEscape)
	return out, out != chunk
}

func (f *TokenRestoreFilter) Flush() (string, bool) {
	if f.pending == "" {
		return "", false
	}
	tail := f.pending
	f.pending = ""
	out, _ := RestoreTokens(tail, f.Session, f.JSONEscape)
	return out, true
}

// ---- buffered responses ----

// restoreReasoningKeys hold reasoning text, restored only when the binding asks.
var restoreReasoningKeys = map[string]bool{"reasoning_content": true, "reasoning": true, "thinking": true, "reasoning_details": true}

// restoreOpaqueKeys hold signed or encrypted data and are never touched.
var restoreOpaqueKeys = map[string]bool{"signature": true, "encrypted_content": true, "thoughtSignature": true, "thought_signature": true}

// restoreJSONTextKeys hold JSON text, whose restored values must be escaped.
var restoreJSONTextKeys = map[string]bool{"arguments": true, "partial_json": true}

func isReasoningObject(object map[string]any) bool {
	switch stringField(object["type"]) {
	case "thinking", "redacted_thinking", "reasoning", "reasoning_text", "summary_text":
		return true
	}
	thought, _ := object["thought"].(bool)
	return thought
}

// RestoreJSONValue restores placeholders in the string leaves of a decoded
// JSON value in place. Reasoning is skipped unless reasoning is set.
func RestoreJSONValue(value any, session *textxform.Session, reasoning bool) bool {
	changed := false
	switch typed := value.(type) {
	case map[string]any:
		if !reasoning && isReasoningObject(typed) {
			return false
		}
		for key, item := range typed {
			if restoreOpaqueKeys[key] || (!reasoning && restoreReasoningKeys[key]) {
				continue
			}
			if text, ok := item.(string); ok {
				if restored, did := RestoreTokens(text, session, restoreJSONTextKeys[key]); did {
					typed[key] = restored
					changed = true
				}
				continue
			}
			if RestoreJSONValue(item, session, reasoning) {
				changed = true
			}
		}
	case []any:
		for i, item := range typed {
			if text, ok := item.(string); ok {
				if restored, did := RestoreTokens(text, session, false); did {
					typed[i] = restored
					changed = true
				}
				continue
			}
			if RestoreJSONValue(item, session, reasoning) {
				changed = true
			}
		}
	}
	return changed
}

// RestoreBody restores the placeholders of session in a whole response body,
// JSON or plain text. Audio and other binary bodies, and bodies without a
// placeholder, are returned unchanged.
func RestoreBody(session *textxform.Session, body []byte, contentType string) (out []byte) {
	if session == nil || len(body) == 0 || !bytes.Contains(body, []byte("[[")) {
		return body
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			recordRestoreFailure(recovered)
			out = body
		}
	}()
	lowered := strings.ToLower(contentType)
	if strings.Contains(lowered, "json") || json.Valid(body) {
		value, err := decodeJSON(body)
		if err == nil {
			if !RestoreJSONValue(value, session, session.RestoreReasoning) {
				return body
			}
			encoded, err := MarshalJSON(value)
			if err != nil {
				return body
			}
			return []byte(encoded)
		}
	}
	if lowered == "" || strings.HasPrefix(lowered, "text/") {
		restored, _ := RestoreTokens(string(body), session, false)
		return []byte(restored)
	}
	return body
}

// ---- SSE streams ----

// StreamRestorer restores placeholders in an SSE stream of any
// protocol. Each text channel (a choice, a content block, an output item, a
// tool call) has its own filter, so parallel deltas never mix; reasoning
// channels exist only when the binding restores reasoning.
type StreamRestorer struct {
	session   *textxform.Session
	reasoning bool
	channels  map[string]*restoreChannel
	order     []string
}

type restoreChannel struct {
	filter TokenRestoreFilter
	// flushBlock builds an SSE event that carries held-back text, from the
	// last event of the channel.
	flushBlock func(text string) ([]byte, error)
}

// NewStreamRestorer restores the placeholders of session in an SSE stream; a
// nil session gives a nil restorer, which passes events through.
func NewStreamRestorer(session *textxform.Session) *StreamRestorer {
	if session == nil {
		return nil
	}
	return &StreamRestorer{session: session, reasoning: session.RestoreReasoning, channels: map[string]*restoreChannel{}}
}

func (s *StreamRestorer) channel(key string, jsonEscape bool) *restoreChannel {
	ch := s.channels[key]
	if ch == nil {
		ch = &restoreChannel{filter: TokenRestoreFilter{Session: s.session, JSONEscape: jsonEscape}}
		s.channels[key] = ch
		s.order = append(s.order, key)
	}
	return ch
}

// feed passes one delta through its channel. final drains the channel into
// this delta, for the last event of the channel.
func (s *StreamRestorer) feed(key string, jsonEscape bool, text string, final bool, flushBlock func(string) ([]byte, error)) string {
	ch := s.channel(key, jsonEscape)
	out, _ := ch.filter.Feed(text)
	if final {
		tail, _ := ch.filter.Flush()
		return out + tail
	}
	if ch.filter.HasPending() {
		ch.flushBlock = flushBlock
	}
	return out
}

// flushWhere emits the held-back text of the channels key selects.
func (s *StreamRestorer) flushWhere(match func(string) bool) [][]byte {
	var out [][]byte
	for _, key := range s.order {
		ch := s.channels[key]
		if ch == nil || !ch.filter.HasPending() || !match(key) {
			continue
		}
		text, _ := ch.filter.Flush()
		if text == "" || ch.flushBlock == nil {
			continue
		}
		block, err := ch.flushBlock(text)
		if err != nil {
			continue
		}
		out = append(out, block)
	}
	return out
}

// Flush emits the held-back text of every channel, for the end of a stream.
func (s *StreamRestorer) Flush() [][]byte {
	if s == nil {
		return nil
	}
	return s.flushWhere(func(string) bool { return true })
}

func (s *StreamRestorer) flushPrefix(prefix string) [][]byte {
	return s.flushWhere(func(key string) bool { return strings.HasPrefix(key, prefix) })
}

// Apply restores one SSE event. It returns the events to write, in order:
// held-back text of channels that end here, then the event itself.
func (s *StreamRestorer) Apply(block []byte) (out [][]byte) {
	if s == nil || len(block) == 0 {
		return [][]byte{block}
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			recordRestoreFailure(recovered)
			out = [][]byte{block}
		}
	}()
	eventName, data, commentOnly, done := parseSSEEvent(block)
	if commentOnly {
		return [][]byte{block}
	}
	if done {
		return append(s.Flush(), block)
	}
	if len(data) == 0 {
		return [][]byte{block}
	}
	value, err := decodeJSON(data)
	if err != nil {
		return [][]byte{block}
	}
	payload, ok := value.(map[string]any)
	if !ok {
		return [][]byte{block}
	}
	before, changed := s.restorePayload(eventName, payload)
	if !changed {
		return append(before, block)
	}
	encoded, err := formatSSEEvent(eventName, payload)
	if err != nil {
		return append(before, block)
	}
	return append(before, encoded)
}

func (s *StreamRestorer) restorePayload(eventName string, payload map[string]any) ([][]byte, bool) {
	if _, ok := payload["choices"].([]any); ok {
		return s.restoreChatChunk(payload)
	}
	eventType := stringField(payload["type"])
	switch {
	case strings.HasPrefix(eventType, "response."):
		return s.restoreResponsesEvent(eventName, eventType, payload)
	case eventType == "content_block_delta" || eventType == "content_block_start" || eventType == "content_block_stop" ||
		eventType == "message_start" || eventType == "message_delta" || eventType == "message_stop":
		return s.restoreAnthropicEvent(eventName, eventType, payload)
	}
	if root := geminiStreamRoot(payload); root != nil {
		return s.restoreGeminiChunk(payload, root)
	}
	return nil, RestoreJSONValue(payload, s.session, s.reasoning)
}

func sseFlushBuilder(eventName string, build func(text string) map[string]any) func(string) ([]byte, error) {
	return func(text string) ([]byte, error) {
		return formatSSEEvent(eventName, build(text))
	}
}

// ---- OpenAI Chat ----

func (s *StreamRestorer) restoreChatChunk(payload map[string]any) ([][]byte, bool) {
	changed := false
	var before [][]byte
	choices, _ := payload["choices"].([]any)
	envelope := func(choice map[string]any) map[string]any {
		out := map[string]any{"choices": []any{choice}}
		for _, key := range []string{"id", "object", "created", "model", "system_fingerprint"} {
			if value, ok := payload[key]; ok {
				out[key] = value
			}
		}
		return out
	}
	for _, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		if !ok {
			continue
		}
		idx := indexKey(choice["index"])
		final := choice["finish_reason"] != nil
		prefix := "chat:" + idx + ":"
		seen := map[string]bool{}
		delta, _ := choice["delta"].(map[string]any)
		if delta != nil {
			if text, ok := delta["content"].(string); ok {
				key := prefix + "text"
				seen[key] = true
				restored := s.feed(key, false, text, final, sseFlushBuilder("", func(t string) map[string]any {
					return envelope(map[string]any{"index": choice["index"], "delta": map[string]any{"content": t}})
				}))
				if restored != text {
					delta["content"] = restored
					changed = true
				}
			}
			if s.reasoning {
				for _, field := range []string{"reasoning_content", "reasoning", "thinking"} {
					text, ok := delta[field].(string)
					if !ok {
						continue
					}
					key := prefix + "reasoning:" + field
					seen[key] = true
					name := field
					restored := s.feed(key, false, text, final, sseFlushBuilder("", func(t string) map[string]any {
						return envelope(map[string]any{"index": choice["index"], "delta": map[string]any{name: t}})
					}))
					if restored != text {
						delta[field] = restored
						changed = true
					}
				}
			}
			calls, _ := delta["tool_calls"].([]any)
			for _, rawCall := range calls {
				call, ok := rawCall.(map[string]any)
				if !ok {
					continue
				}
				function, _ := call["function"].(map[string]any)
				if function == nil {
					continue
				}
				text, ok := function["arguments"].(string)
				if !ok {
					continue
				}
				callIndex := call["index"]
				key := prefix + "tool:" + indexKey(callIndex)
				seen[key] = true
				restored := s.feed(key, true, text, final, sseFlushBuilder("", func(t string) map[string]any {
					return envelope(map[string]any{"index": choice["index"], "delta": map[string]any{
						"tool_calls": []any{map[string]any{"index": callIndex, "function": map[string]any{"arguments": t}}},
					}})
				}))
				if restored != text {
					function["arguments"] = restored
					changed = true
				}
			}
		}
		if message, ok := choice["message"].(map[string]any); ok && RestoreJSONValue(message, s.session, s.reasoning) {
			changed = true
		}
		if final {
			before = append(before, s.flushWhere(func(key string) bool { return strings.HasPrefix(key, prefix) && !seen[key] })...)
		}
	}
	return before, changed
}

// ---- Anthropic Messages ----

func (s *StreamRestorer) restoreAnthropicEvent(eventName, eventType string, payload map[string]any) ([][]byte, bool) {
	idx := indexKey(payload["index"])
	key := "anthropic:" + idx
	switch eventType {
	case "content_block_delta":
		delta, _ := payload["delta"].(map[string]any)
		if delta == nil {
			return nil, false
		}
		deltaType := stringField(delta["type"])
		field, jsonEscape := "", false
		switch deltaType {
		case "text_delta":
			field = "text"
		case "input_json_delta":
			field, jsonEscape = "partial_json", true
		case "thinking_delta":
			if !s.reasoning {
				return nil, false
			}
			field = "thinking"
		default:
			return nil, false
		}
		text, ok := delta[field].(string)
		if !ok {
			return nil, false
		}
		index := payload["index"]
		restored := s.feed(key, jsonEscape, text, false, sseFlushBuilder(eventName, func(t string) map[string]any {
			return map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": deltaType, field: t}}
		}))
		if restored == text {
			return nil, false
		}
		delta[field] = restored
		return nil, true
	case "content_block_stop":
		return s.flushWhere(func(k string) bool { return k == key }), false
	case "message_delta", "message_stop":
		return s.Flush(), RestoreJSONValue(payload, s.session, s.reasoning)
	}
	return nil, RestoreJSONValue(payload, s.session, s.reasoning)
}

// ---- OpenAI Responses ----

func (s *StreamRestorer) restoreResponsesEvent(eventName, eventType string, payload map[string]any) ([][]byte, bool) {
	output := indexKey(payload["output_index"])
	content := indexKey(payload["content_index"])
	if _, ok := payload["summary_index"]; ok {
		content = "s" + indexKey(payload["summary_index"])
	}
	deltaEvent := func(key string, jsonEscape bool) ([][]byte, bool) {
		text, ok := payload["delta"].(string)
		if !ok {
			return nil, false
		}
		template := DeepCopyJSONMap(payload)
		restored := s.feed(key, jsonEscape, text, false, sseFlushBuilder(eventName, func(t string) map[string]any {
			next := DeepCopyJSONMap(template)
			next["delta"] = t
			return next
		}))
		if restored == text {
			return nil, false
		}
		payload["delta"] = restored
		return nil, true
	}
	switch eventType {
	case "response.output_text.delta", "response.refusal.delta":
		return deltaEvent("responses:"+output+":text:"+content, false)
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		return deltaEvent("responses:"+output+":args", eventType == "response.function_call_arguments.delta")
	case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		if !s.reasoning {
			return nil, false
		}
		return deltaEvent("responses:"+output+":reasoning:"+content, false)
	case "response.output_text.done", "response.refusal.done":
		before := s.flushPrefix("responses:" + output + ":text:" + content)
		return before, RestoreJSONValue(payload, s.session, s.reasoning)
	case "response.function_call_arguments.done", "response.custom_tool_call_input.done":
		before := s.flushPrefix("responses:" + output + ":args")
		return before, RestoreJSONValue(payload, s.session, s.reasoning)
	case "response.reasoning_text.done", "response.reasoning_summary_text.done":
		if !s.reasoning {
			return nil, false
		}
		before := s.flushPrefix("responses:" + output + ":reasoning:" + content)
		return before, RestoreJSONValue(payload, s.session, true)
	case "response.output_item.done":
		before := s.flushPrefix("responses:" + output + ":")
		return before, RestoreJSONValue(payload, s.session, s.reasoning)
	case "response.completed", "response.incomplete", "response.failed":
		before := s.Flush()
		return before, RestoreJSONValue(payload, s.session, s.reasoning)
	}
	return nil, RestoreJSONValue(payload, s.session, s.reasoning)
}

// ---- Gemini ----

func geminiStreamRoot(payload map[string]any) map[string]any {
	if _, ok := payload["candidates"].([]any); ok {
		return payload
	}
	if response, ok := payload["response"].(map[string]any); ok {
		if _, ok := response["candidates"].([]any); ok {
			return response
		}
	}
	return nil
}

func (s *StreamRestorer) restoreGeminiChunk(payload, root map[string]any) ([][]byte, bool) {
	changed := false
	var before [][]byte
	candidates, _ := root["candidates"].([]any)
	for i, rawCandidate := range candidates {
		candidate, ok := rawCandidate.(map[string]any)
		if !ok {
			continue
		}
		idx := strconv.Itoa(i)
		if candidate["index"] != nil {
			idx = indexKey(candidate["index"])
		}
		final := stringField(candidate["finishReason"]) != "" || stringField(candidate["finish_reason"]) != ""
		prefix := "gemini:" + idx + ":"
		seen := map[string]bool{}
		content, _ := candidate["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		for _, rawPart := range parts {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			thought, _ := part["thought"].(bool)
			if text, ok := part["text"].(string); ok && (!thought || s.reasoning) {
				key := prefix + "text"
				if thought {
					key = prefix + "thought"
				}
				seen[key] = true
				candidateIndex, isThought := candidate["index"], thought
				restored := s.feed(key, false, text, final, sseFlushBuilder("", func(t string) map[string]any {
					part := map[string]any{"text": t}
					if isThought {
						part["thought"] = true
					}
					next := map[string]any{"content": map[string]any{"role": "model", "parts": []any{part}}}
					if candidateIndex != nil {
						next["index"] = candidateIndex
					}
					return map[string]any{"candidates": []any{next}}
				}))
				if restored != text {
					part["text"] = restored
					changed = true
				}
			}
			for _, key := range []string{"functionCall", "function_call"} {
				if call, ok := part[key].(map[string]any); ok && RestoreJSONValue(call["args"], s.session, s.reasoning) {
					changed = true
				}
			}
		}
		if final {
			before = append(before, s.flushWhere(func(key string) bool { return strings.HasPrefix(key, prefix) && !seen[key] })...)
		}
	}
	return before, changed
}

// ---- upstream direction ----

// Retokenize puts placeholders back into text the gateway produced
// from a restored response before it is sent upstream again, such as the
// replayed items of an image bridge round. Every original of the session is
// replaced, longest first.
func Retokenize(value any, session *textxform.Session) any {
	if session == nil || session.Len() == 0 {
		return value
	}
	originals, tokens := session.Originals()
	set := textmatch.NewLiteralSet(originals, true)
	var walk func(any) any
	walk = func(value any) any {
		switch typed := value.(type) {
		case string:
			return retokenizeText(typed, set, tokens)
		case map[string]any:
			for key, item := range typed {
				if restoreOpaqueKeys[key] {
					continue
				}
				typed[key] = walk(item)
			}
			return typed
		case []any:
			for i, item := range typed {
				typed[i] = walk(item)
			}
			return typed
		}
		return value
	}
	return walk(value)
}

func retokenizeText(text string, set *textmatch.LiteralSet, tokens []string) string {
	matches := set.FindAll(text)
	if len(matches) == 0 {
		return text
	}
	spans := make([]textmatch.Span, len(matches))
	for i, match := range matches {
		spans[i] = textmatch.Span{Start: match.Start, End: match.End, Type: strconv.Itoa(match.Index)}
	}
	picked := textxform.SelectSpans(spans, func(string) int { return 0 }, nil)
	var b strings.Builder
	last := 0
	for _, span := range picked {
		index, _ := strconv.Atoi(span.Type)
		b.WriteString(text[last:span.Start])
		b.WriteString(tokens[index])
		last = span.End
	}
	b.WriteString(text[last:])
	return b.String()
}

// ApplyAll restores out, which may hold several events, and returns the
// events to write.
func (s *StreamRestorer) ApplyAll(out []byte) [][]byte {
	if s == nil {
		return [][]byte{out}
	}
	var blocks [][]byte
	for _, event := range SplitSSEEvents(out) {
		blocks = append(blocks, s.Apply(event)...)
	}
	return blocks
}
