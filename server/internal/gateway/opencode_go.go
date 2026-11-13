package gateway

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"xlyra/server/internal/protocolspec"
	routeengine "xlyra/server/internal/router"
	"xlyra/server/internal/store"
)

const (
	openCodeGoSessionHeader   = "x-opencode-session-id"
	openCodeGoAffinityHeader  = "x-session-affinity"
	openCodeGoUserAgent       = "opencode/latest/2.1.1/cli"
	openCodeGoIdentifierSize  = 26
	openCodeGoTimestampSize   = 12
	openCodeGoRandomSize      = openCodeGoIdentifierSize - openCodeGoTimestampSize
	openCodeGoIdentifierChars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

func isOpenCodeGoSite(siteType string) bool {
	return strings.EqualFold(strings.TrimSpace(siteType), "opencode_go")
}

func isOpenCodeZenSite(candidate routeengine.Candidate) bool {
	return isOpenCodeGoSite(candidate.Site.SiteType) || protocolspec.IsOpenCodeZenBaseURL(candidate.Site.BaseURL)
}

func applyOpenCodeGoDefaultTools(payload map[string]any, candidate routeengine.Candidate, protocol gatewayProtocolAdapter) {
	if payload == nil || protocol == nil || !isOpenCodeZenSite(candidate) {
		return
	}
	if _, exists := payload["tools"]; exists {
		return
	}

	parameters := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "文件路径",
			},
		},
		"required": []any{"path"},
	}
	var tool any
	path := endpointPath(protocol.UpstreamPath(candidate.Site.BaseURL))
	switch {
	case strings.HasSuffix(path, gatewayEndpointChatCompletions):
		tool = map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "read",
				"description": "读取文件",
				"parameters":  parameters,
			},
		}
	case strings.HasSuffix(path, gatewayEndpointResponses):
		tool = map[string]any{
			"type":        "function",
			"name":        "read",
			"description": "读取文件",
			"parameters":  parameters,
		}
	case strings.HasSuffix(path, gatewayEndpointMessages):
		tool = map[string]any{
			"name":         "read",
			"description":  "读取文件",
			"input_schema": anthropicToolInputSchema(parameters),
		}
	default:
		return
	}
	payload["tools"] = []any{tool}
}

func buildGatewayUpstreamPayload(protocol gatewayProtocolAdapter, request gatewayRequest, candidate routeengine.Candidate) (map[string]any, error) {
	payload, err := protocol.BuildUpstreamPayload(request, candidate)
	if err != nil {
		return nil, err
	}
	// Conversion can omit an explicitly empty tools array. Only add a fallback
	// when the downstream request omitted tools entirely.
	if _, exists := request.Payload["tools"]; !exists {
		applyOpenCodeGoDefaultTools(payload, candidate, protocol)
	}
	return payload, nil
}

// applyOpenCodeGoGatewayHeaders replaces all application headers with the
// set the official OpenCode CLI sends, which the Zen endpoints expect.
func applyOpenCodeGoGatewayHeaders(req *http.Request, request gatewayRequest, apiKey string) {
	if req == nil {
		return
	}
	req.Header = make(http.Header)
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", openCodeGoUserAgent)
	req.Header.Set("x-opencode-client", "cli")

	sessionID := openCodeGoValidHeaderID(request.DownstreamHeaders.Get(openCodeGoSessionHeader))
	if sessionID == "" {
		sessionID = openCodeGoNewSessionID()
	}
	affinityID := openCodeGoValidHeaderID(request.DownstreamHeaders.Get(openCodeGoAffinityHeader))
	if affinityID == "" {
		affinityID = sessionID
	}
	req.Header.Set(openCodeGoSessionHeader, sessionID)
	req.Header.Set(openCodeGoAffinityHeader, affinityID)
	// The official client repeats the session across the session header
	// family and bills the free tier per fresh 40-hex project id.
	req.Header.Set("x-opencode-session", sessionID)
	req.Header.Set("x-session-id", sessionID)
	req.Header.Set("x-opencode-project", openCodeGoNewProjectID())
}

// openCodeGoNewProjectID mints the fresh 40-hex project id every request
// carries; a shared id would share one pre-burned free-tier quota bucket.
func openCodeGoNewProjectID() string {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		for index := range buf {
			buf[index] = byte(time.Now().UnixNano() >> (index % 8 * 8))
		}
	}
	return hex.EncodeToString(buf)
}

func openCodeGoValidHeaderID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) != len("ses_")+openCodeGoIdentifierSize || !strings.HasPrefix(value, "ses_") {
		return ""
	}
	for index, char := range value[len("ses_"):] {
		if index < openCodeGoTimestampSize {
			if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
				return ""
			}
			continue
		}
		if !strings.ContainsRune(openCodeGoIdentifierChars, char) {
			return ""
		}
	}
	return value
}

// openCodeGoSessionWindowMS is how far back the upstream still accepts a
// session's encoded creation time; sessions dated outside it answer 429.
const openCodeGoSessionWindowMS = 30 * 24 * 60 * 60 * 1000

// openCodeGoDescendingTimeRE matches the inverted-timestamp encoding of an
// official session id: the low flag bit fixed by the inversion turns the 11th
// and 12th hex digits into the "f[c-f]" marker, so plausible times all share
// this descending shape.
var openCodeGoDescendingTimeRE = regexp.MustCompile(`^[0-9a-f]{9}f[c-f][0-9a-f]$`)

// openCodeGoSessionPhaseMS decodes the creation time (in milliseconds) that
// an official session id encodes, reporting false for other shapes.
func openCodeGoSessionPhaseMS(id string) (int64, bool) {
	if len(id) != len("ses_")+openCodeGoIdentifierSize || !strings.HasPrefix(id, "ses_") {
		return 0, false
	}
	timePart := id[len("ses_") : len("ses_")+openCodeGoTimestampSize]
	if !openCodeGoDescendingTimeRE.MatchString(timePart) {
		return 0, false
	}
	inverted, err := strconv.ParseUint(timePart, 16, 64)
	if err != nil {
		return 0, false
	}
	current := (^inverted) & 0xffffffffffff
	return int64(current >> 12), true
}

// openCodeGoIsOfficialSessionID reports whether value carries the official
// client's session shape and encodes a creation time still inside the
// accepted window.
func openCodeGoIsOfficialSessionID(value string) bool {
	value = strings.TrimSpace(value)
	phase, ok := openCodeGoSessionPhaseMS(value)
	if !ok {
		return false
	}
	age := time.Now().UnixMilli() - phase
	if age < 0 {
		age = -age
	}
	return age <= openCodeGoSessionWindowMS
}

// openCodeGoNewSessionID mints a session id in the official client's
// descending-time encoding: today's UTC midnight minus a random offset of at
// most 30 days, shifted left by 12 with a set low bit, bitwise-inverted into
// 12 hex characters, followed by 14 base62 characters. The upstream decodes
// that time and refuses implausible ones, so plain hex timestamps fail.
func openCodeGoNewSessionID() string {
	seed := make([]byte, 4+openCodeGoRandomSize)
	if _, err := rand.Read(seed); err != nil {
		for index := range seed {
			seed[index] = byte(time.Now().UnixNano() >> (index % 8 * 8))
		}
	}
	offset := binary.BigEndian.Uint32(seed[0:4]) % openCodeGoSessionWindowMS
	midnight := time.Now().UTC().UnixMilli() / 86400000 * 86400000
	base := big.NewInt(midnight - int64(offset))
	value := new(big.Int).Or(new(big.Int).Lsh(base, 12), big.NewInt(1))
	mask := new(big.Int).SetUint64(0xffffffffffff)
	inverted := new(big.Int).And(new(big.Int).Not(value), mask)
	timestamp := fmt.Sprintf("%012x", inverted)

	random := make([]byte, openCodeGoRandomSize)
	for index := range random {
		random[index] = openCodeGoIdentifierChars[int(seed[4+index])%len(openCodeGoIdentifierChars)]
	}
	return "ses_" + timestamp + string(random)
}

func classifyOpenCodeGoUsageLimit(candidate routeengine.Candidate, result gatewayAttemptResult, body []byte, now time.Time) (gatewayAttemptResult, bool) {
	if !isOpenCodeGoSite(candidate.Site.SiteType) || result.upstreamStatusCode != http.StatusTooManyRequests {
		return result, false
	}
	var payload struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		Metadata struct {
			Workspace string `json:"workspace"`
			LimitName string `json:"limitName"`
		} `json:"metadata"`
	}
	if json.Unmarshal(body, &payload) != nil || !strings.EqualFold(strings.TrimSpace(payload.Error.Type), "GoUsageLimitError") {
		return result, false
	}
	if now.IsZero() {
		now = time.Now()
	}
	duration := openCodeGoUsageLimitCooldownDuration(result)
	resetAt := now.Add(duration)
	result.errorType = "opencode_go_usage_limit"
	result.errorMessage = nonEmptyString(strings.TrimSpace(payload.Error.Message), result.errorMessage)
	result.cooldownReason = store.CooldownReasonOpenCodeGoUsageLimitReached
	result.cooldownScope = "credential"
	result.cooldownDuration = duration
	result.cooldownMetadata = map[string]any{
		"limit_name":  strings.TrimSpace(payload.Metadata.LimitName),
		"workspace":   strings.TrimSpace(payload.Metadata.Workspace),
		"reset_at":    resetAt.UTC().Format(time.RFC3339),
		"observed_at": now.UTC().Format(time.RFC3339),
	}
	return result, true
}

func (h Handler) persistOpenCodeGoUsageLimit(ctx context.Context, candidate routeengine.Candidate, result gatewayAttemptResult) {
	if h.db == nil || !isOpenCodeGoSite(candidate.Site.SiteType) || result.errorType != "opencode_go_usage_limit" {
		return
	}
	usage := map[string]any{
		"source":                      "opencode_go",
		"official_realtime_available": false,
		"available":                   false,
		"retry_after_seconds":         result.retryAfterSeconds,
	}
	for key, value := range result.cooldownMetadata {
		usage[key] = value
	}
	encoded, err := json.Marshal(usage)
	if err != nil {
		return
	}
	if err := store.NewSiteAPIKeyStateRepository(h.db.DB()).UpdateUsage(ctx, result.credentialID, encoded); err != nil && h.logger != nil {
		h.logger.WarnContext(ctx, "failed to persist OpenCode Go usage limit", "scope", "gateway", "site_id", candidate.Site.ID, "credential_id", result.credentialID, "error", err)
	}
}
