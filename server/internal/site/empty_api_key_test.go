package site

import (
	"testing"

	"xlyra/server/internal/jsplugin"
)

func TestAllowsEmptyAPIKeyEnforcesPluginDeclaration(t *testing.T) {
	ClearPluginSiteTypes()
	t.Cleanup(ClearPluginSiteTypes)

	if err := SetPluginSiteTypes([]PluginSiteType{
		{
			Descriptor: SiteTypeDescriptor{
				SiteType:          "acme.open/chat",
				DisplayName:       "Acme Open",
				AllowsEmptyAPIKey: true,
			},
			PluginID: "acme.open",
			Enabled:  true,
			Hosts:    []string{"open.example.com"},
		},
		{
			Descriptor: SiteTypeDescriptor{
				SiteType:          "acme.strict/chat",
				DisplayName:       "Acme Strict",
				AllowsEmptyAPIKey: false,
			},
			PluginID: "acme.strict",
			Enabled:  true,
			Hosts:    []string{"strict.example.com"},
		},
		{
			Descriptor: SiteTypeDescriptor{
				SiteType:          "acme.disabled/chat",
				DisplayName:       "Acme Disabled",
				AllowsEmptyAPIKey: true,
			},
			PluginID: "acme.disabled",
			Enabled:  false,
			Hosts:    []string{"disabled.example.com"},
		},
		{
			Descriptor: SiteTypeDescriptor{
				SiteType:          "acme.pool/account",
				DisplayName:       "Acme Pool",
				AllowsEmptyAPIKey: true,
			},
			PluginID: "acme.pool",
			Enabled:  true,
			Hosts:    []string{"pool.example.com"},
			Pool: &jsplugin.AccountPoolSpec{
				DefaultRegion: "global",
				Regions: []jsplugin.SiteProviderRegion{
					{ID: "global", BaseURL: "https://pool.example.com"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("SetPluginSiteTypes: %v", err)
	}

	if !AnyPluginAllowsEmptyAPIKey() {
		t.Fatal("AnyPluginAllowsEmptyAPIKey = false, want true")
	}
	if !AllowsEmptyAPIKey("acme.open/chat") || !AllowsEmptyAPIKey(" ACME.OPEN/Chat ") {
		t.Fatal("AllowsEmptyAPIKey(acme.open/chat) = false, want true")
	}
	for _, siteType := range []string{
		"openai",
		"anthropic",
		"google_gemini",
		"deepseek",
		"acme.strict/chat",
		"acme.disabled/chat",
		"acme.pool/account",
		"unknown.plugin/chat",
	} {
		if AllowsEmptyAPIKey(siteType) {
			t.Fatalf("AllowsEmptyAPIKey(%q) = true, want false", siteType)
		}
	}

	ClearPluginSiteTypes()
	if AnyPluginAllowsEmptyAPIKey() {
		t.Fatal("AnyPluginAllowsEmptyAPIKey after clear = true, want false")
	}
}

func TestPrepareCreateCredentialInputsEmptyAPIKeyGuard(t *testing.T) {
	ClearPluginSiteTypes()
	t.Cleanup(ClearPluginSiteTypes)

	if err := SetPluginSiteTypes([]PluginSiteType{
		{
			Descriptor: SiteTypeDescriptor{
				SiteType:          "acme.open/chat",
				DisplayName:       "Acme Open",
				AllowsEmptyAPIKey: true,
			},
			PluginID: "acme.open",
			Enabled:  true,
			Hosts:    []string{"open.example.com"},
		},
		{
			Descriptor: SiteTypeDescriptor{
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

	name := "Public"
	emptyInputs := []CredentialInput{{
		Type:        "api_key",
		Secret:      "",
		DisplayName: &name,
	}}

	prepared, err := prepareCreateCredentialInputs("acme.open/chat", emptyInputs)
	if err != nil {
		t.Fatalf("prepareCreateCredentialInputs(acme.open/chat) error = %v", err)
	}
	if len(prepared) != 1 || prepared[0].Secret != "" {
		t.Fatalf("prepared = %#v, want 1 empty-secret api_key credential", prepared)
	}

	for _, siteType := range []string{"openai", "anthropic", "acme.strict/chat"} {
		if _, err := prepareCreateCredentialInputs(siteType, emptyInputs); err == nil {
			t.Fatalf("prepareCreateCredentialInputs(%q) succeeded with empty secret, want error", siteType)
		}
	}
}
