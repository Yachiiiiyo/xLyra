package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	routeengine "xlyra/server/internal/router"
	"xlyra/server/internal/store"
)

const (
	bridgeImageRequestTimeout      = 5 * time.Minute
	bridgeImageMaxAttempts         = 3
	bridgeImageMaxAmbiguousRetries = 1
	imageGenerationFailoverLimit   = math.MaxInt
)

type bridgeImageOutcome struct {
	OK            bool
	B64           string
	RevisedPrompt string
	ErrorMessage  string
}

type ImageGenerationModel struct {
	Model   string
	SiteIDs []uuid.UUID
}

type ImageGenerationRequest struct {
	RequestID string
	APIKeyID  uuid.UUID
	Models    []ImageGenerationModel
	Prompt    string
	Size      string
	Quality   string
	SiteID    *uuid.UUID
}

type ImageGenerationAttempt struct {
	Model    string `json:"model"`
	SiteName string `json:"site"`
	Error    string `json:"error"`
}

type ImageGenerationResult struct {
	OK            bool
	B64           string
	RevisedPrompt string
	Model         string
	SiteName      string
	Attempts      []ImageGenerationAttempt
	ErrorMessage  string
}

type noopResponseWriter struct {
	header http.Header
}

func newNoopResponseWriter() *noopResponseWriter {
	return &noopResponseWriter{header: http.Header{}}
}

func (w *noopResponseWriter) Header() http.Header         { return w.header }
func (w *noopResponseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *noopResponseWriter) WriteHeader(int)             {}

func (h Handler) executeBridgeImageGeneration(
	ctx context.Context,
	requestID string,
	apiKeyID uuid.UUID,
	cfg store.APIKeyImageBridge,
	prompt string,
	spec bridgeImageToolSpec,
) bridgeImageOutcome {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return bridgeImageOutcome{ErrorMessage: "the generate_image call did not include a prompt"}
	}
	// 旧桥接仍是一次调用总计 5 分钟、最多 3 次尝试。
	ctx, cancel := context.WithTimeout(ctx, bridgeImageRequestTimeout)
	defer cancel()
	result := h.generateImage(ctx, ImageGenerationRequest{
		RequestID: requestID,
		APIKeyID:  apiKeyID,
		Models:    []ImageGenerationModel{{Model: cfg.Model}},
		Prompt:    prompt,
		Size:      spec.Size,
		Quality:   spec.Quality,
		SiteID:    cfg.SiteID,
	}, spec.OutputFormat, bridgeImageMaxAttempts)
	outcome := bridgeImageOutcome{OK: result.OK, B64: result.B64, RevisedPrompt: result.RevisedPrompt, ErrorMessage: result.ErrorMessage}
	if !result.OK && len(result.Attempts) > 0 {
		outcome.ErrorMessage = result.Attempts[len(result.Attempts)-1].Error
	}
	return outcome
}

// GenerateImage 按模型优先级、站点路由顺序执行两层兜底。
func (h Handler) GenerateImage(ctx context.Context, req ImageGenerationRequest) ImageGenerationResult {
	return h.generateImage(ctx, req, "", 0)
}

func (h Handler) generateImage(ctx context.Context, req ImageGenerationRequest, outputFormat string, maxAttempts int) ImageGenerationResult {
	result := ImageGenerationResult{Attempts: []ImageGenerationAttempt{}}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		result.ErrorMessage = "image generation prompt is required"
		return result
	}
	resolver := openAIProtocolResolver{db: h.db}
	modelErrors := make([]string, 0, len(req.Models))
	totalAttempts := 0

	for _, selection := range req.Models {
		model := strings.TrimSpace(selection.Model)
		if model == "" {
			continue
		}
		if ctx.Err() != nil {
			result.ErrorMessage = ctx.Err().Error()
			return result
		}
		query := routeengine.CandidateQuery{
			ModelKey:      model,
			EndpointType:  upstreamEndpointTypeOpenAIImage,
			FailoverLimit: imageGenerationFailoverLimit,
		}
		if maxAttempts > 0 {
			query.FailoverLimit = bridgeImageMaxAttempts
		}
		if len(selection.SiteIDs) > 0 {
			query.AllowedSiteIDs = selection.SiteIDs
			if req.SiteID != nil {
				query.AllowedSiteIDs = []uuid.UUID{uuid.Nil}
				for _, siteID := range selection.SiteIDs {
					if siteID == *req.SiteID {
						query.AllowedSiteIDs = []uuid.UUID{*req.SiteID}
						break
					}
				}
			}
		} else if req.SiteID != nil {
			query.AllowedSiteIDs = []uuid.UUID{*req.SiteID}
		}
		plan, err := h.router.Plan(ctx, query)
		if err != nil {
			message := fmt.Sprintf("no route for image model %s: %v", model, err)
			result.Attempts = append(result.Attempts, ImageGenerationAttempt{Model: model, Error: message})
			modelErrors = appendImageModelError(modelErrors, model, err.Error())
			continue
		}

		payload := map[string]any{"model": model, "prompt": prompt, "n": 1}
		if req.Size != "" {
			payload["size"] = req.Size
		}
		if req.Quality != "" {
			payload["quality"] = req.Quality
		}
		if outputFormat != "" {
			payload["output_format"] = outputFormat
		}
		request := gatewayRequest{
			DownstreamPath:    gatewayEndpointImagesGenerations,
			DownstreamHeaders: http.Header{},
			RequestedModel:    model,
			Stream:            false,
			Payload:           payload,
		}
		candidates := append([]routeengine.Candidate{plan.Selected}, plan.Failover...)
		lastError := "image generation failed"
		skipModel := false
		legacyStop := false
		legacyAmbiguousRetries := 0
		for _, candidate := range candidates {
			ambiguousRetries := 0
			for {
				if maxAttempts > 0 && totalAttempts >= maxAttempts {
					break
				}
				if ctx.Err() != nil {
					result.ErrorMessage = ctx.Err().Error()
					return result
				}
				totalAttempts++
				attemptNumber := totalAttempts
				protocol, resolveErr := resolver.Resolve(ctx, request, candidate)
				if resolveErr != nil {
					lastError = resolveErr.Error()
					result.Attempts = append(result.Attempts, ImageGenerationAttempt{Model: model, SiteName: candidate.Site.Name, Error: lastError})
					break
				}
				attemptCtx, cancel := context.WithTimeout(ctx, bridgeImageRequestTimeout)
				attemptResult := h.forwardGatewayRequest(attemptCtx, newNoopResponseWriter(), req.RequestID, attemptNumber, req.APIKeyID, plan.CanonicalModel.ID, candidate, request, nil, protocol)
				cancel()
				if attemptResult.success {
					h.clearCooldownAfterRecovery(ctx, candidate)
					outcome := bridgeImageOutcomeFromBody(attemptResult.body)
					if outcome.OK {
						result.OK = true
						result.B64 = outcome.B64
						result.RevisedPrompt = outcome.RevisedPrompt
						result.Model = model
						result.SiteName = candidate.Site.Name
						return result
					}
					lastError = outcome.ErrorMessage
					result.Attempts = append(result.Attempts, ImageGenerationAttempt{Model: model, SiteName: candidate.Site.Name, Error: lastError})
					break
				}
				lastError = nonEmptyString(attemptResult.errorMessage, attemptResult.errorType)
				result.Attempts = append(result.Attempts, ImageGenerationAttempt{Model: model, SiteName: candidate.Site.Name, Error: lastError})
				if ctx.Err() != nil || attemptResult.errorType == "downstream_client_cancelled" {
					result.ErrorMessage = lastError
					if ctx.Err() != nil {
						result.ErrorMessage = ctx.Err().Error()
					}
					return result
				}
				h.cooldownAfterFailure(ctx, candidate, attemptResult)
				if maxAttempts == 0 && imageFailureSkipsModel(attemptResult) {
					skipModel = true
					break
				}
				if maxAttempts > 0 {
					if bridgeImageFailureIsAmbiguous(attemptResult.errorType) {
						if legacyAmbiguousRetries >= bridgeImageMaxAmbiguousRetries {
							legacyStop = true
						}
						legacyAmbiguousRetries++
					}
					break
				}
				if bridgeImageFailureIsAmbiguous(attemptResult.errorType) && ambiguousRetries < bridgeImageMaxAmbiguousRetries {
					ambiguousRetries++
					continue
				}
				break
			}
			if maxAttempts > 0 && totalAttempts >= maxAttempts {
				break
			}
			if skipModel || legacyStop {
				break
			}
			if ctx.Err() != nil {
				result.ErrorMessage = ctx.Err().Error()
				return result
			}
		}
		modelErrors = appendImageModelError(modelErrors, model, lastError)
	}
	result.ErrorMessage = summarizeImageGenerationErrors(modelErrors)
	if result.ErrorMessage == "" {
		result.ErrorMessage = "image generation failed"
	}
	if h.logger != nil {
		h.logger.WarnContext(ctx, "image generation failed", "scope", "gateway", "request_id", req.RequestID, "attempts", totalAttempts, "error", result.ErrorMessage)
	}
	return result
}

// 审核与参数错误不应在同名模型的其他站点重复消耗调用。
// 只认 400/413/422 与明确的审核/参数类 errorType;401/403/404 等是站点自身的
// 凭据或模型缺失问题,换一个站点仍可能成功,必须继续站点兜底。
func imageFailureSkipsModel(result gatewayAttemptResult) bool {
	errorType := strings.ToLower(strings.TrimSpace(result.errorType))
	if strings.Contains(errorType, "moderation") || strings.Contains(errorType, "invalid_request") || strings.Contains(errorType, "content_policy") {
		return true
	}
	status := result.upstreamStatusCode
	if status == 0 {
		status = result.statusCode
	}
	switch status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}

func appendImageModelError(lines []string, model, message string) []string {
	line := truncateImageError(fmt.Sprintf("%s: %s", model, nonEmptyString(strings.TrimSpace(message), "image generation failed")), 160)
	for _, existing := range lines {
		if strings.HasPrefix(existing, model+":") {
			return lines
		}
	}
	return append(lines, line)
}

func summarizeImageGenerationErrors(lines []string) string {
	if len(lines) > 5 {
		lines = lines[:5]
	}
	return strings.Join(lines, "\n")
}

func truncateImageError(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

func bridgeImageFailureIsAmbiguous(errorType string) bool {
	switch strings.TrimSpace(errorType) {
	case "upstream_timeout", "upstream_response_read_failed", "upstream_stream_failed":
		return true
	default:
		return false
	}
}

func bridgeImageOutcomeFromBody(body []byte) bridgeImageOutcome {
	var response struct {
		Data []struct {
			B64JSON       string `json:"b64_json"`
			URL           string `json:"url"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return bridgeImageOutcome{ErrorMessage: "image response could not be parsed"}
	}
	for _, item := range response.Data {
		if strings.TrimSpace(item.B64JSON) != "" {
			return bridgeImageOutcome{OK: true, B64: item.B64JSON, RevisedPrompt: item.RevisedPrompt}
		}
	}
	return bridgeImageOutcome{ErrorMessage: "image response did not include base64 image data"}
}
