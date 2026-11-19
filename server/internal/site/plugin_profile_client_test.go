package site

import (
	"context"
	"testing"

	"xlyra/server/internal/httpclient"
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/store"
)

func TestPluginProfileClientUsesDeclaredFingerprint(t *testing.T) {
	t.Parallel()
	source := "export const meta = { apiVersion: 1, id: \"acme.probe\", kind: \"quota_probe\" };\nexport function probe() { return { error: \"x\" }; }\n"
	plugin, err := jsplugin.NewPlugin(jsplugin.Manifest{
		ID: "acme.probe", Name: "acme.probe", Version: "1.0.0", APIVersion: 1, HostAPI: 1,
		Kind: jsplugin.KindQuotaProbe, QuotaProbe: jsplugin.QuotaProbeSection{BaseURLMode: "as_is"},
		Transport: jsplugin.TransportSection{Profile: " Claude_Code "},
		SHA256:    map[string]string{"plugin.js": jsplugin.HashSource(source)},
	}, source, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{httpClients: httpclient.NewManager(nil)}
	client, err := service.pluginProfileClient(context.Background(), store.Site{}, plugin)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := service.httpClientForSite(context.Background(), store.Site{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if client == nil || client == plain {
		t.Fatal("declared profile reused the default client")
	}
	again, err := service.pluginProfileClient(context.Background(), store.Site{}, plugin)
	if err != nil {
		t.Fatal(err)
	}
	if again != client {
		t.Fatal("fingerprinted client was not cached")
	}

	plugin.Manifest.Transport.Profile = ""
	fallback, err := service.pluginProfileClient(context.Background(), store.Site{}, plugin)
	if err != nil {
		t.Fatal(err)
	}
	if fallback != plain {
		t.Fatal("missing profile did not use the site client")
	}
}
