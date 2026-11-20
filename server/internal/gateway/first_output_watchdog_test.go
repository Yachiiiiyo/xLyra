package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	routeengine "xlyra/server/internal/router"
	"xlyra/server/internal/site"
)

// blockingStreamProtocol reads the upstream body the way a real stream proxy
// does: it blocks until bytes arrive or the body is closed. When writeFirst is
// set it forwards the first chunk downstream so the watchdog sees output.
type blockingStreamProtocol struct {
	testGatewayProtocol
	writeFirst bool
}

func (p *blockingStreamProtocol) ProxyStream(_ context.Context, w http.ResponseWriter, resp *http.Response, _ time.Time, _ routeengine.Candidate) (streamCaptureState, bool, error) {
	buf := make([]byte, 64)
	n, err := resp.Body.Read(buf)
	started := false
	if p.writeFirst && n > 0 {
		_, _ = w.Write(buf[:n])
		started = true
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return streamCaptureState{endReason: "upstream_stream_read_failed"}, started, err
	}
	return streamCaptureState{streamCompleted: started, endReason: "done"}, started, nil
}

func TestHandleStreamResponseFirstOutputTimeoutFailsUnstartedStream(t *testing.T) {
	t.Parallel()

	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	ctx := withFirstOutputTimeout(context.Background(), 40*time.Millisecond)

	started := time.Now()
	result := Handler{logger: gatewayDiscardLogger()}.handleStreamResponse(
		ctx,
		httptest.NewRecorder(),
		"req-first-output",
		uuid.New(),
		uuid.New(),
		routeengine.Candidate{Site: routeengine.CandidateSite{SiteType: "openai"}},
		&blockingStreamProtocol{},
		&http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader},
		gatewayAttemptResult{attempt: 1, downstreamPath: gatewayEndpointResponses, stream: true},
		started,
	)

	if result.success || result.responseStarted {
		t.Fatalf("result = %#v, want unstarted failure", result)
	}
	if result.errorType != upstreamFirstOutputTimeoutErrorType || result.statusCode != http.StatusBadGateway {
		t.Fatalf("errorType/status = %q/%d, want %q/502", result.errorType, result.statusCode, upstreamFirstOutputTimeoutErrorType)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("handleStreamResponse blocked for %s despite the watchdog", elapsed)
	}
	if !upstreamStreamFailure(result) {
		t.Fatal("first output timeout must count as a stream failure for cooldown streak tracking")
	}
	if decision := credentialFailoverDecisionForResult(result, true); decision.ShouldTryNextCredential {
		t.Fatalf("decision = %#v, a hung site should hand over to the next site, not the next key", decision)
	}
	input, ok := cooldownInputForFailure(routeengine.Candidate{Site: routeengine.CandidateSite{ID: uuid.New()}, Model: routeengine.CandidateModel{SiteModelID: uuid.New()}}, result)
	if !ok || input.Scope != "model" || input.SiteCredentialID != nil {
		t.Fatalf("cooldown input = %#v ok=%v, want a model-scoped cooldown", input, ok)
	}
}

func TestHandleStreamResponseFirstOutputWatchdogStaysQuietAfterOutput(t *testing.T) {
	t.Parallel()

	reader, writer := io.Pipe()
	ctx := withFirstOutputTimeout(context.Background(), 60*time.Millisecond)
	go func() {
		_, _ = writer.Write([]byte("data: hi\n\n"))
		time.Sleep(150 * time.Millisecond)
		_ = writer.Close()
	}()

	recorder := httptest.NewRecorder()
	result := Handler{logger: gatewayDiscardLogger()}.handleStreamResponse(
		ctx,
		recorder,
		"req-first-output-ok",
		uuid.New(),
		uuid.New(),
		routeengine.Candidate{Site: routeengine.CandidateSite{SiteType: "openai"}},
		&blockingStreamProtocol{writeFirst: true},
		&http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader},
		gatewayAttemptResult{attempt: 1, downstreamPath: gatewayEndpointResponses, stream: true},
		time.Now(),
	)

	if !result.success || result.errorType == upstreamFirstOutputTimeoutErrorType {
		t.Fatalf("result = %#v, want success once output has started", result)
	}
	if recorder.Body.String() != "data: hi\n\n" {
		t.Fatalf("downstream body = %q", recorder.Body.String())
	}
}

func TestStreamFirstOutputTimeoutResolution(t *testing.T) {
	t.Parallel()

	explicit := 12_000
	zero := 0
	tests := []struct {
		name         string
		cfg          *site.GatewayConfig
		fallback     bool
		stream       bool
		imageOrAudio bool
		want         time.Duration
	}{
		{name: "default with fallback", fallback: true, stream: true, want: defaultStreamFirstOutputTimeout},
		{name: "last candidate is never cut off", fallback: false, stream: true, want: 0},
		{name: "non stream", fallback: true, stream: false, want: 0},
		{name: "image or audio exempt", fallback: true, stream: true, imageOrAudio: true, want: 0},
		{name: "site override applies without fallback", cfg: &site.GatewayConfig{FirstOutputTimeoutMS: &explicit}, stream: true, want: 12 * time.Second},
		{name: "site override still skips images", cfg: &site.GatewayConfig{FirstOutputTimeoutMS: &explicit}, stream: true, imageOrAudio: true, want: 0},
		{name: "non positive override falls back to default", cfg: &site.GatewayConfig{FirstOutputTimeoutMS: &zero}, fallback: true, stream: true, want: defaultStreamFirstOutputTimeout},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := streamFirstOutputTimeout(tt.cfg, firstOutputPlan{fallbackAvailable: tt.fallback}, tt.stream, tt.imageOrAudio); got != tt.want {
				t.Fatalf("streamFirstOutputTimeout() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestFirstOutputWriterKeepsDownstreamCapabilities(t *testing.T) {
	t.Parallel()

	cancelled := false
	session := newDownstreamSSESession(context.Background(), httptest.NewRecorder(), gatewayEndpointResponses, func() { cancelled = true })
	t.Cleanup(session.Close)
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	watchdog := newFirstOutputWatchdog(session, reader, time.Minute)
	t.Cleanup(watchdog.Stop)

	if _, ok := watchdog.writer.(http.Flusher); !ok {
		t.Fatal("wrapped writer must stay an http.Flusher")
	}
	if _, ok := watchdog.writer.(downstreamSSELifecycleWriter); !ok {
		t.Fatal("wrapped writer must keep the SSE lifecycle hook")
	}
	if _, ok := watchdog.writer.(downstreamSSEFailureWriter); !ok {
		t.Fatal("wrapped writer must keep the SSE failure hook")
	}
	if watchdog.TimedOut() {
		t.Fatal("watchdog fired immediately")
	}
	if _, err := watchdog.writer.Write([]byte("data: x\n\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if cancelled {
		t.Fatal("a successful write must not cancel the session")
	}
}
