package httpx

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// RealClientIP 仅在直接对端属于可信代理时采信转发头。trusted 为额外
// IP/CIDR(启动时解析一次);条目 "*" 表示信任任意对端。无效条目忽略。
func RealClientIP(trusted []string) func(http.Handler) http.Handler {
	prefixes, trustAll := parseTrustedProxies(trusted)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if trustAll || peerIsTrusted(r.RemoteAddr, prefixes) {
				if ip := realClientIP(r); ip != "" {
					r.RemoteAddr = ip
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func parseTrustedProxies(trusted []string) ([]netip.Prefix, bool) {
	var prefixes []netip.Prefix
	for _, entry := range trusted {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if entry == "*" {
			return nil, true
		}
		if prefix, err := netip.ParsePrefix(entry); err == nil && prefix.IsValid() {
			prefixes = append(prefixes, prefix.Masked())
			continue
		}
		if addr, err := netip.ParseAddr(entry); err == nil && addr.IsValid() {
			prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
		}
	}
	return prefixes, false
}

func peerIsTrusted(remoteAddr string, extra []netip.Prefix) bool {
	host := remoteAddr
	if parsedHost, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = parsedHost
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() {
		return true
	}
	for _, prefix := range extra {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func realClientIP(r *http.Request) string {
	candidates := []string{
		r.Header.Get("CF-Connecting-IP"),
		r.Header.Get("True-Client-IP"),
		r.Header.Get("X-Real-IP"),
		firstForwardedIP(r.Header.Get("X-Forwarded-For")),
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		addr, err := netip.ParseAddr(candidate)
		if err != nil || !addr.IsValid() {
			continue
		}
		return candidate
	}
	return ""
}

func firstForwardedIP(value string) string {
	if value == "" {
		return ""
	}
	first, _, _ := strings.Cut(value, ",")
	return first
}
