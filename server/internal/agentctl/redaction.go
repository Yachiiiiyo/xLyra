package agentctl

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

const (
	// RedactedPlaceholder replaces stripped secret strings in control-plane outputs.
	RedactedPlaceholder = "[REDACTED]"

	// DefaultMaxResponseBytes bounds response payload size returned to CLI/agents.
	DefaultMaxResponseBytes = 32 * 1024
)

var urlWithUserinfoRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^/\s:@]+):([^/\s@]+)@`)

// RedactValue recursively converts structs/maps/slices into JSON-normalized structures
// while redacting credential fields, proxy URL passwords, and sensitive headers.
func RedactValue(v any) any {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return v
	}
	return redactNormalized(normalized, "")
}

// RedactURL strips embedded passwords from URLs while preserving scheme, host, and username.
func RedactURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return RedactStringSecrets(rawURL)
	}
	if parsed.User != nil {
		if _, hasPass := parsed.User.Password(); hasPass {
			user := parsed.User.Username()
			// Replace User with dummy marker then swap cleanly so password is never retained.
			parsed.User = url.User(user)
			rebuilt := parsed.String()
			return strings.Replace(rebuilt, user+"@", user+":***@", 1)
		}
	}
	return rawURL
}

// RedactStringSecrets masks credentials embedded inside URL strings.
func RedactStringSecrets(s string) string {
	if s == "" {
		return s
	}
	return urlWithUserinfoRe.ReplaceAllString(s, `${1}${2}:***@`)
}

func redactNormalized(v any, parentKey string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		isHeaderMap := isHeaderMapKey(parentKey)
		for k, val := range x {
			if isHeaderMap {
				out[k] = redactHeaderEntry(k, val)
				continue
			}
			if isSensitiveKey(k) {
				out[k] = redactSensitiveField(val)
				continue
			}
			out[k] = redactNormalized(val, k)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = redactNormalized(item, parentKey)
		}
		return out
	case string:
		if isURLKey(parentKey) {
			return RedactURL(x)
		}
		return RedactStringSecrets(x)
	default:
		return v
	}
}

func redactSensitiveField(val any) any {
	switch x := val.(type) {
	case nil:
		return nil
	case bool:
		return x
	case float64:
		return x
	case string:
		if strings.TrimSpace(x) == "" {
			return ""
		}
		return RedactedPlaceholder
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, v := range x {
			if isNonSecretMetaKey(k) {
				out[k] = v
			} else {
				out[k] = redactSensitiveField(v)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = redactSensitiveField(item)
		}
		return out
	default:
		return RedactedPlaceholder
	}
}

func redactHeaderEntry(headerName string, val any) any {
	low := strings.ToLower(strings.TrimSpace(headerName))
	if low == "authorization" || low == "proxy-authorization" || low == "cookie" || low == "set-cookie" ||
		strings.Contains(low, "api-key") || strings.Contains(low, "token") || strings.Contains(low, "secret") {
		return redactSensitiveField(val)
	}
	return redactNormalized(val, headerName)
}

func isHeaderMapKey(key string) bool {
	low := strings.ToLower(strings.TrimSpace(key))
	return low == "headers" || low == "request_headers" || low == "response_headers" || low == "custom_headers"
}

func isURLKey(key string) bool {
	low := strings.ToLower(strings.TrimSpace(key))
	return low == "url" || low == "proxy_url" || low == "base_url" || strings.HasSuffix(low, "_url")
}

func isNonSecretMetaKey(key string) bool {
	low := strings.ToLower(strings.TrimSpace(key))
	if strings.HasPrefix(low, "has_") || strings.HasPrefix(low, "is_") {
		return true
	}
	switch {
	case strings.HasSuffix(low, "_id"),
		strings.HasSuffix(low, "_ids"),
		strings.HasSuffix(low, "_name"),
		strings.HasSuffix(low, "_count"),
		strings.HasSuffix(low, "_limit"),
		strings.HasSuffix(low, "_prefix"),
		strings.HasSuffix(low, "_suffix"),
		strings.HasSuffix(low, "_hint"),
		strings.HasSuffix(low, "_masked"),
		strings.HasSuffix(low, "_at"),
		strings.HasSuffix(low, "_enabled"),
		strings.HasSuffix(low, "_configured"),
		strings.HasSuffix(low, "_set"),
		strings.HasSuffix(low, "_type"),
		strings.HasSuffix(low, "_status"):
		return true
	}
	return low == "id" || low == "name" || low == "prefix" || low == "hint" || low == "tokens" ||
		low == "input_tokens" || low == "output_tokens" || low == "cached_tokens" || low == "total_tokens" ||
		low == "reasoning_tokens" || low == "public_key"
}

func isSensitiveKey(key string) bool {
	low := strings.ToLower(strings.TrimSpace(key))
	if low == "" || isNonSecretMetaKey(low) {
		return false
	}
	exactOrSubstrings := []string{
		"password", "passwd", "passphrase", "private_key", "secret", "encrypted",
		"totp", "signing_key", "master_key", "client_secret", "refresh_token",
		"access_token", "id_token", "session_token", "api_key", "apikey",
		"credential", "authorization", "cookie", "set_cookie", "bearer",
		"cipher", "salt", "password_hash", "key_hash",
	}
	for _, pat := range exactOrSubstrings {
		if strings.Contains(low, pat) {
			return true
		}
	}
	if low == "token" || low == "key" || low == "auth" ||
		strings.HasSuffix(low, "_token") || strings.HasSuffix(low, "_key") {
		return true
	}
	return false
}

// ProjectFields filters map or slice-of-map data down to the requested field names.
func ProjectFields(data any, fields []string) any {
	if len(fields) == 0 || data == nil {
		return data
	}
	wanted := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f != "" {
			wanted = append(wanted, f)
		}
	}
	if len(wanted) == 0 {
		return data
	}
	switch x := data.(type) {
	case map[string]any:
		// If a list wrapper has "items" or similar and requested fields don't include top-level keys,
		// project inside items while preserving pagination envelope keys if requested or by default.
		if items, ok := x["items"].([]any); ok && !containsString(wanted, "items") {
			copied := make(map[string]any, len(x))
			for k, v := range x {
				copied[k] = v
			}
			copied["items"] = projectSlice(items, wanted)
			return copied
		}
		return projectMap(x, wanted)
	case []any:
		return projectSlice(x, wanted)
	default:
		return data
	}
}

func projectSlice(items []any, wanted []string) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			out = append(out, projectMap(m, wanted))
		} else {
			out = append(out, item)
		}
	}
	return out
}

func projectMap(m map[string]any, wanted []string) map[string]any {
	out := make(map[string]any, len(wanted))
	for _, f := range wanted {
		if v, ok := m[f]; ok {
			out[f] = v
			continue
		}
		// Allow top-level dotted projection like "log.level".
		if strings.Contains(f, ".") {
			if val, ok := extractDotted(m, strings.Split(f, ".")); ok {
				setDotted(out, strings.Split(f, "."), val)
			}
		}
	}
	return out
}

func extractDotted(m map[string]any, parts []string) (any, bool) {
	cur := any(m)
	for _, part := range parts {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		nxt, exists := mm[part]
		if !exists {
			return nil, false
		}
		cur = nxt
	}
	return cur, true
}

func setDotted(m map[string]any, parts []string, val any) {
	cur := m
	for i := 0; i < len(parts)-1; i++ {
		part := parts[i]
		nxt, ok := cur[part].(map[string]any)
		if !ok {
			nxt = make(map[string]any)
			cur[part] = nxt
		}
		cur = nxt
	}
	cur[parts[len(parts)-1]] = val
}

func containsString(slice []string, target string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}

// TruncateEnvelope enforces the response size budget and sets Meta.Truncated when trimmed.
func TruncateEnvelope(env *Envelope, maxBytes int) {
	if env == nil || env.Data == nil {
		return
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxResponseBytes
	}
	raw, err := json.Marshal(env.Data)
	if err != nil || len(raw) <= maxBytes {
		return
	}

	if env.Meta == nil {
		env.Meta = &Meta{}
	}
	env.Meta.Truncated = true

	switch x := env.Data.(type) {
	case []any:
		trimmed := trimSliceToBudget(x, maxBytes)
		env.Data = map[string]any{
			"items":       trimmed,
			"total_items": len(x),
			"shown_items": len(trimmed),
			"truncated":   true,
			"hint":        "响应超过 32 KiB 上限已截断，请使用 --fields 或分页参数收窄范围。",
		}
	case map[string]any:
		if items, ok := x["items"].([]any); ok {
			trimmed := trimSliceToBudget(items, maxBytes-512)
			x["items"] = trimmed
			x["shown_items"] = len(trimmed)
			x["truncated"] = true
			return
		}
		env.Data = map[string]any{
			"truncated": true,
			"size":      len(raw),
			"preview":   string(raw[:min(len(raw), 4096)]),
			"hint":      "响应超过 32 KiB 上限已截断，请使用 --fields 指定所需字段。",
		}
	case string:
		env.Data = x[:min(len(x), maxBytes-256)] + "\n...[truncated]"
	default:
		env.Data = map[string]any{
			"truncated": true,
			"size":      len(raw),
			"preview":   string(raw[:min(len(raw), 4096)]),
		}
	}
}

func trimSliceToBudget(items []any, maxBytes int) []any {
	if maxBytes < 512 {
		maxBytes = 512
	}
	lo, hi := 0, len(items)
	best := 0
	for lo <= hi {
		mid := (lo + hi) / 2
		b, err := json.Marshal(items[:mid])
		if err == nil && len(b) <= maxBytes {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	out := make([]any, best)
	copy(out, items[:best])
	return out
}
