package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRealClientIP(t *testing.T) {
	t.Parallel()

	const original = "10.1.2.3:54321"
	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{
			name: "cf connecting ip wins over other headers",
			headers: map[string]string{
				"CF-Connecting-IP": "203.0.113.1",
				"True-Client-IP":   "203.0.113.2",
				"X-Real-IP":        "203.0.113.3",
				"X-Forwarded-For":  "203.0.113.4, 198.51.100.5",
			},
			want: "203.0.113.1",
		},
		{
			name: "true client ip used when cf is absent",
			headers: map[string]string{
				"True-Client-IP":  "203.0.113.2",
				"X-Real-IP":       "203.0.113.3",
				"X-Forwarded-For": "203.0.113.4",
			},
			want: "203.0.113.2",
		},
		{
			name: "x real ip used when higher priority headers are absent",
			headers: map[string]string{
				"X-Real-IP":       "198.51.100.14",
				"X-Forwarded-For": "203.0.113.4, 198.51.100.5",
			},
			want: "198.51.100.14",
		},
		{
			name: "invalid cf falls through to true client ip",
			headers: map[string]string{
				"CF-Connecting-IP": "not-an-ip",
				"True-Client-IP":   "203.0.113.20",
				"X-Real-IP":        "198.51.100.14",
			},
			want: "203.0.113.20",
		},
		{
			name: "invalid higher headers fall through to x real ip",
			headers: map[string]string{
				"CF-Connecting-IP": "999.1.1.1",
				"True-Client-IP":   "203.0.113.1:443",
				"X-Real-IP":        "198.51.100.21",
				"X-Forwarded-For":  "203.0.113.9",
			},
			want: "198.51.100.21",
		},
		{
			name: "forwarded for uses the first address",
			headers: map[string]string{
				"X-Forwarded-For": "203.0.113.9, 198.51.100.23, 192.0.2.1",
			},
			want: "203.0.113.9",
		},
		{
			name: "invalid first forwarded address does not use later ones",
			headers: map[string]string{
				"X-Forwarded-For": "bogus, 203.0.113.9",
			},
			want: original,
		},
		{
			name: "whitespace around a valid ip is trimmed",
			headers: map[string]string{
				"CF-Connecting-IP": "  203.0.113.50  ",
			},
			want: "203.0.113.50",
		},
		{
			name: "ipv6 cf connecting ip is kept without a port",
			headers: map[string]string{
				"CF-Connecting-IP": "2001:db8::1",
				"X-Forwarded-For":  "203.0.113.4",
			},
			want: "2001:db8::1",
		},
		{
			name: "no forwarding headers keep the original remote addr",
			want: original,
		},
		{
			name: "all invalid candidates keep the original remote addr",
			headers: map[string]string{
				"CF-Connecting-IP": "unknown",
				"True-Client-IP":   "localhost",
				"X-Real-IP":        "1.2.3",
				"X-Forwarded-For":  "nope, 203.0.113.9",
			},
			want: original,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var got string
			handler := RealClientIP(nil)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = r.RemoteAddr
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = original
			for key, value := range tc.headers {
				req.Header.Set(key, value)
			}

			handler.ServeHTTP(httptest.NewRecorder(), req)

			if got != tc.want {
				t.Fatalf("RemoteAddr = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRealClientIPTrust(t *testing.T) {
	t.Parallel()

	const forwarded = "203.0.113.9"
	tests := []struct {
		name    string
		remote  string
		trusted []string
		want    string
	}{
		{
			name:   "private peer trusts forwarding headers",
			remote: "10.1.2.3:54321",
			want:   forwarded,
		},
		{
			name:   "loopback peer trusts forwarding headers",
			remote: "127.0.0.1:54321",
			want:   forwarded,
		},
		{
			name:   "link local peer trusts forwarding headers",
			remote: "169.254.1.1:54321",
			want:   forwarded,
		},
		{
			name:   "public peer ignores forged forwarding headers",
			remote: "192.0.2.10:54321",
			want:   "192.0.2.10:54321",
		},
		{
			name:    "configured public cidr trusts matching peer",
			remote:  "203.0.113.8:443",
			trusted: []string{"203.0.113.0/24"},
			want:    forwarded,
		},
		{
			name:    "configured single ip trusts that peer",
			remote:  "198.51.100.10:443",
			trusted: []string{"198.51.100.10"},
			want:    forwarded,
		},
		{
			name:    "star trusts any peer",
			remote:  "192.0.2.10:54321",
			trusted: []string{"*"},
			want:    forwarded,
		},
		{
			name:    "invalid trusted entries are ignored",
			remote:  "192.0.2.10:54321",
			trusted: []string{"not-a-prefix", "999.1.1.1", ""},
			want:    "192.0.2.10:54321",
		},
		{
			name:   "ipv4 mapped private peer trusts forwarding headers",
			remote: "[::ffff:10.0.0.1]:54321",
			want:   forwarded,
		},
		{
			name:   "ipv4 mapped public peer ignores forwarding headers",
			remote: "[::ffff:192.0.2.10]:54321",
			want:   "[::ffff:192.0.2.10]:54321",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var got string
			handler := RealClientIP(tc.trusted)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = r.RemoteAddr
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remote
			req.Header.Set("X-Forwarded-For", forwarded+", 198.51.100.5")

			handler.ServeHTTP(httptest.NewRecorder(), req)

			if got != tc.want {
				t.Fatalf("RemoteAddr = %q, want %q", got, tc.want)
			}
		})
	}
}
