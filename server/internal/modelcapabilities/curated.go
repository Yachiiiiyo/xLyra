package modelcapabilities

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

type curatedSource struct{}

func (curatedSource) Name() string {
	return sourceCurated
}

func (curatedSource) Lookup(_ context.Context, input Input) (map[string]any, bool, error) {
	if hasAuthoritativeEndpointTypes(input.BaseCapabilities) {
		return nil, false, nil
	}

	if values := curatedStandardModel(input.ModelID); values != nil {
		return values, true, nil
	}

	switch normalizeProvider(input.Provider) {
	case "openai":
		values := curatedOpenAIModel(input.ModelID)
		return values, values != nil, nil
	default:
		return nil, false, nil
	}
}

func hasAuthoritativeEndpointTypes(capabilities map[string]any) bool {
	if capabilities == nil {
		return false
	}
	source, _ := capabilities["source"].(string)
	if strings.ToLower(strings.TrimSpace(source)) != "codex" {
		return false
	}
	switch values := capabilities["supported_endpoint_types"].(type) {
	case []string:
		return len(values) > 0
	case []any:
		return len(values) > 0
	default:
		return false
	}
}

func EndpointTypesForModel(modelID string) []string {
	endpointTypes := curatedEndpointTypesForModel(modelID)
	if len(endpointTypes) == 0 {
		return nil
	}
	return append([]string(nil), endpointTypes...)
}

func InferEndpointTypesForModel(modelID string) []string {
	if endpointTypes := ModelNameEndpointTypes(modelID); len(endpointTypes) > 0 {
		return append([]string(nil), endpointTypes...)
	}
	return []string{"openai"}
}

func InferEndpointTypesForSiteModel(modelID string, siteType string, siteBaseURL string) []string {
	if BaseURLIndicatesAnthropicMessages(siteBaseURL) {
		return []string{"anthropic-messages"}
	}
	if normalizeModelID(siteType) == "openai" {
		if IsImageGenerationModel(modelID) {
			return []string{"openai-image"}
		}
		if endpointTypes := ModelNameEndpointTypes(modelID); len(endpointTypes) > 0 {
			return endpointTypes
		}
		return []string{"openai", "anthropic-messages"}
	}
	return InferEndpointTypesForModel(modelID)
}

func BaseURLIndicatesAnthropicMessages(baseURL string) bool {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return false
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	return strings.EqualFold(segments[len(segments)-1], "anthropic")
}

func ModelNameEndpointTypes(modelID string) []string {
	endpointTypes := inferEndpointTypesForModel(modelID)
	if len(endpointTypes) == 0 {
		return nil
	}
	return append([]string(nil), endpointTypes...)
}

func inferEndpointTypesForModel(modelID string) []string {
	modelID = normalizeModelID(modelID)
	switch {
	case hasAnyPrefix(modelID, "jev-"):
		return []string{"typesafe-systemone"}
	case isMiMoV25TTSModel(modelID):
		return []string{"openai", "openai-audio-speech"}
	case strings.Contains(modelID, "tts"):
		return []string{"openai-audio-speech"}
	case IsImageGenerationModel(modelID):
		if strings.HasPrefix(modelID, "gemini") || isNanoBananaModel(modelID) {
			return []string{"google-gemini"}
		}
		return []string{"openai-image"}
	case hasAnyPrefix(modelID, "grok-"):
		return []string{"openai", "openai-response"}
	default:
		return curatedEndpointTypesForModel(modelID)
	}
}

func IsImageGenerationModel(modelID string) bool {
	modelID = normalizeImageModelID(modelID)
	if modelID == "" {
		return false
	}
	return strings.Contains(modelID, "image") ||
		strings.Contains(modelID, "dall-e") ||
		strings.Contains(modelID, "flux") ||
		isNanoBananaModel(modelID) ||
		hasKnownImageGenerationFamily(modelID)
}

// imageGenerationFamilyMarkers are substrings that belong to image-generation
// families and do not appear in chat model ids. "seed" is not one of them:
// doubao-seed-1.6 is a chat model, while seedream and seededit are not.
var imageGenerationFamilyMarkers = []string{
	"seedream",
	"seededit",
	"cogview",
	"kolors",
	"recraft",
	"ideogram",
	"midjourney",
	"stable-diffusion",
	"sdxl",
	"wanx",
}

func hasKnownImageGenerationFamily(modelID string) bool {
	for _, marker := range imageGenerationFamilyMarkers {
		if strings.Contains(modelID, marker) {
			return true
		}
	}
	if strings.Contains(modelID, "jimeng") && !hasVideoGenerationMarker(modelID) {
		return true
	}
	return hasImageTaskToken(modelID) || hasStableDiffusionCheckpoint(modelID)
}

func hasVideoGenerationMarker(modelID string) bool {
	return strings.Contains(modelID, "video") ||
		strings.Contains(modelID, "vedio") ||
		strings.Contains(modelID, "t2v") ||
		strings.Contains(modelID, "i2v") ||
		strings.Contains(modelID, "v2v")
}

func hasImageTaskToken(modelID string) bool {
	for _, token := range splitImageModelTokens(modelID) {
		switch token {
		case "t2i", "i2i", "txt2img", "text2img":
			return true
		}
	}
	return false
}

func hasStableDiffusionCheckpoint(modelID string) bool {
	for _, token := range splitImageModelTokens(modelID) {
		if token == "sd" {
			return true
		}
		if strings.HasPrefix(token, "sd") && len(token) > 2 && token[2] >= '0' && token[2] <= '9' {
			return true
		}
	}
	return false
}

func splitImageModelTokens(modelID string) []string {
	return strings.FieldsFunc(modelID, func(r rune) bool {
		return r == '-' || r == '/' || r == ':' || r == '.'
	})
}

func isNanoBananaModel(modelID string) bool {
	return strings.HasPrefix(normalizeImageModelID(modelID), "nano-banana-")
}

func normalizeImageModelID(value string) string {
	return strings.ReplaceAll(normalizeModelID(value), "_", "-")
}

func UsesModelNameEndpointInference(siteType string) bool {
	siteType = normalizeModelID(siteType)
	if siteType == "" || strings.Contains(siteType, "/") {
		return false
	}
	switch siteType {
	case "newapi", "codex", "antigravity",
		"opencode_go",
		"deepseek", "minimax", "xiaomi_mimo", "moonshot", "kimi_code",
		"google", "google_gemini", "zhipu", "glm_code",
		"typesafe":
		return false
	default:
		return true
	}
}

func curatedStandardModel(modelID string) map[string]any {
	endpointTypes := curatedEndpointTypesForModel(modelID)
	if len(endpointTypes) == 0 {
		return nil
	}
	return map[string]any{
		"supported_endpoint_types": endpointTypes,
	}
}

func curatedOpenAIModel(modelID string) map[string]any {
	return curatedStandardModel(modelID)
}

func curatedEndpointTypesForModel(modelID string) []string {
	modelID = normalizeModelID(modelID)
	switch {
	case hasAnyPrefix(modelID, "jev-"):
		return []string{"typesafe-systemone"}
	case isMiMoV25TTSModel(modelID):
		return []string{"openai", "openai-audio-speech"}
	case hasAnyPrefix(modelID, "gpt-image"):
		return []string{"openai-image"}
	case isCuratedEmbeddingModel(modelID):
		return []string{"openai-embedding"}
	case hasAnyPrefix(modelID, "claude-"):
		return []string{"anthropic-messages"}
	case modelID == "codex-auto-review":
		return []string{"openai-response"}
	case hasAnyPrefix(modelID, "gpt-5-codex", "gpt-5.1-codex", "gpt-5.2-codex"):
		return []string{"openai-response"}
	case hasAnyPrefix(modelID, "gpt-4.1"):
		return []string{"openai", "openai-response"}
	case gptGenerationAtLeast(modelID, 5):
		return []string{"openai", "openai-response"}
	case hasAnyPrefix(modelID, "gpt-", "o1", "o3", "o4"):
		return []string{"openai"}
	default:
		return nil
	}
}

func isMiMoV25TTSModel(modelID string) bool {
	switch normalizeModelID(modelID) {
	case "mimo-v2.5-tts", "mimo-v2.5-tts-voicedesign", "mimo-v2.5-tts-voiceclone":
		return true
	default:
		return false
	}
}

func gptGenerationAtLeast(modelID string, minMajor int) bool {
	const prefix = "gpt-"
	if !strings.HasPrefix(modelID, prefix) {
		return false
	}
	rest := modelID[len(prefix):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return false
	}
	major, err := strconv.Atoi(rest[:end])
	if err != nil {
		return false
	}
	return major >= minMajor
}

func isCuratedEmbeddingModel(modelID string) bool {
	return strings.Contains(modelID, "embedding") ||
		strings.Contains(modelID, "embed-") ||
		strings.HasPrefix(modelID, "text-embedding") ||
		strings.Contains(modelID, "-embed")
}

func normalizeProvider(value string) string {
	return normalizeModelID(value)
}

func normalizeModelID(value string) string {
	return trimLower(value)
}

func hasAnyPrefix(value string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if len(prefix) == 0 {
			continue
		}
		if len(value) >= len(prefix) && value[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func trimLower(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
