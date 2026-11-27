package jsplugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// HookTransformStream is the per-SSE-event stream rewriting hook.
const HookTransformStream = "transformStream"

// HookCreateStreamFilter is the stateful stream filter factory hook.
const HookCreateStreamFilter = "createStreamFilter"

// StreamTransformContext is the first argument passed to transformStream(ctx, event)
// and createStreamFilter(ctx).
type StreamTransformContext struct {
	SiteType string         `json:"siteType,omitempty" ts:"siteType,optional"`
	Model    string         `json:"model,omitempty" ts:"model,optional"`
	Protocol string         `json:"protocol,omitempty" ts:"protocol,optional"`
	Settings map[string]any `json:"settings,omitempty" ts:"settings,optional,type=Record<string, SettingValue>"`
}

// StreamTransformEvent is the second argument passed to transformStream(ctx, event)
// and the argument passed to StreamFilter.onEvent(event).
type StreamTransformEvent struct {
	Event string `json:"event,omitempty" ts:"event,optional"`
	Data  string `json:"data" ts:"data"`
}

// StreamTransformResult is the object returned by transformStream(ctx, event),
// StreamFilter.onEvent(event), and StreamFilter.onFlush().
type StreamTransformResult struct {
	Event *string `json:"event,omitempty" ts:"event,optional"`
	Data  *string `json:"data,omitempty" ts:"data,optional"`
	Drop  bool    `json:"drop,omitempty" ts:"drop,optional"`
	Done  bool    `json:"done,omitempty" ts:"done,optional"`
}

// StreamFilterSession holds a borrowed runtime and stateful JS stream filter
// object opened by Plugin.OpenStreamFilter. Callers must call Close when the
// stream finishes so the runtime is returned to (or discarded from) its pool.
type StreamFilterSession struct {
	mu       sync.Mutex
	plugin   *Plugin
	pool     *pool
	sess     *session
	handle   *streamFilterHandle
	poisoned bool
	closed   bool
}

// HasStreamFilter reports whether the plugin exports createStreamFilter.
func (p *Plugin) HasStreamFilter() bool {
	return p.HasHook(HookCreateStreamFilter)
}

// HasFlush reports whether the open stream filter defines an onFlush method.
func (f *StreamFilterSession) HasFlush() bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.closed && f.handle != nil && f.handle.hasFlush
}

func (p *Plugin) streamHookBudget() time.Duration {
	if p != nil && p.streamTimeout > 0 {
		return p.streamTimeout
	}
	return streamHookTimeout
}

// OpenStreamFilter acquires one runtime from the plugin's hot pool and invokes
// createStreamFilter(ctx) to create a stateful SSE filter session.
func (p *Plugin) OpenStreamFilter(ctx context.Context, streamCtx StreamTransformContext) (*StreamFilterSession, error) {
	targetPool := p.poolForHook(HookCreateStreamFilter)
	if p == nil || p.program == nil || targetPool == nil {
		return nil, fmt.Errorf("plugin is not loaded")
	}
	hook, ok := p.program.hooks[HookCreateStreamFilter]
	if !ok {
		return nil, fmt.Errorf("%s hook is missing", HookCreateStreamFilter)
	}
	started := time.Now()
	sess, err := targetPool.Acquire(ctx)
	if err != nil {
		return nil, annotate(p, err, HookCreateStreamFilter, 0, time.Since(started).Milliseconds())
	}
	ctxArg := p.injectContextSettings(ctx, streamCtx)
	sess.setMaxSteps(p.maxSteps.Load())
	handle, _, _, discard, callErr := sess.openStreamFilter(ctx, p.streamHookBudget(), hook, p.buildCallEnv(ctx), ctxArg)
	p.recordSessionStats(sess)
	if callErr != nil {
		targetPool.Release(sess, discard || fatalRuntime(callErr))
		if discardLogsFromContext(ctx) {
			if call, ok := callErr.(*CallError); ok {
				call.Logs = nil
				call.DroppedLogs = 0
			}
		}
		return nil, annotate(p, callErr, HookCreateStreamFilter, 0, time.Since(started).Milliseconds())
	}
	return &StreamFilterSession{
		plugin: p,
		pool:   targetPool,
		sess:   sess,
		handle: handle,
	}, nil
}

// OnEvent invokes the stateful filter's onEvent(event) method within the
// per-event streamHookTimeout budget.
func (f *StreamFilterSession) OnEvent(ctx context.Context, event StreamTransformEvent) (StreamTransformResult, []LogEntry, int, error) {
	if f == nil {
		return StreamTransformResult{}, nil, 0, errors.New("stream filter session is nil")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return StreamTransformResult{}, nil, 0, errors.New("stream filter session is closed")
	}
	if f.poisoned {
		return StreamTransformResult{}, nil, 0, errors.New("stream filter session is poisoned")
	}
	started := time.Now()
	f.sess.setMaxSteps(f.plugin.maxSteps.Load())
	out, logs, dropped, discard, callErr := f.sess.callStreamFilter(ctx, f.plugin.streamHookBudget(), f.handle, false, f.plugin.buildCallEnv(ctx), event)
	f.plugin.recordSessionStats(f.sess)
	if discard || fatalRuntime(callErr) {
		f.poisoned = true
	}
	if discardLogsFromContext(ctx) {
		logs, dropped = nil, 0
		if call, ok := callErr.(*CallError); ok {
			call.Logs = nil
			call.DroppedLogs = 0
		}
	}
	duration := time.Since(started).Milliseconds()
	if callErr != nil {
		return StreamTransformResult{}, logs, dropped, annotate(f.plugin, callErr, HookCreateStreamFilter, 0, duration)
	}
	res, decodeErr := decodeStreamTransformResult(out)
	if decodeErr != nil {
		return StreamTransformResult{}, logs, dropped, annotate(f.plugin, decodeErr, HookCreateStreamFilter, 0, duration)
	}
	return res, logs, dropped, nil
}

// OnFlush invokes the stateful filter's optional onFlush() method within
// streamHookTimeout. When the filter does not provide onFlush, it returns a
// zero StreamTransformResult and nil error.
func (f *StreamFilterSession) OnFlush(ctx context.Context) (StreamTransformResult, []LogEntry, int, error) {
	if f == nil {
		return StreamTransformResult{}, nil, 0, errors.New("stream filter session is nil")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return StreamTransformResult{}, nil, 0, errors.New("stream filter session is closed")
	}
	if f.poisoned {
		return StreamTransformResult{}, nil, 0, errors.New("stream filter session is poisoned")
	}
	if !f.handle.hasFlush {
		return StreamTransformResult{}, nil, 0, nil
	}
	started := time.Now()
	f.sess.setMaxSteps(f.plugin.maxSteps.Load())
	out, logs, dropped, discard, callErr := f.sess.callStreamFilter(ctx, f.plugin.streamHookBudget(), f.handle, true, f.plugin.buildCallEnv(ctx), StreamTransformEvent{})
	f.plugin.recordSessionStats(f.sess)
	if discard || fatalRuntime(callErr) {
		f.poisoned = true
	}
	if discardLogsFromContext(ctx) {
		logs, dropped = nil, 0
		if call, ok := callErr.(*CallError); ok {
			call.Logs = nil
			call.DroppedLogs = 0
		}
	}
	duration := time.Since(started).Milliseconds()
	if callErr != nil {
		return StreamTransformResult{}, logs, dropped, annotate(f.plugin, callErr, HookCreateStreamFilter, 0, duration)
	}
	res, decodeErr := decodeStreamTransformResult(out)
	if decodeErr != nil {
		return StreamTransformResult{}, logs, dropped, annotate(f.plugin, decodeErr, HookCreateStreamFilter, 0, duration)
	}
	return res, logs, dropped, nil
}

// Close releases the borrowed runtime back to its pool (or discards it if the
// filter session was poisoned or exceeded MaxLiveBytes). Close is idempotent
// and safe to call concurrently.
func (f *StreamFilterSession) Close() {
	if f == nil {
		return
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	sess := f.sess
	targetPool := f.pool
	plugin := f.plugin
	handle := f.handle
	poisoned := f.poisoned
	f.sess = nil
	f.pool = nil
	f.handle = nil
	f.mu.Unlock()

	if sess == nil || targetPool == nil {
		return
	}
	discard := sess.finishStreamFilter(handle, poisoned)
	if plugin != nil {
		plugin.recordSessionStats(sess)
	}
	targetPool.Release(sess, discard)
}

// CallTransformStream runs transformStream(ctx, event) with the 5 ms per-event budget.
func (p *Plugin) CallTransformStream(ctx context.Context, streamCtx StreamTransformContext, event StreamTransformEvent) (StreamTransformResult, []LogEntry, int, error) {
	out, logs, dropped, err := p.callHook(ctx, HookTransformStream, streamHookTimeout, 0, streamCtx, event)
	if err != nil {
		return StreamTransformResult{}, logs, dropped, err
	}
	res, decodeErr := decodeStreamTransformResult(out)
	if decodeErr != nil {
		return StreamTransformResult{}, logs, dropped, annotate(p, decodeErr, HookTransformStream, 0, 0)
	}
	return res, logs, dropped, nil
}

func decodeStreamTransformResult(raw any) (StreamTransformResult, error) {
	if raw == nil {
		return StreamTransformResult{}, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return StreamTransformResult{}, shapeError("transformStream must return an object")
	}
	for key := range obj {
		switch key {
		case "event", "data", "drop", "done":
		default:
			return StreamTransformResult{}, shapeError("transformStream: unknown field %q", key)
		}
	}
	var out StreamTransformResult
	if rawEvent, exists := obj["event"]; exists && rawEvent != nil {
		str, ok := rawEvent.(string)
		if !ok {
			return StreamTransformResult{}, shapeError("transformStream.event must be a string")
		}
		if strings.ContainsAny(str, "\r\n") {
			return StreamTransformResult{}, shapeError("transformStream.event must not contain CR or LF")
		}
		out.Event = &str
	}
	if rawData, exists := obj["data"]; exists && rawData != nil {
		str, ok := rawData.(string)
		if !ok {
			return StreamTransformResult{}, shapeError("transformStream.data must be a string")
		}
		if len(str) > MaxStreamEventBytes {
			return StreamTransformResult{}, fmt.Errorf("transformStream.data exceeds %d bytes", MaxStreamEventBytes)
		}
		out.Data = &str
	}
	if rawDrop, exists := obj["drop"]; exists && rawDrop != nil {
		b, ok := rawDrop.(bool)
		if !ok {
			return StreamTransformResult{}, shapeError("transformStream.drop must be a boolean")
		}
		out.Drop = b
	}
	if rawDone, exists := obj["done"]; exists && rawDone != nil {
		b, ok := rawDone.(bool)
		if !ok {
			return StreamTransformResult{}, shapeError("transformStream.done must be a boolean")
		}
		out.Done = b
	}
	return out, nil
}
