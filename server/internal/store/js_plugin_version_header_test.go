package store

import (
	"context"
	"testing"
)

func TestListVersionHeadersOmitPackage(t *testing.T) {
	db, cfg, cleanup := openTemporaryMigrationStore(t)
	defer cleanup()
	ctx := context.Background()
	if err := ensureDatabaseInitializedOnce(ctx, cfg); err != nil {
		t.Fatalf("initialize schema: %v", err)
	}
	repo := NewJSPluginRepository(db)
	body := []byte("package-body-should-not-be-selected")
	rows := []JSPluginVersion{
		{
			PluginID: "acme.icon", Version: "1.0.0",
			Manifest:      JSON(`{"kind":"quota_probe","icon":"assets/icon.png"}`),
			Package:       body,
			PackageSHA256: "sha-icon-version",
			Status:        JSPluginStatusVerified,
			SelfTest:      JSON(`{}`),
		},
		{
			PluginID: "acme.plain", Version: "1.0.0",
			Manifest:      JSON(`{"kind":"quota_probe"}`),
			Package:       body,
			PackageSHA256: "sha-plain-version",
			Status:        JSPluginStatusVerified,
			SelfTest:      JSON(`{}`),
		},
	}
	for _, row := range rows {
		if err := repo.CreatePluginVersion(ctx, row, JSPluginSourceUploaded); err != nil {
			t.Fatalf("create %s: %v", row.PluginID, err)
		}
	}

	one, err := repo.ListVersionHeadersForPlugin(ctx, "acme.icon")
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || len(one[0].Package) != 0 || one[0].PackageSHA256 != "sha-icon-version" || len(one[0].Manifest) == 0 || one[0].CreatedAt.IsZero() || one[0].Status != JSPluginStatusVerified {
		t.Fatalf("plugin header = package %d sha %q manifest %d at %v status %q", len(one[0].Package), one[0].PackageSHA256, len(one[0].Manifest), one[0].CreatedAt, one[0].Status)
	}
	all, err := repo.ListAllVersionHeaders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("all headers = %d", len(all))
	}
	for _, row := range all {
		if len(row.Package) != 0 {
			t.Fatalf("%s package bytes were selected (%d)", row.PluginID, len(row.Package))
		}
	}
	full, err := repo.GetVersion(ctx, "acme.icon", "1.0.0")
	if err != nil || string(full.Package) != string(body) {
		t.Fatalf("package column = %q %v", full.Package, err)
	}
}
