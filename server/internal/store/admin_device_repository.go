package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"sort"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrDevicePairingInvalid 表示配对码不可用: 不存在、已使用、已过期三种情况
// 对外不区分, 调用方统一按无效配对码处理。
var ErrDevicePairingInvalid = errors.New("device pairing code is invalid")

// AdminDevice 是一台已配对设备。服务端只保存令牌哈希, 明文令牌只在配对
// 响应里出现一次。
type AdminDevice struct {
	ID            uuid.UUID `gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	AdminID       uuid.UUID
	Name          string
	Platform      string
	TokenHash     string
	LastSeenAt    sql.NullTime
	LastIP        IPAddress `gorm:"column:last_ip"`
	LastUserAgent string
	RevokedAt     sql.NullTime
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// AdminDevicePairing 是一次性配对码。与设备令牌一样只存哈希。
type AdminDevicePairing struct {
	ID        uuid.UUID `gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	AdminID   uuid.UUID
	CodeHash  string
	ExpiresAt time.Time
	UsedAt    sql.NullTime
	CreatedAt time.Time
}

type CreateAdminDevicePairingParams struct {
	AdminID   uuid.UUID
	CodeHash  string
	ExpiresAt time.Time
}

// RedeemAdminDevicePairingParams 描述一次兑换: 配对码里已经带着管理员,
// 所以这里不再传 admin_id, 避免调用方伪造归属。
type RedeemAdminDevicePairingParams struct {
	CodeHash  string
	Name      string
	Platform  string
	TokenHash string
	IPAddress net.IP
	UserAgent string
	Now       time.Time
}

type AdminDeviceRepository struct {
	db *gorm.DB
}

type AdminDevicePairingRepository struct {
	db *gorm.DB
}

func NewAdminDeviceRepository(db *gorm.DB) AdminDeviceRepository {
	return AdminDeviceRepository{db: db}
}

func NewAdminDevicePairingRepository(db *gorm.DB) AdminDevicePairingRepository {
	return AdminDevicePairingRepository{db: db}
}

func (r AdminDevicePairingRepository) Create(ctx context.Context, params CreateAdminDevicePairingParams) (AdminDevicePairing, error) {
	pairing := AdminDevicePairing{
		AdminID:   params.AdminID,
		CodeHash:  params.CodeHash,
		ExpiresAt: params.ExpiresAt,
	}
	if err := r.db.WithContext(ctx).Create(&pairing).Error; err != nil {
		return AdminDevicePairing{}, fmt.Errorf("create admin device pairing: %w", err)
	}
	return pairing, nil
}

// RedeemPairing 在同一个事务里把配对码标记为已用并创建设备。并发兑换时只有
// 第一个事务能把 used_at 从 NULL 改掉, 其余事务拿到 0 行受影响并失败, 所以
// 一个配对码只会产出一台设备。
func (r AdminDeviceRepository) RedeemPairing(ctx context.Context, params RedeemAdminDevicePairingParams) (AdminDevice, error) {
	now := params.Now
	if now.IsZero() {
		now = time.Now()
	}

	var device AdminDevice
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var pairing AdminDevicePairing
		err := tx.Where(&AdminDevicePairing{CodeHash: params.CodeHash}).
			Where("used_at IS NULL AND expires_at > ?", now).
			First(&pairing).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrDevicePairingInvalid
			}
			return fmt.Errorf("load admin device pairing: %w", err)
		}

		marked := tx.Model(&AdminDevicePairing{}).
			Where(&AdminDevicePairing{ID: pairing.ID}).
			Where("used_at IS NULL").
			Update("used_at", now)
		if marked.Error != nil {
			return fmt.Errorf("mark admin device pairing used: %w", marked.Error)
		}
		if marked.RowsAffected == 0 {
			return ErrDevicePairingInvalid
		}

		device = AdminDevice{
			AdminID:       pairing.AdminID,
			Name:          params.Name,
			Platform:      params.Platform,
			TokenHash:     params.TokenHash,
			LastIP:        IPAddress(params.IPAddress),
			LastUserAgent: params.UserAgent,
		}
		if err := tx.Create(&device).Error; err != nil {
			return fmt.Errorf("create admin device: %w", err)
		}
		return nil
	})
	if err != nil {
		return AdminDevice{}, err
	}
	return device, nil
}

// GetActiveByTokenHash 只返回未吊销设备; 闲置失效依赖 last_seen_at/created_at,
// 由 auth 服务按配置判定。
func (r AdminDeviceRepository) GetActiveByTokenHash(ctx context.Context, tokenHash string) (AdminDevice, error) {
	var devices []AdminDevice
	if err := r.db.WithContext(ctx).Where(&AdminDevice{TokenHash: tokenHash}).Find(&devices).Error; err != nil {
		return AdminDevice{}, fmt.Errorf("get active admin device: %w", err)
	}
	for _, device := range devices {
		if !device.RevokedAt.Valid {
			return device, nil
		}
	}
	return AdminDevice{}, fmt.Errorf("get active admin device: %w", gorm.ErrRecordNotFound)
}

func (r AdminDeviceRepository) GetByID(ctx context.Context, deviceID uuid.UUID) (AdminDevice, error) {
	var device AdminDevice
	if err := r.db.WithContext(ctx).Where(&AdminDevice{ID: deviceID}).First(&device).Error; err != nil {
		return AdminDevice{}, fmt.Errorf("get admin device: %w", err)
	}
	return device, nil
}

func (r AdminDeviceRepository) ListByAdmin(ctx context.Context, adminID uuid.UUID) ([]AdminDevice, error) {
	var devices []AdminDevice
	if err := r.db.WithContext(ctx).Where(&AdminDevice{AdminID: adminID}).Find(&devices).Error; err != nil {
		return nil, fmt.Errorf("list admin devices: %w", err)
	}
	active := devices[:0]
	for _, device := range devices {
		if device.RevokedAt.Valid {
			continue
		}
		active = append(active, device)
	}
	sort.SliceStable(active, func(i, j int) bool {
		return active[i].CreatedAt.After(active[j].CreatedAt)
	})
	return active, nil
}

// TouchLastSeen 节流写入最近使用信息: 同一设备在 throttle 窗口内只落库一次,
// 避免每个请求都写一行。
func (r AdminDeviceRepository) TouchLastSeen(ctx context.Context, deviceID uuid.UUID, ip net.IP, userAgent string, now time.Time, throttle time.Duration) error {
	where := clause.Where{Exprs: []clause.Expression{
		clause.Eq{Column: clause.Column{Name: "id"}, Value: deviceID},
		clause.Or(
			clause.Eq{Column: clause.Column{Name: "last_seen_at"}, Value: nil},
			clause.Lte{Column: clause.Column{Name: "last_seen_at"}, Value: now.Add(-throttle)},
		),
	}}
	updates := map[string]any{
		"last_seen_at":    now,
		"last_ip":         IPAddress(ip),
		"last_user_agent": userAgent,
	}
	if err := r.db.WithContext(ctx).Model(&AdminDevice{}).Clauses(where).Updates(updates).Error; err != nil {
		return fmt.Errorf("touch admin device: %w", err)
	}
	return nil
}

// Revoke 吊销管理员名下的某台设备; 不属于该管理员或已吊销时返回
// gorm.ErrRecordNotFound, 由上层转成 404。
func (r AdminDeviceRepository) Revoke(ctx context.Context, adminID uuid.UUID, deviceID uuid.UUID, now time.Time) error {
	result := r.db.WithContext(ctx).Model(&AdminDevice{}).
		Where(&AdminDevice{ID: deviceID, AdminID: adminID}).
		Where("revoked_at IS NULL").
		Update("revoked_at", now)
	if result.Error != nil {
		return fmt.Errorf("revoke admin device: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("revoke admin device: %w", gorm.ErrRecordNotFound)
	}
	return nil
}

func (r AdminDeviceRepository) RevokeByID(ctx context.Context, deviceID uuid.UUID, now time.Time) error {
	result := r.db.WithContext(ctx).Model(&AdminDevice{}).
		Where(&AdminDevice{ID: deviceID}).
		Where("revoked_at IS NULL").
		Update("revoked_at", now)
	if result.Error != nil {
		return fmt.Errorf("revoke admin device: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("revoke admin device: %w", gorm.ErrRecordNotFound)
	}
	return nil
}

func (r AdminDeviceRepository) RevokeAll(ctx context.Context, adminID uuid.UUID, now time.Time) (int64, error) {
	result := r.db.WithContext(ctx).Model(&AdminDevice{}).
		Where(&AdminDevice{AdminID: adminID}).
		Where("revoked_at IS NULL").
		Update("revoked_at", now)
	if result.Error != nil {
		return 0, fmt.Errorf("revoke all admin devices: %w", result.Error)
	}
	return result.RowsAffected, nil
}
