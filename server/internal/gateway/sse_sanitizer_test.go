package gateway

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
)

func TestSanitizeResponsesEventBlockCore(t *testing.T) {
	t.Parallel()

	const incident = `{"response":{"error":{"code":"upstream_stream_read_error","message":"Upstream response stream was interrupted"},"status":"failed"},"type":"response.failed"}`

	t.Run("unchanged bytes", func(t *testing.T) {
		block := []byte("data: {\"type\":\"response.created\",\"sequence_number\":1,\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1710000000,\"status\":\"in_progress\",\"model\":\"grok-test\",\"output\":[]}}\n\n")
		out, repairs := sanitizeResponsesEventBlock(&responsesSanitizerState{}, block)
		if !bytes.Equal(out, block) {
			t.Fatalf("output bytes changed:\n got %q\nwant %q", out, block)
		}
		if len(repairs) != 0 {
			t.Fatalf("repairs = %#v, want none", repairs)
		}
	})

	t.Run("minimal response.failed", func(t *testing.T) {
		block := []byte("data: " + incident + "\n\n")
		out, repairs := sanitizeResponsesEventBlock(&responsesSanitizerState{}, block)
		if len(out) == 0 {
			t.Fatal("expected repaired event")
		}
		payload := decodeSanitizedData(t, out)
		response, _ := payload["response"].(map[string]any)
		if response == nil {
			t.Fatalf("missing response object: %#v", payload)
		}
		if _, ok := response["created_at"]; !ok {
			t.Fatalf("missing created_at: %#v", response)
		}
		if id := anyString(response["id"]); !strings.HasPrefix(id, "resp_xlyra_") {
			t.Fatalf("id = %q", id)
		}
		if response["object"] != "response" || response["status"] != "failed" || response["model"] != "" {
			t.Fatalf("envelope = %#v", response)
		}
		if _, ok := response["output"].([]any); !ok {
			t.Fatalf("output = %#v", response["output"])
		}
		if _, ok := payload["sequence_number"]; !ok {
			t.Fatalf("missing sequence_number: %#v", payload)
		}
		errorObject, _ := response["error"].(map[string]any)
		if anyString(errorObject["code"]) != "upstream_stream_read_error" {
			t.Fatalf("error = %#v", response["error"])
		}
		for _, kind := range []string{
			sseRepairResponseCreatedAtMissing,
			sseRepairResponseIDMissing,
			sseRepairResponseFieldsFilled,
			sseRepairSequenceNumberAdded,
		} {
			if !containsRepair(repairs, kind) {
				t.Fatalf("repairs = %#v, missing %s", repairs, kind)
			}
		}
	})

	t.Run("raw newline split across data lines", func(t *testing.T) {
		block := []byte("data: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"delta\":\"hello\ndata: world\"}\n\n")
		out, repairs := sanitizeResponsesEventBlock(&responsesSanitizerState{}, block)
		payload := decodeSanitizedData(t, out)
		if payload["delta"] != "hello\nworld" {
			t.Fatalf("delta = %#v", payload["delta"])
		}
		if bytes.Contains(out, []byte("\ndata:")) {
			t.Fatalf("repaired event still has a split data line: %q", out)
		}
		if !containsRepair(repairs, sseRepairMultilineDataJoined) || !containsRepair(repairs, sseRepairControlCharEscaped) {
			t.Fatalf("repairs = %#v", repairs)
		}
	})
}

func TestSanitizeResponsesEventBlockEdges(t *testing.T) {
	t.Parallel()

	t.Run("string error", func(t *testing.T) {
		block := []byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":\"boom\"}}\n\n")
		out, repairs := sanitizeResponsesEventBlock(&responsesSanitizerState{}, block)
		payload := decodeSanitizedData(t, out)
		response := payload["response"].(map[string]any)
		errorObject := response["error"].(map[string]any)
		if errorObject["code"] != "upstream_error" || errorObject["message"] != "boom" {
			t.Fatalf("error = %#v", errorObject)
		}
		if !containsRepair(repairs, sseRepairResponseFieldsFilled) {
			t.Fatalf("repairs = %#v", repairs)
		}
	})

	t.Run("data without space and crlf", func(t *testing.T) {
		block := []byte("data:{\"type\":\"response.output_text.delta\",\"sequence_number\":4,\"delta\":\"ok\"}\r\n\r\n")
		out, repairs := sanitizeResponsesEventBlock(&responsesSanitizerState{}, block)
		if !bytes.Equal(out, block) || len(repairs) != 0 {
			t.Fatalf("out=%q repairs=%#v", out, repairs)
		}
	})

	t.Run("drop malformed", func(t *testing.T) {
		out, repairs := sanitizeResponsesEventBlock(&responsesSanitizerState{}, []byte("data: {not-json\n\n"))
		if len(out) != 0 || !containsRepair(repairs, sseRepairDroppedMalformedEvent) {
			t.Fatalf("out=%q repairs=%#v", out, repairs)
		}
	})

	t.Run("large integer", func(t *testing.T) {
		block := []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"x\",\"n\":9007199254740993}\n\n")
		out, _ := sanitizeResponsesEventBlock(&responsesSanitizerState{}, block)
		if !bytes.Contains(out, []byte("9007199254740993")) {
			t.Fatalf("lost integer precision: %s", out)
		}
	})

	t.Run("nested error keeps flat fields", func(t *testing.T) {
		block := []byte("data: {\"type\":\"error\",\"error\":{\"code\":\"server_error\",\"message\":\"boom\"}}\n\n")
		out, repairs := sanitizeResponsesEventBlock(&responsesSanitizerState{}, block)
		payload := decodeSanitizedData(t, out)
		if payload["code"] != "server_error" || payload["message"] != "boom" || payload["param"] != nil {
			t.Fatalf("flat error = %#v", payload)
		}
		nested := payload["error"].(map[string]any)
		if nested["code"] != "server_error" {
			t.Fatalf("nested error dropped: %#v", nested)
		}
		if _, ok := payload["sequence_number"]; !ok || !containsRepair(repairs, sseRepairErrorEventNormalized) {
			t.Fatalf("payload=%#v repairs=%#v", payload, repairs)
		}
	})

	t.Run("comment and done pass through", func(t *testing.T) {
		for _, block := range []string{": xlyra-keepalive\n\n", "data: [DONE]\n\n"} {
			out, repairs := sanitizeResponsesEventBlock(&responsesSanitizerState{}, []byte(block))
			if string(out) != block || len(repairs) != 0 {
				t.Fatalf("block %q became %q repairs=%#v", block, out, repairs)
			}
		}
	})

	t.Run("anthropic message_start", func(t *testing.T) {
		block := []byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\"}}\n\n")
		out, repairs := sanitizeAnthropicEventBlock(block)
		payload := decodeSanitizedData(t, out)
		message := payload["message"].(map[string]any)
		usage := message["usage"].(map[string]any)
		if usage["input_tokens"] != float64(0) || message["role"] != "assistant" || message["type"] != "message" || message["stop_reason"] != nil {
			t.Fatalf("message = %#v", message)
		}
		if _, ok := message["content"].([]any); !ok || !containsRepair(repairs, sseRepairResponseFieldsFilled) {
			t.Fatalf("content=%#v repairs=%#v", message["content"], repairs)
		}
	})
}

func TestSSESanitizerFuzzMutations(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewSource(20261030))
	base := []byte("data: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"delta\":\"hello\"}\n\n")
	for i := 0; i < 48; i++ {
		mutated := mutateSSEBytes(rng, base)
		state := &responsesSanitizerState{}
		capture := &streamCaptureState{}
		var pending bytes.Buffer
		pending.Write(mutated)
		var output bytes.Buffer
		for {
			raw := pending.Bytes()
			end, delim := indexSSEDelimiter(raw)
			if end < 0 {
				break
			}
			block := append([]byte(nil), raw[:end+delim]...)
			pending.Next(end + delim)
			out, repairs := sanitizeResponsesEventBlock(state, block)
			for _, kind := range repairs {
				capture.sseRepairs = appendRepair(capture.sseRepairs, kind)
			}
			output.Write(out)
		}
		if pending.Len() > 0 && rng.Intn(2) == 0 {
			out, _ := sanitizeResponsesEventBlock(state, append([]byte(nil), pending.Bytes()...))
			output.Write(out)
		}
		if !captureHasTerminal(capture, state) {
			terminal, _ := synthesizeResponsesTerminalFailed(state)
			output.Write(terminal)
		}
		assertSanitizerOutputSafe(t, output.Bytes())
	}
}

func captureHasTerminal(capture *streamCaptureState, state *responsesSanitizerState) bool {
	return state.sawTerminal || capture.streamCompleted || capture.sawDone
}

func assertSanitizerOutputSafe(t *testing.T, output []byte) {
	t.Helper()
	if len(output) == 0 {
		return
	}
	text := string(output)
	if strings.Contains(text, `"code":"upstream_stream_interrupted"`) && strings.HasSuffix(strings.TrimSpace(text), "}") {
		return
	}
	for _, block := range splitSSEBlocksForTest(text) {
		_, data, commentOnly, done, _ := parseSSEEventBlock([]byte(block))
		if commentOnly || done || len(data) == 0 {
			continue
		}
		if bytesContainNewline(data) {
			t.Fatalf("data event is not a single line: %q", data)
		}
		var payload map[string]any
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("data event is not json: %v\n%s", err, data)
		}
	}
}

func mutateSSEBytes(rng *rand.Rand, base []byte) []byte {
	out := append([]byte(nil), base...)
	switch rng.Intn(5) {
	case 0:
		at := rng.Intn(len(out) + 1)
		out = append(out[:at], append([]byte{byte(rng.Intn(0x20))}, out[at:]...)...)
	case 1:
		if len(out) > 4 {
			out = out[:rng.Intn(len(out)-1)+1]
		}
	case 2:
		out = append(out, []byte("data: {\"type\":\"response.failed\"}\n")...)
	case 3:
		out = bytes.ReplaceAll(out, []byte("data: "), []byte("data:"))
	default:
		out = bytes.ReplaceAll(out, []byte("\n"), []byte("\r\n"))
	}
	return out
}

func decodeSanitizedData(t *testing.T, block []byte) map[string]any {
	t.Helper()
	_, data, commentOnly, done, _ := parseSSEEventBlock(block)
	if commentOnly || done || len(data) == 0 {
		t.Fatalf("no data payload in %q", block)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("repaired data is not json: %v\n%s", err, data)
	}
	return payload
}

func containsRepair(repairs []string, kind string) bool {
	for _, existing := range repairs {
		if existing == kind {
			return true
		}
	}
	return false
}
