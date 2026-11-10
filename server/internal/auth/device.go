package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"xlyra/server/internal/httpx"
	"xlyra/server/internal/store"
)

const (
	deviceTokenPrefix       = "xld_"
	deviceTokenBytes        = 32
	devicePairingPrefix     = "xlp_"
	devicePairingTokenBytes = 16
	devicePairingLifetime   = 5 * time.Minute
	deviceNameMaxLength     = 64
	devicePlatformIOS       = "ios"
	deviceLastSeenThrottle  = time.Minute
	defaultDeviceIdleDays   = 180
	hoursPerDeviceIdleDay   = 24
	// DeviceActorType 是设备令牌请求在上下文/审计里的身份类型。
	DeviceActorType = "device"
)

var (
	// ErrInvalidPairingCode 覆盖不存在、已使用、已过期三种情况, 对外不区分。
	ErrInvalidPairingCode = errors.New("pairing code is invalid or expired")
	ErrDeviceNameRequired = errors.New("device name is required")
	ErrDeviceNameTooLong  = errors.New("device name must be at most 64 characters")
	ErrDevicePlatform     = errors.New("device platform is not supported")
	ErrDeviceTokenInvalid = errors.New("device token is invalid")
)

type DevicePairingResult struct {
	PairingID uuid.UUID
	Code      string
	ExpiresAt time.Time
}

type DevicePairResult struct {
	Token  string
	Device store.AdminDevice
}

// SetDeviceIdleDays 把启动配置里的 DEVICE_IDLE_DAYS 接到 auth 服务上。
// 0 表示不因闲置失效; 负数在配置层已被拒绝, 这里再兜底回默认值。
func (s *Service) SetDeviceIdleDays(days int) {
	if s == nil {
		return
	}
	s.deviceIdleDays = days
	s.deviceIdleConfigured = true
}

// configuredDeviceIdleLifetime 返回设备闲置失效时长; second 返回值为 true 表示
// 不因闲置失效（DEVICE_IDLE_DAYS=0）, 与 configuredSessionLifetime 的约定一致。
func (s *Service) configuredDeviceIdleLifetime() (time.Duration, bool) {
	days := defaultDeviceIdleDays
	if s != nil && s.deviceIdleConfigured {
		days = s.deviceIdleDays
	}
	if days == 0 {
		return 0, true
	}
	if days < 0 {
		days = defaultDeviceIdleDays
	}
	return time.Duration(days) * hoursPerDeviceIdleDay * time.Hour, false
}

// CreateDevicePairing 生成一次性配对码（128 位, 5 分钟有效, 只存哈希）。
func (s *Service) CreateDevicePairing(ctx context.Context, adminID uuid.UUID) (DevicePairingResult, error) {
	if adminID == uuid.Nil {
		return DevicePairingResult{}, fmt.Errorf("admin id is required")
	}
	code, err := newToken(devicePairingPrefix, devicePairingTokenBytes)
	if err != nil {
		return DevicePairingResult{}, err
	}
	expiresAt := time.Now().Add(devicePairingLifetime)
	pairing, err := s.pairings.Create(ctx, store.CreateAdminDevicePairingParams{
		AdminID:   adminID,
		CodeHash:  hashToken(code),
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return DevicePairingResult{}, err
	}
	return DevicePairingResult{
		PairingID: pairing.ID,
		Code:      code,
		ExpiresAt: pairing.ExpiresAt,
	}, nil
}

// RedeemDevicePairing 兑换配对码并创建设备令牌。失败按 IP 限速: 超限返回
// *LockoutError, 配对码本身的问题统一返回 ErrInvalidPairingCode。
func (s *Service) RedeemDevicePairing(ctx context.Context, code string, name string, platform string, userAgent string, remoteAddr string) (DevicePairResult, error) {
	attemptKey := pairingAttemptKey(remoteAddr)
	if retry, locked := s.pairingGuard.retryAfter(attemptKey); locked {
		return DevicePairResult{}, &LockoutError{RetryAfter: retry}
	}

	name = strings.TrimSpace(name)
	if name == "" {
		return DevicePairResult{}, ErrDeviceNameRequired
	}
	if utf8.RuneCountInString(name) > deviceNameMaxLength {
		return DevicePairResult{}, ErrDeviceNameTooLong
	}
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform != devicePlatformIOS {
		return DevicePairResult{}, ErrDevicePlatform
	}

	code = strings.TrimSpace(code)
	if code == "" {
		return DevicePairResult{}, s.failedPairing(attemptKey, ErrInvalidPairingCode)
	}

	token, err := newToken(deviceTokenPrefix, deviceTokenBytes)
	if err != nil {
		return DevicePairResult{}, err
	}
	device, err := s.devices.RedeemPairing(ctx, store.RedeemAdminDevicePairingParams{
		CodeHash:  hashToken(code),
		Name:      name,
		Platform:  platform,
		TokenHash: hashToken(token),
		IPAddress: parseIP(remoteAddr),
		UserAgent: userAgent,
		Now:       time.Now(),
	})
	if err != nil {
		if errors.Is(err, store.ErrDevicePairingInvalid) {
			return DevicePairResult{}, s.failedPairing(attemptKey, ErrInvalidPairingCode)
		}
		return DevicePairResult{}, err
	}

	s.pairingGuard.reset(attemptKey)
	return DevicePairResult{Token: token, Device: device}, nil
}

// failedPairing 记录一次配对失败; 超过阈值时把错误换成 LockoutError, 让调用方
// 回 429 而不是继续暴露无效配对码。
func (s *Service) failedPairing(attemptKey string, cause error) error {
	if retry, locked := s.pairingGuard.recordFailure(attemptKey); locked {
		return &LockoutError{RetryAfter: retry}
	}
	return cause
}

// ValidateDeviceToken 校验设备令牌: 必须未吊销且未闲置超期。通过后会节流写入
// 最近使用信息（同一设备每分钟最多一次）。
func (s *Service) ValidateDeviceToken(ctx context.Context, token string, userAgent string, remoteAddr string) (store.AdminDevice, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return store.AdminDevice{}, ErrDeviceTokenInvalid
	}
	device, err := s.devices.GetActiveByTokenHash(ctx, hashToken(token))
	if err != nil {
		return store.AdminDevice{}, ErrDeviceTokenInvalid
	}
	if s.deviceIdleExpired(device, time.Now()) {
		return store.AdminDevice{}, ErrDeviceTokenInvalid
	}
	_ = s.devices.TouchLastSeen(ctx, device.ID, parseIP(remoteAddr), userAgent, time.Now(), deviceLastSeenThrottle)
	return device, nil
}

// deviceIdleExpired 判断设备是否已经闲置超期; 最后一次使用时间以 last_seen_at
// 优先, 没有则退回 created_at。
func (s *Service) deviceIdleExpired(device store.AdminDevice, now time.Time) bool {
	lifetime, unlimited := s.configuredDeviceIdleLifetime()
	if unlimited {
		return false
	}
	lastUsed := device.CreatedAt
	if device.LastSeenAt.Valid {
		lastUsed = device.LastSeenAt.Time
	}
	if lastUsed.IsZero() {
		return false
	}
	return now.Sub(lastUsed) > lifetime
}

func (s *Service) ListDevices(ctx context.Context, adminID uuid.UUID) ([]store.AdminDevice, error) {
	if adminID == uuid.Nil {
		return nil, fmt.Errorf("admin id is required")
	}
	return s.devices.ListByAdmin(ctx, adminID)
}

func (s *Service) GetDevice(ctx context.Context, deviceID uuid.UUID) (store.AdminDevice, error) {
	if deviceID == uuid.Nil {
		return store.AdminDevice{}, fmt.Errorf("device id is required")
	}
	device, err := s.devices.GetByID(ctx, deviceID)
	if err != nil {
		return store.AdminDevice{}, err
	}
	if device.RevokedAt.Valid {
		return store.AdminDevice{}, fmt.Errorf("get admin device: %w", gorm.ErrRecordNotFound)
	}
	return device, nil
}

// RevokeDevice 只能吊销自己名下的设备; 他人或不存在的设备都返回
// gorm.ErrRecordNotFound, 由 handler 统一回 404。
func (s *Service) RevokeDevice(ctx context.Context, adminID uuid.UUID, deviceID uuid.UUID) error {
	if adminID == uuid.Nil || deviceID == uuid.Nil {
		return fmt.Errorf("revoke admin device: %w", store.ErrDevicePairingInvalid)
	}
	return s.devices.Revoke(ctx, adminID, deviceID, time.Now())
}

func (s *Service) RevokeAllDevices(ctx context.Context, adminID uuid.UUID) (int64, error) {
	if adminID == uuid.Nil {
		return 0, fmt.Errorf("revoke all admin devices: %w", store.ErrDevicePairingInvalid)
	}
	return s.devices.RevokeAll(ctx, adminID, time.Now())
}

// RevokeSelf 是设备自己退出登录: 按设备 id 吊销, 不动同管理员的其它设备。
func (s *Service) RevokeSelf(ctx context.Context, deviceID uuid.UUID) error {
	if deviceID == uuid.Nil {
		return fmt.Errorf("revoke admin device: %w", store.ErrDevicePairingInvalid)
	}
	return s.devices.RevokeByID(ctx, deviceID, time.Now())
}

// DeviceTokenFromRequest 只接受 Authorization: Bearer xld_…; 会话 Cookie 与
// Access Token 不会被当成设备令牌。
func DeviceTokenFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	token := bearerToken(r.Header.Get("Authorization"))
	if !strings.HasPrefix(token, deviceTokenPrefix) {
		return ""
	}
	return token
}

// DenyDeviceActor 拦住设备令牌不能调用的管理接口（设备管理、账号与密码、
// Access Token、TOTP）: 设备令牌与管理员同权, 但这些接口会自我扩权。
func DenyDeviceActor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if actor, ok := AdminActorFromContext(r.Context()); ok && actor.Type == DeviceActorType {
			httpx.Error(w, r, http.StatusForbidden, "forbidden_for_device", "device tokens are not allowed to access this resource")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// pairingAttemptKey 让配对失败计数只按 IP 聚合（配对请求没有用户名）。
func pairingAttemptKey(remoteAddr string) string {
	ip := strings.TrimSpace(remoteAddr)
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	return strings.ToLower(ip)
}
