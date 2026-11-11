package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestAdminDeviceTableNames(t *testing.T) {
	t.Parallel()

	if got := (AdminDevice{}).TableName(); got != "admin_devices" {
		t.Fatalf("admin device table = %q, want admin_devices", got)
	}
	if got := (AdminDevicePairing{}).TableName(); got != "admin_device_pairings" {
		t.Fatalf("admin device pairing table = %q, want admin_device_pairings", got)
	}
	if _, ok := bootstrapModelsByTable()["admin_devices"]; !ok {
		t.Fatal("bootstrap models must register admin_devices")
	}
	if _, ok := bootstrapModelsByTable()["admin_device_pairings"]; !ok {
		t.Fatal("bootstrap models must register admin_device_pairings")
	}
}

func TestAdminDeviceRedeemPairingCreatesDevice(t *testing.T) {
	t.Parallel()

	adminID := uuid.New()
	pairingID := uuid.New()
	var created AdminDevice
	var markedUsed bool
	db := storeTransactionGorm(t, "device pairing redeem")
	storeReplaceQueryCallback(t, db, func(tx *gorm.DB) {
		pairing, ok := tx.Statement.Dest.(*AdminDevicePairing)
		if !ok {
			tx.AddError(errors.New("unexpected device pairing query destination"))
			return
		}
		*pairing = AdminDevicePairing{ID: pairingID, AdminID: adminID, ExpiresAt: time.Now().Add(time.Minute)}
		tx.Statement.RowsAffected = 1
	})
	storeReplaceUpdateCallback(t, db, func(tx *gorm.DB) {
		markedUsed = true
		tx.Statement.RowsAffected = 1
	})
	storeReplaceCreateCallback(t, db, func(tx *gorm.DB) {
		device, ok := tx.Statement.Dest.(*AdminDevice)
		if !ok {
			tx.AddError(errors.New("unexpected device create destination"))
			return
		}
		created = *device
		device.ID = uuid.New()
		tx.Statement.RowsAffected = 1
	})

	device, err := NewAdminDeviceRepository(db).RedeemPairing(context.Background(), RedeemAdminDevicePairingParams{
		CodeHash:  "hash",
		Name:      "iphone",
		Platform:  "ios",
		TokenHash: "token-hash",
		Now:       time.Now(),
	})
	if err != nil {
		t.Fatalf("RedeemPairing: %v", err)
	}
	if !markedUsed {
		t.Fatal("pairing code was not marked as used")
	}
	if created.AdminID != adminID || created.Name != "iphone" || created.Platform != "ios" || created.TokenHash != "token-hash" {
		t.Fatalf("created device = %#v, want pairing admin and redemption fields", created)
	}
	if device.ID == uuid.Nil {
		t.Fatal("redeemed device has no id")
	}
}

func TestAdminDeviceRedeemPairingRejectsUnusableCode(t *testing.T) {
	t.Parallel()

	t.Run("missing or expired", func(t *testing.T) {
		t.Parallel()

		db := storeTransactionGorm(t, "device pairing missing")
		storeQueryRecordNotFound(t, db)
		_, err := NewAdminDeviceRepository(db).RedeemPairing(context.Background(), RedeemAdminDevicePairingParams{CodeHash: "hash"})
		if !errors.Is(err, ErrDevicePairingInvalid) {
			t.Fatalf("RedeemPairing error = %v, want ErrDevicePairingInvalid", err)
		}
	})

	t.Run("already used", func(t *testing.T) {
		t.Parallel()

		db := storeTransactionGorm(t, "device pairing used")
		storeReplaceQueryCallback(t, db, func(tx *gorm.DB) {
			pairing, ok := tx.Statement.Dest.(*AdminDevicePairing)
			if !ok {
				tx.AddError(errors.New("unexpected device pairing query destination"))
				return
			}
			*pairing = AdminDevicePairing{ID: uuid.New(), AdminID: uuid.New()}
			tx.Statement.RowsAffected = 1
		})
		storeReplaceUpdateCallback(t, db, func(tx *gorm.DB) {
			tx.Statement.RowsAffected = 0
		})
		storeReplaceCreateCallback(t, db, func(tx *gorm.DB) {
			tx.AddError(errors.New("an already used pairing code must not create a device"))
		})
		_, err := NewAdminDeviceRepository(db).RedeemPairing(context.Background(), RedeemAdminDevicePairingParams{CodeHash: "hash"})
		if !errors.Is(err, ErrDevicePairingInvalid) {
			t.Fatalf("RedeemPairing error = %v, want ErrDevicePairingInvalid", err)
		}
	})
}

func TestAdminDeviceRevokeScopesToAdminAndWritesRevokedAt(t *testing.T) {
	t.Parallel()

	db := storeRepositoryOfflineGorm(t)
	updates := map[string]any{}
	scopedToAdmin := false
	scopedToDevice := false
	storeReplaceUpdateCallback(t, db, func(tx *gorm.DB) {
		if dest, ok := tx.Statement.Dest.(map[string]any); ok {
			updates = dest
		}
		where := tx.Statement.Clauses["WHERE"]
		scopedToAdmin = clauseHasColumn(where.Expression, "admin_id")
		scopedToDevice = clauseHasColumn(where.Expression, "id")
		tx.Statement.RowsAffected = 1
	})

	if err := NewAdminDeviceRepository(db).Revoke(context.Background(), uuid.New(), uuid.New(), time.Now()); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if updates["revoked_at"] == nil || updates["apns_token_enc"] != "" || updates["apns_env"] != "" || updates["push_state"] != "" {
		t.Fatalf("revoke updates = %v, want revoked_at plus cleared push registration", updates)
	}
	if !scopedToAdmin || !scopedToDevice {
		t.Fatalf("revoke scope admin/device = %v/%v, want both", scopedToAdmin, scopedToDevice)
	}

	// 他人设备（或已吊销）拿到 0 行受影响, 上层据此回 404。
	db = storeRepositoryOfflineGorm(t)
	storeReplaceUpdateCallback(t, db, func(tx *gorm.DB) {
		tx.Statement.RowsAffected = 0
	})
	err := NewAdminDeviceRepository(db).Revoke(context.Background(), uuid.New(), uuid.New(), time.Now())
	assertStoreRecordNotFound(t, err)
}

func TestAdminDeviceRevokeAllReportsCount(t *testing.T) {
	t.Parallel()

	db := storeRepositoryOfflineGorm(t)
	storeReplaceUpdateCallback(t, db, func(tx *gorm.DB) {
		tx.Statement.RowsAffected = 3
	})

	revoked, err := NewAdminDeviceRepository(db).RevokeAll(context.Background(), uuid.New(), time.Now())
	if err != nil || revoked != 3 {
		t.Fatalf("RevokeAll = %d, %v; want 3", revoked, err)
	}
}

func TestAdminDeviceListSkipsRevokedAndSortsNewestFirst(t *testing.T) {
	t.Parallel()

	adminID := uuid.New()
	older := uuid.New()
	newer := uuid.New()
	revoked := uuid.New()
	db := storeRepositoryOfflineGorm(t)
	storeReplaceQueryCallback(t, db, func(tx *gorm.DB) {
		devices, ok := tx.Statement.Dest.(*[]AdminDevice)
		if !ok {
			tx.AddError(errors.New("unexpected admin device query destination"))
			return
		}
		*devices = []AdminDevice{
			{ID: older, AdminID: adminID, CreatedAt: time.Now().Add(-time.Hour)},
			{ID: revoked, AdminID: adminID, CreatedAt: time.Now(), RevokedAt: sql.NullTime{Time: time.Now(), Valid: true}},
			{ID: newer, AdminID: adminID, CreatedAt: time.Now()},
		}
		tx.Statement.RowsAffected = 3
	})

	devices, err := NewAdminDeviceRepository(db).ListByAdmin(context.Background(), adminID)
	if err != nil {
		t.Fatalf("ListByAdmin: %v", err)
	}
	if len(devices) != 2 || devices[0].ID != newer || devices[1].ID != older {
		t.Fatalf("devices = %#v, want active devices newest first", devices)
	}
}

func TestAdminDeviceGetActiveByTokenHashSkipsRevoked(t *testing.T) {
	t.Parallel()

	activeID := uuid.New()
	db := storeRepositoryOfflineGorm(t)
	storeReplaceQueryCallback(t, db, func(tx *gorm.DB) {
		devices, ok := tx.Statement.Dest.(*[]AdminDevice)
		if !ok {
			tx.AddError(errors.New("unexpected admin device query destination"))
			return
		}
		*devices = []AdminDevice{
			{ID: uuid.New(), TokenHash: "hash", RevokedAt: sql.NullTime{Time: time.Now(), Valid: true}},
			{ID: activeID, TokenHash: "hash"},
		}
		tx.Statement.RowsAffected = 2
	})

	device, err := NewAdminDeviceRepository(db).GetActiveByTokenHash(context.Background(), "hash")
	if err != nil || device.ID != activeID {
		t.Fatalf("GetActiveByTokenHash = %#v, %v; want the active device", device, err)
	}

	db = storeRepositoryOfflineGorm(t)
	storeReplaceQueryCallback(t, db, func(tx *gorm.DB) {
		devices, ok := tx.Statement.Dest.(*[]AdminDevice)
		if !ok {
			tx.AddError(errors.New("unexpected admin device query destination"))
			return
		}
		*devices = []AdminDevice{{ID: uuid.New(), TokenHash: "hash", RevokedAt: sql.NullTime{Time: time.Now(), Valid: true}}}
		tx.Statement.RowsAffected = 1
	})
	if _, err := NewAdminDeviceRepository(db).GetActiveByTokenHash(context.Background(), "hash"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("revoked-only GetActiveByTokenHash error = %v, want record not found", err)
	}
}

func TestAdminDeviceTouchLastSeenThrottlesWrites(t *testing.T) {
	t.Parallel()

	db := storeRepositoryOfflineGorm(t)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	updates := map[string]any{}
	throttled := false
	storeReplaceUpdateCallback(t, db, func(tx *gorm.DB) {
		if dest, ok := tx.Statement.Dest.(map[string]any); ok {
			updates = dest
		}
		where := tx.Statement.Clauses["WHERE"]
		throttled = clauseHasLastSeenLte(where.Expression)
		tx.Statement.RowsAffected = 1
	})

	if err := NewAdminDeviceRepository(db).TouchLastSeen(context.Background(), uuid.New(), nil, "xlyra-ios/1.0", now, time.Minute); err != nil {
		t.Fatalf("TouchLastSeen: %v", err)
	}
	// 节流条件必须落在 WHERE 里: last_seen_at 为空或早于 now-throttle 才更新。
	if !throttled {
		t.Fatal("TouchLastSeen must throttle writes on last_seen_at")
	}
	for _, key := range []string{"last_seen_at", "last_ip", "last_user_agent"} {
		if _, ok := updates[key]; !ok {
			t.Fatalf("TouchLastSeen updates = %v, want %s", updates, key)
		}
	}
}

// clauseHasColumn 递归查找 WHERE 表达式里对某一列的等值条件。
func clauseHasColumn(expr clause.Expression, name string) bool {
	switch value := expr.(type) {
	case clause.Where:
		return anyClauseMatches(value.Exprs, func(item clause.Expression) bool { return clauseHasColumn(item, name) })
	case clause.OrConditions:
		return anyClauseMatches(value.Exprs, func(item clause.Expression) bool { return clauseHasColumn(item, name) })
	case clause.AndConditions:
		return anyClauseMatches(value.Exprs, func(item clause.Expression) bool { return clauseHasColumn(item, name) })
	case clause.Eq:
		column, ok := value.Column.(clause.Column)
		return ok && column.Name == name
	default:
		return false
	}
}

// clauseHasLastSeenLte 在 WHERE 表达式里找 last_seen_at <= now-throttle 的条件。
func clauseHasLastSeenLte(expr clause.Expression) bool {
	switch value := expr.(type) {
	case clause.Where:
		return anyClauseMatches(value.Exprs, clauseHasLastSeenLte)
	case clause.OrConditions:
		return anyClauseMatches(value.Exprs, clauseHasLastSeenLte)
	case clause.AndConditions:
		return anyClauseMatches(value.Exprs, clauseHasLastSeenLte)
	case clause.Lte:
		column, ok := value.Column.(clause.Column)
		return ok && column.Name == "last_seen_at"
	default:
		return false
	}
}

func TestAdminDeviceRegisterPushResetsSyncState(t *testing.T) {
	t.Parallel()

	db := storeRepositoryOfflineGorm(t)
	updates := map[string]any{}
	storeReplaceUpdateCallback(t, db, func(tx *gorm.DB) {
		dest, ok := tx.Statement.Dest.(map[string]any)
		if !ok {
			tx.AddError(errors.New("unexpected push register update destination"))
			return
		}
		updates = dest
		tx.Statement.RowsAffected = 1
	})

	now := time.Now()
	if err := NewAdminDeviceRepository(db).RegisterPush(context.Background(), uuid.New(), "enc-token", "sandbox", now); err != nil {
		t.Fatalf("RegisterPush: %v", err)
	}
	if updates["apns_token_enc"] != "enc-token" || updates["apns_env"] != "sandbox" || updates["push_registered_at"] != now {
		t.Fatalf("register updates = %v, want encrypted token, env and registered_at", updates)
	}
	if _, ok := updates["push_synced_at"]; !ok || !isNullExpr(updates["push_synced_at"]) || updates["push_state"] != "" {
		t.Fatalf("register updates = %v, want push_synced_at NULL and empty push_state", updates)
	}
}

func TestAdminDeviceClearPushDropsToken(t *testing.T) {
	t.Parallel()

	db := storeRepositoryOfflineGorm(t)
	updates := map[string]any{}
	storeReplaceUpdateCallback(t, db, func(tx *gorm.DB) {
		dest, ok := tx.Statement.Dest.(map[string]any)
		if !ok {
			tx.AddError(errors.New("unexpected push clear update destination"))
			return
		}
		updates = dest
		tx.Statement.RowsAffected = 1
	})

	if err := NewAdminDeviceRepository(db).ClearPush(context.Background(), uuid.New()); err != nil {
		t.Fatalf("ClearPush: %v", err)
	}
	if updates["apns_token_enc"] != "" || updates["apns_env"] != "" || !isNullExpr(updates["push_synced_at"]) || updates["push_state"] != "" {
		t.Fatalf("clear updates = %v, want token, env, sync and state cleared", updates)
	}
}

func TestAdminDeviceSetPushStateRejectsUnknownState(t *testing.T) {
	t.Parallel()

	db := storeRepositoryOfflineGorm(t)
	storeReplaceUpdateCallback(t, db, func(tx *gorm.DB) {
		tx.AddError(errors.New("unknown push state must not reach the database"))
	})

	if err := NewAdminDeviceRepository(db).SetPushState(context.Background(), uuid.New(), "maybe", time.Now()); err == nil {
		t.Fatal("SetPushState accepted an unknown state")
	}
}

func isNullExpr(value any) bool {
	expr, ok := value.(clause.Expr)
	return ok && strings.EqualFold(strings.TrimSpace(expr.SQL), "NULL")
}

func anyClauseMatches(exprs []clause.Expression, match func(clause.Expression) bool) bool {
	for _, expr := range exprs {
		if match(expr) {
			return true
		}
	}
	return false
}
