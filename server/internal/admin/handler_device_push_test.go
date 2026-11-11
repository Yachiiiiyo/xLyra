package admin

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xlyra/server/internal/auth"
	"xlyra/server/internal/crypto"
	"xlyra/server/internal/store"
)

// pushWakeCounter 记录推送同步被唤醒的次数。
type pushWakeCounter struct{ calls int }

func (c *pushWakeCounter) WakePushSync() { c.calls++ }

func devicePushFixture(t *testing.T, device store.AdminDevice, audits *[]store.AdminAuditLog) (*gorm.DB, *[]map[string]any) {
	t.Helper()

	updates := make([]map[string]any, 0, 2)
	db := adminGormWithCallbacks(t, adminGormCallbacks{
		query: func(tx *gorm.DB) {
			dest, ok := tx.Statement.Dest.(*store.AdminDevice)
			if !ok {
				tx.AddError(errors.New("unexpected query destination"))
				return
			}
			*dest = device
			tx.Statement.RowsAffected = 1
		},
		update: func(tx *gorm.DB) {
			dest, ok := tx.Statement.Dest.(map[string]any)
			if !ok {
				tx.AddError(errors.New("unexpected update destination"))
				return
			}
			updates = append(updates, dest)
			tx.Statement.RowsAffected = 1
		},
		create: func(tx *gorm.DB) {
			audit, ok := tx.Statement.Dest.(*store.AdminAuditLog)
			if !ok {
				tx.AddError(errors.New("unexpected create destination"))
				return
			}
			*audits = append(*audits, *audit)
			tx.Statement.RowsAffected = 1
		},
	})
	return db, &updates
}

func TestPutDevicePushEncryptsTokenAndAuditsWithoutIt(t *testing.T) {
	t.Parallel()

	deviceID := uuid.New()
	adminID := uuid.New()
	token := "abcd1234"
	var logs bytes.Buffer
	audits := make([]store.AdminAuditLog, 0, 1)
	db, updates := devicePushFixture(t, store.AdminDevice{ID: deviceID, AdminID: adminID, Name: "iphone", Platform: "ios"}, &audits)
	waker := &pushWakeCounter{}
	handler := Handler{logger: slog.New(slog.NewTextHandler(&logs, nil)), auth: auth.NewService(db, adminTestMasterKey), PushSyncWaker: waker}

	req := adminDeviceContext(adminTestRequest(http.MethodPut, "/api/v1/devices/self/push", `{"apns_token":"`+token+`","environment":"sandbox"}`), auth.AdminActor{Type: auth.DeviceActorType, AdminID: adminID, DeviceID: deviceID})
	rec := adminPerform(handler.PutDevicePush, req)

	adminAssertStatus(t, rec, http.StatusNoContent)
	if len(*updates) != 1 {
		t.Fatalf("updates = %d, want one", len(*updates))
	}
	stored, _ := (*updates)[0]["apns_token_enc"].(string)
	if stored == "" || stored == token || strings.Contains(stored, token) {
		t.Fatalf("stored token = %q, want ciphertext that hides the plaintext", stored)
	}
	plain, err := crypto.DecryptSecret(adminTestMasterKey, stored)
	if err != nil || plain != token {
		t.Fatalf("decrypt stored token = %q, %v; want the original token", plain, err)
	}
	if (*updates)[0]["apns_env"] != "sandbox" || !nullSQL((*updates)[0]["push_synced_at"]) || (*updates)[0]["push_state"] != "" {
		t.Fatalf("updates = %v, want sandbox env, unsynced and cleared state", (*updates)[0])
	}
	if len(audits) != 1 || audits[0].Action != "device.push_register" {
		t.Fatalf("audits = %#v, want one device.push_register record", audits)
	}
	if strings.Contains(string(audits[0].Metadata), token) || !strings.Contains(string(audits[0].Metadata), "sandbox") {
		t.Fatalf("audit metadata = %s, want the environment but not the token", audits[0].Metadata)
	}
	if strings.Contains(rec.Body.String(), token) || strings.Contains(logs.String(), token) {
		t.Fatal("plaintext token leaked into the response or the logs")
	}
	if waker.calls != 1 {
		t.Fatalf("WakePushSync calls = %d, want 1", waker.calls)
	}
}

func TestPutDevicePushRejectsBadInput(t *testing.T) {
	t.Parallel()

	device := store.AdminDevice{ID: uuid.New(), AdminID: uuid.New()}
	for _, tc := range []struct {
		name string
		body string
		code string
	}{
		{name: "empty token", body: `{"apns_token":"","environment":"sandbox"}`, code: "invalid_push_token"},
		{name: "non hex token", body: `{"apns_token":"zzzz","environment":"sandbox"}`, code: "invalid_push_token"},
		{name: "too long token", body: `{"apns_token":"` + strings.Repeat("ab", 101) + `","environment":"sandbox"}`, code: "invalid_push_token"},
		{name: "bad environment", body: `{"apns_token":"abcd","environment":"staging"}`, code: "invalid_push_environment"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			audits := make([]store.AdminAuditLog, 0, 1)
			db, _ := devicePushFixture(t, device, &audits)
			handler := Handler{auth: auth.NewService(db, adminTestMasterKey)}
			req := adminDeviceContext(adminTestRequest(http.MethodPut, "/api/v1/devices/self/push", tc.body), auth.AdminActor{Type: auth.DeviceActorType, AdminID: device.AdminID, DeviceID: device.ID})

			assertAdminErrorCode(t, adminPerform(handler.PutDevicePush, req), http.StatusBadRequest, tc.code)
			if len(audits) != 0 {
				t.Fatalf("audits = %#v, want none for rejected input", audits)
			}
		})
	}
}

func TestDevicePushEndpointsRejectNonDeviceActor(t *testing.T) {
	t.Parallel()

	handler := adminHandlerWithAuthService()
	actor := auth.AdminActor{Type: "session", AdminID: uuid.New()}
	for _, tc := range []struct {
		name    string
		method  string
		path    string
		body    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{name: "put", method: http.MethodPut, path: "/api/v1/devices/self/push", body: `{}`, handler: handler.PutDevicePush},
		{name: "delete", method: http.MethodDelete, path: "/api/v1/devices/self/push", handler: handler.DeleteDevicePush},
		{name: "get", method: http.MethodGet, path: "/api/v1/devices/self/push", handler: handler.GetDevicePush},
		{name: "prefs", method: http.MethodPut, path: "/api/v1/devices/self/push/prefs", body: `{}`, handler: handler.PutDevicePushPrefs},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := adminDeviceContext(adminTestRequest(tc.method, tc.path, tc.body), actor)
			assertAdminErrorCode(t, adminPerform(tc.handler, req), http.StatusBadRequest, "not_a_device")
		})
	}
}

func TestGetDevicePushReportsStateAndHubBinding(t *testing.T) {
	t.Parallel()

	registeredAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	device := store.AdminDevice{
		ID:               uuid.New(),
		AdminID:          uuid.New(),
		APNSTokenEnc:     "ciphertext",
		APNSEnv:          "production",
		PushState:        "ok",
		PushPrefs:        store.JSON([]byte(`{"approval":false}`)),
		PushRegisteredAt: sql.NullTime{Time: registeredAt, Valid: true},
	}
	audits := make([]store.AdminAuditLog, 0, 1)
	db, _ := devicePushFixture(t, device, &audits)
	handler := Handler{
		auth: auth.NewService(db, adminTestMasterKey),
		HubStatus: func(context.Context) (bool, string) {
			return true, "last sync failed"
		},
	}
	req := adminDeviceContext(adminTestRequest(http.MethodGet, "/api/v1/devices/self/push", ""), auth.AdminActor{Type: auth.DeviceActorType, AdminID: device.AdminID, DeviceID: device.ID})
	rec := adminPerform(handler.GetDevicePush, req)

	adminAssertStatus(t, rec, http.StatusOK)
	body := adminDecodeJSON[struct {
		Data struct {
			Registered   bool   `json:"registered"`
			Environment  string `json:"environment"`
			State        string `json:"state"`
			RegisteredAt string `json:"registered_at"`
			Prefs        struct {
				Approval bool `json:"approval"`
				Question bool `json:"question"`
				Done     bool `json:"done"`
				Error    bool `json:"error"`
			} `json:"prefs"`
			Hub struct {
				Bound     bool   `json:"bound"`
				LastError string `json:"last_error"`
			} `json:"hub"`
		} `json:"data"`
	}](t, rec)
	if !body.Data.Registered || body.Data.Environment != "production" || body.Data.State != "ok" {
		t.Fatalf("push status = %#v, want registered production ok", body.Data)
	}
	if body.Data.Prefs.Approval || !body.Data.Prefs.Question || !body.Data.Prefs.Done || !body.Data.Prefs.Error {
		t.Fatalf("prefs = %#v, want approval off and the rest on", body.Data.Prefs)
	}
	if !body.Data.Hub.Bound || body.Data.Hub.LastError != "last sync failed" {
		t.Fatalf("hub = %#v, want bound with the last error", body.Data.Hub)
	}
	if strings.Contains(rec.Body.String(), "ciphertext") {
		t.Fatal("response exposed the stored token")
	}
}

func TestGetDevicePushDefaultsHubToUnbound(t *testing.T) {
	t.Parallel()

	device := store.AdminDevice{ID: uuid.New(), AdminID: uuid.New()}
	audits := make([]store.AdminAuditLog, 0, 1)
	db, _ := devicePushFixture(t, device, &audits)
	handler := Handler{auth: auth.NewService(db, adminTestMasterKey)}
	req := adminDeviceContext(adminTestRequest(http.MethodGet, "/api/v1/devices/self/push", ""), auth.AdminActor{Type: auth.DeviceActorType, AdminID: device.AdminID, DeviceID: device.ID})
	rec := adminPerform(handler.GetDevicePush, req)

	adminAssertStatus(t, rec, http.StatusOK)
	body := adminDecodeJSON[struct {
		Data struct {
			Registered bool `json:"registered"`
			Hub        struct {
				Bound bool `json:"bound"`
			} `json:"hub"`
		} `json:"data"`
	}](t, rec)
	if body.Data.Registered || body.Data.Hub.Bound {
		t.Fatalf("push status = %#v, want unregistered and unbound", body.Data)
	}
}

func TestPutDevicePushPrefsMergesMissingKeys(t *testing.T) {
	t.Parallel()

	device := store.AdminDevice{
		ID:        uuid.New(),
		AdminID:   uuid.New(),
		PushPrefs: store.JSON([]byte(`{"approval":false,"question":false}`)),
	}
	audits := make([]store.AdminAuditLog, 0, 1)
	db, updates := devicePushFixture(t, device, &audits)
	handler := Handler{auth: auth.NewService(db, adminTestMasterKey)}
	req := adminDeviceContext(adminTestRequest(http.MethodPut, "/api/v1/devices/self/push/prefs", `{"question":true}`), auth.AdminActor{Type: auth.DeviceActorType, AdminID: device.AdminID, DeviceID: device.ID})
	rec := adminPerform(handler.PutDevicePushPrefs, req)

	adminAssertStatus(t, rec, http.StatusNoContent)
	if len(*updates) != 1 {
		t.Fatalf("updates = %d, want one", len(*updates))
	}
	stored, _ := (*updates)[0]["push_prefs"].(store.JSON)
	saved := pushPrefsValue(stored)
	if saved["approval"] || !saved["question"] || !saved["done"] || !saved["error"] {
		t.Fatalf("saved prefs = %v, want approval kept off, question flipped on, rest on", saved)
	}
	if len(audits) != 1 || audits[0].Action != "device.push_prefs" {
		t.Fatalf("audits = %#v, want one device.push_prefs record", audits)
	}
}

func TestDeleteDevicePushIsIdempotentAndWakesSync(t *testing.T) {
	t.Parallel()

	device := store.AdminDevice{ID: uuid.New(), AdminID: uuid.New()}
	audits := make([]store.AdminAuditLog, 0, 1)
	db, updates := devicePushFixture(t, device, &audits)
	waker := &pushWakeCounter{}
	handler := Handler{auth: auth.NewService(db, adminTestMasterKey), PushSyncWaker: waker}
	actor := auth.AdminActor{Type: auth.DeviceActorType, AdminID: device.AdminID, DeviceID: device.ID}

	for i := 0; i < 2; i++ {
		req := adminDeviceContext(adminTestRequest(http.MethodDelete, "/api/v1/devices/self/push", ""), actor)
		adminAssertStatus(t, adminPerform(handler.DeleteDevicePush, req), http.StatusNoContent)
	}
	if len(*updates) != 2 || (*updates)[0]["apns_token_enc"] != "" || (*updates)[0]["apns_env"] != "" {
		t.Fatalf("updates = %v, want both deletes to clear the registration", *updates)
	}
	if waker.calls != 2 {
		t.Fatalf("WakePushSync calls = %d, want 2", waker.calls)
	}
	if len(audits) != 2 || audits[0].Action != "device.push_unregister" {
		t.Fatalf("audits = %#v, want two device.push_unregister records", audits)
	}
}

func TestDevicePayloadIncludesPushWithoutToken(t *testing.T) {
	t.Parallel()

	registeredAt := time.Now().Add(-time.Minute).UTC()
	device := store.AdminDevice{
		ID:               uuid.New(),
		AdminID:          uuid.New(),
		Name:             "iphone",
		Platform:         "ios",
		APNSTokenEnc:     "ciphertext",
		APNSEnv:          "sandbox",
		PushState:        "invalid",
		PushRegisteredAt: sql.NullTime{Time: registeredAt, Valid: true},
		CreatedAt:        time.Now(),
	}
	audits := make([]store.AdminAuditLog, 0, 1)
	db, _ := devicePushFixture(t, device, &audits)
	handler := Handler{auth: auth.NewService(db, adminTestMasterKey)}

	selfReq := adminDeviceContext(adminTestRequest(http.MethodGet, "/api/v1/devices/self", ""), auth.AdminActor{Type: auth.DeviceActorType, AdminID: device.AdminID, DeviceID: device.ID})
	selfRec := adminPerform(handler.GetDeviceSelf, selfReq)
	adminAssertStatus(t, selfRec, http.StatusOK)
	if !strings.Contains(selfRec.Body.String(), `"registered":true`) || strings.Contains(selfRec.Body.String(), "ciphertext") {
		t.Fatalf("self payload = %s, want push.registered without the token", selfRec.Body.String())
	}

	listDB := adminGormWithCallbacks(t, adminGormCallbacks{query: func(tx *gorm.DB) {
		devices, ok := tx.Statement.Dest.(*[]store.AdminDevice)
		if !ok {
			tx.AddError(errors.New("unexpected query destination"))
			return
		}
		*devices = []store.AdminDevice{device}
		tx.Statement.RowsAffected = 1
	}})
	listHandler := Handler{auth: auth.NewService(listDB, adminTestMasterKey)}
	listReq := adminDeviceContext(adminTestRequest(http.MethodGet, "/api/v1/devices", ""), auth.AdminActor{Type: "session", AdminID: device.AdminID})
	listRec := adminPerform(listHandler.ListDevices, listReq)
	adminAssertStatus(t, listRec, http.StatusOK)
	if !strings.Contains(listRec.Body.String(), `"environment":"sandbox"`) || !strings.Contains(listRec.Body.String(), `"state":"invalid"`) || strings.Contains(listRec.Body.String(), "ciphertext") {
		t.Fatalf("list payload = %s, want push fields without the token", listRec.Body.String())
	}
}

func TestRevokeWakesPushSync(t *testing.T) {
	t.Parallel()

	device := store.AdminDevice{ID: uuid.New(), AdminID: uuid.New()}
	audits := make([]store.AdminAuditLog, 0, 1)
	db, _ := devicePushFixture(t, device, &audits)
	waker := &pushWakeCounter{}
	handler := Handler{auth: auth.NewService(db, adminTestMasterKey), PushSyncWaker: waker}

	// 自助退出。
	req := adminDeviceContext(adminTestRequest(http.MethodDelete, "/api/v1/devices/self", ""), auth.AdminActor{Type: auth.DeviceActorType, AdminID: device.AdminID, DeviceID: device.ID})
	adminAssertStatus(t, adminPerform(handler.DeleteDeviceSelf, req), http.StatusNoContent)

	// 全部吊销。
	allReq := adminDeviceContext(adminTestRequest(http.MethodPost, "/api/v1/devices/revoke-all", ""), auth.AdminActor{Type: "session", AdminID: device.AdminID})
	adminAssertStatus(t, adminPerform(handler.RevokeAllDevices, allReq), http.StatusNoContent)

	// 吊销单台。
	oneReq := adminDeviceContext(adminTestRequest(http.MethodDelete, "/api/v1/devices/"+device.ID.String(), ""), auth.AdminActor{Type: "session", AdminID: device.AdminID})
	oneReq = withRouteParam(oneReq, "id", device.ID.String())
	adminAssertStatus(t, adminPerform(handler.RevokeDevice, oneReq), http.StatusNoContent)

	if waker.calls != 3 {
		t.Fatalf("WakePushSync calls = %d, want one per revoke path", waker.calls)
	}
}

func nullSQL(value any) bool {
	expr, ok := value.(clause.Expr)
	return ok && strings.EqualFold(strings.TrimSpace(expr.SQL), "NULL")
}
