package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"xlyra/server/internal/jsplugin"
	routeengine "xlyra/server/internal/router"
	sitepkg "xlyra/server/internal/site"
)

const streamMalformedLimit = 8

var (
	errUpstreamStreamStatus    = errors.New("upstream_stream_status")
	errUpstreamStreamMalformed = errors.New("upstream_stream_malformed")
)

// streamTransformer rewrites an upstream SSE body into a standard SSE body.
// It implements io.ReadCloser. Close closes the upstream body.
type streamTransformer struct {
	src     io.ReadCloser
	section *jsplugin.StreamSection
	scanner *bufio.Scanner
	out     []byte
	closed  bool
	done    bool
	bad     int
	err     error
}

// applyCandidateStream wraps a 2xx body when the candidate's protocol plugin
// or site-type base declares stream. Overlay plugins are not consulted.
// A non-2xx body is left unchanged so error text still reaches the classifier.
func applyCandidateStream(resp *http.Response, protocol gatewayProtocolAdapter, candidate routeengine.Candidate) {
	if resp == nil || resp.Body == nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return
	}
	section := streamConfigForCandidate(protocol, candidate)
	if section == nil || section.Empty() {
		return
	}
	resp.Body = newStreamTransformer(resp.Body, section)
}

func streamConfigForCandidate(protocol gatewayProtocolAdapter, candidate routeengine.Candidate) *jsplugin.StreamSection {
	if adapter, ok := protocol.(*jsProtocolAdapter); ok && adapter != nil && adapter.plugin != nil {
		if section := adapter.plugin.Manifest.Protocol.Stream; section != nil && !section.Empty() {
			return section
		}
	}
	if item, ok := sitepkg.LookupPluginSiteType(candidate.Site.SiteType); ok && item.Enabled && item.PluginID != "" {
		registry := jsplugin.DefaultCatalog().Registry()
		plugin, found := (*jsplugin.Plugin)(nil), false
		if registry != nil {
			plugin, found = registry.ByPluginID(item.PluginID)
		}
		if found && plugin != nil && plugin.Manifest.DeclaresSiteType() {
			if section := plugin.Manifest.SiteProvider.Protocol.Stream; section != nil && !section.Empty() {
				return section
			}
		}
	}
	return nil
}

func newStreamTransformer(src io.ReadCloser, section *jsplugin.StreamSection) *streamTransformer {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	return &streamTransformer{src: src, section: section, scanner: scanner}
}

func (t *streamTransformer) Read(p []byte) (int, error) {
	if len(t.out) == 0 {
		if err := t.fill(); err != nil {
			return 0, err
		}
	}
	n := copy(p, t.out)
	t.out = t.out[n:]
	return n, nil
}

func (t *streamTransformer) Close() error {
	if t.closed {
		return nil
	}
	t.closed = true
	if t.src == nil {
		return nil
	}
	return t.src.Close()
}

func (t *streamTransformer) fill() error {
	if t.err != nil {
		return t.err
	}
	for len(t.out) == 0 && !t.done {
		event, err := t.readEvent()
		if err != nil {
			t.err = err
			if len(t.out) > 0 {
				return nil
			}
			return err
		}
		if event == nil {
			continue
		}
		rewritten, stop, convErr := t.convert(event)
		if errors.Is(convErr, errUpstreamStreamStatus) {
			t.err = errUpstreamStreamStatus
			_ = t.Close()
			return t.err
		}
		if convErr != nil {
			t.bad++
			if t.bad > streamMalformedLimit {
				t.err = errUpstreamStreamMalformed
				return t.err
			}
			continue
		}
		t.bad = 0
		t.out = append(t.out, rewritten...)
		if stop {
			t.done = true
			if t.section != nil && t.section.Done != nil && t.section.Done.CloseOnDone {
				_ = t.Close()
				t.err = io.EOF
			}
		}
	}
	if len(t.out) == 0 {
		if t.err != nil {
			return t.err
		}
		t.err = io.EOF
		return io.EOF
	}
	return nil
}

type sseEvent struct {
	lines []sseLine
	data  []string
	event string
}

type sseLine struct {
	kind string
	raw  string
}

func (t *streamTransformer) readEvent() (*sseEvent, error) {
	event := &sseEvent{}
	saw := false
	for t.scanner.Scan() {
		line := t.scanner.Text()
		if line == "" {
			if !saw {
				continue
			}
			return event, nil
		}
		saw = true
		if strings.HasPrefix(line, ":") {
			event.lines = append(event.lines, sseLine{kind: "comment", raw: line})
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			event.data = append(event.data, value)
			event.lines = append(event.lines, sseLine{kind: "data", raw: line})
		case "event":
			event.event = value
			event.lines = append(event.lines, sseLine{kind: "event", raw: line})
		default:
			event.lines = append(event.lines, sseLine{kind: "other", raw: line})
		}
	}
	if err := t.scanner.Err(); err != nil {
		return nil, err
	}
	if !saw {
		return nil, io.EOF
	}
	return event, nil
}

func (t *streamTransformer) convert(event *sseEvent) ([]byte, bool, error) {
	section := t.section
	if len(event.data) == 0 {
		return writeEvent(event.lines, nil), false, nil
	}
	payload := strings.Join(event.data, "\n")
	if section != nil && section.Done != nil && markerEquals(section.Done.DataEquals, payload) {
		return t.finish(event, payload, true)
	}
	if section != nil && section.Done != nil && markerEquals(section.Done.Events, event.event) && event.event != "" {
		return t.finish(event, payload, true)
	}
	if section == nil || section.Unwrap == nil {
		next, stop, err := t.applyUsage(payload, false)
		if err != nil {
			return nil, false, err
		}
		return t.finish(event, next, stop)
	}
	root, err := decodeObject(payload)
	if err != nil {
		return nil, false, err
	}
	unwrap := section.Unwrap
	if unwrap.StatusPath != "" && len(unwrap.OKStatuses) > 0 {
		if status, ok := statusAt(root, unwrap.StatusPath); ok && !statusAllowed(unwrap.OKStatuses, status) {
			return nil, false, errUpstreamStreamStatus
		}
	}
	value, ok := valueAt(root, unwrap.Path)
	if !ok {
		return nil, false, errors.New("unwrap path missing")
	}
	next, err := formatUnwrapped(unwrap.Format, value)
	if err != nil {
		return nil, false, err
	}
	if section.Done != nil && markerEquals(section.Done.UnwrappedEquals, next) {
		return t.finish(event, next, true)
	}
	next, stop, err := t.applyUsage(next, true)
	if err != nil {
		return nil, false, err
	}
	return t.finish(event, next, stop)
}

func (t *streamTransformer) finish(event *sseEvent, payload string, stop bool) ([]byte, bool, error) {
	return writeEvent(event.lines, &payload), stop, nil
}

func (t *streamTransformer) applyUsage(payload string, unwrapped bool) (string, bool, error) {
	section := t.section
	stop := false
	if section != nil && section.Done != nil && unwrapped && markerEquals(section.Done.UnwrappedEquals, payload) {
		stop = true
	}
	if section == nil || section.Usage == nil || !jsonObject(payload) {
		return payload, stop, nil
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return payload, stop, nil
	}
	changed := false
	usage := section.Usage
	changed = copyUsageNumber(obj, usage.InputTokens, "usage.prompt_tokens") || changed
	changed = copyUsageNumber(obj, usage.OutputTokens, "usage.completion_tokens") || changed
	changed = copyUsageNumber(obj, usage.CachedTokens, "usage.prompt_tokens_details.cached_tokens") || changed
	changed = copyUsageNumber(obj, usage.ReasoningTokens, "usage.completion_tokens_details.reasoning_tokens") || changed
	if !changed {
		return payload, stop, nil
	}
	encoded, err := json.Marshal(obj)
	if err != nil {
		return "", false, err
	}
	return string(encoded), stop, nil
}

func writeEvent(lines []sseLine, payload *string) []byte {
	var buf bytes.Buffer
	dataWritten := false
	for _, line := range lines {
		if line.kind == "data" {
			if dataWritten {
				continue
			}
			dataWritten = true
			if payload == nil {
				buf.WriteString(line.raw)
			} else {
				for _, part := range strings.Split(*payload, "\n") {
					buf.WriteString("data: ")
					buf.WriteString(part)
					buf.WriteByte('\n')
				}
				continue
			}
		} else {
			buf.WriteString(line.raw)
		}
		buf.WriteByte('\n')
	}
	buf.WriteByte('\n')
	return buf.Bytes()
}

func markerEquals(markers []string, value string) bool {
	for _, marker := range markers {
		if marker == value {
			return true
		}
	}
	return false
}

func decodeObject(payload string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(payload))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return nil, errors.New("payload is not an object")
	}
	return obj, nil
}

func jsonObject(payload string) bool {
	trimmed := strings.TrimSpace(payload)
	return strings.HasPrefix(trimmed, "{")
}

func valueAt(root map[string]any, path string) (any, bool) {
	parsed, err := jsplugin.ParseSecretPath(path)
	if err != nil {
		return nil, false
	}
	cur := any(root)
	for _, seg := range parsed.Segments() {
		if seg.Index {
			arr, ok := cur.([]any)
			if !ok || seg.At < 0 || seg.At >= len(arr) {
				return nil, false
			}
			cur = arr[seg.At]
			continue
		}
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		next, exists := obj[seg.Key]
		if !exists {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func statusAt(root map[string]any, path string) (int, bool) {
	value, ok := valueAt(root, path)
	if !ok || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case json.Number:
		n, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return int(n), true
	case float64:
		return int(typed), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

func statusAllowed(allowed []int, status int) bool {
	for _, item := range allowed {
		if item == status {
			return true
		}
	}
	return false
}

func formatUnwrapped(format string, value any) (string, error) {
	switch format {
	case "", "json":
		switch value.(type) {
		case map[string]any, []any:
		default:
			return "", errors.New("unwrap value is not json")
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	case "json_string":
		text, ok := value.(string)
		if !ok {
			return "", errors.New("unwrap value is not a string")
		}
		var parsed any
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			return "", err
		}
		switch parsed.(type) {
		case map[string]any, []any:
		default:
			return "", errors.New("unwrapped string is not json")
		}
		encoded, err := json.Marshal(parsed)
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	case "raw":
		text, ok := value.(string)
		if !ok {
			return "", errors.New("unwrap value is not a string")
		}
		return text, nil
	default:
		return "", fmt.Errorf("unwrap format %q", format)
	}
}

func copyUsageNumber(root map[string]any, from, to string) bool {
	if from == "" || from == to {
		return false
	}
	value, ok := valueAt(root, from)
	if !ok {
		return false
	}
	number, ok := asNumber(value)
	if !ok {
		return false
	}
	if _, exists := valueAt(root, to); exists {
		return false
	}
	return setPath(root, to, number)
}

func asNumber(value any) (any, bool) {
	switch typed := value.(type) {
	case json.Number:
		if strings.ContainsAny(typed.String(), ".eE") {
			n, err := typed.Float64()
			if err != nil {
				return nil, false
			}
			return n, true
		}
		n, err := typed.Int64()
		if err != nil {
			return nil, false
		}
		return n, true
	case float64, int, int64:
		return typed, true
	default:
		return nil, false
	}
}

func setPath(root map[string]any, path string, value any) bool {
	parsed, err := jsplugin.ParseSecretPath(path)
	if err != nil || len(parsed.Segments()) == 0 {
		return false
	}
	segs := parsed.Segments()
	cur := any(root)
	for i, seg := range segs[:len(segs)-1] {
		next := segs[i+1]
		if seg.Index {
			return false
		}
		obj, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		child, exists := obj[seg.Key]
		if !exists || child == nil {
			if next.Index {
				return false
			}
			child = map[string]any{}
			obj[seg.Key] = child
		}
		cur = child
	}
	last := segs[len(segs)-1]
	if last.Index {
		return false
	}
	obj, ok := cur.(map[string]any)
	if !ok {
		return false
	}
	if _, exists := obj[last.Key]; exists {
		return false
	}
	obj[last.Key] = value
	return true
}
