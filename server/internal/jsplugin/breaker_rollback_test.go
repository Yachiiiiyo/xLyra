package jsplugin

import (
	"context"
	"testing"

	"xlyra/server/internal/store"
)

type rollbackCapture struct {
	pluginID string
	version  string
	reason   string
}

func (r *rollbackCapture) RollbackAutoUpdate(_ context.Context, pluginID, version, reason string) {
	r.pluginID = pluginID
	r.version = version
	r.reason = reason
}

func TestBreakerTripInvokesAutoRollback(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	repo := store.NewJSPluginRepository(st.DB())
	const id = "acme.rollback"
	if err := repo.CreatePluginVersion(ctx, store.JSPluginVersion{
		PluginID: id, Version: "1.1.0", Manifest: store.JSON(`{"id":"acme.rollback","version":"1.1.0","kind":"quota_probe"}`),
		Package: []byte("pkg"), PackageSHA256: "abc", Status: store.JSPluginStatusEnabled,
		SelfTest: store.JSON(`{"ok":true}`),
	}, store.JSPluginSourceMarket); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(st, nil)
	got := &rollbackCapture{}
	mgr.SetAutoUpdateRollback(got)
	mgr.onBreakerTrip(id, "1.1.0", "error_rate")
	if got.pluginID != id || got.version != "1.1.0" || got.reason != "error_rate" {
		t.Fatalf("rollback = %+v", got)
	}
	row, err := repo.GetVersion(ctx, id, "1.1.0")
	if err != nil || row.Status != store.JSPluginStatusDisabled {
		t.Fatalf("status = %s %v", row.Status, err)
	}
}
