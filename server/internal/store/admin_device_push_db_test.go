package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAdminDevicePushRoundTripOnPostgres(t *testing.T) {
	st := openCloudBindingStore(t)
	ctx := context.Background()

	admin, err := NewAdminRepository(st.DB()).Create(ctx, CreateAdminParams{
		Username:     "push-owner-" + uuid.NewString()[:8],
		PasswordHash: "hash",
		Role:         "owner",
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}

	devices := NewAdminDeviceRepository(st.DB())
	pairings := NewAdminDevicePairingRepository(st.DB())
	pairing, err := pairings.Create(ctx, CreateAdminDevicePairingParams{
		AdminID:   admin.ID,
		CodeHash:  "pairing-" + uuid.NewString(),
		ExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("create pairing: %v", err)
	}
	device, err := devices.RedeemPairing(ctx, RedeemAdminDevicePairingParams{
		CodeHash:  pairing.CodeHash,
		Name:      "iphone",
		Platform:  "ios",
		TokenHash: "token-" + uuid.NewString(),
		Now:       time.Now(),
	})
	if err != nil {
		t.Fatalf("redeem pairing: %v", err)
	}
	if device.APNSTokenEnc != "" || device.PushPrefs != nil && string(device.PushPrefs) != "{}" && string(device.PushPrefs) != "" {
		t.Fatalf("fresh device push fields = %#v, want empty", device)
	}

	registeredAt := time.Now().UTC().Truncate(time.Microsecond)
	if err := devices.RegisterPush(ctx, device.ID, "ciphertext", "sandbox", registeredAt); err != nil {
		t.Fatalf("RegisterPush: %v", err)
	}
	stored, err := devices.GetByID(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.APNSTokenEnc != "ciphertext" || stored.APNSEnv != "sandbox" || stored.PushSyncedAt.Valid || stored.PushState != "" {
		t.Fatalf("registered device = %#v, want ciphertext, sandbox, unsynced and empty state", stored)
	}
	if !stored.PushRegisteredAt.Valid || stored.PushRegisteredAt.Time.Sub(registeredAt).Abs() > time.Second {
		t.Fatalf("push_registered_at = %v, want %v", stored.PushRegisteredAt, registeredAt)
	}

	prefs := JSON([]byte(`{"approval":false,"question":true}`))
	if err := devices.SetPushPrefs(ctx, device.ID, prefs); err != nil {
		t.Fatalf("SetPushPrefs: %v", err)
	}
	syncedAt := time.Now().UTC().Truncate(time.Microsecond)
	if err := devices.MarkPushSynced(ctx, device.ID, syncedAt); err != nil {
		t.Fatalf("MarkPushSynced: %v", err)
	}
	stateAt := syncedAt.Add(time.Second)
	if err := devices.SetPushState(ctx, device.ID, "ok", stateAt); err != nil {
		t.Fatalf("SetPushState: %v", err)
	}
	stored, err = devices.GetByID(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetByID after updates: %v", err)
	}
	if string(stored.PushPrefs) == "" || !stored.PushSyncedAt.Valid || stored.PushState != "ok" || !stored.PushStateAt.Valid {
		t.Fatalf("updated device = %#v, want prefs, synced_at and state", stored)
	}

	active, err := devices.ListActiveWithPushToken(ctx)
	if err != nil {
		t.Fatalf("ListActiveWithPushToken: %v", err)
	}
	if !pushDeviceListed(active, device.ID) {
		t.Fatalf("active devices with token = %#v, want %s", active, device.ID)
	}

	if err := devices.MarkAllPushUnsynced(ctx, admin.ID); err != nil {
		t.Fatalf("MarkAllPushUnsynced: %v", err)
	}
	stored, err = devices.GetByID(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetByID after unsync: %v", err)
	}
	if stored.PushSyncedAt.Valid || stored.PushState != "" || stored.APNSTokenEnc != "ciphertext" {
		t.Fatalf("unsynced device = %#v, want token kept, sync and state cleared", stored)
	}

	if err := devices.ClearPush(ctx, device.ID); err != nil {
		t.Fatalf("ClearPush: %v", err)
	}
	stored, err = devices.GetByID(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetByID after clear: %v", err)
	}
	if stored.APNSTokenEnc != "" || stored.APNSEnv != "" || stored.PushState != "" {
		t.Fatalf("cleared device = %#v, want no push registration", stored)
	}
	active, err = devices.ListActiveWithPushToken(ctx)
	if err != nil {
		t.Fatalf("ListActiveWithPushToken after clear: %v", err)
	}
	if pushDeviceListed(active, device.ID) {
		t.Fatal("cleared device is still listed as having a push token")
	}

	if err := devices.RegisterPush(ctx, device.ID, "ciphertext-2", "production", time.Now()); err != nil {
		t.Fatalf("RegisterPush again: %v", err)
	}
	if err := devices.Revoke(ctx, admin.ID, device.ID, time.Now()); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	stored, err = devices.GetByID(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetByID after revoke: %v", err)
	}
	if !stored.RevokedAt.Valid || stored.APNSTokenEnc != "" || stored.APNSEnv != "" {
		t.Fatalf("revoked device = %#v, want revoked with push registration cleared", stored)
	}
	active, err = devices.ListActiveWithPushToken(ctx)
	if err != nil {
		t.Fatalf("ListActiveWithPushToken after revoke: %v", err)
	}
	if pushDeviceListed(active, device.ID) {
		t.Fatal("revoked device is still listed as having a push token")
	}

	// 约束: 非法环境与非法推送结果不能入库。
	if err := st.DB().WithContext(ctx).Model(&AdminDevice{}).Where(&AdminDevice{ID: device.ID}).Update("apns_env", "staging").Error; err == nil {
		t.Fatal("apns_env check constraint accepted an unknown environment")
	}
	if err := st.DB().WithContext(ctx).Model(&AdminDevice{}).Where(&AdminDevice{ID: device.ID}).Update("push_state", "maybe").Error; err == nil {
		t.Fatal("push_state check constraint accepted an unknown state")
	}

}

func pushDeviceListed(devices []AdminDevice, id uuid.UUID) bool {
	for _, device := range devices {
		if device.ID == id {
			return true
		}
	}
	return false
}
