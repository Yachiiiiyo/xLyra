package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const responsesIncidentFailed = `{"response":{"error":{"code":"upstream_stream_read_error","message":"Upstream response stream was interrupted"},"status":"failed"},"type":"response.failed"}`

func TestResponsesPassthroughSanitizesCommittedStreams(t *testing.T) {
	t.Parallel()

	compliantDelta := gatewaySSEEvent("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":1,"delta":"hi"}`)
	compliantDone := gatewaySSEEvent("response.created", `{"type":"response.created","sequence_number":1,"response":{"id":"resp_1","object":"response","created_at":1710000000,"status":"in_progress","model":"grok-test","output":[]}}`) +
		gatewaySSEEvent("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":2,"delta":"hi"}`) +
		gatewaySSEEvent("response.completed", `{"type":"response.completed","sequence_number":3,"response":{"id":"resp_1","object":"response","created_at":1710000000,"status":"completed","model":"grok-test","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`)

	t.Run("minimal failed after commit", func(t *testing.T) {
		body := compliantDelta + "data: " + responsesIncidentFailed + "\n\n"
		rec, capture, err := passthroughFromUpstream(t, body)
		if err != nil {
			t.Fatalf("passthrough: %v", err)
		}
		assertStrictResponsesEvents(t, rec.Body.String())
		for _, kind := range []string{sseRepairResponseCreatedAtMissing, sseRepairResponseIDMissing, sseRepairResponseFieldsFilled, sseRepairSequenceNumberAdded} {
			if !containsRepair(capture.sseRepairs, kind) {
				t.Fatalf("stream_sanitized = %#v, missing %s", streamMetadataEnvelope(capture)["stream_sanitized"], kind)
			}
		}
		if capture.errorDetail != responsesIncidentFailed {
			t.Fatalf("stream_error_detail = %q, want upstream original", capture.errorDetail)
		}
	})

	t.Run("raw newline split across data lines", func(t *testing.T) {
		body := compliantDelta + "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":2,\"delta\":\"hello\ndata: world\"}\n\n"
		rec, capture, err := passthroughFromUpstream(t, body)
		if err != nil {
			t.Fatalf("passthrough: %v", err)
		}
		assertStrictResponsesEvents(t, rec.Body.String())
		if !strings.Contains(rec.Body.String(), `"delta":"hello\nworld"`) && !strings.Contains(rec.Body.String(), `"delta":"hello\nworld"`) {
			if !strings.Contains(rec.Body.String(), `hello\nworld`) {
				t.Fatalf("repaired delta missing escaped newline: %s", rec.Body.String())
			}
		}
		if !containsRepair(capture.sseRepairs, sseRepairControlCharEscaped) || !containsRepair(capture.sseRepairs, sseRepairMultilineDataJoined) {
			t.Fatalf("stream_sanitized = %#v", capture.sseRepairs)
		}
		if capture.errorDetail != "" {
			t.Fatalf("stream_error_detail = %q, want empty", capture.errorDetail)
		}
	})

	t.Run("eof after commit synthesizes failed", func(t *testing.T) {
		rec, capture, err := passthroughFromUpstream(t, compliantDelta)
		if err != nil {
			t.Fatalf("passthrough: %v", err)
		}
		assertStrictResponsesEvents(t, rec.Body.String())
		if !strings.Contains(rec.Body.String(), `"code":"upstream_stream_interrupted"`) {
			t.Fatalf("missing synthesized failure: %s", rec.Body.String())
		}
		if !containsRepair(capture.sseRepairs, sseRepairTerminalFailedSynthesized) {
			t.Fatalf("stream_sanitized = %#v", capture.sseRepairs)
		}
		if capture.errorDetail != "" {
			t.Fatalf("synthesized failure must not replace stream_error_detail, got %q", capture.errorDetail)
		}
		if capture.endReason != "upstream_stream_eof" {
			t.Fatalf("endReason = %q", capture.endReason)
		}
	})

	t.Run("compliant stream is byte identical", func(t *testing.T) {
		rec, capture, err := passthroughFromUpstream(t, compliantDone)
		if err != nil {
			t.Fatalf("passthrough: %v", err)
		}
		if rec.Body.String() != compliantDone {
			t.Fatalf("body = %q\nwant %q", rec.Body.String(), compliantDone)
		}
		assertStrictResponsesEvents(t, rec.Body.String())
		if streamMetadataEnvelope(capture)["stream_sanitized"] != nil {
			t.Fatalf("stream_sanitized = %#v, want null", streamMetadataEnvelope(capture)["stream_sanitized"])
		}
		if capture.errorDetail != "" {
			t.Fatalf("stream_error_detail = %q", capture.errorDetail)
		}
	})
}

func TestResponsesPassthroughKeepsPreOutputFailedFailover(t *testing.T) {
	t.Parallel()

	rec, capture, err := passthroughFromUpstream(t, "data: "+responsesIncidentFailed+"\n\n")
	if err != nil {
		t.Fatalf("passthrough: %v", err)
	}
	if rec.Code != 0 && rec.Body.Len() != 0 {
		t.Fatalf("pre-output failure leaked: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("pre-output failure wrote %q", rec.Body.String())
	}
	if capture.semanticFailure == nil || capture.endReason != "upstream_stream_error" {
		t.Fatalf("expected failover, capture=%+v", capture)
	}
	if capture.errorDetail != responsesIncidentFailed {
		t.Fatalf("stream_error_detail = %q", capture.errorDetail)
	}
}

func passthroughFromUpstream(t *testing.T, body string) (*httptest.ResponseRecorder, streamCaptureState, error) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(upstream.Close)
	resp, err := upstream.Client().Get(upstream.URL)
	if err != nil {
		t.Fatalf("upstream get: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	rec := httptest.NewRecorder()
	capture, _, err := proxyResponsesStreamPassthrough(t.Context(), rec, resp, time.Now())
	return rec, capture, err
}

func assertStrictResponsesEvents(t *testing.T, body string) {
	t.Helper()
	seen := 0
	for _, block := range splitSSEBlocksForTest(body) {
		_, data, commentOnly, done, _ := parseSSEEventBlock([]byte(block))
		if commentOnly || done || len(data) == 0 {
			continue
		}
		if bytesContainNewline(data) {
			t.Fatalf("data event is not a single line: %q", data)
		}
		if err := decodeStrictResponsesEvent(data); err != nil {
			t.Fatalf("strict decode: %v\n%s", err, data)
		}
		seen++
	}
	if seen == 0 {
		t.Fatalf("no data events in %q", body)
	}
}

func splitSSEBlocksForTest(body string) []string {
	normalized := strings.ReplaceAll(body, "\r\n", "\n")
	parts := strings.Split(normalized, "\n\n")
	var blocks []string
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		blocks = append(blocks, part+"\n\n")
	}
	return blocks
}

func bytesContainNewline(data []byte) bool {
	for _, b := range data {
		if b == '\n' || b == '\r' {
			return true
		}
	}
	return false
}

func decodeStrictResponsesEvent(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if _, ok := raw["sequence_number"]; !ok {
		return fmt.Errorf("missing field sequence_number")
	}
	var event struct {
		SequenceNumber int64 `json:"sequence_number"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	response, ok := raw["response"]
	if !ok {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(response, &fields); err != nil {
		return err
	}
	for _, key := range []string{"id", "object", "created_at", "status", "model", "output"} {
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("missing field %s", key)
		}
	}
	var envelope struct {
		ID        string          `json:"id"`
		Object    string          `json:"object"`
		CreatedAt int64           `json:"created_at"`
		Status    string          `json:"status"`
		Model     string          `json:"model"`
		Output    json.RawMessage `json:"output"`
	}
	return json.Unmarshal(response, &envelope)
}
