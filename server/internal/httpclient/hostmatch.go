package httpclient

import (
	"net"
	"strings"
)

// MatchHostPattern reports whether host matches pattern. Both are trimmed,
// lowercased, and stripped of a port. A pattern starting with "*." matches
// exactly one label under that domain, not the bare domain and not a deeper
// subdomain. The suffix after "*." must itself contain a dot, so "*.com" does
// not match. Any other "*" makes the pattern invalid. An IP matches only the
// same literal; a wildcard never matches an IP.
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
		if suffix == "" || strings.Contains(suffix, "*") || !strings.Contains(suffix, ".") {
			return false
		}
		if !strings.HasSuffix(host, "."+suffix) {
			return false
		}
		label := strings.TrimSuffix(host, "."+suffix)
		return label != "" && !strings.Contains(label, ".")
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
	if name, port, err := net.SplitHostPort(value); err == nil && name != "" && port != "" {
		value = name
	} else if name, _, ok := strings.Cut(value, ":"); ok && name != "" && !strings.Contains(name, ":") && net.ParseIP(value) == nil {
		value = name
	}
	value = strings.TrimPrefix(value, "[")
	value = strings.TrimSuffix(value, "]")
	return strings.TrimSuffix(value, ".")
}
