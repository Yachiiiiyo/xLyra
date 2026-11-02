package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	routeengine "xlyra/server/internal/router"
)

const (
	sseRepairResponseCreatedAtMissing  = "response_created_at_missing"
	sseRepairResponseIDMissing         = "response_id_missing"
	sseRepairResponseFieldsFilled      = "response_fields_filled"
	sseRepairSequenceNumberAdded       = "sequence_number_added"
	sseRepairErrorEventNormalized      = "error_event_normalized"
	sseRepairControlCharEscaped        = "control_char_escaped"
	sseRepairMultilineDataJoined       = "multiline_data_joined"
	sseRepairDroppedMalformedEvent     = "dropped_malformed_event"
	sseRepairTerminalFailedSynthesized = "terminal_failed_synthesized"
)

type responsesSanitizerState struct {
	responseID    string
	createdAt     int64
	haveCreatedAt bool
	model         string
	fallbackModel string
	maxSequence   int64
	sawTerminal   bool
}

func sanitizeResponsesEventBlock(state *responsesSanitizerState, block []byte) ([]byte, []string) {
	if state == nil {
		state = &responsesSanitizerState{}
	}
	eventName, data, commentOnly, done, multiline := parseSSEEventBlock(block)
	if commentOnly || done {
		return append([]byte(nil), block...), nil
	}

	var repairs []string
	if multiline {
		repairs = appendRepair(repairs, sseRepairMultilineDataJoined)
	}
	value, err := decodeJSONUseNumber(data)
	if err != nil {
		escaped, changed := repairJSONControlChars(data)
		if changed {
			value, err = decodeJSONUseNumber(escaped)
			if err == nil {
				repairs = appendRepair(repairs, sseRepairControlCharEscaped)
			}
		}
	}
	if err != nil {
		return nil, appendRepair(repairs, sseRepairDroppedMalformedEvent)
	}
	payload, ok := value.(map[string]any)
	if !ok {
		if len(repairs) == 0 {
			return append([]byte(nil), block...), nil
		}
		out, encErr := formatRepairedSSE(eventName, value)
		if encErr != nil {
			return nil, appendRepair(repairs, sseRepairDroppedMalformedEvent)
		}
		return out, repairs
	}

	repairs = appendRepairs(repairs, fillResponsesEventFields(payload, state))
	if isResponsesTerminalType(strings.TrimSpace(anyString(payload["type"]))) {
		state.sawTerminal = true
	}
	if len(repairs) == 0 {
		return append([]byte(nil), block...), nil
	}
	out, encErr := formatRepairedSSE(eventName, payload)
	if encErr != nil {
		return nil, appendRepair(nil, sseRepairDroppedMalformedEvent)
	}
	return out, repairs
}

func synthesizeResponsesTerminalFailed(state *responsesSanitizerState) ([]byte, []string) {
	if state == nil {
		state = &responsesSanitizerState{}
	}
	payload := map[string]any{
		"type":            "response.failed",
		"sequence_number": state.nextSequence(),
		"response": map[string]any{
			"id":         state.ensureID(),
			"object":     "response",
			"created_at": state.ensureCreatedAt(),
			"status":     "failed",
			"model":      state.modelOrFallback(),
			"output":     []any{},
			"error": map[string]any{
				"code":    "upstream_stream_interrupted",
				"message": "Upstream response stream was interrupted",
			},
		},
	}
	state.sawTerminal = true
	out, err := formatRepairedSSE("response.failed", payload)
	if err != nil {
		return nil, []string{sseRepairDroppedMalformedEvent}
	}
	return out, []string{sseRepairTerminalFailedSynthesized}
}

func fillResponsesEventFields(payload map[string]any, state *responsesSanitizerState) []string {
	if payload == nil {
		return nil
	}
	if state == nil {
		state = &responsesSanitizerState{}
	}
	var repairs []string
	eventType := strings.TrimSpace(anyString(payload["type"]))
	if seq, ok := jsonInt64(payload["sequence_number"]); ok && seq > state.maxSequence {
		state.maxSequence = seq
	}
	if response, ok := payload["response"].(map[string]any); ok {
		repairs = appendRepairs(repairs, fillResponsesObject(eventType, response, state))
	}
	if strings.HasPrefix(eventType, "response.") || eventType == "error" {
		if _, ok := payload["sequence_number"]; !ok {
			payload["sequence_number"] = state.nextSequence()
			repairs = appendRepair(repairs, sseRepairSequenceNumberAdded)
		}
	}
	if eventType == "error" && normalizeResponsesErrorEvent(payload) {
		repairs = appendRepair(repairs, sseRepairErrorEventNormalized)
	}
	return repairs
}

func fillResponsesObject(eventType string, response map[string]any, state *responsesSanitizerState) []string {
	if response == nil || state == nil {
		return nil
	}
	var repairs []string
	if id := strings.TrimSpace(anyString(response["id"])); id != "" {
		if state.responseID == "" {
			state.responseID = id
		}
	} else if _, ok := response["id"]; !ok {
		response["id"] = state.ensureID()
		repairs = appendRepair(repairs, sseRepairResponseIDMissing)
	}
	if _, ok := response["object"]; !ok {
		response["object"] = "response"
		repairs = appendRepair(repairs, sseRepairResponseFieldsFilled)
	}
	if _, ok := response["created_at"]; !ok {
		response["created_at"] = state.ensureCreatedAt()
		repairs = appendRepair(repairs, sseRepairResponseCreatedAtMissing)
	} else {
		state.observeCreatedAt(response["created_at"])
	}
	if _, ok := response["status"]; !ok {
		if status, ok := responsesStatusForType(eventType); ok {
			response["status"] = status
			repairs = appendRepair(repairs, sseRepairResponseFieldsFilled)
		}
	}
	if model, ok := response["model"]; ok {
		if text := strings.TrimSpace(anyString(model)); text != "" && state.model == "" {
			state.model = text
		}
	} else {
		response["model"] = state.modelOrFallback()
		repairs = appendRepair(repairs, sseRepairResponseFieldsFilled)
	}
	if _, ok := response["output"]; !ok {
		response["output"] = []any{}
		repairs = appendRepair(repairs, sseRepairResponseFieldsFilled)
	}
	if eventType == "response.failed" {
		if message, ok := response["error"].(string); ok {
			response["error"] = map[string]any{"code": "upstream_error", "message": message}
			repairs = appendRepair(repairs, sseRepairResponseFieldsFilled)
		}
	}
	return repairs
}

func normalizeResponsesErrorEvent(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	nested, _ := payload["error"].(map[string]any)
	changed := false
	if _, ok := payload["code"]; !ok {
		payload["code"] = nestedString(nested, "code")
		changed = true
	}
	if _, ok := payload["message"]; !ok {
		payload["message"] = nestedString(nested, "message")
		changed = true
	}
	if _, ok := payload["param"]; !ok {
		if nested != nil {
			if param, ok := nested["param"]; ok {
				payload["param"] = param
			} else {
				payload["param"] = nil
			}
		} else {
			payload["param"] = nil
		}
		changed = true
	}
	return changed
}

func repairJSONControlChars(data []byte) ([]byte, bool) {
	var out bytes.Buffer
	inString := false
	escaped := false
	changed := false
	for i := 0; i < len(data); i++ {
		b := data[i]
		if !inString {
			out.WriteByte(b)
			if b == '"' {
				inString = true
			}
			continue
		}
		if escaped {
			out.WriteByte(b)
			escaped = false
			continue
		}
		if b == '\\' {
			out.WriteByte(b)
			escaped = true
			continue
		}
		if b == '"' {
			out.WriteByte(b)
			inString = false
			continue
		}
		if b < 0x20 {
			changed = true
			switch b {
			case '\n':
				out.WriteString(`\n`)
			case '\r':
				out.WriteString(`\r`)
			case '\t':
				out.WriteString(`\t`)
			default:
				fmt.Fprintf(&out, `\u%04x`, b)
			}
			continue
		}
		out.WriteByte(b)
	}
	if !changed {
		return data, false
	}
	return out.Bytes(), true
}

func (s *responsesSanitizerState) ensureID() string {
	if s.responseID == "" {
		s.responseID = "resp_xlyra_" + uuid.NewString()
	}
	return s.responseID
}

func (s *responsesSanitizerState) ensureCreatedAt() int64 {
	if !s.haveCreatedAt {
		s.createdAt = time.Now().Unix()
		s.haveCreatedAt = true
	}
	return s.createdAt
}

func (s *responsesSanitizerState) observeCreatedAt(value any) {
	if s.haveCreatedAt {
		return
	}
	parsed, ok := jsonInt64(value)
	if !ok {
		return
	}
	s.createdAt = parsed
	s.haveCreatedAt = true
}

func (s *responsesSanitizerState) modelOrFallback() string {
	if s.model != "" {
		return s.model
	}
	return s.fallbackModel
}

func (s *responsesSanitizerState) nextSequence() int64 {
	s.maxSequence++
	return s.maxSequence
}

func responsesStatusForType(eventType string) (string, bool) {
	switch eventType {
	case "response.created", "response.in_progress", "response.queued":
		return "in_progress", true
	case "response.completed":
		return "completed", true
	case "response.failed":
		return "failed", true
	case "response.incomplete":
		return "incomplete", true
	default:
		return "", false
	}
}

func isResponsesTerminalType(eventType string) bool {
	switch eventType {
	case "response.completed", "response.incomplete", "response.failed", "response.error":
		return true
	default:
		return false
	}
}

func parseSSEEventBlock(block []byte) (eventName string, data []byte, commentOnly bool, done bool, multiline bool) {
	body := block
	switch {
	case bytes.HasSuffix(body, []byte("\r\n\r\n")):
		body = body[:len(body)-4]
	case bytes.HasSuffix(body, []byte("\n\n")):
		body = body[:len(body)-2]
	}
	var parts [][]byte
	sawData := false
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 || line[0] == ':' {
			continue
		}
		if bytes.HasPrefix(line, []byte("event:")) {
			name := line[len("event:"):]
			if len(name) > 0 && name[0] == ' ' {
				name = name[1:]
			}
			eventName = string(name)
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		sawData = true
		value := line[len("data:"):]
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		parts = append(parts, append([]byte(nil), value...))
	}
	if !sawData {
		return eventName, nil, true, false, false
	}
	joined := bytes.Join(parts, []byte("\n"))
	if bytes.Equal(bytes.TrimSpace(joined), []byte("[DONE]")) {
		return eventName, joined, false, true, len(parts) > 1
	}
	return eventName, joined, false, false, len(parts) > 1
}

func decodeJSONUseNumber(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("trailing json")
		}
		return nil, err
	}
	return value, nil
}

func formatRepairedSSE(eventName string, payload any) ([]byte, error) {
	encoded, err := marshalSSEJSON(payload)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if eventName != "" {
		buf.WriteString("event: ")
		buf.WriteString(eventName)
		buf.WriteByte('\n')
	}
	buf.WriteString("data: ")
	buf.Write(encoded)
	buf.WriteString("\n\n")
	return buf.Bytes(), nil
}

func marshalSSEJSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func jsonInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	default:
		return 0, false
	}
}

func nestedString(object map[string]any, key string) string {
	if object == nil {
		return ""
	}
	return strings.TrimSpace(anyString(object[key]))
}

func appendRepair(repairs []string, kind string) []string {
	for _, existing := range repairs {
		if existing == kind {
			return repairs
		}
	}
	return append(repairs, kind)
}

func appendRepairs(repairs []string, more []string) []string {
	for _, kind := range more {
		repairs = appendRepair(repairs, kind)
	}
	return repairs
}

type sseSanitizeMeta struct {
	SiteID        string
	ChannelID     string
	RouteTargetID string
}

type sseSanitizeMetaKey struct{}

func withSSESanitizeMeta(ctx context.Context, candidate routeengine.Candidate) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	meta := sseSanitizeMeta{}
	if candidate.Site.ID != uuid.Nil {
		meta.SiteID = candidate.Site.ID.String()
	}
	if candidate.Credential.ID != nil && *candidate.Credential.ID != uuid.Nil {
		meta.ChannelID = candidate.Credential.ID.String()
	}
	if candidate.Model.SiteModelID != uuid.Nil {
		meta.RouteTargetID = candidate.Model.SiteModelID.String()
	}
	return context.WithValue(ctx, sseSanitizeMetaKey{}, meta)
}

type responsesSSEExit struct {
	dst     io.Writer
	flusher http.Flusher
	pending bytes.Buffer
	state   *responsesSanitizerState
	capture *streamCaptureState
	ctx     context.Context
}

func newResponsesSSEExit(ctx context.Context, dst io.Writer, flusher http.Flusher, capture *streamCaptureState) *responsesSSEExit {
	return &responsesSSEExit{
		dst:     dst,
		flusher: flusher,
		state:   &responsesSanitizerState{},
		capture: capture,
		ctx:     ctx,
	}
}

func (e *responsesSSEExit) Write(p []byte) error {
	e.pending.Write(p)
	for {
		raw := e.pending.Bytes()
		end, delim := indexSSEDelimiter(raw)
		if end < 0 {
			return nil
		}
		block := append([]byte(nil), raw[:end+delim]...)
		e.pending.Next(end + delim)
		if err := e.emit(block); err != nil {
			return err
		}
	}
}

func (e *responsesSSEExit) Finish() error {
	if e.pending.Len() == 0 {
		return nil
	}
	block := append([]byte(nil), e.pending.Bytes()...)
	e.pending.Reset()
	return e.emit(block)
}

func (e *responsesSSEExit) finishTerminal() error {
	if err := e.Finish(); err != nil {
		return err
	}
	if e.capture != nil && (e.capture.streamCompleted || e.capture.sawDone || e.capture.endReason == "response_incomplete" || e.state.sawTerminal) {
		return nil
	}
	out, repairs := synthesizeResponsesTerminalFailed(e.state)
	e.record(repairs)
	if len(out) == 0 {
		return nil
	}
	if _, err := e.dst.Write(out); err != nil {
		return err
	}
	e.flush()
	return nil
}

func (e *responsesSSEExit) emit(block []byte) error {
	if e.capture != nil && e.state.fallbackModel == "" {
		e.state.fallbackModel = e.capture.responseModel
	}
	rememberRawFailureDetail(e.capture, block)
	out, repairs := sanitizeResponsesEventBlock(e.state, block)
	e.record(repairs)
	if len(out) == 0 {
		return nil
	}
	if _, err := e.dst.Write(out); err != nil {
		return err
	}
	e.flush()
	return nil
}

func (e *responsesSSEExit) record(repairs []string) {
	if len(repairs) == 0 {
		return
	}
	if e.capture != nil {
		for _, kind := range repairs {
			e.capture.sseRepairs = appendRepair(e.capture.sseRepairs, kind)
		}
	}
	logSSERepairs(e.ctx, repairs)
}

func (e *responsesSSEExit) flush() {
	if e.flusher != nil {
		e.flusher.Flush()
	}
}

func rememberRawFailureDetail(capture *streamCaptureState, block []byte) {
	if capture == nil || strings.TrimSpace(capture.errorDetail) != "" {
		return
	}
	_, data, commentOnly, done, _ := parseSSEEventBlock(block)
	if commentOnly || done || len(data) == 0 {
		return
	}
	var event struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &event) != nil {
		return
	}
	switch event.Type {
	case "response.failed", "response.error", "error":
		capture.errorDetail = truncatedStreamErrorDetail(string(data))
	}
}

func logSSERepairs(ctx context.Context, repairs []string) {
	if len(repairs) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	meta, _ := ctx.Value(sseSanitizeMetaKey{}).(sseSanitizeMeta)
	slog.Default().WarnContext(ctx, "sse stream sanitized",
		"scope", "gateway",
		"repairs", repairs,
		"site_id", meta.SiteID,
		"channel_id", meta.ChannelID,
		"route_target_id", meta.RouteTargetID,
	)
}

type protocolSSEExit struct {
	dst      io.Writer
	flusher  http.Flusher
	pending  bytes.Buffer
	capture  *streamCaptureState
	ctx      context.Context
	sanitize func([]byte) ([]byte, []string)
}

func newProtocolSSEExit(ctx context.Context, dst io.Writer, flusher http.Flusher, capture *streamCaptureState, sanitize func([]byte) ([]byte, []string)) *protocolSSEExit {
	return &protocolSSEExit{dst: dst, flusher: flusher, capture: capture, ctx: ctx, sanitize: sanitize}
}

func (e *protocolSSEExit) Write(p []byte) error {
	e.pending.Write(p)
	for {
		raw := e.pending.Bytes()
		end, delim := indexSSEDelimiter(raw)
		if end < 0 {
			return nil
		}
		block := append([]byte(nil), raw[:end+delim]...)
		e.pending.Next(end + delim)
		if err := e.emit(block); err != nil {
			return err
		}
	}
}

func (e *protocolSSEExit) Finish() error {
	if e.pending.Len() == 0 {
		return nil
	}
	block := append([]byte(nil), e.pending.Bytes()...)
	e.pending.Reset()
	return e.emit(block)
}

func (e *protocolSSEExit) emit(block []byte) error {
	out, repairs := e.sanitize(block)
	if e.capture != nil {
		for _, kind := range repairs {
			e.capture.sseRepairs = appendRepair(e.capture.sseRepairs, kind)
		}
	}
	if len(repairs) > 0 {
		logSSERepairs(e.ctx, repairs)
	}
	if len(out) == 0 {
		return nil
	}
	if _, err := e.dst.Write(out); err != nil {
		return err
	}
	if e.flusher != nil {
		e.flusher.Flush()
	}
	return nil
}

func fillBridgeFailureEnvelope(b *bridgeStreamWriter, payload map[string]any) {
	if b == nil || payload == nil {
		return
	}
	state := &responsesSanitizerState{
		responseID:    b.responseID,
		createdAt:     b.createdAt,
		haveCreatedAt: b.createdAt != 0,
		model:         b.model,
	}
	eventType := strings.TrimSpace(anyString(payload["type"]))
	if _, ok := payload["response"].(map[string]any); !ok && (eventType == "response.failed" || eventType == "response.error" || eventType == "error") {
		response := map[string]any{}
		if errValue, exists := payload["error"]; exists {
			response["error"] = errValue
		}
		payload["response"] = response
	}
	fillResponsesEventFields(payload, state)
}

func sanitizeAnthropicEventBlock(block []byte) ([]byte, []string) {
	return sanitizeProtocolEventBlock(block, fillAnthropicEventFields)
}

func sanitizeChatEventBlock(block []byte) ([]byte, []string) {
	return sanitizeProtocolEventBlock(block, nil)
}

func sanitizeProtocolEventBlock(block []byte, fill func(map[string]any) []string) ([]byte, []string) {
	eventName, data, commentOnly, done, multiline := parseSSEEventBlock(block)
	if commentOnly || done {
		return append([]byte(nil), block...), nil
	}
	var repairs []string
	if multiline {
		repairs = appendRepair(repairs, sseRepairMultilineDataJoined)
	}
	value, err := decodeJSONUseNumber(data)
	if err != nil {
		escaped, changed := repairJSONControlChars(data)
		if changed {
			value, err = decodeJSONUseNumber(escaped)
			if err == nil {
				repairs = appendRepair(repairs, sseRepairControlCharEscaped)
			}
		}
	}
	if err != nil {
		return nil, appendRepair(repairs, sseRepairDroppedMalformedEvent)
	}
	payload, ok := value.(map[string]any)
	if ok && fill != nil {
		repairs = appendRepairs(repairs, fill(payload))
	}
	if len(repairs) == 0 {
		return append([]byte(nil), block...), nil
	}
	encoded := any(value)
	if ok {
		encoded = payload
	}
	out, encErr := formatRepairedSSE(eventName, encoded)
	if encErr != nil {
		return nil, appendRepair(repairs, sseRepairDroppedMalformedEvent)
	}
	return out, repairs
}

func fillAnthropicEventFields(payload map[string]any) []string {
	if payload == nil {
		return nil
	}
	var repairs []string
	switch strings.TrimSpace(anyString(payload["type"])) {
	case "message_start":
		message, ok := payload["message"].(map[string]any)
		if !ok {
			message = map[string]any{}
			payload["message"] = message
			repairs = appendRepair(repairs, sseRepairResponseFieldsFilled)
		}
		if _, ok := message["usage"]; !ok {
			message["usage"] = map[string]any{"input_tokens": int64(0), "output_tokens": int64(0)}
			repairs = appendRepair(repairs, sseRepairResponseFieldsFilled)
		}
		if _, ok := message["content"]; !ok {
			message["content"] = []any{}
			repairs = appendRepair(repairs, sseRepairResponseFieldsFilled)
		}
		if _, ok := message["stop_reason"]; !ok {
			message["stop_reason"] = nil
			repairs = appendRepair(repairs, sseRepairResponseFieldsFilled)
		}
		if _, ok := message["role"]; !ok {
			message["role"] = "assistant"
			repairs = appendRepair(repairs, sseRepairResponseFieldsFilled)
		}
		if _, ok := message["type"]; !ok {
			message["type"] = "message"
			repairs = appendRepair(repairs, sseRepairResponseFieldsFilled)
		}
	case "message_delta":
		if _, ok := payload["usage"]; !ok {
			payload["usage"] = map[string]any{"output_tokens": int64(0)}
			repairs = appendRepair(repairs, sseRepairResponseFieldsFilled)
		}
	}
	return repairs
}

func indexSSEDelimiter(raw []byte) (int, int) {
	lf := bytes.Index(raw, []byte("\n\n"))
	crlf := bytes.Index(raw, []byte("\r\n\r\n"))
	if lf < 0 && crlf < 0 {
		return -1, 0
	}
	if crlf >= 0 && (lf < 0 || crlf < lf) {
		return crlf, 4
	}
	return lf, 2
}
