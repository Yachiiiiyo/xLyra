package jsplugin

import (
	"context"
	"strings"
	"testing"

	"xlyra/server/internal/store"
)

func TestWithStorageScopeResolutionOrder(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	const id = "acme.scope"
	if err := store.NewJSPluginRepository(st.DB()).UpsertPlugin(ctx, id, store.JSPluginSourceUploaded); err != nil {
		t.Fatal(err)
	}
	repo := store.NewJSPluginStorageRepository(st.DB())

	source := `export const meta = { apiVersion: 1, id: "acme.scope", kind: "quota_probe" };
export function probe() {
  const seen = storage.get("seen");
  return { result: { kind: "balance", plan: seen == null ? "" : String(seen), entries: [{ label: "ok", remaining: 1 }] } };
}`
	manifest := testManifest(id, source)
	manifest.Permissions = []string{PermissionStorage}
	plugin, err := NewPlugin(manifest, source, []Fixture{{Name: "unused"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	plugin.SetStorageRepository(repo)
	if err := NewStoreStorage(repo, id, StorageScopeGlobal, "").Set(ctx, "seen", "global"); err != nil {
		t.Fatal(err)
	}
	if err := NewStoreStorage(repo, id, StorageScopeSite, "site-1").Set(ctx, "seen", "site"); err != nil {
		t.Fatal(err)
	}

	if got := probePlan(t, plugin, context.Background()); got != "global" {
		t.Fatalf("default scope = %q", got)
	}
	if got := probePlan(t, plugin, WithStorageScope(context.Background(), StorageScopeSite, "site-1")); got != "site" {
		t.Fatalf("site scope = %q", got)
	}
	explicit, err := NewMemoryStorage(map[string]any{"seen": "explicit"})
	if err != nil {
		t.Fatal(err)
	}
	got := probePlan(t, plugin, WithCallStorage(WithStorageScope(context.Background(), StorageScopeSite, "site-1"), explicit))
	if got != "explicit" {
		t.Fatalf("explicit backend = %q", got)
	}
	if got := probePlan(t, plugin, WithStorageScope(context.Background(), StorageScopeSite, "")); got != "global" {
		t.Fatalf("empty scope id = %q", got)
	}

	noPerm := manifest
	noPerm.Permissions = nil
	bare, err := NewPlugin(noPerm, source, []Fixture{{Name: "unused"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	bare.SetStorageRepository(repo)
	if _, err := bare.CallProbe(WithStorageScope(context.Background(), StorageScopeSite, "site-1"), ProbeContext{}, nil); err == nil || !strings.Contains(err.Error(), "permission_denied") {
		t.Fatalf("storage without permission = %v", err)
	}
}

func probePlan(t *testing.T, plugin *Plugin, ctx context.Context) string {
	t.Helper()
	decision, err := plugin.CallProbe(ctx, ProbeContext{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Result == nil {
		t.Fatalf("probe result missing: %#v", decision)
	}
	return decision.Result.Plan
}
