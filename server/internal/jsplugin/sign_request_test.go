package jsplugin

import (
	"strings"
	"testing"
)

func TestDecodeProtocolSignRewrite(t *testing.T) {
	t.Parallel()
	body := `{"model":"m"}`
	got, err := decodeProtocolSign(map[string]any{
		"headers": map[string]any{"X-Session": "abc"},
		"query":   map[string]any{"nonce": "1"},
		"body":    body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != "rewrite" || got.Headers["X-Session"] != "abc" || got.Query["nonce"] != "1" || got.Body == nil || *got.Body != body {
		t.Fatalf("got = %#v", got)
	}
	signed, err := decodeProtocolSign(map[string]any{"stringToSign": "x", "algorithm": "hmac-sha256", "header": "X-Sig"})
	if err != nil || signed.Mode != "sign" {
		t.Fatalf("sign mode = %#v err=%v", signed, err)
	}
}

func TestDecodeProtocolSignRewriteRejects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		value map[string]any
		want  string
	}{
		{name: "empty", value: map[string]any{}, want: "expected stringToSign"},
		{name: "sign field", value: map[string]any{"headers": map[string]any{"X-A": "1"}, "algorithm": "hmac-sha256"}, want: "not allowed without stringToSign"},
		{name: "query in sign", value: map[string]any{"stringToSign": "x", "algorithm": "hmac-sha256", "header": "X-Sig", "query": map[string]any{"a": "b"}}, want: "not allowed in sign mode"},
		{name: "control header", value: map[string]any{"headers": map[string]any{"X-A": "a\nb"}}, want: "single line"},
		{name: "control query", value: map[string]any{"query": map[string]any{"a\nb": "1"}}, want: "single line"},
		{name: "forbidden header", value: map[string]any{"headers": map[string]any{"Cookie": "a=b"}}, want: "not allowed"},
		{name: "body type", value: map[string]any{"body": 1}, want: "expected string"},
		{name: "body size", value: map[string]any{"body": strings.Repeat("a", MaxSignBodyBytes+1)}, want: "longer than"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeProtocolSign(tc.value)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	headers := map[string]any{}
	for i := 0; i < maxSignRewriteItems+1; i++ {
		headers[sprintfHeader(i)] = "x"
	}
	if _, err := decodeProtocolSign(map[string]any{"headers": headers}); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("too many headers: %v", err)
	}
}

func TestTopLevelRequestValidation(t *testing.T) {
	t.Parallel()
	source := kindlessSource(`export function signRequest() { return { headers: { "X-A": "1" } }; }`)
	manifest := kindlessManifest(source, func(m *Manifest) {
		m.Settings = []SettingSpec{{Key: "clientVersion", Type: "string", Default: "1"}}
		m.Request = SiteProviderRequest{Headers: HeaderMap(map[string]string{
			"X-Rand":    "{{random.hex:40}}",
			"X-Session": "{{session.key}}",
			"X-Ver":     "{{setting.clientVersion}}",
			"X-Now":     "{{now.unix}}",
			"X-Model":   "{{model.name}}",
			"X-Meta":    "{{model.meta.vendor}}",
			"X-ID":      "{{uuid}}",
		})}
	})
	if err := validateManifest(manifest, source); err != nil {
		t.Fatal(err)
	}

	cred := manifest
	cred.Request = SiteProviderRequest{Headers: HeaderMap(map[string]string{"Authorization": "Bearer {{cred.api_key}}"})}
	if err := validateManifest(cred, source); err == nil || !strings.Contains(err.Error(), "unknown placeholder") {
		t.Fatalf("cred = %v", err)
	}
	region := manifest
	region.Request = SiteProviderRequest{Headers: HeaderMap(map[string]string{"Origin": "{{region.origin}}"})}
	if err := validateManifest(region, source); err == nil || !strings.Contains(err.Error(), "unknown placeholder") {
		t.Fatalf("region = %v", err)
	}
	strip := manifest
	strip.Request = SiteProviderRequest{StripDownstreamHeaders: true, Headers: HeaderMap(map[string]string{"X-A": "1"})}
	if err := validateManifest(strip, source); err != nil {
		t.Fatalf("strip = %v", err)
	}
	streamOnly := manifest
	streamOnly.Request = SiteProviderRequest{UpstreamStreamOnly: true}
	if err := validateManifest(streamOnly, source); err == nil || !strings.Contains(err.Error(), "upstreamStreamOnly") {
		t.Fatalf("streamOnly = %v", err)
	}
	unknown := manifest
	unknown.Request = SiteProviderRequest{Headers: HeaderMap(map[string]string{"X-Ver": "{{setting.missing}}"})}
	if err := validateManifest(unknown, source); err == nil || !strings.Contains(err.Error(), `unknown setting "missing"`) {
		t.Fatalf("setting = %v", err)
	}

	siteSource := meta("acme.example", KindSiteProvider)
	both := siteProviderManifest(siteSource)
	both.Request = SiteProviderRequest{Headers: HeaderMap(map[string]string{"X-A": "1"})}
	both.SiteProvider.Request = SiteProviderRequest{Headers: HeaderMap(map[string]string{"X-B": "2"})}
	if err := validateManifest(both, siteSource); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflict = %v", err)
	}
	topOnly := siteProviderManifest(siteSource)
	topOnly.Request = SiteProviderRequest{Headers: HeaderMap(map[string]string{
		"Authorization": "Bearer {{cred.api_key}}",
	}), StripDownstreamHeaders: true}
	if err := validateManifest(topOnly, siteSource); err != nil {
		t.Fatalf("top-level site request: %v", err)
	}
	regionOnly := siteProviderManifest(siteSource)
	regionOnly.Request = SiteProviderRequest{Headers: HeaderMap(map[string]string{"Origin": "{{region.origin}}"})}
	if err := validateManifest(regionOnly, siteSource); err == nil || !strings.Contains(err.Error(), "region") {
		t.Fatalf("region without attrs = %v", err)
	}
}
