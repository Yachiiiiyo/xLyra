package textpipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/google/uuid"

	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/textxform"
)

func init() {
	jsplugin.RegisterGatewayPipelineFixtureRunner(RunFixture)
}

// RunFixture executes an end-to-end gatewayPipeline fixture using the same Go
// codec, native matcher, hook runner, transformer and stream restorer as the
// production gateway.
func RunFixture(ctx context.Context, plugin *jsplugin.Plugin, fixture jsplugin.Fixture) error {
	if plugin == nil || !plugin.Manifest.DeclaresGatewayPipeline() {
		return fmt.Errorf("gatewayPipeline fixture requires a gatewayPipeline manifest section")
	}
	section := *plugin.Manifest.GatewayPipeline
	params := defaultPipelineParams(section)
	var hostOptValues map[string]any
	if fixture.Binding != nil {
		for k, v := range fixture.Binding.Params {
			params[k] = v
		}
		hostOptValues = fixture.Binding.HostOptions
	}
	hostOpts, err := jsplugin.ResolvePipelineHostOptions(section, hostOptValues)
	if err != nil {
		return fmt.Errorf("binding.hostOptions: %w", err)
	}
	binding, err := jsplugin.ResolvePipelineBinding(plugin, uuid.New(), "fixture", params, hostOpts)
	if err != nil {
		return fmt.Errorf("resolve fixture binding: %w", err)
	}

	endpoint := strings.TrimSpace(fixture.Endpoint)
	if endpoint == "" {
		endpoint = strings.TrimSpace(fixture.Ctx.Endpoint)
	}
	if endpoint == "" {
		endpoint = "/v1/chat/completions"
	}
	model := strings.TrimSpace(fixture.Ctx.Model)
	if model == "" {
		if m, ok := fixture.Payload["model"].(string); ok {
			model = m
		}
	}

	result := Run(ctx, binding, Request{
		Endpoint: endpoint,
		Model:    model,
		Payload:  fixture.Payload,
	}, Options{
		NoCache: true,
	})

	if fixture.Expect.Status != 0 || fixture.Expect.ErrorCode != "" || fixture.Expect.Error != "" {
		if result.Failure == nil {
			return fmt.Errorf("expected pipeline failure (status=%d code=%q err=%q), got outcome %q",
				fixture.Expect.Status, fixture.Expect.ErrorCode, fixture.Expect.Error, result.Metrics.Outcome)
		}
		if fixture.Expect.Status != 0 && result.Failure.Status != fixture.Expect.Status {
			return fmt.Errorf("failure status = %d, want %d", result.Failure.Status, fixture.Expect.Status)
		}
		if fixture.Expect.ErrorCode != "" && result.Failure.Code != fixture.Expect.ErrorCode {
			return fmt.Errorf("failure code = %q, want %q", result.Failure.Code, fixture.Expect.ErrorCode)
		}
		if fixture.Expect.Error != "" && !strings.Contains(result.Failure.Message, fixture.Expect.Error) && !strings.Contains(result.ErrorKind, fixture.Expect.Error) {
			return fmt.Errorf("failure %q (%s) does not match %q", result.Failure.Message, result.ErrorKind, fixture.Expect.Error)
		}
		return nil
	}
	if result.Failure != nil {
		return fmt.Errorf("unexpected pipeline failure: HTTP %d %s: %s", result.Failure.Status, result.Failure.Code, result.Failure.Message)
	}

	actualUpstream := fixture.Payload
	if result.Changed && result.Payload != nil {
		actualUpstream = result.Payload
	}
	if fixture.Expect.UpstreamPayload != nil {
		if err := compareJSONSemantic("upstreamPayload", actualUpstream, fixture.Expect.UpstreamPayload); err != nil {
			return err
		}
	}

	if fixture.Upstream == nil {
		return nil
	}
	var restoreSession *textxform.Session
	if result.Session != nil && result.Session.Reversible && result.Session.Len() > 0 {
		restoreSession = result.Session
	}

	if len(fixture.Upstream.SSEChunks) > 0 {
		gotSSE := restoreFixtureSSEChunks(restoreSession, fixture.Upstream.SSEChunks)
		if fixture.Expect.DownstreamSSE != "" {
			if gotSSE != fixture.Expect.DownstreamSSE {
				return fmt.Errorf("downstreamSSE mismatch:\ngot:\n%s\nwant:\n%s", gotSSE, fixture.Expect.DownstreamSSE)
			}
		}
		if fixture.Expect.DownstreamResponse != nil {
			events, err := parseSSEJSONEvents(gotSSE)
			if err != nil {
				return fmt.Errorf("parse restored SSE events: %w", err)
			}
			if err := compareJSONSemantic("downstreamResponse", events, fixture.Expect.DownstreamResponse); err != nil {
				return err
			}
		}
		return nil
	}

	contentType := fixture.Upstream.ContentType
	var rawBody []byte
	if fixture.Upstream.JSON != nil {
		encoded, err := json.Marshal(fixture.Upstream.JSON)
		if err != nil {
			return fmt.Errorf("marshal upstream.json: %w", err)
		}
		rawBody = encoded
		if contentType == "" {
			contentType = "application/json"
		}
	} else {
		rawBody = []byte(fixture.Upstream.Body)
		if contentType == "" {
			contentType = "text/plain"
		}
	}
	restored := RestoreBody(restoreSession, rawBody, contentType)
	if fixture.Expect.DownstreamResponse != nil {
		switch want := fixture.Expect.DownstreamResponse.(type) {
		case string:
			if string(restored) != want {
				return fmt.Errorf("downstreamResponse = %q, want %q", string(restored), want)
			}
		default:
			var got any
			if err := json.Unmarshal(restored, &got); err != nil {
				return fmt.Errorf("decode restored downstream response: %w", err)
			}
			if err := compareJSONSemantic("downstreamResponse", got, want); err != nil {
				return err
			}
		}
	}
	return nil
}

func defaultPipelineParams(section jsplugin.GatewayPipelineSection) map[string]any {
	params := map[string]any{}
	for _, spec := range section.Inputs {
		if !spec.IsEntity() && spec.Default != nil {
			params[spec.Name] = spec.Default
		}
	}
	return params
}

func restoreFixtureSSEChunks(session *textxform.Session, chunks []string) string {
	restorer := NewStreamRestorer(session)
	var pending bytes.Buffer
	var out bytes.Buffer
	emitBlock := func(block []byte) {
		for _, b := range restorer.ApplyAll(block) {
			out.Write(b)
		}
	}
	for _, chunk := range chunks {
		pending.WriteString(chunk)
		for {
			raw := pending.Bytes()
			end, delim := indexSSEDelimiter(raw)
			if end < 0 {
				break
			}
			block := append([]byte(nil), raw[:end+delim]...)
			pending.Next(end + delim)
			emitBlock(block)
		}
	}
	if pending.Len() > 0 {
		emitBlock(append([]byte(nil), pending.Bytes()...))
		pending.Reset()
	}
	for _, b := range restorer.Flush() {
		out.Write(b)
	}
	return out.String()
}

func parseSSEJSONEvents(raw string) ([]any, error) {
	var events []any
	for _, block := range SplitSSEEvents([]byte(raw)) {
		_, data, commentOnly, done := parseSSEEvent(block)
		if commentOnly || done || len(data) == 0 {
			continue
		}
		var v any
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		events = append(events, v)
	}
	return events, nil
}

func compareJSONSemantic(label string, got, want any) error {
	gb, err := json.Marshal(got)
	if err != nil {
		return fmt.Errorf("%s: marshal actual: %w", label, err)
	}
	wb, err := json.Marshal(want)
	if err != nil {
		return fmt.Errorf("%s: marshal expected: %w", label, err)
	}
	var gn, wn any
	_ = json.Unmarshal(gb, &gn)
	_ = json.Unmarshal(wb, &wn)
	if !reflect.DeepEqual(gn, wn) {
		return fmt.Errorf("%s mismatch:\ngot:  %s\nwant: %s", label, string(gb), string(wb))
	}
	return nil
}
