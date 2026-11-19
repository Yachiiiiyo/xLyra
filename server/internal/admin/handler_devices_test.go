package admin

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"xlyra/server/internal/auth"
	"xlyra/server/internal/store"
)

func adminDeviceContext(req *http.Request, actor auth.AdminActor) *http.Request {
	return req.WithContext(auth.WithAdminActor(req.Context(), actor))
}

func adminDeviceCaptureAudits(t *testing.T, db *gorm.DB, existing func(*gorm.DB), audits *[]store.AdminAuditLog) {
	t.Helper()

	if err := db.Callback().Create().Replace("gorm:create", func(tx *gorm.DB) {
		if audit, ok := tx.Statement.Dest.(*store.AdminAuditLog); ok {
			*audits = append(*audits, *audit)
			tx.Statement.RowsAffected = 1
			return
		}
		if existing != nil {
			existing(tx)
			return
		}
		tx.Statement.RowsAffected = 1
	}); err != nil {
		t.Fatalf("replace create callback: %v", err)
	}
}

func TestCreateDevicePairingHandlerReturnsCodeAndAuditsWithoutSecrets(t *testing.T) {
	t.Parallel()

	adminID := uuid.New()
	var created store.AdminDevicePairing
	audits := make([]store.AdminAuditLog, 0, 1)
	db := adminGormWithCallbacks(t, adminGormCallbacks{
		create: func(tx *gorm.DB) {
			if pairing, ok := tx.Statement.Dest.(*store.AdminDevicePairing); ok {
				pairing.ID = uuid.New()
				created = *pairing
				tx.Statement.RowsAffected = 1
				return
			}
			if audit, ok := tx.Statement.Dest.(*store.AdminAuditLog); ok {
				audits = append(audits, *audit)
				tx.Statement.RowsAffected = 1
				return
			}
			tx.AddError(errors.New("unexpected create destination"))
		},
	})
	handler := Handler{auth: auth.NewService(db, adminTestMasterKey)}
	req := adminDeviceContext(adminTestRequest(http.MethodPost, "/api/v1/devices/pairings", ""), auth.AdminActor{Type: "session", AdminID: adminID, SessionID: uuid.New()})
	rec := adminPerform(handler.CreateDevicePairing, req)

	adminAssertStatus(t, rec, http.StatusCreated)
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	body := adminDecodeJSON[struct {
		Data struct {
			PairingID string `json:"pairing_id"`
			Code      string `json:"code"`
			ExpiresAt string `json:"expires_at"`
		} `json:"data"`
	}](t, rec)
	if body.Data.PairingID != created.ID.String() || !strings.HasPrefix(body.Data.Code, "xlp_") || body.Data.ExpiresAt == "" {
		t.Fatalf("payload = %#v, want pairing id, code and expiry", body.Data)
	}
	if len(audits) != 1 || audits[0].Action != "device.pairing.create" || audits[0].ActorType != "session" {
		t.Fatalf("audits = %#v, want one device.pairing.create record", audits)
	}
	if strings.Contains(string(audits[0].Metadata), body.Data.Code) {
		t.Fatal("audit metadata must not contain the pairing code")
	}
}

func TestRedeemDevicePairingHandlerPairsDevice(t *testing.T) {
	t.Parallel()

	adminID := uuid.New()
	audits := make([]store.AdminAuditLog, 0, 1)
	db := adminTransactionGormWithCallbacks(t, adminGormCallbacks{
		query: func(tx *gorm.DB) {
			switch dest := tx.Statement.Dest.(type) {
			case *store.AdminDevicePairing:
				*dest = store.AdminDevicePairing{ID: uuid.New(), AdminID: adminID, ExpiresAt: time.Now().Add(time.Minute)}
			case *store.Admin:
				*dest = store.Admin{ID: adminID, Username: "owner", Role: "owner", Status: "active", CreatedAt: time.Now()}
			default:
				tx.AddError(errors.New("unexpected query destination"))
				return
			}
			tx.Statement.RowsAffected = 1
		},
		update: func(tx *gorm.DB) {
			tx.Statement.RowsAffected = 1
		},
		create: func(tx *gorm.DB) {
			if device, ok := tx.Statement.Dest.(*store.AdminDevice); ok {
				device.ID = uuid.New()
				tx.Statement.RowsAffected = 1
				return
			}
			if audit, ok := tx.Statement.Dest.(*store.AdminAuditLog); ok {
				audits = append(audits, *audit)
				tx.Statement.RowsAffected = 1
				return
			}
			tx.AddError(errors.New("unexpected create destination"))
		},
	})
	handler := Handler{auth: auth.NewService(db, adminTestMasterKey)}
	req := adminJSONRequest(t, http.MethodPost, "/api/v1/devices/pair", map[string]any{
		"code":        "xlp_pair-code",
		"device_name": "iPhone 15",
		"platform":    "ios",
	})
	rec := adminPerform(handler.RedeemDevicePairing, req)

	adminAssertStatus(t, rec, http.StatusCreated)
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	body := adminDecodeJSON[struct {
		Data struct {
			DeviceID string `json:"device_id"`
			Token    string `json:"token"`
			Admin    struct {
				ID       string `json:"id"`
				Username string `json:"username"`
			} `json:"admin"`
		} `json:"data"`
	}](t, rec)
	if !strings.HasPrefix(body.Data.Token, "xld_") || body.Data.DeviceID == "" || body.Data.Admin.ID != adminID.String() {
		t.Fatalf("payload = %#v, want device id, token and admin", body.Data)
	}
	if len(audits) != 1 || audits[0].Action != "device.pair" || !audits[0].Success || audits[0].ActorType != "device" {
		t.Fatalf("audits = %#v, want one successful device.pair record", audits)
	}
	metadata := string(audits[0].Metadata)
	if strings.Contains(metadata, body.Data.Token) || strings.Contains(metadata, "xlp_pair-code") {
		t.Fatal("audit metadata must not contain the device token or pairing code")
	}
}

func TestRedeemDevicePairingHandlerRateLimitsFailures(t *testing.T) {
	t.Parallel()

	audits := make([]store.AdminAuditLog, 0, 8)
	db := adminTransactionGormWithCallbacks(t, adminGormCallbacks{
		query: func(tx *gorm.DB) {
			tx.AddError(gorm.ErrRecordNotFound)
		},
		create: func(tx *gorm.DB) {
			if audit, ok := tx.Statement.Dest.(*store.AdminAuditLog); ok {
				audits = append(audits, *audit)
				tx.Statement.RowsAffected = 1
				return
			}
			tx.AddError(errors.New("invalid pairing code must not create a device"))
		},
	})
	handler := Handler{auth: auth.NewService(db, adminTestMasterKey)}

	locked := false
	for attempt := 0; attempt < 8 && !locked; attempt++ {
		req := adminJSONRequest(t, http.MethodPost, "/api/v1/devices/pair", map[string]any{
			"code":        "xlp_wrong-code",
			"device_name": "iPhone 15",
			"platform":    "ios",
		})
		rec := adminPerform(handler.RedeemDevicePairing, req)
		switch rec.Code {
		case http.StatusBadRequest:
			assertAdminErrorCode(t, rec, http.StatusBadRequest, "invalid_pairing_code")
		case http.StatusTooManyRequests:
			assertAdminErrorCode(t, rec, http.StatusTooManyRequests, "too_many_attempts")
			if rec.Header().Get("Retry-After") == "" {
				t.Fatal("429 response must carry Retry-After")
			}
			locked = true
		default:
			t.Fatalf("unexpected status %d; body=%s", rec.Code, rec.Body.String())
		}
	}
	if !locked {
		t.Fatal("pairing failures were never rate limited")
	}
	if len(audits) == 0 {
		t.Fatal("pairing failures must be audited")
	}
	for _, audit := range audits {
		if audit.Action != "device.pair" || audit.Success {
			t.Fatalf("audit = %#v, want a failed device.pair record", audit)
		}
		if strings.Contains(string(audit.Metadata), "xlp_wrong-code") {
			t.Fatal("failure audit must not contain the pairing code")
		}
	}
}

func TestRedeemDevicePairingHandlerValidatesInput(t *testing.T) {
	t.Parallel()

	handler := Handler{auth: auth.NewService(adminGormWithCallbacks(t, adminGormCallbacks{
		query: func(tx *gorm.DB) {
			tx.AddError(errors.New("input validation must run before any database access"))
		},
	}), adminTestMasterKey)}

	req := adminJSONRequest(t, http.MethodPost, "/api/v1/devices/pair", map[string]any{
		"code":        "xlp_some-code",
		"device_name": "   ",
		"platform":    "ios",
	})
	rec := adminPerform(handler.RedeemDevicePairing, req)
	assertAdminErrorCode(t, rec, http.StatusBadRequest, "invalid_device_name")

	req = adminJSONRequest(t, http.MethodPost, "/api/v1/devices/pair", map[string]any{
		"code":        "xlp_some-code",
		"device_name": "iPhone 15",
		"platform":    "android",
	})
	rec = adminPerform(handler.RedeemDevicePairing, req)
	assertAdminErrorCode(t, rec, http.StatusBadRequest, "invalid_platform")
}

func TestListDevicesHandlerReturnsDevices(t *testing.T) {
	t.Parallel()

	adminID := uuid.New()
	deviceID := uuid.New()
	lastSeen := time.Now().Add(-time.Minute)
	db := adminGormWithCallbacks(t, adminGormCallbacks{query: func(tx *gorm.DB) {
		devices, ok := tx.Statement.Dest.(*[]store.AdminDevice)
		if !ok {
			tx.AddError(errors.New("unexpected device query destination"))
			return
		}
		*devices = []store.AdminDevice{{
			ID:         deviceID,
			AdminID:    adminID,
			Name:       "iPhone 15",
			Platform:   "ios",
			LastSeenAt: sql.NullTime{Time: lastSeen, Valid: true},
			LastIP:     store.IPAddress{203, 0, 113, 7},
			CreatedAt:  time.Now(),
		}}
		tx.Statement.RowsAffected = 1
	}})
	handler := Handler{auth: auth.NewService(db, adminTestMasterKey)}
	req := adminDeviceContext(adminTestRequest(http.MethodGet, "/api/v1/devices", ""), auth.AdminActor{Type: "session", AdminID: adminID})
	rec := adminPerform(handler.ListDevices, req)

	adminAssertStatus(t, rec, http.StatusOK)
	body := adminDecodeJSON[struct {
		Data []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Platform   string `json:"platform"`
			LastSeenAt string `json:"last_seen_at"`
			LastIP     string `json:"last_ip"`
			Current    bool   `json:"current"`
		} `json:"data"`
	}](t, rec)
	if len(body.Data) != 1 {
		t.Fatalf("devices = %#v, want one device", body.Data)
	}
	item := body.Data[0]
	if item.ID != deviceID.String() || item.Name != "iPhone 15" || item.Platform != "ios" || item.Current || item.LastSeenAt == "" || item.LastIP != "203.0.113.7" {
		t.Fatalf("device payload = %#v, want device details with current=false", item)
	}
}

func TestRevokeDeviceHandlerScopesToAdmin(t *testing.T) {
	t.Parallel()

	adminID := uuid.New()
	deviceID := uuid.New()
	audits := make([]store.AdminAuditLog, 0, 1)
	db := adminGormWithCallbacks(t, adminGormCallbacks{
		update: func(tx *gorm.DB) {
			tx.Statement.RowsAffected = 1
		},
		create: func(tx *gorm.DB) {
			audit, ok := tx.Statement.Dest.(*store.AdminAuditLog)
			if !ok {
				tx.AddError(errors.New("unexpected create destination"))
				return
			}
			audits = append(audits, *audit)
			tx.Statement.RowsAffected = 1
		},
	})
	handler := Handler{auth: auth.NewService(db, adminTestMasterKey)}
	req := adminDeviceContext(adminRequestWithRouteParam(http.MethodDelete, "/api/v1/devices/"+deviceID.String(), "", "id", deviceID.String()), auth.AdminActor{Type: "session", AdminID: adminID})
	rec := adminPerform(handler.RevokeDevice, req)

	adminAssertStatus(t, rec, http.StatusNoContent)
	if len(audits) != 1 || audits[0].Action != "device.revoke" || audits[0].ResourceID != deviceID.String() {
		t.Fatalf("audits = %#v, want one device.revoke record", audits)
	}

	// 他人设备/不存在的设备: store 返回 0 行受影响, handler 回 404。
	db = adminGormWithCallbacks(t, adminGormCallbacks{update: func(tx *gorm.DB) {
		tx.Statement.RowsAffected = 0
	}})
	handler = Handler{auth: auth.NewService(db, adminTestMasterKey)}
	req = adminDeviceContext(adminRequestWithRouteParam(http.MethodDelete, "/api/v1/devices/"+uuid.NewString(), "", "id", uuid.NewString()), auth.AdminActor{Type: "session", AdminID: adminID})
	rec = adminPerform(handler.RevokeDevice, req)
	assertAdminErrorCode(t, rec, http.StatusNotFound, "not_found")

	// 非法 uuid 同样回 404, 不区分「不存在」与「格式错误」。
	req = adminDeviceContext(adminRequestWithRouteParam(http.MethodDelete, "/api/v1/devices/not-a-uuid", "", "id", "not-a-uuid"), auth.AdminActor{Type: "session", AdminID: adminID})
	rec = adminPerform(handler.RevokeDevice, req)
	assertAdminErrorCode(t, rec, http.StatusNotFound, "not_found")
}

func TestRevokeAllDevicesHandler(t *testing.T) {
	t.Parallel()

	audits := make([]store.AdminAuditLog, 0, 1)
	db := adminGormWithCallbacks(t, adminGormCallbacks{
		update: func(tx *gorm.DB) {
			tx.Statement.RowsAffected = 2
		},
		create: func(tx *gorm.DB) {
			audit, ok := tx.Statement.Dest.(*store.AdminAuditLog)
			if !ok {
				tx.AddError(errors.New("unexpected create destination"))
				return
			}
			audits = append(audits, *audit)
			tx.Statement.RowsAffected = 1
		},
	})
	handler := Handler{auth: auth.NewService(db, adminTestMasterKey)}
	req := adminDeviceContext(adminTestRequest(http.MethodPost, "/api/v1/devices/revoke-all", ""), auth.AdminActor{Type: "session", AdminID: uuid.New()})
	rec := adminPerform(handler.RevokeAllDevices, req)

	adminAssertStatus(t, rec, http.StatusNoContent)
	if len(audits) != 1 || audits[0].Action != "device.revoke_all" || !strings.Contains(string(audits[0].Metadata), "2") {
		t.Fatalf("audits = %#v, want one device.revoke_all record with the revoked count", audits)
	}
}

func TestDeviceSelfEndpointsRequireDeviceActor(t *testing.T) {
	t.Parallel()

	handler := adminHandlerWithAuthService()
	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "get", method: http.MethodGet, path: "/api/v1/devices/self"},
		{name: "delete", method: http.MethodDelete, path: "/api/v1/devices/self"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sessionReq := adminDeviceContext(adminTestRequest(tc.method, tc.path, ""), auth.AdminActor{Type: "session", AdminID: uuid.New()})
			if tc.method == http.MethodGet {
				assertAdminErrorCode(t, adminPerform(handler.GetDeviceSelf, sessionReq), http.StatusBadRequest, "not_a_device")
				return
			}
			assertAdminErrorCode(t, adminPerform(handler.DeleteDeviceSelf, sessionReq), http.StatusBadRequest, "not_a_device")
		})
	}
}

func TestDeviceSelfEndpointsWorkForDeviceActor(t *testing.T) {
	t.Parallel()

	adminID := uuid.New()
	deviceID := uuid.New()
	lastSeen := time.Now().Add(-time.Minute)
	audits := make([]store.AdminAuditLog, 0, 1)
	db := adminGormWithCallbacks(t, adminGormCallbacks{
		query: func(tx *gorm.DB) {
			device, ok := tx.Statement.Dest.(*store.AdminDevice)
			if !ok {
				tx.AddError(errors.New("unexpected device query destination"))
				return
			}
			*device = store.AdminDevice{
				ID:         deviceID,
				AdminID:    adminID,
				Name:       "iPhone 15",
				Platform:   "ios",
				LastSeenAt: sql.NullTime{Time: lastSeen, Valid: true},
				CreatedAt:  time.Now(),
			}
			tx.Statement.RowsAffected = 1
		},
		update: func(tx *gorm.DB) {
			tx.Statement.RowsAffected = 1
		},
		create: func(tx *gorm.DB) {
			audit, ok := tx.Statement.Dest.(*store.AdminAuditLog)
			if !ok {
				tx.AddError(errors.New("unexpected create destination"))
				return
			}
			audits = append(audits, *audit)
			tx.Statement.RowsAffected = 1
		},
	})
	handler := Handler{auth: auth.NewService(db, adminTestMasterKey)}
	actor := auth.AdminActor{Type: auth.DeviceActorType, AdminID: adminID, DeviceID: deviceID}

	req := adminDeviceContext(adminTestRequest(http.MethodGet, "/api/v1/devices/self", ""), actor)
	rec := adminPerform(handler.GetDeviceSelf, req)
	adminAssertStatus(t, rec, http.StatusOK)
	body := adminDecodeJSON[struct {
		Data struct {
			ID      string `json:"id"`
			Current bool   `json:"current"`
		} `json:"data"`
	}](t, rec)
	if body.Data.ID != deviceID.String() || !body.Data.Current {
		t.Fatalf("self payload = %#v, want the current device", body.Data)
	}

	req = adminDeviceContext(adminTestRequest(http.MethodDelete, "/api/v1/devices/self", ""), actor)
	rec = adminPerform(handler.DeleteDeviceSelf, req)
	adminAssertStatus(t, rec, http.StatusNoContent)
	if len(audits) != 1 || audits[0].Action != "device.revoke_self" || audits[0].ActorType != "device" {
		t.Fatalf("audits = %#v, want one device.revoke_self record", audits)
	}
}

func TestAuthStateReturnsDeviceAuthTypeWithoutAccessAudit(t *testing.T) {
	t.Parallel()

	adminID := uuid.New()
	deviceID := uuid.New()
	queryCount := 0
	db := adminGormWithCallbacks(t, adminGormCallbacks{
		query: func(tx *gorm.DB) {
			queryCount++
			switch dest := tx.Statement.Dest.(type) {
			case *[]store.AdminDevice:
				*dest = []store.AdminDevice{{ID: deviceID, AdminID: adminID, Name: "iPhone 15", Platform: "ios", TokenHash: authDeviceTestHash()}}
			case *store.Admin:
				*dest = store.Admin{ID: adminID, Username: "owner", Role: "owner", Status: "active"}
			default:
				tx.AddError(errors.New("unexpected auth state query destination"))
				return
			}
			tx.Statement.RowsAffected = 1
		},
		update: func(tx *gorm.DB) {
			tx.Statement.RowsAffected = 1
		},
		create: func(tx *gorm.DB) {
			tx.AddError(errors.New("device auth state must not write an access audit"))
		},
	})
	handler := Handler{auth: auth.NewService(db, adminTestMasterKey)}
	req := adminTestRequest(http.MethodGet, "/api/v1/auth/state", "")
	req.Header.Set("Authorization", "Bearer "+authDeviceTestToken())
	rec := adminPerform(handler.AuthState, req)

	adminAssertStatus(t, rec, http.StatusOK)
	body := adminDecodeJSON[struct {
		Authenticated bool   `json:"authenticated"`
		AuthType      string `json:"auth_type"`
		Admin         struct {
			ID string `json:"id"`
		} `json:"admin"`
	}](t, rec)
	if !body.Authenticated || body.AuthType != "device" || body.Admin.ID != adminID.String() {
		t.Fatalf("auth state = %#v, want authenticated device", body)
	}
	if queryCount != 2 {
		t.Fatalf("query count = %d, want device and admin queries", queryCount)
	}
	if !headerValuesContain(rec.Header().Values("Vary"), "Authorization") {
		t.Fatalf("Vary = %v, want Authorization", rec.Header().Values("Vary"))
	}
}

func TestAuditAdminMutationSkipsDeviceAgentWrites(t *testing.T) {
	t.Parallel()

	audits := make([]store.AdminAuditLog, 0, 3)
	db := adminGormWithCallbacks(t, adminGormCallbacks{create: func(tx *gorm.DB) {
		audit, ok := tx.Statement.Dest.(*store.AdminAuditLog)
		if !ok {
			tx.AddError(errors.New("unexpected create destination"))
			return
		}
		audits = append(audits, *audit)
		tx.Statement.RowsAffected = 1
	}})
	handler := Handler{auth: auth.NewService(db, adminTestMasterKey)}
	deviceActor := auth.AdminActor{Type: auth.DeviceActorType, AdminID: uuid.New(), DeviceID: uuid.New()}
	sessionActor := auth.AdminActor{Type: "session", AdminID: uuid.New(), SessionID: uuid.New()}
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	for _, tc := range []struct {
		name      string
		method    string
		path      string
		actor     auth.AdminActor
		wantAudit bool
	}{
		{name: "device agent write", method: http.MethodPost, path: "/api/v1/agent/sessions", actor: deviceActor, wantAudit: false},
		{name: "device other write", method: http.MethodPost, path: "/api/v1/devices/revoke-all", actor: deviceActor, wantAudit: true},
		{name: "session agent write", method: http.MethodPost, path: "/api/v1/agent/sessions", actor: sessionActor, wantAudit: true},
		{name: "device agent read", method: http.MethodGet, path: "/api/v1/agent/sessions", actor: deviceActor, wantAudit: false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			audits = audits[:0]
			req := adminDeviceContext(adminTestRequest(tc.method, tc.path, ""), tc.actor)
			rec := adminPerform(handler.AuditAdminMutation(okHandler).ServeHTTP, req)

			adminAssertStatus(t, rec, http.StatusNoContent)
			if tc.wantAudit {
				if len(audits) != 1 || audits[0].Action != "admin_api.mutation" || audits[0].ActorType != tc.actor.Type {
					t.Fatalf("audits = %#v, want one admin_api.mutation record", audits)
				}
				return
			}
			if len(audits) != 0 {
				t.Fatalf("audits = %#v, want none", audits)
			}
		})
	}
}

func TestDeviceHandlersRequireAuthService(t *testing.T) {
	t.Parallel()

	rec := adminPerform((Handler{}).CreateDevicePairing, adminTestRequest(http.MethodPost, "/api/v1/devices/pairings", ""))
	assertAdminErrorCode(t, rec, http.StatusUnauthorized, "unauthorized")

	rec = adminPerform((Handler{}).RedeemDevicePairing, adminTestRequest(http.MethodPost, "/api/v1/devices/pair", "{}"))
	assertAdminErrorCode(t, rec, http.StatusServiceUnavailable, "auth_unavailable")

	rec = adminPerform((Handler{}).ListDevices, adminTestRequest(http.MethodGet, "/api/v1/devices", ""))
	assertAdminErrorCode(t, rec, http.StatusUnauthorized, "unauthorized")
}

// authDeviceTestToken 与 authDeviceTestHash 必须一一对应: 设备令牌明文与它的
// sha256 十六进制（服务端只存后者）。
func authDeviceTestToken() string {
	return "xld_test-device-token"
}

func authDeviceTestHash() string {
	sum := sha256.Sum256([]byte(authDeviceTestToken()))
	return hex.EncodeToString(sum[:])
}
