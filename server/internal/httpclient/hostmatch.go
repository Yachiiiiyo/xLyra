package httpclient

import (
	"net"
	"strings"
)

// MatchHostPattern reports whether host matches pattern. Both are trimmed,
// lowercased, and stripped of a port. A pattern starting with "*." matches
// that domain's subdomains only, not the bare domain and not a suffix of
// another name. Any other "*" makes the pattern invalid. An IP matches only
// the same literal; a wildcard never matches an IP.
func MatchHostPattern(pattern, host string) bool {
	pattern = hostPatternName(pattern)
	host = hostPatternName(host)
	if pattern == "" || host == "" {
		return false
	}
	if net.ParseIP(host) != nil || net.ParseIP(pattern) != nil {
		return pattern == host
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := strings.TrimPrefix(pattern, "*.")
		if suffix == "" || strings.Contains(suffix, "*") || !strings.HasSuffix(host, "."+suffix) {
			return false
		}
		return len(host) > len(suffix)+1
	}
	if strings.Contains(pattern, "*") {
		return false
	}
	return pattern == host
}

func hostPatternName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || strings.Contains(value, "://") || strings.Contains(value, "/") {
		return ""
	}
	if name, _, ok := strings.Cut(value, ":"); ok && name != "" && !strings.Contains(name, ":") && net.ParseIP(value) == nil {
		value = name
	}
	return strings.TrimSuffix(value, ".")
}
