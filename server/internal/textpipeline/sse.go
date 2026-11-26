package textpipeline

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// parseSSEEvent splits one SSE event into its name and joined data lines.
func parseSSEEvent(block []byte) (eventName string, data []byte, commentOnly bool, done bool) {
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
		parts = append(parts, value)
	}
	if !sawData {
		return eventName, nil, true, false
	}
	joined := bytes.Join(parts, []byte("\n"))
	return eventName, joined, false, bytes.Equal(bytes.TrimSpace(joined), []byte("[DONE]"))
}

// formatSSEEvent encodes one SSE event.
func formatSSEEvent(eventName string, payload any) ([]byte, error) {
	encoded, err := MarshalJSON(payload)
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
	buf.WriteString(encoded)
	buf.WriteString("\n\n")
	return buf.Bytes(), nil
}

// indexSSEDelimiter finds the end of the first event in raw.
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

// SplitSSEEvents splits raw into complete events; a tail without a
// delimiter is the last element.
func SplitSSEEvents(raw []byte) [][]byte {
	var out [][]byte
	for len(raw) > 0 {
		end, delim := indexSSEDelimiter(raw)
		if end < 0 {
			out = append(out, raw)
			break
		}
		out = append(out, raw[:end+delim])
		raw = raw[end+delim:]
	}
	return out
}

// indexKey formats a JSON index field, decoded with or without UseNumber.
func indexKey(value any) string {
	switch typed := value.(type) {
	case json.Number:
		if n, err := typed.Int64(); err == nil {
			return strconv.FormatInt(n, 10)
		}
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	}
	return "0"
}

// DeepCopyJSON copies the maps and slices of a decoded JSON value.
func DeepCopyJSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = DeepCopyJSON(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = DeepCopyJSON(item)
		}
		return out
	default:
		return value
	}
}

// DeepCopyJSONMap copies a decoded JSON object.
func DeepCopyJSONMap(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	out, _ := DeepCopyJSON(payload).(map[string]any)
	return out
}
