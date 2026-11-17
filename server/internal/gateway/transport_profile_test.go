package gateway

import (
	"testing"

	"xlyra/server/internal/httpclient"
	"xlyra/server/internal/jsplugin"
	routeengine "xlyra/server/internal/router"
	"xlyra/server/internal/site"
)

func transportPlugin(t *testing.T, id, profile string) *jsplugin.Plugin {
	t.Helper()
	source := "export const meta = { apiVersion: 1, id: \"" + id + "\", kind: \"error_classifier\" };\nexport function classify() { return { failureClass: \"unknown\" }; }\n"
	plugin, err := jsplugin.NewPlugin(jsplugin.Manifest{
		ID: id, Name: id, Version: "1.0.0", APIVersion: 1, HostAPI: 1, Kind: jsplugin.KindErrorClassifier,
		Transport: jsplugin.TransportSection{Profile: profile},
		SHA256:    map[string]string{"plugin.js": jsplugin.HashSource(source)},
	}, source, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	return plugin
}

func siteTypeTransportPlugin(id, profile string) *jsplugin.Plugin {
	return &jsplugin.Plugin{Manifest: jsplugin.Manifest{
		ID: id, SiteProvider: jsplugin.SiteProviderSection{SiteType: id + "/chat"},
		Transport: jsplugin.TransportSection{Profile: profile},
	}}
}

func installTransportPlugins(t *testing.T, plugins ...*jsplugin.Plugin) {
	t.Helper()
	catalog := jsplugin.DefaultCatalog()
	previous := catalog.Current()
	builtins, err := jsplugin.LoadBuiltins()
	if err != nil {
		t.Fatal(err)
	}
	catalog.Replace(&jsplugin.Snapshot{Gen: 9401, Registry: jsplugin.NewRegistry(append(builtins.Plugins(), plugins...)...)})
	t.Cleanup(func() {
		if previous != nil {
			catalog.Replace(previous)
		} else {
			_ = catalog.InitBuiltins()
		}
	})
}

func TestPluginTransportProfilePriority(t *testing.T) {
	mounted := transportPlugin(t, "acme.mounted", "claude_code")
	base := siteTypeTransportPlugin("acme.base", "claude_code")
	plain := transportPlugin(t, "acme.plain", "")
	installTransportPlugins(t, mounted, base, plain)
	site.ClearPluginSiteTypes()
	t.Cleanup(site.ClearPluginSiteTypes)
	if err := site.SetPluginSiteTypes([]site.PluginSiteType{{
		Descriptor: site.SiteTypeDescriptor{SiteType: "acme.base/chat"},
		PluginID:   "acme.base",
		Enabled:    true,
	}}); err != nil {
		t.Fatal(err)
	}

	on := true
	candidate := routeengine.Candidate{Site: routeengine.CandidateSite{
		SiteType:  "acme.base/chat",
		PluginIDs: []string{"acme.mounted"},
	}}
	if got := pluginTransportProfile(candidate, &site.GatewayConfig{ImpersonateClaudeCodeClient: &on}); got != "claude_code" {
		t.Fatalf("admin switch = %q", got)
	}
	if got := upstreamClientProfileForRequest(nil, upstreamClientProfileRequest{TLSFingerprint: "claude_code"}, "").TLSFingerprint; got != httpclient.TLSFingerprintClaudeCode {
		t.Fatalf("request fingerprint = %q", got)
	}
	if got := upstreamClientProfileForRequest(nil, upstreamClientProfileRequest{ClaudeCode: true, TLSFingerprint: "other"}, "").TLSFingerprint; got != httpclient.TLSFingerprintClaudeCode {
		t.Fatalf("ClaudeCode override = %q", got)
	}

	if got := pluginTransportProfile(candidate, nil); got != "claude_code" {
		t.Fatalf("mounted = %q", got)
	}
	profile := upstreamClientProfileForRequest(nil, upstreamClientProfileRequest{TLSFingerprint: pluginTransportProfile(candidate, nil)}, "")
	if profile.TLSFingerprint != httpclient.TLSFingerprintClaudeCode {
		t.Fatalf("upstream profile = %q", profile.TLSFingerprint)
	}

	baseOnly := routeengine.Candidate{Site: routeengine.CandidateSite{SiteType: "acme.base/chat", PluginIDs: []string{"acme.plain"}}}
	if got := pluginTransportProfile(baseOnly, nil); got != "claude_code" {
		t.Fatalf("base = %q", got)
	}
	if got := pluginTransportProfile(routeengine.Candidate{Site: routeengine.CandidateSite{SiteType: "openai"}}, nil); got != "" {
		t.Fatalf("none = %q", got)
	}
}
