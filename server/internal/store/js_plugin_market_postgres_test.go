package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestPostgresJSPluginMarketSubscription(t *testing.T) {
	db, cfg, cleanup := openTemporaryMigrationStore(t)
	defer cleanup()
	ctx := context.Background()
	if err := ensureDatabaseInitializedOnce(ctx, cfg); err != nil {
		t.Fatalf("initialize schema: %v", err)
	}
	repo := NewJSPluginRepository(db)

	got, err := repo.GetMarketSubscription(ctx, "acme.demo")
	if err != nil {
		t.Fatalf("GetMarketSubscription(missing): %v", err)
	}
	if got.PluginID != "acme.demo" || got.UpdatePolicy != JSPluginUpdatePolicyNotify || got.PausedReason != "" {
		t.Fatalf("missing subscription = %#v, want notify default", got)
	}
	listed, err := repo.ListMarketSubscriptions(ctx)
	if err != nil || len(listed) != 0 {
		t.Fatalf("ListMarketSubscriptions() = %#v, %v, want none", listed, err)
	}

	if err := repo.UpsertPlugin(ctx, "acme.demo", JSPluginSourceMarket); err != nil {
		t.Fatalf("upsert plugin: %v", err)
	}
	if err := repo.UpsertMarketSubscription(ctx, JSPluginMarketSubscription{
		PluginID: "acme.demo", UpdatePolicy: JSPluginUpdatePolicyOff, PausedReason: "rolled back",
	}); err != nil {
		t.Fatalf("upsert subscription: %v", err)
	}
	got, err = repo.GetMarketSubscription(ctx, "acme.demo")
	if err != nil {
		t.Fatalf("GetMarketSubscription: %v", err)
	}
	if got.UpdatePolicy != JSPluginUpdatePolicyOff || got.PausedReason != "rolled back" || got.UpdatedAt.IsZero() {
		t.Fatalf("subscription = %#v", got)
	}
	listed, err = repo.ListMarketSubscriptions(ctx)
	if err != nil || len(listed) != 1 || listed[0].PluginID != "acme.demo" {
		t.Fatalf("ListMarketSubscriptions() = %#v, %v", listed, err)
	}

	if err := repo.UpsertMarketSubscription(ctx, JSPluginMarketSubscription{
		PluginID: "acme.demo", UpdatePolicy: JSPluginUpdatePolicyAuto,
	}); err != nil {
		t.Fatalf("upsert subscription again: %v", err)
	}
	got, err = repo.GetMarketSubscription(ctx, "acme.demo")
	if err != nil {
		t.Fatalf("GetMarketSubscription after update: %v", err)
	}
	if got.UpdatePolicy != JSPluginUpdatePolicyAuto || got.PausedReason != "" {
		t.Fatalf("updated subscription = %#v", got)
	}
}

func TestPostgresJSPluginMarketStateRoundTrip(t *testing.T) {
	db, cfg, cleanup := openTemporaryMigrationStore(t)
	defer cleanup()
	ctx := context.Background()
	if err := ensureDatabaseInitializedOnce(ctx, cfg); err != nil {
		t.Fatalf("initialize schema: %v", err)
	}
	repo := NewJSPluginRepository(db)

	got, err := repo.GetMarketState(ctx)
	if err != nil || got.ID != 0 || got.RootSeq != 0 || got.LeaseOwner != "" || got.LeaseUntil != nil || got.RootEnvelope != nil || got.SourceURL != "" {
		t.Fatalf("GetMarketState(missing) = %#v, %v, want zero", got, err)
	}

	synced := time.Date(2026, 10, 25, 4, 0, 0, 0, time.UTC)
	attempt := synced.Add(time.Minute)
	want := JSPluginMarketState{
		RootSeq:                  3,
		RootPayloadSHA256:        "root-sha",
		RootEnvelope:             []byte{1, 2, 3},
		RootETag:                 `"root"`,
		IndexSeq:                 9,
		IndexPayloadSHA256:       "index-sha",
		IndexEnvelope:            []byte{4, 5},
		IndexETag:                `"index"`,
		RevocationsSeq:           2,
		RevocationsPayloadSHA256: "revocations-sha",
		RevocationsEnvelope:      []byte{6},
		RevocationsETag:          `"revocations"`,
		IndexSyncedAt:            &synced,
		LastAttemptAt:            &attempt,
		LastError:                "timeout",
		SourceURL:                "https://registry.example.com/v1/",
	}
	if err := repo.SaveMarketState(ctx, want); err != nil {
		t.Fatalf("SaveMarketState: %v", err)
	}
	got, err = repo.GetMarketState(ctx)
	if err != nil {
		t.Fatalf("GetMarketState: %v", err)
	}
	assertMarketState(t, got, want)

	want.RootSeq = 4
	want.LastError = ""
	want.IndexEnvelope = nil
	if err := repo.SaveMarketState(ctx, want); err != nil {
		t.Fatalf("SaveMarketState again: %v", err)
	}
	got, err = repo.GetMarketState(ctx)
	if err != nil {
		t.Fatalf("GetMarketState after update: %v", err)
	}
	assertMarketState(t, got, want)

	ok, err := repo.TryAcquireMarketLease(ctx, "holder", time.Minute)
	if err != nil || !ok {
		t.Fatalf("TryAcquireMarketLease() = %v, %v, want acquired", ok, err)
	}
	want.RootSeq = 5
	if err := repo.SaveMarketState(ctx, want); err != nil {
		t.Fatalf("SaveMarketState while leased: %v", err)
	}
	got, err = repo.GetMarketState(ctx)
	if err != nil {
		t.Fatalf("GetMarketState while leased: %v", err)
	}
	if got.LeaseOwner != "holder" || got.LeaseUntil == nil || got.RootSeq != 5 {
		t.Fatalf("save cleared the lease or the payload: %#v", got)
	}
}

func TestPostgresJSPluginMarketLease(t *testing.T) {
	db, cfg, cleanup := openTemporaryMigrationStore(t)
	defer cleanup()
	ctx := context.Background()
	if err := ensureDatabaseInitializedOnce(ctx, cfg); err != nil {
		t.Fatalf("initialize schema: %v", err)
	}
	repo := NewJSPluginRepository(db)

	ok, err := repo.TryAcquireMarketLease(ctx, "a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("A TryAcquireMarketLease() = %v, %v, want acquired", ok, err)
	}
	ok, err = repo.TryAcquireMarketLease(ctx, "b", time.Minute)
	if err != nil || ok {
		t.Fatalf("B TryAcquireMarketLease() while A holds it = %v, %v, want not acquired", ok, err)
	}
	ok, err = repo.TryAcquireMarketLease(ctx, "a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("A renew = %v, %v, want acquired", ok, err)
	}
	ok, err = repo.TryAcquireMarketLease(ctx, "b", time.Minute)
	if err != nil || ok {
		t.Fatalf("B after A renew = %v, %v, want not acquired", ok, err)
	}

	if err := db.Exec(`UPDATE js_plugin_market_state SET lease_until = NOW() - INTERVAL '1 second' WHERE id = 1`).Error; err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	ok, err = repo.TryAcquireMarketLease(ctx, "b", time.Minute)
	if err != nil || !ok {
		t.Fatalf("B after expiry = %v, %v, want acquired", ok, err)
	}
	state, err := repo.GetMarketState(ctx)
	if err != nil || state.LeaseOwner != "b" || state.LeaseUntil == nil {
		t.Fatalf("state after B acquired = %#v, %v", state, err)
	}

	if err := repo.ReleaseMarketLease(ctx, "b"); err != nil {
		t.Fatalf("ReleaseMarketLease: %v", err)
	}
	state, err = repo.GetMarketState(ctx)
	if err != nil || state.LeaseOwner != "" || state.LeaseUntil != nil {
		t.Fatalf("state after release = %#v, %v", state, err)
	}
	ok, err = repo.TryAcquireMarketLease(ctx, "a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("A after release = %v, %v, want acquired", ok, err)
	}
}

func TestPostgresJSPluginMarketAutoUpdateWindow(t *testing.T) {
	db, cfg, cleanup := openTemporaryMigrationStore(t)
	defer cleanup()
	ctx := context.Background()
	if err := ensureDatabaseInitializedOnce(ctx, cfg); err != nil {
		t.Fatalf("initialize schema: %v", err)
	}
	repo := NewJSPluginRepository(db)
	if err := repo.UpsertPlugin(ctx, "acme.demo", JSPluginSourceMarket); err != nil {
		t.Fatalf("upsert plugin: %v", err)
	}

	row, err := repo.CreateAutoUpdate(ctx, JSPluginAutoUpdate{
		PluginID: "acme.demo", FromVersion: "1.0.0", ToVersion: "1.1.0", Status: JSPluginAutoUpdateApplied,
	})
	if err != nil {
		t.Fatalf("CreateAutoUpdate: %v", err)
	}
	if row.ID == uuid.Nil || row.CreatedAt.IsZero() || row.UpdatedAt.IsZero() {
		t.Fatalf("created row was not filled: %#v", row)
	}
	found, err := repo.FindRecentAppliedAutoUpdate(ctx, "acme.demo", "1.1.0", time.Hour)
	if err != nil || found.ID != row.ID {
		t.Fatalf("FindRecentAppliedAutoUpdate() = %#v, %v, want the new row", found, err)
	}

	if err := db.Exec(`UPDATE js_plugin_auto_updates SET created_at = NOW() - INTERVAL '2 hours' WHERE id = ?`, row.ID).Error; err != nil {
		t.Fatalf("age auto update: %v", err)
	}
	_, err = repo.FindRecentAppliedAutoUpdate(ctx, "acme.demo", "1.1.0", time.Hour)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("FindRecentAppliedAutoUpdate(within 1h of a 2h-old row) = %v, want not found", err)
	}
	found, err = repo.FindRecentAppliedAutoUpdate(ctx, "acme.demo", "1.1.0", 3*time.Hour)
	if err != nil || found.ID != row.ID {
		t.Fatalf("FindRecentAppliedAutoUpdate(within 3h) = %#v, %v", found, err)
	}

	if err := repo.UpdateAutoUpdateStatus(ctx, row.ID, JSPluginAutoUpdateRolledBack, "breaker"); err != nil {
		t.Fatalf("UpdateAutoUpdateStatus: %v", err)
	}
	_, err = repo.FindRecentAppliedAutoUpdate(ctx, "acme.demo", "1.1.0", 3*time.Hour)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("rolled back row should leave the applied window: %v", err)
	}
}

func TestPostgresJSPluginVersionOrigin(t *testing.T) {
	db, cfg, cleanup := openTemporaryMigrationStore(t)
	defer cleanup()
	ctx := context.Background()
	if err := ensureDatabaseInitializedOnce(ctx, cfg); err != nil {
		t.Fatalf("initialize schema: %v", err)
	}
	repo := NewJSPluginRepository(db)

	manual := JSPluginVersion{
		PluginID: "acme.demo", Version: "1.0.0", Manifest: JSON(`{}`), Package: []byte{1},
		PackageSHA256: "manual-sha", Status: JSPluginStatusUploaded, SelfTest: JSON(`{}`),
	}
	if err := repo.CreatePluginVersion(ctx, manual, JSPluginSourceUploaded); err != nil {
		t.Fatalf("CreatePluginVersion manual: %v", err)
	}
	got, err := repo.GetVersion(ctx, "acme.demo", "1.0.0")
	if err != nil {
		t.Fatalf("GetVersion: %v", err)
	}
	if got.Origin != JSPluginOriginManual || len(got.Attestation) != 0 {
		t.Fatalf("empty origin = %#v, want manual and no attestation", got)
	}
	if err := repo.UpdateVersionAttestation(ctx, "acme.demo", "1.0.0", JSPluginOriginMarket, JSON(`{"payloadType":"application/vnd.xlyra.attestation+json"}`)); err != nil {
		t.Fatalf("UpdateVersionAttestation: %v", err)
	}
	got, err = repo.GetVersion(ctx, "acme.demo", "1.0.0")
	if err != nil {
		t.Fatalf("GetVersion after attestation: %v", err)
	}
	if got.Origin != JSPluginOriginMarket || got.PackageSHA256 != "manual-sha" || !jsonEqual(got.Attestation, JSON(`{"payloadType":"application/vnd.xlyra.attestation+json"}`)) {
		t.Fatalf("updated attestation origin=%q sha=%q attestation=%s", got.Origin, got.PackageSHA256, got.Attestation)
	}

	market := manual
	market.Version = "1.1.0"
	market.PackageSHA256 = "market-sha"
	market.Origin = JSPluginOriginMarket
	market.Attestation = JSON(`{"v":1}`)
	if err := repo.CreatePluginVersion(ctx, market, JSPluginSourceMarket); err != nil {
		t.Fatalf("CreatePluginVersion market: %v", err)
	}
	got, err = repo.GetVersion(ctx, "acme.demo", "1.1.0")
	if err != nil {
		t.Fatalf("GetVersion market: %v", err)
	}
	if got.Origin != JSPluginOriginMarket || !jsonEqual(got.Attestation, JSON(`{"v":1}`)) {
		t.Fatalf("market version = origin %q attestation %s", got.Origin, got.Attestation)
	}
}

func assertMarketState(t *testing.T, got, want JSPluginMarketState) {
	t.Helper()
	if got.ID != 1 {
		t.Fatalf("state id = %d, want 1", got.ID)
	}
	got.ID = 0
	if !timePtrEqual(got.IndexSyncedAt, want.IndexSyncedAt) || !timePtrEqual(got.RevocationsSyncedAt, want.RevocationsSyncedAt) || !timePtrEqual(got.LastAttemptAt, want.LastAttemptAt) {
		t.Fatalf("state times = index %v revocations %v attempt %v", got.IndexSyncedAt, got.RevocationsSyncedAt, got.LastAttemptAt)
	}
	got.IndexSyncedAt, got.RevocationsSyncedAt, got.LastAttemptAt = want.IndexSyncedAt, want.RevocationsSyncedAt, want.LastAttemptAt
	got.LeaseOwner, got.LeaseUntil = want.LeaseOwner, want.LeaseUntil
	if !bytes.Equal(got.RootEnvelope, want.RootEnvelope) || !bytes.Equal(got.IndexEnvelope, want.IndexEnvelope) || !bytes.Equal(got.RevocationsEnvelope, want.RevocationsEnvelope) {
		t.Fatalf("state envelopes = %v %v %v", got.RootEnvelope, got.IndexEnvelope, got.RevocationsEnvelope)
	}
	got.RootEnvelope, got.IndexEnvelope, got.RevocationsEnvelope = want.RootEnvelope, want.IndexEnvelope, want.RevocationsEnvelope
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("state = %#v, want %#v", got, want)
	}
}

func timePtrEqual(got, want *time.Time) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return got.Equal(*want)
}

func jsonEqual(got, want JSON) bool {
	var left, right any
	if err := json.Unmarshal(got, &left); err != nil {
		return false
	}
	if err := json.Unmarshal(want, &right); err != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}
