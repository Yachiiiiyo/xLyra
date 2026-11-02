package market

import (
	"testing"

	"xlyra/server/internal/marketproto"
	"xlyra/server/internal/store"
)

func TestAhead(t *testing.T) {
	index := &marketproto.Index{Plugins: []marketproto.Plugin{
		{ID: "acme.demo", Versions: []marketproto.PluginVersion{{Version: "1.0.0"}, {Version: "1.2.0"}}},
		{ID: "acme.empty", Versions: nil},
	}}
	cases := []struct {
		name   string
		local  string
		id     string
		index  *marketproto.Index
		ahead  bool
		market string
	}{
		{name: "newer than newest indexed", local: "1.3.0", id: "acme.demo", index: index, ahead: true, market: "1.2.0"},
		{name: "equal", local: "1.2.0", id: "acme.demo", index: index, ahead: false, market: "1.2.0"},
		{name: "older", local: "1.0.0", id: "acme.demo", index: index, ahead: false, market: "1.2.0"},
		{name: "missing plugin", local: "0.1.0", id: "acme.missing", index: index, ahead: true, market: ""},
		{name: "plugin without versions", local: "0.1.0", id: "acme.empty", index: index, ahead: true, market: ""},
		{name: "no index", local: "9.0.0", id: "acme.demo", index: nil, ahead: false, market: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ahead, marketVersion := Ahead(tc.local, tc.index, tc.id)
			if ahead != tc.ahead || marketVersion != tc.market {
				t.Fatalf("ahead=%v market=%q want ahead=%v market=%q", ahead, marketVersion, tc.ahead, tc.market)
			}
		})
	}
}

func TestCandidateVersionPrefersEnabled(t *testing.T) {
	rows := []store.JSPluginVersion{
		{Version: "2.0.0", Status: store.JSPluginStatusDisabled},
		{Version: "1.1.0", Status: store.JSPluginStatusEnabled},
		{Version: "1.0.0", Status: store.JSPluginStatusDisabled},
	}
	got, ok := CandidateVersion(rows)
	if !ok || got.Version != "1.1.0" {
		t.Fatalf("enabled candidate = %q ok=%v", got.Version, ok)
	}
	for i := range rows {
		rows[i].Status = store.JSPluginStatusDisabled
	}
	got, ok = CandidateVersion(rows)
	if !ok || got.Version != "2.0.0" {
		t.Fatalf("latest candidate = %q ok=%v", got.Version, ok)
	}
}
