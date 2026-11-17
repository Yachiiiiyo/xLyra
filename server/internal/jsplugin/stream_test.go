package jsplugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseStreamSection(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"unwrap": {"path": "body.choices[0]", "format": "json_string", "statusPath": "statusCode", "okStatuses": [200]},
		"done": {"dataEquals": ["[DONE]"], "unwrappedEquals": ["[DONE]"], "events": ["finish"], "closeOnDone": true},
		"usage": {
			"inputTokens": "usage.prompt_tokens",
			"outputTokens": "usage.completion_tokens",
			"cachedTokens": "usage.prompt_tokens_details.cached_tokens",
			"reasoningTokens": "usage.completion_tokens_details.reasoning_tokens"
		}
	}`)
	section, err := ParseStreamSection(raw)
	if err != nil {
		t.Fatal(err)
	}
	if section == nil || section.Unwrap.Format != "json_string" || !section.Done.CloseOnDone {
		t.Fatalf("section = %#v", section)
	}
	empty, err := ParseStreamSection(nil)
	if err != nil || empty != nil {
		t.Fatalf("empty = %#v err=%v", empty, err)
	}
	def, err := ParseStreamSection([]byte(`{"unwrap":{"path":"body"}}`))
	if err != nil || def.Unwrap.Format != "json" {
		t.Fatalf("default format = %#v err=%v", def, err)
	}
}

func TestParseStreamSectionRejects(t *testing.T) {
	t.Parallel()
	cases := []string{
		`{"unwrap":{"path":""}}`,
		`{"unwrap":{"path":"a.b.c.d.e.f.g.h.i"}}`,
		`{"unwrap":{"path":"bad path"}}`,
		`{"unwrap":{"path":"body","format":"xml"}}`,
		`{"unwrap":{"path":"body","okStatuses":[99]}}`,
		`{"usage":{"inputTokens":"usage.*"}}`,
		`{"done":{"dataEquals":[""]}}`,
	}
	for _, raw := range cases {
		if _, err := ParseStreamSection([]byte(raw)); err == nil {
			t.Fatalf("ParseStreamSection(%s) = nil", raw)
		}
	}
	long := strings.Repeat("a.", 60) + "a"
	if _, err := ParseStreamSection([]byte(`{"unwrap":{"path":"` + long + `"}}`)); err == nil {
		t.Fatal("long path accepted")
	}
}

func TestProtocolAndSiteProviderValidateStream(t *testing.T) {
	t.Parallel()
	source := "export function probe() { return { error: \"x\" }; }\n"
	base := testManifest("acme.probe", source)
	base.Kind = KindProtocol
	base.Protocol = ProtocolSection{Name: "acme", DownstreamPath: "/v1/acme", EndpointType: "acme", Method: "POST", Auth: "none"}
	base.Protocol.Stream = &StreamSection{Unwrap: &StreamUnwrap{Path: "nope.*"}}
	if err := validateManifest(base, source); err == nil || !strings.Contains(err.Error(), "protocol.stream") {
		t.Fatalf("protocol stream = %v", err)
	}
	base.Protocol.Stream = &StreamSection{Unwrap: &StreamUnwrap{Path: "body", Format: "raw"}}
	if err := validateManifest(base, source); err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal(StreamSection{Usage: &StreamUsage{InputTokens: "a[0].b"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseStreamSection(raw); err != nil {
		t.Fatal(err)
	}
}
