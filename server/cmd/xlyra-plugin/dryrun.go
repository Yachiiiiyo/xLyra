package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/google/uuid"

	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/textpipeline"
)

func runDryRun(args []string) {
	fs := flag.NewFlagSet("dry-run", flag.ExitOnError)
	paramsPath := fs.String("params", "", "path to JSON file with binding params")
	hostOptsPath := fs.String("host-options", "", "path to JSON file with host options")
	endpoint := fs.String("endpoint", "", "downstream endpoint path (default: /v1/chat/completions)")
	model := fs.String("model", "dry-run", "model name passed to inspectSegments")
	dir := fs.String("dir", ".", "plugin project directory")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fatal(fmt.Errorf("input file path (or '-' for stdin) is required"))
	}
	inputPath := fs.Arg(0)

	var params map[string]any
	if *paramsPath != "" {
		raw, err := os.ReadFile(*paramsPath)
		if err != nil {
			fatal(fmt.Errorf("read --params: %w", err))
		}
		if err := json.Unmarshal(raw, &params); err != nil {
			fatal(fmt.Errorf("parse --params: %w", err))
		}
	}
	var hostOpts map[string]any
	if *hostOptsPath != "" {
		raw, err := os.ReadFile(*hostOptsPath)
		if err != nil {
			fatal(fmt.Errorf("read --host-options: %w", err))
		}
		if err := json.Unmarshal(raw, &hostOpts); err != nil {
			fatal(fmt.Errorf("parse --host-options: %w", err))
		}
	}

	var rawInput []byte
	var err error
	if inputPath == "-" {
		rawInput, err = io.ReadAll(os.Stdin)
	} else {
		rawInput, err = os.ReadFile(inputPath)
	}
	if err != nil {
		fatal(fmt.Errorf("read input: %w", err))
	}

	out, err := executeDryRun(*dir, *endpoint, *model, params, hostOpts, rawInput)
	if err != nil {
		fatal(err)
	}
	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fatal(err)
	}
	fmt.Fprintln(os.Stdout, string(encoded))
}

func executeDryRun(dir, endpoint, model string, params, hostOptValues map[string]any, rawInput []byte) (map[string]any, error) {
	built, err := buildProject(dir)
	if err != nil {
		return nil, err
	}
	_, _, plugin, err := built.assemble()
	if err != nil {
		return nil, err
	}
	if !plugin.Manifest.DeclaresGatewayPipeline() {
		return nil, fmt.Errorf("plugin %q does not declare a gatewayPipeline section", plugin.Manifest.ID)
	}
	section := *plugin.Manifest.GatewayPipeline
	mergedParams := map[string]any{}
	for _, spec := range section.Inputs {
		if !spec.IsEntity() && spec.Default != nil {
			mergedParams[spec.Name] = spec.Default
		}
	}
	for k, v := range params {
		mergedParams[k] = v
	}
	hostOpts, err := jsplugin.ResolvePipelineHostOptions(section, hostOptValues)
	if err != nil {
		return nil, err
	}
	binding, err := jsplugin.ResolvePipelineBinding(plugin, uuid.New(), "dry-run", mergedParams, hostOpts)
	if err != nil {
		return nil, err
	}

	ep := strings.TrimSpace(endpoint)
	plainText := false
	var payload map[string]any
	if err := json.Unmarshal(rawInput, &payload); err != nil || payload == nil {
		plainText = true
		if ep == "" {
			ep = "/v1/chat/completions"
		}
		payload = map[string]any{
			"model": model,
			"messages": []any{
				map[string]any{"role": "user", "content": string(rawInput)},
			},
		}
	} else if ep == "" {
		ep = "/v1/chat/completions"
	}

	result := textpipeline.Run(context.Background(), binding, textpipeline.Request{
		Endpoint: ep,
		Model:    model,
		Payload:  payload,
	}, textpipeline.Options{NoCache: true})

	type findingView struct {
		SegmentID   string  `json:"segment_id"`
		Role        string  `json:"role,omitempty"`
		Field       string  `json:"field"`
		Start       int     `json:"start"`
		End         int     `json:"end"`
		Type        string  `json:"type"`
		Source      string  `json:"source,omitempty"`
		Score       float64 `json:"score,omitempty"`
		Placeholder string  `json:"placeholder,omitempty"`
		Applied     bool    `json:"applied"`
	}
	findings := make([]findingView, 0)
	for i, seg := range result.Segments {
		appliedBySpan := map[[3]any]string{}
		if i < len(result.Applied) {
			for _, ap := range result.Applied[i] {
				appliedBySpan[[3]any{ap.Start, ap.End, ap.Type}] = ap.Placeholder
			}
		}
		if i < len(result.Findings) {
			for _, f := range result.Findings[i] {
				ph, ok := appliedBySpan[[3]any{f.Start, f.End, f.Type}]
				findings = append(findings, findingView{
					SegmentID: seg.ID, Role: seg.Role, Field: seg.Field,
					Start: f.Start, End: f.End, Type: f.Type, Source: f.Source, Score: f.Score,
					Placeholder: ph, Applied: ok,
				})
			}
		}
		if i < len(result.Applied) {
			for _, ap := range result.Applied[i] {
				if ap.Propagated {
					findings = append(findings, findingView{
						SegmentID: seg.ID, Role: seg.Role, Field: seg.Field,
						Start: ap.Start, End: ap.End, Type: ap.Type, Source: "propagated",
						Placeholder: ap.Placeholder, Applied: true,
					})
				}
			}
		}
	}
	outPayload := payload
	if result.Changed && result.Payload != nil {
		outPayload = result.Payload
	}
	out := map[string]any{
		"outcome":             result.Metrics.Outcome,
		"changed":             result.Changed,
		"metrics":             result.Metrics,
		"findings":            findings,
		"transformed_payload": outPayload,
	}
	if result.Failure != nil {
		out["failure"] = map[string]any{
			"status":  result.Failure.Status,
			"code":    result.Failure.Code,
			"message": result.Failure.Message,
		}
	}
	if plainText {
		transformedText := string(rawInput)
		if msgs, ok := outPayload["messages"].([]any); ok {
			for _, rawMsg := range msgs {
				if m, ok := rawMsg.(map[string]any); ok && m["role"] == "user" {
					if content, ok := m["content"].(string); ok {
						transformedText = content
					}
				}
			}
		}
		out["transformed_text"] = transformedText
	}
	return out, nil
}
