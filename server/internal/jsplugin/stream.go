package jsplugin

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	streamPathMaxDepth = 8
	streamPathMaxRunes = 128

	streamFormatJSON       = "json"
	streamFormatJSONString = "json_string"
	streamFormatRaw        = "raw"
)

// StreamSection is a declarative SSE transform. An absent section leaves the
// upstream body unchanged.
type StreamSection struct {
	Unwrap *StreamUnwrap `json:"unwrap,omitempty"`
	Done   *StreamDone   `json:"done,omitempty"`
	Usage  *StreamUsage  `json:"usage,omitempty"`
}

// StreamUnwrap pulls the real chunk out of each SSE data payload.
type StreamUnwrap struct {
	Path       string `json:"path"`
	Format     string `json:"format,omitempty"`
	StatusPath string `json:"statusPath,omitempty"`
	OKStatuses []int  `json:"okStatuses,omitempty"`
}

// StreamDone names extra end-of-stream markers.
type StreamDone struct {
	DataEquals      []string `json:"dataEquals,omitempty"`
	UnwrappedEquals []string `json:"unwrappedEquals,omitempty"`
	Events          []string `json:"events,omitempty"`
	CloseOnDone     bool     `json:"closeOnDone,omitempty"`
}

// StreamUsage maps token counts onto the standard usage object.
// cost and billable are not applied in this stage.
type StreamUsage struct {
	InputTokens     string `json:"inputTokens,omitempty"`
	OutputTokens    string `json:"outputTokens,omitempty"`
	CachedTokens    string `json:"cachedTokens,omitempty"`
	ReasoningTokens string `json:"reasoningTokens,omitempty"`
}

// Empty reports whether the section configures nothing.
func (s *StreamSection) Empty() bool {
	if s == nil {
		return true
	}
	return s.Unwrap == nil && s.Done == nil && s.Usage == nil
}

// ParseStreamSection decodes and checks a stream object.
// A nil or empty raw value returns a nil section.
func ParseStreamSection(raw json.RawMessage) (*StreamSection, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var section StreamSection
	if err := json.Unmarshal(raw, &section); err != nil {
		return nil, fmt.Errorf("stream: %w", err)
	}
	if err := validateStreamSection(&section, "stream"); err != nil {
		return nil, err
	}
	if section.Empty() {
		return nil, nil
	}
	return &section, nil
}

func validateStreamSection(section *StreamSection, what string) error {
	if section == nil || section.Empty() {
		return nil
	}
	if section.Unwrap != nil {
		unwrap := section.Unwrap
		if strings.TrimSpace(unwrap.Path) == "" {
			return fmt.Errorf("%s.unwrap.path is required", what)
		}
		if err := validateStreamPath(unwrap.Path, what+".unwrap.path"); err != nil {
			return err
		}
		switch unwrap.Format {
		case "":
			unwrap.Format = streamFormatJSON
		case streamFormatJSON, streamFormatJSONString, streamFormatRaw:
		default:
			return fmt.Errorf("%s.unwrap.format %q is not supported", what, unwrap.Format)
		}
		if unwrap.StatusPath != "" {
			if err := validateStreamPath(unwrap.StatusPath, what+".unwrap.statusPath"); err != nil {
				return err
			}
		}
		if len(unwrap.OKStatuses) > 32 {
			return fmt.Errorf("%s.unwrap.okStatuses exceeds 32 items", what)
		}
		for _, status := range unwrap.OKStatuses {
			if status < 100 || status > 599 {
				return fmt.Errorf("%s.unwrap.okStatuses: %d is not an HTTP status", what, status)
			}
		}
	}
	if section.Done != nil {
		done := section.Done
		if err := validateStreamMarkers(done.DataEquals, what+".done.dataEquals"); err != nil {
			return err
		}
		if err := validateStreamMarkers(done.UnwrappedEquals, what+".done.unwrappedEquals"); err != nil {
			return err
		}
		if err := validateStreamMarkers(done.Events, what+".done.events"); err != nil {
			return err
		}
	}
	if section.Usage != nil {
		usage := section.Usage
		for _, item := range []struct {
			path  string
			field string
		}{
			{usage.InputTokens, "inputTokens"},
			{usage.OutputTokens, "outputTokens"},
			{usage.CachedTokens, "cachedTokens"},
			{usage.ReasoningTokens, "reasoningTokens"},
		} {
			if item.path == "" {
				continue
			}
			if err := validateStreamPath(item.path, what+".usage."+item.field); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateStreamMarkers(values []string, what string) error {
	if len(values) > 16 {
		return fmt.Errorf("%s exceeds 16 items", what)
	}
	for _, value := range values {
		if value == "" || len(value) > streamPathMaxRunes {
			return fmt.Errorf("%s contains an empty or oversized marker", what)
		}
	}
	return nil
}

func validateStreamPath(raw, what string) error {
	if len([]rune(raw)) > streamPathMaxRunes {
		return fmt.Errorf("%s exceeds %d characters", what, streamPathMaxRunes)
	}
	parsed, err := ParseSecretPath(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if len(parsed.segs) > streamPathMaxDepth {
		return fmt.Errorf("%s exceeds depth %d", what, streamPathMaxDepth)
	}
	return nil
}
