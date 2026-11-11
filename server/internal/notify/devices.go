package notify

import (
	"context"
	"strings"

	"xlyra/server/internal/store"
)

// DeviceLister 是列出已登记推送设备所需的只读查询。
// store.AdminDeviceRepository 满足该接口。
type DeviceLister interface {
	ListActiveWithPushToken(ctx context.Context) ([]store.AdminDevice, error)
}

// StoreDevices 把设备仓库适配成 Devices。
// 只保留活动的 ios、有 token、push_state 不是 invalid 的设备。
type StoreDevices struct {
	Repo DeviceLister
}

func (s StoreDevices) ListPushDevices(ctx context.Context) ([]Device, error) {
	if s.Repo == nil {
		return nil, nil
	}
	rows, err := s.Repo.ListActiveWithPushToken(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Device, 0, len(rows))
	for _, row := range rows {
		if !row.RevokedAt.Valid && strings.EqualFold(row.Platform, "ios") &&
			strings.TrimSpace(row.APNSTokenEnc) != "" && row.PushState != "invalid" {
			out = append(out, Device{ID: row.ID.String(), Prefs: ParsePrefs(row.PushPrefs)})
		}
	}
	return out, nil
}
