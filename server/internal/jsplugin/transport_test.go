package jsplugin

import (
	"strings"
	"testing"

	"xlyra/server/internal/httpclient"
)

func TestValidateTransportProfile(t *testing.T) {
	t.Parallel()
	source := "export const meta = { apiVersion: 1, id: \"acme.probe\", kind: \"quota_probe\" };\nexport function probe() { return { error: \"x\" }; }\n"
	base := testManifest("acme.probe", source)
	cases := []struct {
		name    string
		profile string
		want    string
	}{
		{name: "empty"},
		{name: "claude_code", profile: "claude_code"},
		{name: "case and space", profile: " Claude_Code "},
		{name: "bun", profile: "bun", want: `transport.profile "bun" is not supported by this build`},
		{name: "other", profile: "curl", want: `transport.profile "curl" is not supported (supported: claude_code)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := base
			manifest.Transport.Profile = tc.profile
			err := validateManifest(manifest, source)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("validate = %v", err)
				}
				if got := manifest.TransportProfile(); got != strings.ToLower(strings.TrimSpace(tc.profile)) {
					t.Fatalf("TransportProfile = %q", got)
				}
				return
			}
			if err == nil || err.Error() != tc.want {
				t.Fatalf("validate = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestMatchHostPattern(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pattern string
		host    string
		want    bool
	}{
		{pattern: "*.example.com", host: "a.example.com", want: true},
		{pattern: "*.example.com", host: "a.b.example.com", want: true},
		{pattern: " *.Example.COM ", host: "A.EXAMPLE.COM:443", want: true},
		{pattern: "*.example.com", host: "example.com", want: false},
		{pattern: "*.example.com", host: "example.com.evil.com", want: false},
		{pattern: "*.example.com", host: "notexample.com", want: false},
		{pattern: "api.example.com", host: "api.example.com", want: true},
		{pattern: "api.example.com", host: "API.Example.COM:8443", want: true},
		{pattern: "api.example.com", host: "evil.api.example.com", want: false},
		{pattern: "ex*.example.com", host: "ex.example.com", want: false},
		{pattern: "example.*.com", host: "example.a.com", want: false},
		{pattern: "*", host: "example.com", want: false},
		{pattern: "*.example.com", host: "127.0.0.1", want: false},
		{pattern: "127.0.0.1", host: "127.0.0.1", want: true},
		{pattern: "api.example.com", host: "127.0.0.1", want: false},
		{pattern: "", host: "example.com", want: false},
		{pattern: "*.example.com", host: "", want: false},
	}
	for _, tc := range cases {
		if got := httpclient.MatchHostPattern(tc.pattern, tc.host); got != tc.want {
			t.Fatalf("MatchHostPattern(%q, %q) = %v, want %v", tc.pattern, tc.host, got, tc.want)
		}
	}
}

func TestValidSiteProviderHostAllowsWildcard(t *testing.T) {
	t.Parallel()
	if err := validSiteProviderHost("*.example.com"); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"*example.com", "example.*.com", "*.", "127.0.0.1"} {
		if err := validSiteProviderHost(host); err == nil {
			t.Fatalf("validSiteProviderHost(%q) = nil", host)
		}
	}
}
