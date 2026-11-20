package gateway

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"xlyra/server/internal/site"
)

const (
	// defaultStreamFirstOutputTimeout bounds how long a streaming attempt may sit
	// after the response headers without producing any downstream output while a
	// later route candidate is waiting to take over. It must stay below the
	// streaming failover budget minus the response header timeout, otherwise the
	// failover this exists for would already be out of budget.
	defaultStreamFirstOutputTimeout = 50 * time.Second

	// lastCandidateFirstOutputTimeout is the backstop for the last candidate.
	// Nothing can take over, so it is generous: successful streams were seen
	// answering after 90s, and this only exists to free a request that is hung
	// for good instead of holding it until the upstream gives up on its own.
	lastCandidateFirstOutputTimeout = 120 * time.Second

	upstreamFirstOutputTimeoutErrorType = "upstream_first_output_timeout"
)

type firstOutputContextKey struct{}

type firstOutputPlan struct {
	// fallbackAvailable reports that a later route candidate can take over.
	fallbackAvailable bool
}

func withFirstOutputPlan(ctx context.Context, fallbackAvailable bool) context.Context {
	return context.WithValue(ctx, firstOutputContextKey{}, firstOutputPlan{fallbackAvailable: fallbackAvailable})
}

func firstOutputPlanFromContext(ctx context.Context) firstOutputPlan {
	plan, _ := ctx.Value(firstOutputContextKey{}).(firstOutputPlan)
	return plan
}

type firstOutputTimeoutContextKey struct{}

func withFirstOutputTimeout(ctx context.Context, timeout time.Duration) context.Context {
	return context.WithValue(ctx, firstOutputTimeoutContextKey{}, timeout)
}

func firstOutputTimeoutFromContext(ctx context.Context) time.Duration {
	timeout, _ := ctx.Value(firstOutputTimeoutContextKey{}).(time.Duration)
	return timeout
}

// streamFirstOutputTimeout resolves the first-output timeout of one streaming
// attempt. Zero disables the watchdog. A site that sets first_output_timeout_ms
// always gets it; every other site gets the default for its position: the short
// one while a fallback candidate exists, the generous backstop for the last
// candidate. Image and audio streams legitimately stay silent for a long time.
func firstOutputExemptEndpoint(downstreamPath string) bool {
	switch downstreamPath {
	case gatewayEndpointImagesGenerations, gatewayEndpointImagesEdits, gatewayEndpointAudioSpeech:
		return true
	default:
		return false
	}
}

func streamFirstOutputTimeout(cfg *site.GatewayConfig, plan firstOutputPlan, stream bool, imageOrAudio bool) time.Duration {
	if !stream || imageOrAudio {
		return 0
	}
	if cfg != nil && cfg.FirstOutputTimeoutMS != nil && *cfg.FirstOutputTimeoutMS > 0 {
		return time.Duration(*cfg.FirstOutputTimeoutMS) * time.Millisecond
	}
	if !plan.fallbackAvailable {
		return lastCandidateFirstOutputTimeout
	}
	return defaultStreamFirstOutputTimeout
}

// firstOutputWatchdog closes the upstream body when nothing has been written to
// the downstream writer within the timeout, which turns a silent hang into a
// read error the caller can classify and fail over from.
type firstOutputWatchdog struct {
	mu      sync.Mutex
	seen    bool
	fired   bool
	timer   *time.Timer
	writer  http.ResponseWriter
	timeout time.Duration
}

func newFirstOutputWatchdog(dst http.ResponseWriter, body io.Closer, timeout time.Duration) *firstOutputWatchdog {
	watchdog := &firstOutputWatchdog{timeout: timeout}
	watchdog.writer = &firstOutputWriter{ResponseWriter: dst, watchdog: watchdog}
	watchdog.timer = time.AfterFunc(timeout, func() {
		watchdog.mu.Lock()
		if watchdog.seen {
			watchdog.mu.Unlock()
			return
		}
		watchdog.fired = true
		watchdog.mu.Unlock()
		_ = body.Close()
	})
	return watchdog
}

func (w *firstOutputWatchdog) markOutput() {
	w.mu.Lock()
	w.seen = true
	w.mu.Unlock()
	w.timer.Stop()
}

func (w *firstOutputWatchdog) Stop() {
	w.timer.Stop()
}

// TimedOut reports whether the watchdog closed the upstream body before any
// output reached the downstream writer.
func (w *firstOutputWatchdog) TimedOut() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fired && !w.seen
}

// firstOutputWriter forwards to the downstream writer and reports the first
// non-empty write. It keeps the optional interfaces the stream protocols and
// the downstream SSE session rely on.
type firstOutputWriter struct {
	http.ResponseWriter
	watchdog *firstOutputWatchdog
}

func (w *firstOutputWriter) Write(body []byte) (int, error) {
	if len(body) > 0 {
		w.watchdog.markOutput()
	}
	return w.ResponseWriter.Write(body)
}

func (w *firstOutputWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *firstOutputWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *firstOutputWriter) WriteSSEFailure(failure downstreamSSEFailure) bool {
	if writer, ok := w.ResponseWriter.(downstreamSSEFailureWriter); ok {
		return writer.WriteSSEFailure(failure)
	}
	return false
}

func (w *firstOutputWriter) FinishSSE() {
	if writer, ok := w.ResponseWriter.(downstreamSSELifecycleWriter); ok {
		writer.FinishSSE()
	}
}
