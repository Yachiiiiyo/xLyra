// Package textpipeline is the protocol work of a gateway pipeline: it splits
// request payloads into text segments and writes them back, runs a binding's
// matcher, hook and transform over them, and restores placeholders in
// responses. The gateway, the admin dry run and plugin fixtures share it.
package textpipeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
)

// Downstream paths the codec knows. They are the gateway's endpoint paths.
const (
	EndpointChatCompletions   = "/v1/chat/completions"
	EndpointResponses         = "/v1/responses"
	EndpointMessages          = "/v1/messages"
	EndpointGeminiGenerate    = "/v1beta/models/{model}:generateContent"
	EndpointEmbeddings        = "/v1/embeddings"
	EndpointImagesGenerations = "/v1/images/generations"
	EndpointImagesEdits       = "/v1/images/edits"
)

// Segment roles and fields. A segment is one piece of request text the
// gateway pipeline inspects; the rest of the request is never shown.
const (
	SegmentRoleSystem    = "system"
	SegmentRoleUser      = "user"
	SegmentRoleAssistant = "assistant"
	SegmentRoleTool      = "tool"

	SegmentFieldInstructions   = "instructions"
	SegmentFieldMessageText    = "message.text"
	SegmentFieldToolArguments  = "tool.arguments"
	SegmentFieldToolResult     = "tool.result"
	SegmentFieldToolDefinition = "tool.definition"
	SegmentFieldPrompt         = "prompt"
	SegmentFieldEmbeddingInput = "embedding.input"
)

var (
	// ErrUnsupportedEndpoint means the codec cannot extract text from the endpoint.
	ErrUnsupportedEndpoint = errors.New("endpoint is not supported by the gateway pipeline")
	// ErrPayloadTooLarge means the extracted text is over the pipeline limit.
	ErrPayloadTooLarge = errors.New("extracted text exceeds the gateway pipeline limit")
)

// Segment is one extracted text with where it came from.
type Segment struct {
	ID    string
	Role  string
	Field string
	Text  string
	// IsJSONString marks a leaf of a JSON document that is itself a string
	// field, such as tool arguments; it is written back re-encoded.
	IsJSONString bool
}

// SegmentCodecOptions controls extraction.
type SegmentCodecOptions struct {
	ScanToolDefinitions bool
	// MaxInspectBytes bounds the total text extracted; 0 means no bound.
	MaxInspectBytes int
}

// SegmentDocument is a payload split into segments that can be written back.
type SegmentDocument struct {
	Endpoint string
	Payload  map[string]any
	Segments []Segment

	setters    []func(string)
	finalizers []func()
	hint       func(string)
	opts       SegmentCodecOptions
	total      int
	err        error
}

// SegmentCodecSupports reports whether the codec handles a downstream path.
func SegmentCodecSupports(endpointPath string) bool {
	switch endpointPath {
	case EndpointChatCompletions, EndpointResponses, EndpointMessages,
		EndpointGeminiGenerate, EndpointEmbeddings,
		EndpointImagesGenerations, EndpointImagesEdits:
		return true
	}
	return false
}

// ExtractSegments splits payload into segments in request order. It reads
// payload in place; ApplySegmentReplacements writes back into the same maps.
func ExtractSegments(endpointPath string, payload map[string]any, opts SegmentCodecOptions) (*SegmentDocument, error) {
	if !SegmentCodecSupports(endpointPath) {
		return nil, ErrUnsupportedEndpoint
	}
	doc := &SegmentDocument{Endpoint: endpointPath, Payload: payload, opts: opts}
	if payload == nil {
		return doc, nil
	}
	switch endpointPath {
	case EndpointChatCompletions:
		doc.extractChat()
	case EndpointResponses:
		doc.extractResponses()
	case EndpointMessages:
		doc.extractAnthropic()
	case EndpointGeminiGenerate:
		doc.extractGemini()
	case EndpointEmbeddings:
		doc.extractEmbeddings()
	case EndpointImagesGenerations, EndpointImagesEdits:
		doc.addField(payload, "prompt", "", SegmentFieldPrompt)
	}
	if doc.err != nil {
		return nil, doc.err
	}
	return doc, nil
}

// ApplySegmentReplacements writes texts back by segment position; a nil
// slice or an equal text leaves the segment as it was. systemHint, when not
// empty, is appended to the system prompt of protocols that have one.
func ApplySegmentReplacements(doc *SegmentDocument, texts []string, systemHint string) {
	if doc == nil {
		return
	}
	for i, text := range texts {
		if i >= len(doc.Segments) || text == doc.Segments[i].Text {
			continue
		}
		doc.setters[i](text)
		doc.Segments[i].Text = text
	}
	for _, finalize := range doc.finalizers {
		finalize()
	}
	if systemHint != "" && doc.hint != nil {
		doc.hint(systemHint)
	}
}

// TotalBytes is the size of all extracted text.
func (d *SegmentDocument) TotalBytes() int {
	if d == nil {
		return 0
	}
	return d.total
}

func (d *SegmentDocument) add(role, field, text string, jsonString bool, set func(string)) {
	if d.err != nil || text == "" {
		return
	}
	d.total += len(text)
	if d.opts.MaxInspectBytes > 0 && d.total > d.opts.MaxInspectBytes {
		d.err = ErrPayloadTooLarge
		return
	}
	d.Segments = append(d.Segments, Segment{
		ID: "seg_" + strconv.Itoa(len(d.Segments)), Role: role, Field: field, Text: text, IsJSONString: jsonString,
	})
	d.setters = append(d.setters, set)
}

// addField adds container[key] when it is a string.
func (d *SegmentDocument) addField(container map[string]any, key, role, field string) {
	text, ok := container[key].(string)
	if !ok {
		return
	}
	d.add(role, field, text, false, func(next string) { container[key] = next })
}

// addTextOrParts adds container[key] when it is a string, or the text of
// each part of an array whose type is one of partTypes.
func (d *SegmentDocument) addTextOrParts(container map[string]any, key, role, field string, partTypes ...string) {
	switch value := container[key].(type) {
	case string:
		d.add(role, field, value, false, func(next string) { container[key] = next })
	case []any:
		for _, raw := range value {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			partType := stringField(part["type"])
			for _, want := range partTypes {
				if partType == want {
					d.addField(part, "text", role, field)
					break
				}
			}
		}
	}
}

// addLeaves adds every string leaf of an object or array value in place.
func (d *SegmentDocument) addLeaves(value any, role, field string, jsonString bool, changed *bool) {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range sortedKeys(typed) {
			if text, ok := typed[key].(string); ok {
				container, name := typed, key
				d.add(role, field, text, jsonString, func(next string) {
					container[name] = next
					if changed != nil {
						*changed = true
					}
				})
				continue
			}
			d.addLeaves(typed[key], role, field, jsonString, changed)
		}
	case []any:
		for i, item := range typed {
			if text, ok := item.(string); ok {
				container, index := typed, i
				d.add(role, field, text, jsonString, func(next string) {
					container[index] = next
					if changed != nil {
						*changed = true
					}
				})
				continue
			}
			d.addLeaves(item, role, field, jsonString, changed)
		}
	}
}

// addJSONString adds container[key], a string holding JSON such as tool
// arguments. An object or array is split into its string leaves and written
// back re-encoded; anything else is one plain segment.
func (d *SegmentDocument) addJSONString(container map[string]any, key, role, field string) {
	raw, ok := container[key].(string)
	if !ok || raw == "" {
		return
	}
	parsed, err := decodeJSON([]byte(raw))
	if err == nil {
		switch parsed.(type) {
		case map[string]any, []any:
			changed := false
			d.addLeaves(parsed, role, field, true, &changed)
			d.finalizers = append(d.finalizers, func() {
				if !changed {
					return
				}
				if encoded, err := MarshalJSON(parsed); err == nil {
					container[key] = encoded
				}
			})
			return
		}
	}
	d.add(role, field, raw, false, func(next string) { container[key] = next })
}

// MarshalJSON encodes value without escaping HTML characters.
func MarshalJSON(value any) (string, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// schemaTextKeys are the keys of a JSON schema whose values are prose a
// pipeline may inspect. Type names, property names and patterns are kept.
var schemaTextKeys = map[string]bool{"description": true, "title": true, "examples": true, "default": true, "const": true, "enum": true}

// addSchemaText adds the prose of a tool's JSON schema.
func (d *SegmentDocument) addSchemaText(value any, role, field string) {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range sortedKeys(typed) {
			if schemaTextKeys[key] {
				if text, ok := typed[key].(string); ok {
					container, name := typed, key
					d.add(role, field, text, false, func(next string) { container[name] = next })
				} else {
					d.addLeaves(typed[key], role, field, false, nil)
				}
				continue
			}
			d.addSchemaText(typed[key], role, field)
		}
	case []any:
		for _, item := range typed {
			d.addSchemaText(item, role, field)
		}
	}
}

func chatSegmentRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "system", "developer":
		return SegmentRoleSystem
	case "assistant", "model":
		return SegmentRoleAssistant
	case "tool", "function":
		return SegmentRoleTool
	case "user":
		return SegmentRoleUser
	}
	return ""
}

func (d *SegmentDocument) extractChat() {
	messages, _ := d.Payload["messages"].([]any)
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := chatSegmentRole(stringField(message["role"]))
		field := SegmentFieldMessageText
		if role == SegmentRoleTool {
			field = SegmentFieldToolResult
		}
		d.addTextOrParts(message, "content", role, field, "text")
		if calls, ok := message["tool_calls"].([]any); ok {
			for _, rawCall := range calls {
				call, ok := rawCall.(map[string]any)
				if !ok {
					continue
				}
				if function, ok := call["function"].(map[string]any); ok {
					d.addJSONString(function, "arguments", SegmentRoleAssistant, SegmentFieldToolArguments)
				}
			}
		}
		if function, ok := message["function_call"].(map[string]any); ok {
			d.addJSONString(function, "arguments", SegmentRoleAssistant, SegmentFieldToolArguments)
		}
	}
	if d.opts.ScanToolDefinitions {
		tools, _ := d.Payload["tools"].([]any)
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if function, ok := tool["function"].(map[string]any); ok {
				d.addField(function, "description", SegmentRoleSystem, SegmentFieldToolDefinition)
				d.addSchemaText(function["parameters"], SegmentRoleSystem, SegmentFieldToolDefinition)
			}
		}
	}
	d.hint = func(hint string) {
		messages, _ := d.Payload["messages"].([]any)
		for _, raw := range messages {
			message, ok := raw.(map[string]any)
			if !ok || chatSegmentRole(stringField(message["role"])) != SegmentRoleSystem {
				continue
			}
			switch content := message["content"].(type) {
			case string:
				message["content"] = joinHint(content, hint)
				return
			case []any:
				message["content"] = append(content, map[string]any{"type": "text", "text": hint})
				return
			}
		}
		d.Payload["messages"] = append([]any{map[string]any{"role": "system", "content": hint}}, messages...)
	}
}

func (d *SegmentDocument) extractResponses() {
	d.addField(d.Payload, "instructions", SegmentRoleSystem, SegmentFieldInstructions)
	switch input := d.Payload["input"].(type) {
	case string:
		d.add(SegmentRoleUser, SegmentFieldMessageText, input, false, func(next string) { d.Payload["input"] = next })
	case []any:
		for _, raw := range input {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch itemType := stringField(item["type"]); {
			case itemType == "message" || (itemType == "" && item["role"] != nil):
				d.addTextOrParts(item, "content", chatSegmentRole(stringField(item["role"])), SegmentFieldMessageText, "input_text", "output_text", "text")
			case itemType == "function_call":
				d.addJSONString(item, "arguments", SegmentRoleAssistant, SegmentFieldToolArguments)
			case itemType == "custom_tool_call":
				d.addField(item, "input", SegmentRoleAssistant, SegmentFieldToolArguments)
			case itemType == "function_call_output" || itemType == "custom_tool_call_output":
				d.addTextOrParts(item, "output", SegmentRoleTool, SegmentFieldToolResult, "input_text", "output_text", "text")
			}
		}
	}
	if d.opts.ScanToolDefinitions {
		tools, _ := d.Payload["tools"].([]any)
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			d.addField(tool, "description", SegmentRoleSystem, SegmentFieldToolDefinition)
			d.addSchemaText(tool["parameters"], SegmentRoleSystem, SegmentFieldToolDefinition)
		}
	}
	d.hint = func(hint string) {
		switch instructions := d.Payload["instructions"].(type) {
		case string:
			d.Payload["instructions"] = joinHint(instructions, hint)
		case nil:
			d.Payload["instructions"] = hint
		}
	}
}

func (d *SegmentDocument) extractAnthropic() {
	d.addTextOrParts(d.Payload, "system", SegmentRoleSystem, SegmentFieldInstructions, "text")
	messages, _ := d.Payload["messages"].([]any)
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := chatSegmentRole(stringField(message["role"]))
		switch content := message["content"].(type) {
		case string:
			d.add(role, SegmentFieldMessageText, content, false, func(next string) { message["content"] = next })
		case []any:
			for _, rawBlock := range content {
				block, ok := rawBlock.(map[string]any)
				if !ok {
					continue
				}
				// thinking and redacted_thinking carry signatures and are
				// never touched, nor are images and documents.
				switch stringField(block["type"]) {
				case "text":
					d.addField(block, "text", role, SegmentFieldMessageText)
				case "tool_use":
					d.addLeaves(block["input"], SegmentRoleAssistant, SegmentFieldToolArguments, false, nil)
				case "tool_result":
					d.addTextOrParts(block, "content", SegmentRoleTool, SegmentFieldToolResult, "text")
				}
			}
		}
	}
	if d.opts.ScanToolDefinitions {
		tools, _ := d.Payload["tools"].([]any)
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			d.addField(tool, "description", SegmentRoleSystem, SegmentFieldToolDefinition)
			d.addSchemaText(tool["input_schema"], SegmentRoleSystem, SegmentFieldToolDefinition)
		}
	}
	d.hint = func(hint string) {
		switch system := d.Payload["system"].(type) {
		case string:
			d.Payload["system"] = joinHint(system, hint)
		case []any:
			d.Payload["system"] = append(system, map[string]any{"type": "text", "text": hint})
		case nil:
			d.Payload["system"] = hint
		}
	}
}

func geminiField(container map[string]any, camel, snake string) (any, string) {
	if value, ok := container[camel]; ok {
		return value, camel
	}
	if value, ok := container[snake]; ok {
		return value, snake
	}
	return nil, camel
}

func (d *SegmentDocument) addGeminiParts(content map[string]any, role string) {
	parts, _ := content["parts"].([]any)
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if thought, _ := part["thought"].(bool); thought {
			continue
		}
		d.addField(part, "text", role, SegmentFieldMessageText)
		if call, _ := geminiField(part, "functionCall", "function_call"); call != nil {
			if callMap, ok := call.(map[string]any); ok {
				d.addLeaves(callMap["args"], SegmentRoleAssistant, SegmentFieldToolArguments, false, nil)
			}
		}
		if response, _ := geminiField(part, "functionResponse", "function_response"); response != nil {
			if responseMap, ok := response.(map[string]any); ok {
				d.addLeaves(responseMap["response"], SegmentRoleTool, SegmentFieldToolResult, false, nil)
			}
		}
	}
}

func (d *SegmentDocument) extractGemini() {
	system, systemKey := geminiField(d.Payload, "systemInstruction", "system_instruction")
	if systemMap, ok := system.(map[string]any); ok {
		parts, _ := systemMap["parts"].([]any)
		for _, raw := range parts {
			if part, ok := raw.(map[string]any); ok {
				d.addField(part, "text", SegmentRoleSystem, SegmentFieldInstructions)
			}
		}
	}
	contents, _ := d.Payload["contents"].([]any)
	for _, raw := range contents {
		content, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := chatSegmentRole(stringField(content["role"]))
		if role == "" {
			role = SegmentRoleUser
		}
		d.addGeminiParts(content, role)
	}
	if d.opts.ScanToolDefinitions {
		tools, _ := d.Payload["tools"].([]any)
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			declarations, _ := geminiField(tool, "functionDeclarations", "function_declarations")
			list, _ := declarations.([]any)
			for _, rawDecl := range list {
				decl, ok := rawDecl.(map[string]any)
				if !ok {
					continue
				}
				d.addField(decl, "description", SegmentRoleSystem, SegmentFieldToolDefinition)
				d.addSchemaText(decl["parameters"], SegmentRoleSystem, SegmentFieldToolDefinition)
			}
		}
	}
	d.hint = func(hint string) {
		system, _ := d.Payload[systemKey].(map[string]any)
		if system == nil {
			d.Payload[systemKey] = map[string]any{"parts": []any{map[string]any{"text": hint}}}
			return
		}
		parts, _ := system["parts"].([]any)
		system["parts"] = append(parts, map[string]any{"text": hint})
	}
}

func (d *SegmentDocument) extractEmbeddings() {
	switch input := d.Payload["input"].(type) {
	case string:
		d.add(SegmentRoleUser, SegmentFieldEmbeddingInput, input, false, func(next string) { d.Payload["input"] = next })
	case []any:
		for i, item := range input {
			if text, ok := item.(string); ok {
				index := i
				d.add(SegmentRoleUser, SegmentFieldEmbeddingInput, text, false, func(next string) { input[index] = next })
			}
		}
	}
}

func joinHint(text, hint string) string {
	if strings.TrimSpace(text) == "" {
		return hint
	}
	return text + "\n\n" + hint
}

// sortedKeys keeps the segment order of an object stable across requests,
// which keeps placeholder numbers stable.
func sortedKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func decodeJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, errors.New("trailing data after JSON value")
	}
	return value, nil
}

func stringField(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}
