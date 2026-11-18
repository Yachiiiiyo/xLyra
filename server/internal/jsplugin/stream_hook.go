package jsplugin

import (
	"context"
	"fmt"
	"strings"
)

// HookTransformStream is the per-SSE-event stream rewriting hook.
const HookTransformStream = "transformStream"

// StreamTransformContext is the first argument passed to transformStream(ctx, event).
type StreamTransformContext struct {
	SiteType string         `json:"siteType,omitempty" ts:"siteType,optional"`
	Model    string         `json:"model,omitempty" ts:"model,optional"`
	Protocol string         `json:"protocol,omitempty" ts:"protocol,optional"`
	Settings map[string]any `json:"settings,omitempty" ts:"settings,optional,type=Record<string, SettingValue>"`
}

func (c StreamTransformContext) asMap() map[string]any {
	out := map[string]any{
		"siteType": c.SiteType,
		"model":    c.Model,
		"protocol": c.Protocol,
	}
	if len(c.Settings) > 0 {
		out["settings"] = c.Settings
	}
	return out
}

// StreamTransformEvent is the second argument passed to transformStream(ctx, event).
type StreamTransformEvent struct {
	Event string `json:"event,omitempty" ts:"event,optional"`
	Data  string `json:"data" ts:"data"`
}

func (e StreamTransformEvent) asMap() map[string]any {
	out := map[string]any{
		"data": e.Data,
	}
	if e.Event != "" {
		out["event"] = e.Event
	}
	return out
}

// StreamTransformResult is the object returned by transformStream(ctx, event).
type StreamTransformResult struct {
	Event *string `json:"event,omitempty" ts:"event,optional"`
	Data  *string `json:"data,omitempty" ts:"data,optional"`
	Drop  bool    `json:"drop,omitempty" ts:"drop,optional"`
	Done  bool    `json:"done,omitempty" ts:"done,optional"`
}

// CallTransformStream runs transformStream(ctx, event) with the 5 ms per-event budget.
func (p *Plugin) CallTransformStream(ctx context.Context, streamCtx StreamTransformContext, event StreamTransformEvent) (StreamTransformResult, []LogEntry, int, error) {
	out, logs, dropped, err := p.callHook(ctx, HookTransformStream, streamHookTimeout, 0, streamCtx.asMap(), event.asMap())
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
