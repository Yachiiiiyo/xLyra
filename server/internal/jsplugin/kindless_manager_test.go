package jsplugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestEnableKindlessGrantAndTrust(t *testing.T) {
	st := openTestStore(t)
	mgr := NewManager(st, nil)
	ctx := context.Background()

	source := "export const meta = { apiVersion: 1, id: \"acme.hooks\" };\nexport function probe() { return { error: \"later\" }; }\n"
	manifest := Manifest{
		ID: "acme.hooks", Name: "Hooks", Version: "1.0.0", APIVersion: 1, HostAPI: 1,
		Permissions: []string{"storage"},
		SHA256:      map[string]string{"plugin.js": hashSource(source)},
	}
	fixture := []byte(`{"name":"one","hook":"probe","expect":{"error":"later"}}`)
	if _, err := mgr.Upload(ctx, "", siteProviderZip(t, manifest, source, map[string][]byte{"fixtures/one.json": fixture})); err != nil {
		t.Fatal(err)
	}
	err := mgr.Enable(ctx, "acme.hooks", "1.0.0", EnableOptions{ConfirmUntrusted: true})
	var required *PermissionsRequiredError
	if !errors.As(err, &required) || strings.Join(required.Required, ",") != "storage" {
		t.Fatalf("missing grant: %v", err)
	}
	if err := mgr.Enable(ctx, "acme.hooks", "1.0.0", EnableOptions{ConfirmUntrusted: true, GrantPermissions: []string{"storage"}}); err != nil {
		t.Fatal(err)
	}
	row, err := mgr.repo.GetVersion(ctx, "acme.hooks", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	var granted []string
	if err := json.Unmarshal(row.GrantedPermissions, &granted); err != nil {
		t.Fatal(err)
	}
	if strings.Join(granted, ",") != "storage" {
		t.Fatalf("granted = %#v", granted)
	}

	secretSrc := "export const meta = { apiVersion: 1, id: \"acme.secret\" };\nexport function probe() { return { error: \"later\" }; }\n"
	secret := Manifest{
		ID: "acme.secret", Name: "Secret", Version: "1.0.0", APIVersion: 1, HostAPI: 1,
		Permissions: []string{"secret.use"},
		SHA256:      map[string]string{"plugin.js": hashSource(secretSrc)},
	}
	if _, err := mgr.Upload(ctx, "", siteProviderZip(t, secret, secretSrc, map[string][]byte{"fixtures/one.json": fixture})); err != nil {
		t.Fatal(err)
	}
	err = mgr.Enable(ctx, "acme.secret", "1.0.0", EnableOptions{ConfirmUntrusted: true})
	var untrusted *SiteProviderUntrustedError
	if !errors.As(err, &untrusted) || !errors.Is(err, ErrSiteProviderUntrusted) {
		t.Fatalf("secret.use unsigned: %v", err)
	}

	siteSrc := "export const meta = { apiVersion: 1, id: \"acme.example\" };\n"
	siteManifest := siteProviderManifest(siteSrc)
	siteManifest.Kind = ""
	if _, err := mgr.Upload(ctx, "", siteProviderZip(t, siteManifest, siteSrc, nil)); err != nil {
		t.Fatal(err)
	}
	err = mgr.Enable(ctx, "acme.example", "1.0.0", EnableOptions{ConfirmUntrusted: true})
	untrusted = nil
	if !errors.As(err, &untrusted) || !errors.Is(err, ErrSiteProviderUntrusted) {
		t.Fatalf("kindless site type unsigned: %v", err)
	}
}
