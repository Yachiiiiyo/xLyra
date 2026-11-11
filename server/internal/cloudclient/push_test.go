package cloudclient

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"xlyra/server/internal/crypto"
	"xlyra/server/internal/store"
)

func TestReportOmitsDevicesWithoutASource(t *testing.T) {
	st := openCloudStore(t)
	client := newTestClient(t, st, "https://cloud.example", nil)
	report, err := client.Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	assertExactKeys(t, raw, "xlyraVersion", "platform", "plugins", "counts", "sentAt")
}

func TestReportDevicesAreIOSOnlyAndEmptyListIsSent(t *testing.T) {
	st := openCloudStore(t)
	admin := createPushAdmin(t, st)
	repo := store.NewAdminDeviceRepository(st.DB())
	seen := time.Date(2026, 3, 4, 5, 6, 7, 0, time.FixedZone("CST", 8*3600))
	older := seen.Add(-time.Hour)
	ios := redeemPushDevice(t, st, admin, "iphone", "ios")
	mac := redeemPushDevice(t, st, admin, "mac", "macos")
	other := redeemPushDevice(t, st, admin, "older-phone", "iOS")
	if err := repo.TouchLastSeen(context.Background(), ios.ID, nil, "", seen, 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.TouchLastSeen(context.Background(), other.ID, nil, "", older, 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.RegisterPush(context.Background(), ios.ID, "ciphertext", "sandbox", time.Now()); err != nil {
		t.Fatal(err)
	}

	client := newTestClient(t, st, "https://cloud.example", nil)
	client.devices = devicePushAdapter{repo: repo}
	report, err := client.Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Devices) != 2 {
		t.Fatalf("devices = %#v, want the two ios rows and not %s", report.Devices, mac.ID)
	}
	if report.Devices[0].Ref != ios.ID.String() || report.Devices[0].Name != "iphone" || report.Devices[0].Platform != "ios" {
		t.Fatalf("first device = %#v", report.Devices[0])
	}
	if report.Devices[0].LastActiveAt != seen.UTC().Format(time.RFC3339) || !report.Devices[0].PushRegistered || report.Devices[0].PushEnv != "sandbox" {
		t.Fatalf("first device fields = %#v", report.Devices[0])
	}
	if report.Devices[1].Ref != other.ID.String() || report.Devices[1].PushRegistered || report.Devices[1].LastActiveAt != older.UTC().Format(time.RFC3339) {
		t.Fatalf("second device = %#v", report.Devices[1])
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"devices":[`) || strings.Contains(string(raw), "ciphertext") || strings.Contains(string(raw), mac.Name) {
		t.Fatalf("report = %s", raw)
	}

	empty := newTestClient(t, st, "https://cloud.example", nil)
	empty.devices = devicePushAdapter{repo: store.NewAdminDeviceRepository(openCloudStore(t).DB())}
	none, err := empty.Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(none)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"devices":[]`) {
		t.Fatalf("empty report = %s", raw)
	}
}

func TestHeartbeatThenSyncRegistersAndDeletes(t *testing.T) {
	st := openCloudStore(t)
	admin := createPushAdmin(t, st)
	repo := store.NewAdminDeviceRepository(st.DB())
	token := "ab" + strings.Repeat("cd", 16)
	withToken := redeemPushDevice(t, st, admin, "iphone", "ios")
	without := redeemPushDevice(t, st, admin, "old-phone", "ios")
	enc, err := crypto.EncryptSecret(cloudTestMasterKey, token)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RegisterPush(context.Background(), withToken.ID, enc, "production", time.Now()); err != nil {
		t.Fatal(err)
	}
	// 清除推送的设备也要同步一次删除; 直接把 synced 置空模拟刚清掉 token。
	if err := repo.MarkPushUnsynced(context.Background(), without.ID); err != nil {
		t.Fatal(err)
	}

	cloud := newFakeCloud("approve")
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	client := newTestClient(t, st, srv.URL, nil)
	client.devices = devicePushAdapter{repo: repo}
	if _, err := client.StartPairing(context.Background(), "desk"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go client.Run(ctx)

	waitUntil(t, 5*time.Second, func() bool {
		registered, regErr := repo.GetByID(context.Background(), withToken.ID)
		cleared, clearErr := repo.GetByID(context.Background(), without.ID)
		return regErr == nil && registered.PushSyncedAt.Valid && clearErr == nil && cleared.PushSyncedAt.Valid
	})
	body := cloud.lastBody()
	if !strings.Contains(string(body), `"devices":[`) || !strings.Contains(string(body), withToken.ID.String()) {
		t.Fatalf("heartbeat = %s", body)
	}
	puts := cloud.pushCalls("PUT")
	if len(puts) != 1 || puts[0].ref != withToken.ID.String() || puts[0].token != token || puts[0].env != "production" {
		t.Fatalf("puts = %#v", puts)
	}
	deletes := cloud.pushCalls("DELETE")
	if len(deletes) != 1 || deletes[0].ref != without.ID.String() {
		t.Fatalf("deletes = %#v", deletes)
	}
	if strings.Contains(string(body), token) || strings.Contains(string(body), enc) {
		t.Fatal("heartbeat leaked the token")
	}
}

func TestSyncDeviceLimitAndNetworkFailure(t *testing.T) {
	t.Run("device limit", func(t *testing.T) {
		st := openCloudStore(t)
		admin := createPushAdmin(t, st)
		repo := store.NewAdminDeviceRepository(st.DB())
		device := redeemPushDevice(t, st, admin, "iphone", "ios")
		enc, err := crypto.EncryptSecret(cloudTestMasterKey, "token-limit")
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.RegisterPush(context.Background(), device.ID, enc, "sandbox", time.Now()); err != nil {
			t.Fatal(err)
		}
		cloud := newFakeCloud("approve")
		cloud.pushPutStatus = http.StatusConflict
		cloud.pushPutCode = "device_limit"
		srv := httptest.NewServer(cloud)
		t.Cleanup(srv.Close)
		client := newTestClient(t, st, srv.URL, nil)
		client.devices = devicePushAdapter{repo: repo}
		if _, err := client.StartPairing(context.Background(), "desk"); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go client.Run(ctx)
		waitUntil(t, 5*time.Second, func() bool {
			stored, err := repo.GetByID(context.Background(), device.ID)
			return err == nil && stored.PushState == "error"
		})
		stored, err := repo.GetByID(context.Background(), device.ID)
		if err != nil || stored.PushSyncedAt.Valid {
			t.Fatalf("limited device = %#v err=%v", stored, err)
		}
	})

	t.Run("network failure does not stop heartbeats", func(t *testing.T) {
		st := openCloudStore(t)
		admin := createPushAdmin(t, st)
		repo := store.NewAdminDeviceRepository(st.DB())
		device := redeemPushDevice(t, st, admin, "iphone", "ios")
		enc, err := crypto.EncryptSecret(cloudTestMasterKey, "token-down")
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.RegisterPush(context.Background(), device.ID, enc, "sandbox", time.Now()); err != nil {
			t.Fatal(err)
		}
		cloud := newFakeCloud("approve")
		cloud.dropPush = true
		srv := httptest.NewServer(cloud)
		t.Cleanup(srv.Close)
		client := newTestClient(t, st, srv.URL, nil)
		client.devices = devicePushAdapter{repo: repo}
		if _, err := client.StartPairing(context.Background(), "desk"); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go client.Run(ctx)
		repoBinding := store.NewCloudBindingRepository(st.DB())
		waitUntil(t, 5*time.Second, func() bool {
			row, err := repoBinding.Get(context.Background())
			return err == nil && row.LastSyncAt != nil && cloud.count("/api/instance/heartbeat") >= 2
		})
		stored, err := repo.GetByID(context.Background(), device.ID)
		if err != nil || stored.PushSyncedAt.Valid {
			t.Fatalf("device synced despite the outage: %#v err=%v", stored, err)
		}
	})
}

func TestWakePushSyncMergesAndBeatsTheInterval(t *testing.T) {
	st := openCloudStore(t)
	admin := createPushAdmin(t, st)
	repo := store.NewAdminDeviceRepository(st.DB())
	device := redeemPushDevice(t, st, admin, "iphone", "ios")
	enc, err := crypto.EncryptSecret(cloudTestMasterKey, "token-wake")
	if err != nil {
		t.Fatal(err)
	}
	cloud := newFakeCloud("approve")
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	client := newTestClient(t, st, srv.URL, nil)
	client.heartbeatEvery = time.Hour
	client.wakeHeartbeatGap = 0
	client.devices = devicePushAdapter{repo: repo}
	if _, err := client.StartPairing(context.Background(), "desk"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go client.Run(ctx)
	binding := store.NewCloudBindingRepository(st.DB())
	waitUntil(t, 5*time.Second, func() bool {
		row, err := binding.Get(context.Background())
		return err == nil && row.Status == store.CloudStatusBound && cloud.count("/api/instance/heartbeat") >= 1
	})
	if err := repo.RegisterPush(context.Background(), device.ID, enc, "sandbox", time.Now()); err != nil {
		t.Fatal(err)
	}
	before := cloud.count("/api/instance/heartbeat")
	started := time.Now()
	for i := 0; i < 5; i++ {
		client.WakePushSync()
	}
	waitUntil(t, 2*time.Second, func() bool {
		stored, err := repo.GetByID(context.Background(), device.ID)
		puts := cloud.pushCalls("PUT")
		return err == nil && stored.PushSyncedAt.Valid && cloud.count("/api/instance/heartbeat") > before && len(puts) >= 1 && puts[0].token == "token-wake"
	})
	if time.Since(started) > 5*time.Second {
		t.Fatalf("wake took %s, want well under the hour interval", time.Since(started))
	}
	time.Sleep(400 * time.Millisecond)
	hearts := cloud.count("/api/instance/heartbeat") - before
	if hearts < 1 || hearts > 2 {
		t.Fatalf("heartbeats after five wakes = %d, want them merged", hearts)
	}
}

func TestBindingChangeResyncsEveryDevice(t *testing.T) {
	st := openCloudStore(t)
	admin := createPushAdmin(t, st)
	repo := store.NewAdminDeviceRepository(st.DB())
	device := redeemPushDevice(t, st, admin, "iphone", "ios")
	enc, err := crypto.EncryptSecret(cloudTestMasterKey, "token-rebind")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RegisterPush(context.Background(), device.ID, enc, "sandbox", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkPushSynced(context.Background(), device.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetPushState(context.Background(), device.ID, "ok", time.Now()); err != nil {
		t.Fatal(err)
	}
	cloud := newFakeCloud("approve")
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	client := newTestClient(t, st, srv.URL, nil)
	client.devices = devicePushAdapter{repo: repo}
	if _, err := client.StartPairing(context.Background(), "desk"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go client.Run(ctx)
	waitUntil(t, 5*time.Second, func() bool {
		stored, err := repo.GetByID(context.Background(), device.ID)
		return err == nil && stored.PushSyncedAt.Valid && stored.PushState == "" && len(cloud.pushCalls("PUT")) >= 1
	})
}

func TestRevokeStillClearsTheBinding(t *testing.T) {
	st := openCloudStore(t)
	admin := createPushAdmin(t, st)
	repo := store.NewAdminDeviceRepository(st.DB())
	device := redeemPushDevice(t, st, admin, "iphone", "ios")
	enc, err := crypto.EncryptSecret(cloudTestMasterKey, "token-revoke")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RegisterPush(context.Background(), device.ID, enc, "sandbox", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkPushSynced(context.Background(), device.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	cloud := newFakeCloud("revoke")
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	client := newTestClient(t, st, srv.URL, nil)
	client.devices = devicePushAdapter{repo: repo}
	if _, err := client.StartPairing(context.Background(), "desk"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go client.Run(ctx)
	binding := store.NewCloudBindingRepository(st.DB())
	waitUntil(t, 5*time.Second, func() bool {
		row, err := binding.Get(context.Background())
		return err == nil && row.Status == store.CloudStatusRevoked && row.LastError == "已在账号侧解除连接" && row.InstanceID == ""
	})
	stored, err := repo.GetByID(context.Background(), device.ID)
	if err != nil || stored.PushSyncedAt.Valid {
		t.Fatalf("revoked sync mark = %#v err=%v", stored.PushSyncedAt, err)
	}
	stable := cloud.count("/api/instance/heartbeat")
	time.Sleep(250 * time.Millisecond)
	if got := cloud.count("/api/instance/heartbeat"); got != stable {
		t.Fatalf("heartbeats after revoke = %d, was %d", got, stable)
	}
}

func TestSendPushMapsHubResponses(t *testing.T) {
	secretTitle := "secret-title-do-not-log"
	secretBody := "secret-body-do-not-log"
	cases := []struct {
		name   string
		status int
		code   string
		result string
		retry  string
		want   PushResult
		state  string
		unsync bool
		wake   bool
		err    bool
	}{
		{name: "ok", status: http.StatusOK, result: "ok", want: PushOK, state: "ok"},
		{name: "unregistered", status: http.StatusOK, result: "unregistered", want: PushUnregistered, state: "invalid"},
		{name: "rate limited", status: http.StatusServiceUnavailable, code: "rate_limited", retry: "12", want: PushRetryLater, err: true},
		{name: "too many", status: http.StatusTooManyRequests, code: "rate_limited", retry: "3", want: PushRetryLater, err: true},
		{name: "gateway error", status: http.StatusBadGateway, code: "error", want: PushRetryLater, state: "error"},
		{name: "rejected", status: http.StatusUnprocessableEntity, code: "rejected", want: PushRejected},
		{name: "missing device", status: http.StatusNotFound, code: "device_not_found", want: PushRetryLater, unsync: true, wake: true},
		{name: "not registered", status: http.StatusConflict, code: "device_not_registered", want: PushRetryLater, unsync: true, wake: true},
		{name: "invalid device", status: http.StatusConflict, code: "device_invalid", want: PushUnregistered},
		{name: "push unavailable", status: http.StatusServiceUnavailable, code: "push_unavailable", want: PushNotReady},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := openCloudStore(t)
			admin := createPushAdmin(t, st)
			repo := store.NewAdminDeviceRepository(st.DB())
			device := redeemPushDevice(t, st, admin, "iphone", "ios")
			if err := repo.MarkPushSynced(context.Background(), device.ID, time.Now()); err != nil {
				t.Fatal(err)
			}
			if err := repo.SetPushState(context.Background(), device.ID, "ok", time.Now()); err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			cloud := newFakeCloud("approve")
			cloud.sendStatus = tc.status
			cloud.sendCode = tc.code
			cloud.sendResult = tc.result
			cloud.sendRetryAfter = tc.retry
			srv := httptest.NewServer(cloud)
			t.Cleanup(srv.Close)
			client := newTestClient(t, st, srv.URL, nil)
			client.logger = logger
			client.devices = devicePushAdapter{repo: repo}
			// 这里只验证 404/409 会唤醒同步；心跳限速间隔由专门的测试覆盖。
			client.wakeHeartbeatGap = 0
			bindClient(t, st, client, cloud.instanceID)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			go client.Run(ctx)

			badge := 2
			result, err := client.SendPush(context.Background(), PushRequest{
				DeviceID: device.ID.String(), Kind: "approval", Title: secretTitle, Body: secretBody,
				SessionID: "sess", RefID: "ref", HighRisk: true, Badge: &badge, CollapseID: "c", Priority: 10,
			})
			if result != tc.want {
				t.Fatalf("result = %d, want %d (err=%v)", result, tc.want, err)
			}
			if tc.err {
				var retry *RetryAfterError
				if err == nil || !errorAs(err, &retry) || strconvItoa(retry.Seconds) != tc.retry {
					t.Fatalf("err = %v, want retry-after %s", err, tc.retry)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			stored, err := repo.GetByID(context.Background(), device.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.state != "" && stored.PushState != tc.state {
				t.Fatalf("state = %q, want %q", stored.PushState, tc.state)
			}
			if tc.unsync && stored.PushSyncedAt.Valid {
				t.Fatal("device stayed synced")
			}
			if !tc.unsync && !stored.PushSyncedAt.Valid {
				t.Fatal("device was marked unsynced")
			}
			text := logs.String()
			if strings.Contains(text, secretTitle) || strings.Contains(text, secretBody) || strings.Contains(text, "token") {
				t.Fatalf("log leaked a secret: %s", text)
			}
			sent := cloud.lastSend()
			if !strings.Contains(string(sent), secretTitle) || !strings.Contains(string(sent), `"badge":2`) || !strings.Contains(string(sent), `"high_risk":true`) {
				t.Fatalf("send body = %s", sent)
			}
			if tc.wake {
				waitUntil(t, 2*time.Second, func() bool {
					return cloud.count("/api/instance/heartbeat") >= 2
				})
			}
		})
	}
}

func TestSendPushUnboundIsNotReady(t *testing.T) {
	st := openCloudStore(t)
	client := newTestClient(t, st, "https://cloud.example", nil)
	result, err := client.SendPush(context.Background(), PushRequest{DeviceID: uuid.NewString(), Kind: "done", Title: "t", Body: "b"})
	if err != nil || result != PushNotReady {
		t.Fatalf("result=%d err=%v", result, err)
	}
}

func TestSendPushNetworkAndRevoked(t *testing.T) {
	t.Run("network", func(t *testing.T) {
		st := openCloudStore(t)
		admin := createPushAdmin(t, st)
		repo := store.NewAdminDeviceRepository(st.DB())
		device := redeemPushDevice(t, st, admin, "iphone", "ios")
		cloud := newFakeCloud("approve")
		cloud.dropSend = true
		srv := httptest.NewServer(cloud)
		t.Cleanup(srv.Close)
		client := newTestClient(t, st, srv.URL, nil)
		client.devices = devicePushAdapter{repo: repo}
		bindClient(t, st, client, cloud.instanceID)
		result, err := client.SendPush(context.Background(), PushRequest{DeviceID: device.ID.String(), Kind: "error", Title: "t", Body: "b"})
		if err == nil || result != PushRetryLater {
			t.Fatalf("result=%d err=%v", result, err)
		}
		stored, err := repo.GetByID(context.Background(), device.ID)
		if err != nil || stored.PushState != "error" {
			t.Fatalf("state = %#v err=%v", stored, err)
		}
	})

	t.Run("revoked", func(t *testing.T) {
		st := openCloudStore(t)
		admin := createPushAdmin(t, st)
		repo := store.NewAdminDeviceRepository(st.DB())
		device := redeemPushDevice(t, st, admin, "iphone", "ios")
		if err := repo.MarkPushSynced(context.Background(), device.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
		cloud := newFakeCloud("approve")
		cloud.sendStatus = http.StatusUnauthorized
		cloud.sendCode = "instance_revoked"
		srv := httptest.NewServer(cloud)
		t.Cleanup(srv.Close)
		client := newTestClient(t, st, srv.URL, nil)
		client.devices = devicePushAdapter{repo: repo}
		bindClient(t, st, client, cloud.instanceID)
		result, err := client.SendPush(context.Background(), PushRequest{DeviceID: device.ID.String(), Kind: "done", Title: "t", Body: "b"})
		if err != errRevoked || result != PushNotReady {
			t.Fatalf("result=%d err=%v", result, err)
		}
		row, err := store.NewCloudBindingRepository(st.DB()).Get(context.Background())
		if err != nil || row.Status != store.CloudStatusRevoked || row.InstanceID != "" {
			t.Fatalf("binding = %#v err=%v", row, err)
		}
		stored, err := repo.GetByID(context.Background(), device.ID)
		if err != nil || stored.PushSyncedAt.Valid {
			t.Fatalf("sync mark = %#v err=%v", stored.PushSyncedAt, err)
		}
	})
}

func TestSyncSkipsAnUndecryptableToken(t *testing.T) {
	st := openCloudStore(t)
	admin := createPushAdmin(t, st)
	repo := store.NewAdminDeviceRepository(st.DB())
	bad := redeemPushDevice(t, st, admin, "bad", "ios")
	good := redeemPushDevice(t, st, admin, "good", "ios")
	enc, err := crypto.EncryptSecret(cloudTestMasterKey, "good-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RegisterPush(context.Background(), bad.ID, "not-ciphertext", "sandbox", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := repo.RegisterPush(context.Background(), good.ID, enc, "sandbox", time.Now()); err != nil {
		t.Fatal(err)
	}
	cloud := newFakeCloud("approve")
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	client := newTestClient(t, st, srv.URL, nil)
	client.devices = devicePushAdapter{repo: repo}
	if _, err := client.StartPairing(context.Background(), "desk"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go client.Run(ctx)
	waitUntil(t, 5*time.Second, func() bool {
		stored, err := repo.GetByID(context.Background(), good.ID)
		return err == nil && stored.PushSyncedAt.Valid
	})
	stored, err := repo.GetByID(context.Background(), bad.ID)
	if err != nil || stored.PushState != "error" || stored.PushSyncedAt.Valid {
		t.Fatalf("bad device = %#v err=%v", stored, err)
	}
	puts := cloud.pushCalls("PUT")
	if len(puts) != 1 || puts[0].token != "good-token" {
		t.Fatalf("puts = %#v", puts)
	}
}

func TestSyncRevokedDuringRegister(t *testing.T) {
	st := openCloudStore(t)
	admin := createPushAdmin(t, st)
	repo := store.NewAdminDeviceRepository(st.DB())
	device := redeemPushDevice(t, st, admin, "iphone", "ios")
	enc, err := crypto.EncryptSecret(cloudTestMasterKey, "token-mid")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RegisterPush(context.Background(), device.ID, enc, "sandbox", time.Now()); err != nil {
		t.Fatal(err)
	}
	cloud := newFakeCloud("approve")
	cloud.pushPutStatus = http.StatusUnauthorized
	cloud.pushPutCode = "instance_revoked"
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	client := newTestClient(t, st, srv.URL, nil)
	client.devices = devicePushAdapter{repo: repo}
	if _, err := client.StartPairing(context.Background(), "desk"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go client.Run(ctx)
	binding := store.NewCloudBindingRepository(st.DB())
	waitUntil(t, 5*time.Second, func() bool {
		row, err := binding.Get(context.Background())
		return err == nil && row.Status == store.CloudStatusRevoked
	})
	stable := cloud.count("/api/instance/heartbeat")
	time.Sleep(200 * time.Millisecond)
	if got := cloud.count("/api/instance/heartbeat"); got != stable {
		t.Fatalf("heartbeats after revoke = %d, was %d", got, stable)
	}
}

// devicePushAdapter 是测试里的 DeviceStore, 直接用真实仓库。
type devicePushAdapter struct {
	repo store.AdminDeviceRepository
}

func (a devicePushAdapter) ListActiveRecent(ctx context.Context, limit int) ([]store.AdminDevice, error) {
	return a.repo.ListActiveRecent(ctx, limit)
}

func (a devicePushAdapter) ListUnsyncedPush(ctx context.Context) ([]store.AdminDevice, error) {
	return a.repo.ListUnsyncedPush(ctx)
}

func (a devicePushAdapter) MarkPushSynced(ctx context.Context, id uuid.UUID, at time.Time) error {
	return a.repo.MarkPushSynced(ctx, id, at)
}

func (a devicePushAdapter) SetPushState(ctx context.Context, id uuid.UUID, state string, at time.Time) error {
	return a.repo.SetPushState(ctx, id, state, at)
}

func (a devicePushAdapter) MarkPushUnsynced(ctx context.Context, id uuid.UUID) error {
	return a.repo.MarkPushUnsynced(ctx, id)
}

func (a devicePushAdapter) MarkAllPushUnsynced(ctx context.Context) error {
	return a.repo.MarkEveryDevicePushUnsynced(ctx)
}

func createPushAdmin(t *testing.T, st *store.Store) store.Admin {
	t.Helper()
	admin, err := store.NewAdminRepository(st.DB()).Create(context.Background(), store.CreateAdminParams{
		Username:     "push-" + uuid.NewString()[:8],
		PasswordHash: "hash",
		Role:         "owner",
		Status:       "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	return admin
}

func redeemPushDevice(t *testing.T, st *store.Store, admin store.Admin, name, platform string) store.AdminDevice {
	t.Helper()
	pairings := store.NewAdminDevicePairingRepository(st.DB())
	pairing, err := pairings.Create(context.Background(), store.CreateAdminDevicePairingParams{
		AdminID:   admin.ID,
		CodeHash:  "code-" + uuid.NewString(),
		ExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	device, err := store.NewAdminDeviceRepository(st.DB()).RedeemPairing(context.Background(), store.RedeemAdminDevicePairingParams{
		CodeHash:  pairing.CodeHash,
		Name:      name,
		Platform:  platform,
		TokenHash: "hash-" + uuid.NewString(),
		Now:       time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return device
}

func bindClient(t *testing.T, st *store.Store, client *Client, instanceID string) {
	t.Helper()
	if _, err := client.StartPairing(context.Background(), "desk"); err != nil {
		t.Fatal(err)
	}
	if err := store.NewCloudBindingRepository(st.DB()).MarkBound(context.Background(), instanceID, "desk", "Ada", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func errorAs(err error, target **RetryAfterError) bool {
	retry, ok := err.(*RetryAfterError)
	if ok {
		*target = retry
	}
	return ok
}

func strconvItoa(n int) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

type pushCall struct {
	method string
	ref    string
	token  string
	env    string
	body   []byte
}

func (h *fakeCloud) pushCalls(method string) []pushCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]pushCall, 0, len(h.pushes))
	for _, call := range h.pushes {
		if call.method == method {
			out = append(out, call)
		}
	}
	return out
}

func (h *fakeCloud) lastSend() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]byte(nil), h.sendBody...)
}

func (h *fakeCloud) servePush(w http.ResponseWriter, r *http.Request, body []byte) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/instance/push/") {
		return false
	}
	h.mu.Lock()
	ok, code, message, serverTime := h.signatureError(r, body)
	verified := ok
	drop := verified && ((h.dropPush && r.URL.Path != "/api/instance/push/send") || (h.dropSend && r.URL.Path == "/api/instance/push/send"))
	replyStatus, replyCode, replyResult, retryAfter := 0, "", "", ""
	if verified && !drop {
		switch {
		case r.URL.Path == "/api/instance/push/send" && r.Method == http.MethodPost:
			h.sendBody = append([]byte(nil), body...)
			replyStatus, replyCode, replyResult, retryAfter = h.sendStatus, h.sendCode, h.sendResult, h.sendRetryAfter
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/instance/push/devices/"):
			var req struct {
				Token string `json:"apns_token"`
				Env   string `json:"env"`
			}
			_ = json.Unmarshal(body, &req)
			h.pushes = append(h.pushes, pushCall{
				method: r.Method,
				ref:    strings.TrimPrefix(r.URL.Path, "/api/instance/push/devices/"),
				token:  req.Token,
				env:    req.Env,
				body:   append([]byte(nil), body...),
			})
			replyStatus, replyCode = http.StatusNoContent, ""
			if h.pushPutStatus != 0 {
				replyStatus, replyCode = h.pushPutStatus, h.pushPutCode
			}
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/instance/push/devices/"):
			h.pushes = append(h.pushes, pushCall{
				method: r.Method,
				ref:    strings.TrimPrefix(r.URL.Path, "/api/instance/push/devices/"),
			})
			replyStatus = http.StatusNoContent
		default:
			replyStatus, replyCode = http.StatusNotFound, "not_found"
		}
	}
	h.mu.Unlock()
	if !verified {
		writeCloudError(w, http.StatusUnauthorized, code, message, serverTime)
		return true
	}
	if drop {
		hijackClose(w)
		return true
	}
	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	switch {
	case replyResult != "":
		writeJSON(w, replyStatus, map[string]any{"ok": replyResult != "unregistered", "result": replyResult})
	case replyCode != "":
		writeCloudError(w, replyStatus, replyCode, replyCode, "")
	default:
		w.WriteHeader(replyStatus)
	}
	return true
}

func hijackClose(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return
	}
	conn, _, err := hj.Hijack()
	if err == nil {
		_ = conn.Close()
	}
}

// Hub 每个实例每分钟只放行 1 次心跳。被唤醒时推送登记要立刻同步，
// 但紧跟着的心跳要等限速放行，不能在窗口内再发一次被 429 拒绝。
func TestWakeSyncsPushAtOnceButHeartbeatRespectsHubRateLimit(t *testing.T) {
	st := openCloudStore(t)
	admin := createPushAdmin(t, st)
	repo := store.NewAdminDeviceRepository(st.DB())
	device := redeemPushDevice(t, st, admin, "iphone", "ios")
	enc, err := crypto.EncryptSecret(cloudTestMasterKey, "token-gate")
	if err != nil {
		t.Fatal(err)
	}
	cloud := newFakeCloud("approve")
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	client := newTestClient(t, st, srv.URL, nil)
	client.heartbeatEvery = time.Hour
	client.wakeHeartbeatGap = 3 * time.Second
	client.devices = devicePushAdapter{repo: repo}
	if _, err := client.StartPairing(context.Background(), "desk"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go client.Run(ctx)
	binding := store.NewCloudBindingRepository(st.DB())
	waitUntil(t, 5*time.Second, func() bool {
		row, err := binding.Get(context.Background())
		return err == nil && row.Status == store.CloudStatusBound && cloud.count("/api/instance/heartbeat") >= 1
	})
	if err := repo.RegisterPush(context.Background(), device.ID, enc, "sandbox", time.Now()); err != nil {
		t.Fatal(err)
	}
	before := cloud.count("/api/instance/heartbeat")
	client.WakePushSync()
	waitUntil(t, 2*time.Second, func() bool {
		puts := cloud.pushCalls("PUT")
		return len(puts) >= 1 && puts[0].token == "token-gate"
	})
	if got := cloud.count("/api/instance/heartbeat"); got != before {
		t.Fatalf("heartbeat inside the rate-limit gap: %d -> %d", before, got)
	}
	waitUntil(t, 6*time.Second, func() bool { return cloud.count("/api/instance/heartbeat") > before })
}
