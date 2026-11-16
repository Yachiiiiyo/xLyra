package gateway

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"xlyra/server/internal/jsplugin"
	routeengine "xlyra/server/internal/router"
	"xlyra/server/internal/site"
	"xlyra/server/internal/upstream"
)

func TestRewriteAndClassifySeeSiteSettings(t *testing.T) {
	site.ClearPluginSiteTypes()
	t.Cleanup(site.ClearPluginSiteTypes)

	const id = "acme.settings"
	source := `export function rewritePayload(ctx, payload) {
  if (!ctx.settings || ctx.settings.region !== "intl") throw new Error("region " + (ctx.settings && ctx.settings.region));
  payload.region = ctx.settings.region;
  return payload;
}
export function classify(ctx, input) {
  if (!ctx.settings || ctx.settings.region !== "intl") return { class: "unknown", reason: "missing" };
  return { class: "limited", reason: ctx.settings.region };
}`
	plugin := providerClassifierPlugin(t, id, source)
	plugin.Manifest.Settings = []jsplugin.SettingSpec{
		{Key: "region", Type: jsplugin.SettingTypeString, Default: "cn", Options: []string{"cn", "intl"}},
	}
	plugin.SetGlobalSettings(map[string]any{"region": "cn"})
	useGatewayPlugins(t, plugin)
	installProviderTypes(t, providerType(plugin, true))

	siteID := uuid.New()
	candidate := routeengine.Candidate{
		Site: routeengine.CandidateSite{
			ID:       siteID,
			SiteType: plugin.Manifest.SiteProvider.SiteType,
			PluginSettings: map[string]map[string]any{
				id: {"region": "intl"},
			},
		},
		Model: routeengine.CandidateModel{UpstreamName: "m"},
	}
	rewritten, err := applyProviderRewrite(context.Background(), candidate, &testGatewayProtocol{}, map[string]any{"model": "m"})
	if err != nil {
		t.Fatal(err)
	}
	if rewritten["region"] != "intl" || rewritten["model"] != "m" {
		t.Fatalf("rewritten = %#v", rewritten)
	}

	failure := applyErrorClassifierPlugin(candidate, http.StatusTooManyRequests, upstream.Failure{
		Class: upstream.FailureTransient, Code: "acme_slow", Message: "slow",
	})
	if failure.Class != upstream.FailureLimited || failure.Reason != "intl" {
		t.Fatalf("classified = %#v", failure)
	}

	globalOnly := candidate
	globalOnly.Site.PluginSettings = nil
	failure = applyErrorClassifierPlugin(globalOnly, http.StatusTooManyRequests, upstream.Failure{
		Class: upstream.FailureTransient, Code: "acme_slow", Message: "slow",
	})
	if failure.Class == upstream.FailureLimited {
		t.Fatalf("missing site override still classified as site value: %#v", failure)
	}
}
