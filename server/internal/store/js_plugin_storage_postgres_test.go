package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestPostgresJSPluginStorageAndSettingsDefault(t *testing.T) {
	db, cfg, cleanup := openTemporaryMigrationStore(t)
	defer cleanup()
	ctx := context.Background()
	if err := ensureDatabaseInitializedOnce(ctx, cfg); err != nil {
		t.Fatalf("initialize schema: %v", err)
	}

	repo := NewJSPluginRepository(db)
	const pluginID = "acme.storage"
	if err := repo.UpsertPlugin(ctx, pluginID, JSPluginSourceUploaded); err != nil {
		t.Fatal(err)
	}
	row, err := repo.GetPlugin(ctx, pluginID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(row.Settings)) != "{}" {
		t.Fatalf("settings default = %s, want {}", row.Settings)
	}

	storage := NewJSPluginStorageRepository(db)
	if err := storage.Set(ctx, pluginID, JSPluginStorageScopeGlobal, "", "counter", JSON(`1`)); err != nil {
		t.Fatal(err)
	}
	if err := storage.Set(ctx, pluginID, JSPluginStorageScopeSite, "site-a", "counter", JSON(`2`)); err != nil {
		t.Fatal(err)
	}
	got, ok, err := storage.Get(ctx, pluginID, JSPluginStorageScopeGlobal, "", "counter")
	if err != nil || !ok || string(got) != "1" {
		t.Fatalf("global get = %s ok=%v err=%v", got, ok, err)
	}
	got, ok, err = storage.Get(ctx, pluginID, JSPluginStorageScopeSite, "site-a", "counter")
	if err != nil || !ok || string(got) != "2" {
		t.Fatalf("site get = %s ok=%v err=%v", got, ok, err)
	}
	missing, ok, err := storage.Get(ctx, pluginID, JSPluginStorageScopeSite, "site-b", "counter")
	if err != nil || ok || missing != nil {
		t.Fatalf("other scope get = %s ok=%v err=%v", missing, ok, err)
	}

	keys, err := storage.Keys(ctx, pluginID, JSPluginStorageScopeSite, "site-a")
	if err != nil || len(keys) != 1 || keys[0] != "counter" {
		t.Fatalf("keys = %#v err=%v", keys, err)
	}
	deleted, err := storage.Delete(ctx, pluginID, JSPluginStorageScopeSite, "site-a", "counter")
	if err != nil || !deleted {
		t.Fatalf("delete = %v %v", deleted, err)
	}
	if _, ok, err := storage.Get(ctx, pluginID, JSPluginStorageScopeSite, "site-a", "counter"); err != nil || ok {
		t.Fatalf("deleted get ok=%v err=%v", ok, err)
	}
	if still, ok, err := storage.Get(ctx, pluginID, JSPluginStorageScopeGlobal, "", "counter"); err != nil || !ok || string(still) != "1" {
		t.Fatalf("global survived delete = %s ok=%v err=%v", still, ok, err)
	}

	for i := 0; i < JSPluginStorageMaxKeysPerScope; i++ {
		key := fmt.Sprintf("k%02d", i)
		if err := storage.Set(ctx, pluginID, JSPluginStorageScopeCredential, "cred-1", key, JSON(`"x"`)); err != nil {
			t.Fatalf("set key %d: %v", i, err)
		}
	}
	if err := storage.Set(ctx, pluginID, JSPluginStorageScopeCredential, "cred-1", "overflow", JSON(`"x"`)); !errors.Is(err, ErrJSPluginStorageKeyLimit) {
		t.Fatalf("65th key = %v", err)
	}
	if err := storage.Set(ctx, pluginID, JSPluginStorageScopeCredential, "cred-1", "k00", JSON(`"y"`)); err != nil {
		t.Fatalf("overwrite existing key: %v", err)
	}

	big := `"` + strings.Repeat("a", JSPluginStorageMaxBytesPerScope) + `"`
	if err := storage.Set(ctx, pluginID, JSPluginStorageScopeSite, "site-big", "blob", JSON(big)); !errors.Is(err, ErrJSPluginStorageByteLimit) {
		t.Fatalf("oversize value = %v", err)
	}
	half := `"` + strings.Repeat("b", JSPluginStorageMaxBytesPerScope/2) + `"`
	if err := storage.Set(ctx, pluginID, JSPluginStorageScopeSite, "site-bytes", "one", JSON(half)); err != nil {
		t.Fatal(err)
	}
	if err := storage.Set(ctx, pluginID, JSPluginStorageScopeSite, "site-bytes", "two", JSON(half)); !errors.Is(err, ErrJSPluginStorageByteLimit) {
		t.Fatalf("cumulative bytes = %v", err)
	}
	if err := storage.Set(ctx, pluginID, JSPluginStorageScopeSite, "site-bytes", "one", JSON(`"short"`)); err != nil {
		t.Fatalf("overwrite counts new size: %v", err)
	}
	if err := storage.Set(ctx, pluginID, JSPluginStorageScopeSite, "site-bytes", "two", JSON(half)); err != nil {
		t.Fatalf("second key after shrink: %v", err)
	}

	if err := storage.Set(ctx, pluginID, "user", "", "nope", JSON(`1`)); err == nil {
		t.Fatal("illegal scope_type was accepted")
	}

	if err := repo.DeletePlugin(ctx, pluginID); err != nil {
		t.Fatal(err)
	}
	var left int64
	if err := db.WithContext(ctx).Model(&JSPluginStorageEntry{}).Where("plugin_id = ?", pluginID).Count(&left).Error; err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("storage rows after plugin delete = %d", left)
	}
}
