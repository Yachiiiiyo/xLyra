package auth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	authcrypto "xlyra/server/internal/crypto"
	"xlyra/server/internal/store"
)

func authDeviceWithQueryCallback(t *testing.T, callback func(*gorm.DB)) *Service {
	t.Helper()

	service := NewService(authPostgresGorm(t), "test-master-key")
	authReplaceQueryCallback(t, service.db, callback)
	return service
}

// authDeviceTransactionService 用只支持事务的假连接搭服务: 兑换配对码要开事务,
// 具体查询/写入由回调模拟, 不依赖真实数据库。
func authDeviceTransactionService(t *testing.T) *Service {
	t.Helper()

	return NewService(authTransactionOnlyGorm(t), "test-master-key")
}

func authDeviceWithTokenHash(token string, createdAt time.Time, lastSeen time.Time) store.AdminDevice {
	device := store.AdminDevice{
		ID:        uuid.New(),
		AdminID:   uuid.New(),
		Name:      "iphone",
		Platform:  devicePlatformIOS,
		TokenHash: hashToken(token),
		CreatedAt: createdAt,
	}
	if !lastSeen.IsZero() {
		device.LastSeenAt = sql.NullTime{Time: lastSeen, Valid: true}
	}
	return device
}

func authDeviceQueryCallback(t *testing.T, device store.AdminDevice) func(*gorm.DB) {
	t.Helper()

	return func(tx *gorm.DB) {
		items, ok := tx.Statement.Dest.(*[]store.AdminDevice)
		if !ok {
			tx.AddError(errors.New("unexpected admin device query destination"))
			return
		}
		*items = []store.AdminDevice{device}
		tx.Statement.RowsAffected = 1
	}
}

func TestCreateDevicePairingStoresOnlyHash(t *testing.T) {
	t.Parallel()

	adminID := uuid.New()
	var created store.AdminDevicePairing
	service := NewService(authPostgresGorm(t), "test-master-key")
	authReplaceCreateCallback(t, service.db, func(tx *gorm.DB) {
		pairing, ok := tx.Statement.Dest.(*store.AdminDevicePairing)
		if !ok {
			tx.AddError(errors.New("unexpected pairing create destination"))
			return
		}
		created = *pairing
		pairing.ID = uuid.New()
		created.ID = pairing.ID
		tx.Statement.RowsAffected = 1
	})

	before := time.Now()
	result, err := service.CreateDevicePairing(context.Background(), adminID)
	if err != nil {
		t.Fatalf("CreateDevicePairing: %v", err)
	}

	if !strings.HasPrefix(result.Code, devicePairingPrefix) {
		t.Fatalf("pairing code = %q, want %q prefix", result.Code, devicePairingPrefix)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(result.Code, devicePairingPrefix))
	if err != nil || len(raw) != devicePairingTokenBytes {
		t.Fatalf("pairing code payload = %d bytes (%v), want %d", len(raw), err, devicePairingTokenBytes)
	}
	if created.CodeHash != hashToken(result.Code) || created.CodeHash == result.Code {
		t.Fatalf("stored code hash = %q, want sha256 of the code", created.CodeHash)
	}
	if created.AdminID != adminID {
		t.Fatalf("stored pairing admin = %s, want %s", created.AdminID, adminID)
	}
	if want := before.Add(devicePairingLifetime); created.ExpiresAt.Before(want.Add(-time.Minute)) || created.ExpiresAt.After(want.Add(time.Minute)) {
		t.Fatalf("pairing expires at %s, want about %s", created.ExpiresAt, want)
	}
	if result.PairingID != created.ID || result.ExpiresAt.IsZero() {
		t.Fatalf("result = %#v, want created pairing identity", result)
	}
}

func TestRedeemDevicePairingCreatesDeviceOnce(t *testing.T) {
	t.Parallel()

	adminID := uuid.New()
	var createdDevice store.AdminDevice
	var markedUsedAt sql.NullTime
	service := authDeviceTransactionService(t)
	authReplaceQueryCallback(t, service.db, func(tx *gorm.DB) {
		pairing, ok := tx.Statement.Dest.(*store.AdminDevicePairing)
		if !ok {
			tx.AddError(errors.New("unexpected pairing query destination"))
			return
		}
		*pairing = store.AdminDevicePairing{ID: uuid.New(), AdminID: adminID, ExpiresAt: time.Now().Add(time.Minute)}
		tx.Statement.RowsAffected = 1
	})
	authReplaceUpdateCallback(t, service.db, func(tx *gorm.DB) {
		markedUsedAt = sql.NullTime{Time: time.Now(), Valid: true}
		tx.Statement.RowsAffected = 1
	})
	authReplaceCreateCallback(t, service.db, func(tx *gorm.DB) {
		device, ok := tx.Statement.Dest.(*store.AdminDevice)
		if !ok {
			tx.AddError(errors.New("unexpected device create destination"))
			return
		}
		createdDevice = *device
		device.ID = uuid.New()
		tx.Statement.RowsAffected = 1
	})

	result, err := service.RedeemDevicePairing(context.Background(), "xlp_pairing-code", " iPhone 15 ", "IOS", "xlyra-ios/1.0", "203.0.113.7:8123")
	if err != nil {
		t.Fatalf("RedeemDevicePairing: %v", err)
	}

	if !strings.HasPrefix(result.Token, deviceTokenPrefix) {
		t.Fatalf("device token = %q, want %q prefix", result.Token, deviceTokenPrefix)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(result.Token, deviceTokenPrefix))
	if err != nil || len(raw) != deviceTokenBytes {
		t.Fatalf("device token payload = %d bytes (%v), want %d", len(raw), err, deviceTokenBytes)
	}
	if createdDevice.TokenHash != hashToken(result.Token) || createdDevice.TokenHash == result.Token {
		t.Fatalf("stored token hash = %q, want sha256 of the token", createdDevice.TokenHash)
	}
	if createdDevice.AdminID != adminID || createdDevice.Name != "iPhone 15" || createdDevice.Platform != devicePlatformIOS {
		t.Fatalf("created device = %#v, want admin, trimmed name and normalized platform", createdDevice)
	}
	if !markedUsedAt.Valid {
		t.Fatal("pairing code was not marked as used")
	}
}

func TestRedeemDevicePairingRejectsUnusableCode(t *testing.T) {
	t.Parallel()

	service := authDeviceTransactionService(t)
	authReplaceQueryCallback(t, service.db, func(tx *gorm.DB) {
		tx.AddError(gorm.ErrRecordNotFound)
	})
	authReplaceCreateCallback(t, service.db, func(tx *gorm.DB) {
		tx.AddError(errors.New("unusable pairing code must not create a device"))
	})

	_, err := service.RedeemDevicePairing(context.Background(), "xlp_unknown", "iphone", devicePlatformIOS, "agent", "203.0.113.7")
	assertAuthErrorIs(t, "RedeemDevicePairing unknown code", err, ErrInvalidPairingCode)

	// 并发兑换里输了的那一次: 查询还能看到配对码, 但 used_at 已经是非 NULL。
	authReplaceQueryCallback(t, service.db, func(tx *gorm.DB) {
		pairing, ok := tx.Statement.Dest.(*store.AdminDevicePairing)
		if !ok {
			tx.AddError(errors.New("unexpected pairing query destination"))
			return
		}
		*pairing = store.AdminDevicePairing{ID: uuid.New(), AdminID: uuid.New(), ExpiresAt: time.Now().Add(time.Minute)}
		tx.Statement.RowsAffected = 1
	})
	authReplaceUpdateCallback(t, service.db, func(tx *gorm.DB) {
		tx.Statement.RowsAffected = 0
	})
	_, err = service.RedeemDevicePairing(context.Background(), "xlp_already-used", "iphone", devicePlatformIOS, "agent", "203.0.113.7")
	assertAuthErrorIs(t, "RedeemDevicePairing already used code", err, ErrInvalidPairingCode)
}

func TestRedeemDevicePairingValidatesInput(t *testing.T) {
	t.Parallel()

	service := NewService(nil, "test-master-key")
	ctx := context.Background()

	_, err := service.RedeemDevicePairing(ctx, "xlp_code", "   ", devicePlatformIOS, "agent", "203.0.113.7")
	assertAuthErrorIs(t, "RedeemDevicePairing blank name", err, ErrDeviceNameRequired)

	_, err = service.RedeemDevicePairing(ctx, "xlp_code", strings.Repeat("长", 65), devicePlatformIOS, "agent", "203.0.113.7")
	assertAuthErrorIs(t, "RedeemDevicePairing long name", err, ErrDeviceNameTooLong)

	_, err = service.RedeemDevicePairing(ctx, "xlp_code", "iphone", "android", "agent", "203.0.113.7")
	assertAuthErrorIs(t, "RedeemDevicePairing platform", err, ErrDevicePlatform)
}

func TestRedeemDevicePairingRateLimitsFailuresPerIP(t *testing.T) {
	t.Parallel()

	service := authDeviceTransactionService(t)
	authReplaceQueryCallback(t, service.db, func(tx *gorm.DB) {
		tx.AddError(gorm.ErrRecordNotFound)
	})

	var lockErr *LockoutError
	for attempt := 0; attempt < loginMaxFailures+1; attempt++ {
		_, err := service.RedeemDevicePairing(context.Background(), "xlp_wrong", "iphone", devicePlatformIOS, "agent", "203.0.113.9:5555")
		if errors.As(err, &lockErr) {
			break
		}
		assertAuthErrorIs(t, "RedeemDevicePairing wrong code", err, ErrInvalidPairingCode)
	}
	if lockErr == nil {
		t.Fatalf("no lockout after %d failed pairings", loginMaxFailures+1)
	}
	if lockErr.RetryAfter <= 0 {
		t.Fatalf("lockout retry after = %s, want positive", lockErr.RetryAfter)
	}

	// 换一个 IP 仍然能正常尝试, 说明限速是按 IP 聚合的。
	_, err := service.RedeemDevicePairing(context.Background(), "xlp_wrong", "iphone", devicePlatformIOS, "agent", "198.51.100.4:5555")
	assertAuthErrorIs(t, "RedeemDevicePairing other ip", err, ErrInvalidPairingCode)
}

func TestRedeemDevicePairingConcurrencyOnlyOneWins(t *testing.T) {
	t.Parallel()

	adminID := uuid.New()
	var mu sync.Mutex
	used := false
	createdCount := 0
	service := authDeviceTransactionService(t)
	// 并发兑换只验证事务原子性, 这里关掉配对限速（nil 守卫 = 不限速）。
	service.pairingGuard = nil
	authReplaceQueryCallback(t, service.db, func(tx *gorm.DB) {
		pairing, ok := tx.Statement.Dest.(*store.AdminDevicePairing)
		if !ok {
			tx.AddError(errors.New("unexpected pairing query destination"))
			return
		}
		mu.Lock()
		alreadyUsed := used
		mu.Unlock()
		if alreadyUsed {
			tx.AddError(gorm.ErrRecordNotFound)
			return
		}
		*pairing = store.AdminDevicePairing{ID: uuid.New(), AdminID: adminID, ExpiresAt: time.Now().Add(time.Minute)}
		tx.Statement.RowsAffected = 1
	})
	authReplaceUpdateCallback(t, service.db, func(tx *gorm.DB) {
		mu.Lock()
		defer mu.Unlock()
		if used {
			tx.Statement.RowsAffected = 0
			return
		}
		used = true
		tx.Statement.RowsAffected = 1
	})
	authReplaceCreateCallback(t, service.db, func(tx *gorm.DB) {
		mu.Lock()
		createdCount++
		mu.Unlock()
		tx.Statement.RowsAffected = 1
	})

	const workers = 8
	var wg sync.WaitGroup
	var muResults sync.Mutex
	successes := 0
	failures := 0
	unexpected := make([]string, 0)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.RedeemDevicePairing(context.Background(), "xlp_race", "iphone", devicePlatformIOS, "agent", "203.0.113.11")
			muResults.Lock()
			defer muResults.Unlock()
			if err == nil {
				successes++
				return
			}
			if errors.Is(err, ErrInvalidPairingCode) {
				failures++
				return
			}
			unexpected = append(unexpected, err.Error())
		}()
	}
	wg.Wait()

	if successes != 1 || failures != workers-1 || createdCount != 1 {
		t.Fatalf("successes/failures/devices = %d/%d/%d, want 1/%d/1 (unexpected: %v)", successes, failures, createdCount, workers-1, unexpected)
	}
}

func TestValidateDeviceTokenAcceptsActiveDeviceAndTouchesThrottled(t *testing.T) {
	t.Parallel()

	token := "xld_active-device-token"
	device := authDeviceWithTokenHash(token, time.Now().Add(-time.Hour), time.Time{})
	touchCount := 0
	service := authDeviceWithQueryCallback(t, authDeviceQueryCallback(t, device))
	authReplaceUpdateCallback(t, service.db, func(tx *gorm.DB) {
		touchCount++
		tx.Statement.RowsAffected = 1
	})

	got, err := service.ValidateDeviceToken(context.Background(), token, "xlyra-ios/1.0", "203.0.113.7:8123")
	if err != nil {
		t.Fatalf("ValidateDeviceToken: %v", err)
	}
	if got.ID != device.ID || got.AdminID != device.AdminID {
		t.Fatalf("device = %#v, want %#v", got, device)
	}
	if touchCount != 1 {
		t.Fatalf("last-seen touch count = %d, want 1", touchCount)
	}
}

func TestValidateDeviceTokenRejectsRevokedDevice(t *testing.T) {
	t.Parallel()

	token := "xld_revoked-device-token"
	device := authDeviceWithTokenHash(token, time.Now(), time.Time{})
	device.RevokedAt = sql.NullTime{Time: time.Now(), Valid: true}
	service := authDeviceWithQueryCallback(t, authDeviceQueryCallback(t, device))

	_, err := service.ValidateDeviceToken(context.Background(), token, "agent", "203.0.113.7")
	assertAuthErrorIs(t, "ValidateDeviceToken revoked", err, ErrDeviceTokenInvalid)
}

func TestValidateDeviceTokenRejectsIdleDevice(t *testing.T) {
	t.Parallel()

	token := "xld_idle-device-token"
	device := authDeviceWithTokenHash(token, time.Now().AddDate(0, 0, -200), time.Time{})
	service := authDeviceWithQueryCallback(t, authDeviceQueryCallback(t, device))
	authReplaceUpdateCallback(t, service.db, func(tx *gorm.DB) {
		tx.Statement.RowsAffected = 1
	})

	_, err := service.ValidateDeviceToken(context.Background(), token, "agent", "203.0.113.7")
	assertAuthErrorIs(t, "ValidateDeviceToken idle", err, ErrDeviceTokenInvalid)

	// DEVICE_IDLE_DAYS=0 表示不因闲置失效。
	service.SetDeviceIdleDays(0)
	if _, err := service.ValidateDeviceToken(context.Background(), token, "agent", "203.0.113.7"); err != nil {
		t.Fatalf("ValidateDeviceToken with idle disabled: %v", err)
	}

	// 闲置判定以 last_seen_at 优先: 刚用过就不算超期。
	service.SetDeviceIdleDays(defaultDeviceIdleDays)
	fresh := authDeviceWithTokenHash(token, time.Now().AddDate(0, 0, -200), time.Now().Add(-time.Hour))
	service = authDeviceWithQueryCallback(t, authDeviceQueryCallback(t, fresh))
	authReplaceUpdateCallback(t, service.db, func(tx *gorm.DB) {
		tx.Statement.RowsAffected = 1
	})
	if _, err := service.ValidateDeviceToken(context.Background(), token, "agent", "203.0.113.7"); err != nil {
		t.Fatalf("ValidateDeviceToken with recent last_seen_at: %v", err)
	}
}

func TestDeviceIdleLifetimeDefaultsAndZero(t *testing.T) {
	t.Parallel()

	var service *Service
	if lifetime, unlimited := service.configuredDeviceIdleLifetime(); unlimited || lifetime != defaultDeviceIdleDays*24*time.Hour {
		t.Fatalf("default idle lifetime = %s/%v, want %d days", lifetime, unlimited, defaultDeviceIdleDays)
	}

	service = &Service{}
	service.SetDeviceIdleDays(30)
	if lifetime, unlimited := service.configuredDeviceIdleLifetime(); unlimited || lifetime != 30*24*time.Hour {
		t.Fatalf("configured idle lifetime = %s/%v, want 30 days", lifetime, unlimited)
	}

	service.SetDeviceIdleDays(0)
	if _, unlimited := service.configuredDeviceIdleLifetime(); !unlimited {
		t.Fatal("zero idle days should disable idle expiry")
	}

	service.SetDeviceIdleDays(-1)
	if lifetime, unlimited := service.configuredDeviceIdleLifetime(); unlimited || lifetime != defaultDeviceIdleDays*24*time.Hour {
		t.Fatalf("negative idle days fallback = %s/%v, want default", lifetime, unlimited)
	}
}

func TestRequireAdminSessionAcceptsDeviceTokenWithoutAccessAudit(t *testing.T) {
	t.Parallel()

	token := "xld_middleware-device-token"
	device := authDeviceWithTokenHash(token, time.Now(), time.Time{})
	service := authDeviceWithQueryCallback(t, authDeviceQueryCallback(t, device))
	authReplaceUpdateCallback(t, service.db, func(tx *gorm.DB) {
		tx.Statement.RowsAffected = 1
	})
	authReplaceCreateCallback(t, service.db, func(tx *gorm.DB) {
		tx.AddError(errors.New("device requests must not write per-request audit"))
	})

	req := authTestRequest(http.MethodGet, "/api/v1/profile")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := authRecorder()
	called := false
	service.RequireAdminSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		actor, ok := AdminActorFromContext(r.Context())
		if !ok || actor.Type != DeviceActorType || actor.DeviceID != device.ID || actor.AdminID != device.AdminID {
			t.Fatalf("admin actor = %#v/%v, want device actor", actor, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, req)

	if !called || rec.Code != http.StatusNoContent {
		t.Fatalf("handler called/code = %v/%d, want device pass-through", called, rec.Code)
	}
}

func TestRequireAdminSessionRejectsBadDeviceTokens(t *testing.T) {
	t.Parallel()

	idle := authDeviceWithTokenHash("xld_idle-token", time.Now().AddDate(0, 0, -400), time.Time{})
	revoked := authDeviceWithTokenHash("xld_revoked-token", time.Now(), time.Time{})
	revoked.RevokedAt = sql.NullTime{Time: time.Now(), Valid: true}

	for _, tc := range []struct {
		name   string
		token  string
		device store.AdminDevice
	}{
		{name: "idle", token: "xld_idle-token", device: idle},
		{name: "revoked", token: "xld_revoked-token", device: revoked},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			device := tc.device
			service := authDeviceWithQueryCallback(t, authDeviceQueryCallback(t, device))
			req := authTestRequest(http.MethodGet, "/api/v1/profile")
			req.Header.Set("Authorization", "Bearer "+tc.token)
			rec := authPerform(service.RequireAdminSession(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("handler must not run for an unusable device token")
			})), req)

			assertAuthJSONError(t, rec, http.StatusUnauthorized, "unauthorized", "admin session is required")
		})
	}
}

func TestDenyDeviceActorBlocksOnlyDeviceActor(t *testing.T) {
	t.Parallel()

	handler := DenyDeviceActor(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	deviceReq := authTestRequest(http.MethodPost, "/api/v1/devices/pairings")
	deviceReq = deviceReq.WithContext(WithAdminActor(deviceReq.Context(), AdminActor{Type: DeviceActorType, AdminID: uuid.New(), DeviceID: uuid.New()}))
	rec := authPerform(handler, deviceReq)
	assertAuthJSONError(t, rec, http.StatusForbidden, "forbidden_for_device", "device tokens are not allowed to access this resource")

	sessionReq := authTestRequest(http.MethodPost, "/api/v1/devices/pairings")
	sessionReq = sessionReq.WithContext(WithAdminActor(sessionReq.Context(), AdminActor{Type: "session", AdminID: uuid.New(), SessionID: uuid.New()}))
	rec = authPerform(handler, sessionReq)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("session actor status = %d, want pass-through", rec.Code)
	}
}

func TestDeviceTokenFromRequestOnlyReadsBearerDeviceTokens(t *testing.T) {
	t.Parallel()

	req := authTestRequest(http.MethodGet, "/api/v1/profile")
	if got := DeviceTokenFromRequest(req); got != "" {
		t.Fatalf("device token without header = %q, want empty", got)
	}
	req.Header.Set("Authorization", "Bearer xlyra-admin-abcdef")
	if got := DeviceTokenFromRequest(req); got != "" {
		t.Fatalf("non-device bearer = %q, want empty", got)
	}
	req.Header.Set("Authorization", "Bearer xld_device-token")
	if got := DeviceTokenFromRequest(req); got != "xld_device-token" {
		t.Fatalf("device bearer = %q, want trimmed device token", got)
	}
	req.AddCookie(&http.Cookie{Name: "xlyra_admin_session", Value: "xld_cookie-should-not-be-a-device-token"})
	if got := DeviceTokenFromRequest(req); got != "xld_device-token" {
		t.Fatalf("device token = %q, want header only", got)
	}
}

func TestChangeAdminPasswordRevokesDevices(t *testing.T) {
	t.Parallel()

	adminID := uuid.New()
	passwordHash, err := authcrypto.HashPassword("current-password-123")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	service := NewService(authPostgresGorm(t), "test-master-key")
	authReplaceQueryCallback(t, service.db, func(tx *gorm.DB) {
		switch dest := tx.Statement.Dest.(type) {
		case *store.Admin:
			*dest = store.Admin{ID: adminID, Username: "root", PasswordHash: passwordHash, Status: "active"}
		case *[]store.AdminSession:
			*dest = nil
		default:
			tx.AddError(errors.New("unexpected password change query destination"))
			return
		}
		tx.Statement.RowsAffected = 1
	})
	updatedTables := make([]string, 0, 2)
	authReplaceUpdateCallback(t, service.db, func(tx *gorm.DB) {
		updatedTables = append(updatedTables, tx.Statement.Table)
		tx.Statement.RowsAffected = 1
	})
	authReplaceDeleteCallback(t, service.db, func(tx *gorm.DB) {
		tx.Statement.RowsAffected = 1
	})

	if _, err := service.ChangeAdminPassword(context.Background(), adminID, "current-password-123", "new-password-456", uuid.New()); err != nil {
		t.Fatalf("ChangeAdminPassword: %v", err)
	}

	found := false
	for _, table := range updatedTables {
		if table == "admin_devices" {
			found = true
		}
	}
	if !found {
		t.Fatalf("updated tables = %v, want admin_devices revoked after password change", updatedTables)
	}
}
