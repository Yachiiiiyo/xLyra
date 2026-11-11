package notify

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"

	"xlyra/server/internal/store"
)

type fakeLister struct {
	rows []store.AdminDevice
	err  error
}

func (f fakeLister) ListActiveWithPushToken(context.Context) ([]store.AdminDevice, error) {
	return f.rows, f.err
}

func TestStoreDevicesFilters(t *testing.T) {
	ios := store.AdminDevice{
		ID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), Platform: "iOS",
		APNSTokenEnc: "cipher", PushPrefs: store.JSON(`{"done":false}`),
	}
	invalid := ios
	invalid.ID = uuid.MustParse("00000000-0000-0000-0000-000000000002")
	invalid.PushState = "invalid"
	noToken := ios
	noToken.ID = uuid.MustParse("00000000-0000-0000-0000-000000000003")
	noToken.APNSTokenEnc = "  "
	android := ios
	android.ID = uuid.MustParse("00000000-0000-0000-0000-000000000004")
	android.Platform = "android"
	revoked := ios
	revoked.ID = uuid.MustParse("00000000-0000-0000-0000-000000000005")
	revoked.RevokedAt = sql.NullTime{Time: time.Now(), Valid: true}
	plain := ios
	plain.ID = uuid.MustParse("00000000-0000-0000-0000-000000000006")
	plain.PushPrefs = nil

	got, err := (StoreDevices{Repo: fakeLister{rows: []store.AdminDevice{ios, invalid, noToken, android, revoked, plain}}}).ListPushDevices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("可推送设备 = %d, want 2 (%+v)", len(got), got)
	}
	if got[0].ID != ios.ID.String() || got[0].Prefs["done"] != false || len(got[0].Prefs) != 1 {
		t.Fatalf("第一台 = %+v", got[0])
	}
	if got[1].ID != plain.ID.String() || got[1].Prefs != nil {
		t.Fatalf("缺省 prefs 应全开, got %+v", got[1])
	}
}

func TestParsePrefs(t *testing.T) {
	if ParsePrefs(nil) != nil || ParsePrefs([]byte("{}")) != nil || ParsePrefs([]byte("nope")) != nil {
		t.Fatal("空或非法 prefs 应视为全开")
	}
	got := ParsePrefs([]byte(`{"approval":false,"question":true}`))
	if got["approval"] || !got["question"] {
		t.Fatalf("prefs = %#v", got)
	}
}
