package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sitepkg "xlyra/server/internal/site"
	"xlyra/server/internal/store"
)

func TestListSiteTypesExposesAllowsEmptyAPIKeyOnlyForOptedInPlugin(t *testing.T) {
	sitepkg.ClearPluginSiteTypes()
	t.Cleanup(sitepkg.ClearPluginSiteTypes)

	if err := sitepkg.SetPluginSiteTypes([]sitepkg.PluginSiteType{
		{
			Descriptor: sitepkg.SiteTypeDescriptor{
				SiteType:           "acme.open/chat",
				DisplayName:        "Acme Open",
				Subtitle:           "Optional key",
				CredentialKey:      "acme.open/chat",
				OfficialBaseURL:    "https://open.example.com",
				ShowInCreateDialog: true,
				AllowsEmptyAPIKey:  true,
			},
			PluginID: "acme.open",
			Enabled:  true,
			Hosts:    []string{"open.example.com"},
		},
		{
			Descriptor: sitepkg.SiteTypeDescriptor{
				SiteType:           "acme.strict/chat",
				DisplayName:        "Acme Strict",
				Subtitle:           "Required key",
				CredentialKey:      "acme.strict/chat",
				OfficialBaseURL:    "https://strict.example.com",
				ShowInCreateDialog: true,
				AllowsEmptyAPIKey:  false,
			},
			PluginID: "acme.strict",
			Enabled:  true,
			Hosts:    []string{"strict.example.com"},
		},
	}); err != nil {
		t.Fatalf("SetPluginSiteTypes: %v", err)
	}

	rec := adminPerform((Handler{}).ListSiteTypes, adminTestRequest(http.MethodGet, "/api/v1/site-types", ""))
	adminAssertStatus(t, rec, http.StatusOK)

	var envelope struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	byType := map[string]map[string]json.RawMessage{}
	for _, raw := range envelope.Items {
		obj := mapFromRawObject(t, raw)
		var st string
		if err := json.Unmarshal(obj["site_type"], &st); err != nil {
			t.Fatalf("decode site_type: %v", err)
		}
		byType[st] = obj
	}

	if string(byType["acme.open/chat"]["allows_empty_api_key"]) != "true" {
		t.Fatalf("acme.open/chat allows_empty_api_key = %s, want true", byType["acme.open/chat"]["allows_empty_api_key"])
	}
	if _, exists := byType["acme.strict/chat"]["allows_empty_api_key"]; exists {
		t.Fatalf("acme.strict/chat should omit allows_empty_api_key, got %s", byType["acme.strict/chat"]["allows_empty_api_key"])
	}
	if _, exists := byType["openai"]["allows_empty_api_key"]; exists {
		t.Fatalf("built-in openai should omit allows_empty_api_key, got %s", byType["openai"]["allows_empty_api_key"])
	}
}

func TestResolveSiteCredentialsEmptyAPIKeyGuard(t *testing.T) {
	sitepkg.ClearPluginSiteTypes()
	t.Cleanup(sitepkg.ClearPluginSiteTypes)

	if err := sitepkg.SetPluginSiteTypes([]sitepkg.PluginSiteType{
		{
			Descriptor: sitepkg.SiteTypeDescriptor{
				SiteType:          "acme.open/chat",
				DisplayName:       "Acme Open",
				AllowsEmptyAPIKey: true,
			},
			PluginID: "acme.open",
			Enabled:  true,
			Hosts:    []string{"open.example.com"},
		},
		{
			Descriptor: sitepkg.SiteTypeDescriptor{
				SiteType:          "acme.strict/chat",
				DisplayName:       "Acme Strict",
				AllowsEmptyAPIKey: false,
			},
			PluginID: "acme.strict",
			Enabled:  true,
			Hosts:    []string{"strict.example.com"},
		},
	}); err != nil {
		t.Fatalf("SetPluginSiteTypes: %v", err)
	}

	handler := Handler{}

	// 1. Opted-in plugin allows manually adding an API key with an empty secret.
	rec := httptest.NewRecorder()
	req := adminTestRequest(http.MethodPost, "/api/v1/sites", "")
	creds, ok := handler.resolveSiteCredentials(rec, req, siteUpsertRequest{
		SiteType: "acme.open/chat",
		APIKeys: []siteAPIKeyRequest{
			{APIKey: "   ", Name: "No Auth"},
		},
	}, true)
	if !ok {
		t.Fatalf("resolveSiteCredentials(acme.open/chat empty key) failed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(creds) != 1 || creds[0].Secret != "" || creds[0].DisplayName == nil || *creds[0].DisplayName != "No Auth" {
		t.Fatalf("creds = %#v, want single empty-secret api_key named No Auth", creds)
	}

	// 2. Opted-in plugin still requires adding at least one API key on create.
	rec = httptest.NewRecorder()
	if _, ok := handler.resolveSiteCredentials(rec, req, siteUpsertRequest{
		SiteType: "acme.open/chat",
	}, true); ok {
		t.Fatal("resolveSiteCredentials(acme.open/chat without any api_keys) succeeded, want rejection")
	}
	assertAdminErrorCode(t, rec, http.StatusBadRequest, "invalid_api_key")

	// 3. Built-in site and non-opted-in plugin still reject empty API key secrets.
	for _, siteType := range []string{"openai", "anthropic", "acme.strict/chat"} {
		rec = httptest.NewRecorder()
		if _, ok := handler.resolveSiteCredentials(rec, req, siteUpsertRequest{
			SiteType: siteType,
			APIKeys:  []siteAPIKeyRequest{{APIKey: "", Name: "Empty"}},
		}, true); ok {
			t.Fatalf("resolveSiteCredentials(%q empty key) succeeded, want rejection", siteType)
		}
		assertAdminErrorCode(t, rec, http.StatusBadRequest, "invalid_api_key")
	}

	// 4. Site payload reflects allows_empty_api_key accurately.
	openPayload := handler.sitePayloadWithCapabilities(store.Site{SiteType: "acme.open/chat"})
	if openPayload["allows_empty_api_key"] != true {
		t.Fatalf("open site allows_empty_api_key = %#v, want true", openPayload["allows_empty_api_key"])
	}
	strictPayload := handler.sitePayloadWithCapabilities(store.Site{SiteType: "acme.strict/chat"})
	if strictPayload["allows_empty_api_key"] != false {
		t.Fatalf("strict site allows_empty_api_key = %#v, want false", strictPayload["allows_empty_api_key"])
	}
	builtinPayload := handler.sitePayloadWithCapabilities(store.Site{SiteType: "openai"})
	if builtinPayload["allows_empty_api_key"] != false {
		t.Fatalf("openai site allows_empty_api_key = %#v, want false", builtinPayload["allows_empty_api_key"])
	}
}
