package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"xlyra/server/internal/auth"
	"xlyra/server/internal/store"
)

// RedeemDevicePairing 是 App 扫码后的公开配对入口: 无需登录, 按 IP 限速,
// 明文令牌只在这个响应里出现一次, 因此禁止缓存。
func (h Handler) RedeemDevicePairing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.auth == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "auth_unavailable", "auth service is not available")
		return
	}

	var payload struct {
		Code       string `json:"code"`
		DeviceName string `json:"device_name"`
		Platform   string `json:"platform"`
	}
	if !h.decodeJSON(w, r, &payload) {
		return
	}

	result, err := h.auth.RedeemDevicePairing(r.Context(), payload.Code, payload.DeviceName, payload.Platform, r.UserAgent(), r.RemoteAddr)
	if err != nil {
		// 失败审计不落配对码, 避免明文进日志/审计。
		failedActor := auth.AdminActor{Type: "system"}
		var lockErr *auth.LockoutError
		switch {
		case errors.As(err, &lockErr):
			retryAfter := int64(lockErr.RetryAfter/time.Second) + 1
			w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
			h.writeError(w, r, http.StatusTooManyRequests, "too_many_attempts", "too many failed pairing attempts, please try again later")
			h.recordAudit(r, failedActor, "device.pair", "", "", false, "too_many_attempts", nil)
		case errors.Is(err, auth.ErrInvalidPairingCode):
			h.writeError(w, r, http.StatusBadRequest, "invalid_pairing_code", "pairing code is invalid or expired")
			h.recordAudit(r, failedActor, "device.pair", "", "", false, "invalid_pairing_code", nil)
		case errors.Is(err, auth.ErrDeviceNameRequired), errors.Is(err, auth.ErrDeviceNameTooLong):
			h.writeError(w, r, http.StatusBadRequest, "invalid_device_name", err.Error())
		case errors.Is(err, auth.ErrDevicePlatform):
			h.writeError(w, r, http.StatusBadRequest, "invalid_platform", err.Error())
		default:
			h.writeError(w, r, http.StatusInternalServerError, "device_pair_failed", "failed to pair the device")
		}
		return
	}

	admin, err := h.auth.GetAdminByID(r.Context(), result.Device.AdminID)
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, "device_pair_failed", "failed to load the paired admin")
		return
	}

	h.logInfo("admin device paired", "admin_id", admin.ID, "device_id", result.Device.ID)
	h.recordAudit(r, auth.AdminActor{Type: auth.DeviceActorType, AdminID: result.Device.AdminID, DeviceID: result.Device.ID}, "device.pair", "admin_device", result.Device.ID.String(), true, "", map[string]any{
		"device_name": result.Device.Name,
		"platform":    result.Device.Platform,
	})
	h.writePayload(w, http.StatusCreated, map[string]any{
		"data": map[string]any{
			"device_id": result.Device.ID.String(),
			"token":     result.Token,
			"admin":     adminPayload(admin),
		},
	})
}

// CreateDevicePairing 生成一次性配对码（Web 身份专用, 不再额外要 TOTP）。
func (h Handler) CreateDevicePairing(w http.ResponseWriter, r *http.Request) {
	actor := currentAdminActor(r)
	if h.auth == nil || actor.AdminID == uuid.Nil {
		h.writeError(w, r, http.StatusUnauthorized, "unauthorized", "admin session is required")
		return
	}

	result, err := h.auth.CreateDevicePairing(r.Context(), actor.AdminID)
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, "pairing_create_failed", "failed to create the pairing code")
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	h.recordAudit(r, actor, "device.pairing.create", "admin_device_pairing", result.PairingID.String(), true, "", map[string]any{
		"expires_at": timeString(result.ExpiresAt),
	})
	h.writePayload(w, http.StatusCreated, map[string]any{
		"data": map[string]any{
			"pairing_id": result.PairingID.String(),
			"code":       result.Code,
			"expires_at": timeString(result.ExpiresAt),
		},
	})
}

func (h Handler) ListDevices(w http.ResponseWriter, r *http.Request) {
	actor := currentAdminActor(r)
	if h.auth == nil || actor.AdminID == uuid.Nil {
		h.writeError(w, r, http.StatusUnauthorized, "unauthorized", "admin session is required")
		return
	}

	devices, err := h.auth.ListDevices(r.Context(), actor.AdminID)
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, "devices_list_failed", "failed to list devices")
		return
	}

	items := make([]map[string]any, 0, len(devices))
	for _, device := range devices {
		// 这个接口只允许 Web 身份访问, 所以 current 恒为 false。
		items = append(items, devicePayload(device, false))
	}
	h.writePayload(w, http.StatusOK, map[string]any{"data": items})
}

// RevokeDevice 吊销管理员名下的设备; 他人或不存在一律 404, 不泄露设备是否存在。
func (h Handler) RevokeDevice(w http.ResponseWriter, r *http.Request) {
	actor := currentAdminActor(r)
	if h.auth == nil || actor.AdminID == uuid.Nil {
		h.writeError(w, r, http.StatusUnauthorized, "unauthorized", "admin session is required")
		return
	}

	deviceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.writeError(w, r, http.StatusNotFound, "not_found", "device not found")
		return
	}
	if err := h.auth.RevokeDevice(r.Context(), actor.AdminID, deviceID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			h.writeError(w, r, http.StatusNotFound, "not_found", "device not found")
			return
		}
		h.writeError(w, r, http.StatusInternalServerError, "device_revoke_failed", "failed to revoke the device")
		return
	}

	h.recordAudit(r, actor, "device.revoke", "admin_device", deviceID.String(), true, "", nil)
	h.wakePushSync()
	w.WriteHeader(http.StatusNoContent)
}

// RevokeAllDevices 一次性吊销管理员的全部设备（手机丢失时的兜底操作）。
func (h Handler) RevokeAllDevices(w http.ResponseWriter, r *http.Request) {
	actor := currentAdminActor(r)
	if h.auth == nil || actor.AdminID == uuid.Nil {
		h.writeError(w, r, http.StatusUnauthorized, "unauthorized", "admin session is required")
		return
	}

	revoked, err := h.auth.RevokeAllDevices(r.Context(), actor.AdminID)
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, "device_revoke_failed", "failed to revoke devices")
		return
	}

	h.recordAudit(r, actor, "device.revoke_all", "admin_device", "", true, "", map[string]any{"revoked": revoked})
	h.wakePushSync()
	w.WriteHeader(http.StatusNoContent)
}

// GetDeviceSelf 只对设备令牌开放; 会话/Access Token 传 not_a_device。
func (h Handler) GetDeviceSelf(w http.ResponseWriter, r *http.Request) {
	device, ok := h.currentDevice(w, r)
	if !ok {
		return
	}
	h.writePayload(w, http.StatusOK, map[string]any{"data": devicePayload(device, true)})
}

// DeleteDeviceSelf 是 App 自助退出: 吊销当前设备令牌。
func (h Handler) DeleteDeviceSelf(w http.ResponseWriter, r *http.Request) {
	device, ok := h.currentDevice(w, r)
	if !ok {
		return
	}

	if err := h.auth.RevokeSelf(r.Context(), device.ID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			h.writeError(w, r, http.StatusNotFound, "not_found", "device not found")
			return
		}
		h.writeError(w, r, http.StatusInternalServerError, "device_revoke_failed", "failed to revoke the device")
		return
	}

	h.recordAudit(r, auth.AdminActor{Type: auth.DeviceActorType, AdminID: device.AdminID, DeviceID: device.ID}, "device.revoke_self", "admin_device", device.ID.String(), true, "", nil)
	h.wakePushSync()
	w.WriteHeader(http.StatusNoContent)
}

// PutDevicePush 登记（或更换）当前设备的 APNs token。明文 token 只用于加密, 不进
// 响应、审计和日志。
func (h Handler) PutDevicePush(w http.ResponseWriter, r *http.Request) {
	device, ok := h.currentDevice(w, r)
	if !ok {
		return
	}

	var payload struct {
		APNSToken   string `json:"apns_token"`
		Environment string `json:"environment"`
	}
	if !h.decodeJSON(w, r, &payload) {
		return
	}
	token := strings.TrimSpace(payload.APNSToken)
	if !validAPNSToken(token) {
		h.writeError(w, r, http.StatusBadRequest, "invalid_push_token", "apns token must be 1 to 200 hex characters")
		return
	}
	env := payload.Environment
	if env != "sandbox" && env != "production" {
		h.writeError(w, r, http.StatusBadRequest, "invalid_push_environment", "push environment must be sandbox or production")
		return
	}

	if err := h.auth.RegisterDevicePush(r.Context(), device.ID, token, env); err != nil {
		h.writeError(w, r, http.StatusInternalServerError, "device_push_failed", "failed to register the push token")
		return
	}

	h.recordAudit(r, deviceActor(device), "device.push_register", "admin_device", device.ID.String(), true, "", map[string]any{"environment": env})
	h.wakePushSync()
	w.WriteHeader(http.StatusNoContent)
}

// DeleteDevicePush 清除当前设备的推送登记, 没有登记过也返回 204。
func (h Handler) DeleteDevicePush(w http.ResponseWriter, r *http.Request) {
	device, ok := h.currentDevice(w, r)
	if !ok {
		return
	}

	if err := h.auth.ClearDevicePush(r.Context(), device.ID); err != nil {
		h.writeError(w, r, http.StatusInternalServerError, "device_push_failed", "failed to clear the push token")
		return
	}

	h.recordAudit(r, deviceActor(device), "device.push_unregister", "admin_device", device.ID.String(), true, "", nil)
	h.wakePushSync()
	w.WriteHeader(http.StatusNoContent)
}

// GetDevicePush 返回当前设备的推送状态。registered 只表示有没有 token, 不返回 token 本身。
func (h Handler) GetDevicePush(w http.ResponseWriter, r *http.Request) {
	device, ok := h.currentDevice(w, r)
	if !ok {
		return
	}

	bound, lastError := false, ""
	if h.HubStatus != nil {
		bound, lastError = h.HubStatus(r.Context())
	}

	h.writePayload(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"registered":    device.APNSTokenEnc != "",
			"environment":   device.APNSEnv,
			"state":         device.PushState,
			"registered_at": nullTimeValue(device.PushRegisteredAt),
			"prefs":         pushPrefsValue(device.PushPrefs),
			"hub": map[string]any{
				"bound":      bound,
				"last_error": lastError,
			},
		},
	})
}

// PutDevicePushPrefs 合并通知开关: 请求里缺省的键保持原值。
func (h Handler) PutDevicePushPrefs(w http.ResponseWriter, r *http.Request) {
	device, ok := h.currentDevice(w, r)
	if !ok {
		return
	}

	var raw map[string]json.RawMessage
	if !h.decodeJSON(w, r, &raw) {
		return
	}
	prefs := pushPrefsValue(device.PushPrefs)
	for _, key := range pushPrefKeys {
		value, present := raw[key]
		if !present {
			continue
		}
		var enabled bool
		if err := json.Unmarshal(value, &enabled); err != nil {
			h.writeError(w, r, http.StatusBadRequest, "invalid_push_prefs", "push preferences must be booleans")
			return
		}
		prefs[key] = enabled
	}

	encoded, err := json.Marshal(prefs)
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, "device_push_failed", "failed to save the push preferences")
		return
	}
	if err := h.auth.SetDevicePushPrefs(r.Context(), device.ID, store.JSON(encoded)); err != nil {
		h.writeError(w, r, http.StatusInternalServerError, "device_push_failed", "failed to save the push preferences")
		return
	}

	h.recordAudit(r, deviceActor(device), "device.push_prefs", "admin_device", device.ID.String(), true, "", map[string]any{"prefs": prefs})
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) wakePushSync() {
	if h.PushSyncWaker != nil {
		h.PushSyncWaker.WakePushSync()
	}
}

// currentDevice 取出设备令牌对应的设备; 非设备身份返回 400 not_a_device。
func (h Handler) currentDevice(w http.ResponseWriter, r *http.Request) (store.AdminDevice, bool) {
	actor, ok := auth.AdminActorFromContext(r.Context())
	if !ok || h.auth == nil || actor.Type != auth.DeviceActorType || actor.DeviceID == uuid.Nil {
		h.writeError(w, r, http.StatusBadRequest, "not_a_device", "this endpoint requires a device token")
		return store.AdminDevice{}, false
	}

	device, err := h.auth.GetDevice(r.Context(), actor.DeviceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			h.writeError(w, r, http.StatusNotFound, "not_found", "device not found")
			return store.AdminDevice{}, false
		}
		h.writeError(w, r, http.StatusInternalServerError, "device_load_failed", "failed to load the device")
		return store.AdminDevice{}, false
	}
	return device, true
}

func devicePayload(device store.AdminDevice, current bool) map[string]any {
	return map[string]any{
		"id":           device.ID.String(),
		"name":         device.Name,
		"platform":     device.Platform,
		"created_at":   timeString(device.CreatedAt),
		"last_seen_at": nullTimeValue(device.LastSeenAt),
		"last_ip":      deviceIPValue(device.LastIP),
		"current":      current,
		"push":         devicePushValue(device),
	}
}

func devicePushValue(device store.AdminDevice) map[string]any {
	return map[string]any{
		"registered":    device.APNSTokenEnc != "",
		"environment":   device.APNSEnv,
		"state":         device.PushState,
		"prefs":         pushPrefsValue(device.PushPrefs),
		"registered_at": nullTimeValue(device.PushRegisteredAt),
	}
}

var pushPrefKeys = []string{"approval", "question", "done", "error"}

// pushPrefsValue 把库里的开关解出来; 缺省的键按全开处理。
func pushPrefsValue(raw store.JSON) map[string]bool {
	prefs := map[string]bool{"approval": true, "question": true, "done": true, "error": true}
	if len(raw) == 0 {
		return prefs
	}
	var stored map[string]bool
	if err := json.Unmarshal(raw, &stored); err != nil {
		return prefs
	}
	for _, key := range pushPrefKeys {
		if value, ok := stored[key]; ok {
			prefs[key] = value
		}
	}
	return prefs
}

func deviceActor(device store.AdminDevice) auth.AdminActor {
	return auth.AdminActor{Type: auth.DeviceActorType, AdminID: device.AdminID, DeviceID: device.ID}
}

// validAPNSToken 要求 1 到 200 位的十六进制字符串。
func validAPNSToken(token string) bool {
	length := utf8.RuneCountInString(token)
	if length < 1 || length > 200 {
		return false
	}
	for _, r := range token {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func deviceIPValue(ip store.IPAddress) any {
	parsed := ip.NetIP()
	if parsed == nil {
		return nil
	}
	return parsed.String()
}
