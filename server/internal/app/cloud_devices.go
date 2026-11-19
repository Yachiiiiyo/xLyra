package app

import (
	"context"
	"time"

	"github.com/google/uuid"

	"xlyra/server/internal/store"
)

// devicePushStore 把设备仓库接到 cloudclient。仓库里按管理员的 MarkAllPushUnsynced
// 只清有 token 的行; 实例侧要连已清除 token 的设备一起标成未同步。
type devicePushStore struct {
	repo store.AdminDeviceRepository
}

func (s devicePushStore) ListActiveRecent(ctx context.Context, limit int) ([]store.AdminDevice, error) {
	return s.repo.ListActiveRecent(ctx, limit)
}

func (s devicePushStore) ListUnsyncedPush(ctx context.Context) ([]store.AdminDevice, error) {
	return s.repo.ListUnsyncedPush(ctx)
}

func (s devicePushStore) MarkPushSynced(ctx context.Context, id uuid.UUID, at time.Time) error {
	return s.repo.MarkPushSynced(ctx, id, at)
}

func (s devicePushStore) SetPushState(ctx context.Context, id uuid.UUID, state string, at time.Time) error {
	return s.repo.SetPushState(ctx, id, state, at)
}

func (s devicePushStore) MarkPushUnsynced(ctx context.Context, id uuid.UUID) error {
	return s.repo.MarkPushUnsynced(ctx, id)
}

func (s devicePushStore) MarkAllPushUnsynced(ctx context.Context) error {
	return s.repo.MarkEveryDevicePushUnsynced(ctx)
}
