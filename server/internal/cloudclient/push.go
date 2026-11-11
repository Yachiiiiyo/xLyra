package cloudclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"xlyra/server/internal/crypto"
	"xlyra/server/internal/store"
)

// PushResult is what a caller can act on. The Hub's result strings stay on this side.
type PushResult int

const (
	// PushOK means the Hub accepted the notification.
	PushOK PushResult = iota
	// PushUnregistered means the device token is no longer usable.
	PushUnregistered
	// PushRetryLater means a transient failure; the caller should back off.
	PushRetryLater
	// PushRejected means the Hub refused the payload.
	PushRejected
	// PushNotReady means this instance cannot send (unbound, or the Hub has no APNs).
	PushNotReady
)

// PushRequest is one notification. Title and body are never logged.
type PushRequest struct {
	DeviceID   string
	Kind       string
	Title      string
	Body       string
	SessionID  string
	RefID      string
	HighRisk   bool
	Badge      *int
	CollapseID string
	Priority   int
}

// RetryAfterError carries the Hub's Retry-After, in seconds, for a retryable send.
type RetryAfterError struct {
	Seconds int
}

func (e *RetryAfterError) Error() string {
	if e == nil || e.Seconds <= 0 {
		return "push retry later"
	}
	return "push retry after " + strconv.Itoa(e.Seconds) + "s"
}

// SendPush delivers one notification through the Hub. Any replica may call it;
// the send lease is only for heartbeats. An unbound instance returns PushNotReady
// without an error so the caller can drop the notification quietly.
func (c *Client) SendPush(ctx context.Context, req PushRequest) (PushResult, error) {
	row, err := c.boundRow(ctx)
	if errors.Is(err, ErrNotBound) {
		return PushNotReady, nil
	}
	if err != nil {
		return PushNotReady, err
	}
	deviceID, err := uuid.Parse(strings.TrimSpace(req.DeviceID))
	if err != nil {
		return PushRejected, errors.New("设备 id 无效")
	}
	body, err := json.Marshal(pushSendBody(req))
	if err != nil {
		return PushRejected, err
	}
	status, raw, header, err := c.signedExchangeHeaders(ctx, row, http.MethodPost, "/api/instance/push/send", "", "application/json", body)
	if err != nil {
		c.notePushState(ctx, deviceID, "error")
		c.warn("xLyra Cloud push send failed", "device", deviceID.String())
		return PushRetryLater, err
	}
	return c.mapPushResponse(ctx, deviceID, status, raw, header)
}

func pushSendBody(req PushRequest) map[string]any {
	body := map[string]any{
		"device_ref": req.DeviceID,
		"kind":       req.Kind,
		"title":      req.Title,
		"body":       req.Body,
		"session_id": req.SessionID,
		"ref_id":     req.RefID,
		"high_risk":  req.HighRisk,
	}
	if req.Badge != nil {
		body["badge"] = *req.Badge
	}
	if req.CollapseID != "" {
		body["collapse_id"] = req.CollapseID
	}
	if req.Priority != 0 {
		body["priority"] = req.Priority
	}
	return body
}

func (c *Client) mapPushResponse(ctx context.Context, deviceID uuid.UUID, status int, raw []byte, header http.Header) (PushResult, error) {
	if status >= 200 && status < 300 {
		result := pushResultField(raw)
		switch result {
		case "unregistered":
			c.notePushState(ctx, deviceID, "invalid")
			return PushUnregistered, nil
		default:
			c.notePushState(ctx, deviceID, "ok")
			return PushOK, nil
		}
	}
	cloudErr, _ := cloudError(status, raw).(*CloudError)
	code := codeOf(cloudErr)
	// Hub 的发送结果只带 result，没有 error.code；cloudError 会把空 code 兜底成 invalid_request。
	if code == "" || code == "invalid_request" {
		if result := pushResultField(raw); result != "" {
			code = result
		}
	}
	if code == "instance_revoked" {
		c.handleRevoked(ctx)
		return PushNotReady, errRevoked
	}
	switch {
	case status == http.StatusUnprocessableEntity:
		return PushRejected, nil
	case status == http.StatusNotFound && code == "device_not_found":
		c.markOneUnsynced(ctx, deviceID)
		c.WakePushSync()
		return PushRetryLater, nil
	case status == http.StatusConflict && code == "device_not_registered":
		c.markOneUnsynced(ctx, deviceID)
		c.WakePushSync()
		return PushRetryLater, nil
	case status == http.StatusConflict && code == "device_invalid":
		return PushUnregistered, nil
	case status == http.StatusServiceUnavailable && code == "push_unavailable":
		return PushNotReady, nil
	case status == http.StatusServiceUnavailable && code == "rate_limited":
		return PushRetryLater, retryAfterError(header)
	case status == http.StatusTooManyRequests:
		return PushRetryLater, retryAfterError(header)
	case status == http.StatusBadGateway && code == "error":
		c.notePushState(ctx, deviceID, "error")
		return PushRetryLater, nil
	default:
		c.warn("xLyra Cloud push send rejected", "device", deviceID.String(), "status", status, "code", code)
		return PushRetryLater, nil
	}
}

func pushResultField(raw []byte) string {
	var body struct {
		OK     bool   `json:"ok"`
		Result string `json:"result"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return ""
	}
	if body.Result != "" {
		return body.Result
	}
	if body.OK {
		return "ok"
	}
	return ""
}

func retryAfterError(header http.Header) error {
	if header == nil {
		return nil
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(header.Get("Retry-After")))
	if err != nil || seconds <= 0 {
		return nil
	}
	return &RetryAfterError{Seconds: seconds}
}

// syncPush registers or deletes device tokens after a successful heartbeat.
// A failure on one device never fails the heartbeat loop. instance_revoked is
// the only error returned to the caller.
func (c *Client) syncPush(ctx context.Context, row store.CloudBinding) error {
	if c.devices == nil {
		return nil
	}
	devices, err := c.devices.ListUnsyncedPush(ctx)
	if err != nil {
		c.warn("xLyra Cloud push sync list failed", "error", err.Error())
		return nil
	}
	for _, device := range devices {
		if ctx.Err() != nil {
			return nil
		}
		if err := c.syncOneDevice(ctx, row, device); errors.Is(err, errRevoked) {
			return err
		}
	}
	return nil
}

func (c *Client) syncOneDevice(ctx context.Context, row store.CloudBinding, device store.AdminDevice) error {
	ref := device.ID.String()
	path := "/api/instance/push/devices/" + ref
	if strings.TrimSpace(device.APNSTokenEnc) == "" {
		status, raw, err := c.signedExchange(ctx, row, http.MethodDelete, path, "", "", nil)
		if err != nil {
			c.warn("xLyra Cloud push delete failed", "device", ref)
			return nil
		}
		return c.finishSync(ctx, device.ID, status, raw, false)
	}
	token, err := crypto.DecryptSecret(c.masterKey, device.APNSTokenEnc)
	if err != nil {
		c.notePushState(ctx, device.ID, "error")
		c.warn("xLyra Cloud push token unreadable", "device", ref)
		return nil
	}
	body, err := json.Marshal(map[string]string{
		"apns_token": token,
		"env":        device.APNSEnv,
	})
	if err != nil {
		return nil
	}
	status, raw, err := c.signedExchange(ctx, row, http.MethodPut, path, "", "application/json", body)
	if err != nil {
		c.warn("xLyra Cloud push register failed", "device", ref)
		return nil
	}
	return c.finishSync(ctx, device.ID, status, raw, true)
}

func (c *Client) finishSync(ctx context.Context, deviceID uuid.UUID, status int, raw []byte, register bool) error {
	if status >= 200 && status < 300 {
		if err := c.devices.MarkPushSynced(ctx, deviceID, c.now()); err != nil {
			c.warn("xLyra Cloud push sync was not saved", "device", deviceID.String(), "error", err.Error())
		}
		return nil
	}
	cloudErr, _ := cloudError(status, raw).(*CloudError)
	code := codeOf(cloudErr)
	if code == "instance_revoked" {
		c.handleRevoked(ctx)
		return errRevoked
	}
	if register && status == http.StatusConflict && code == "device_limit" {
		c.notePushState(ctx, deviceID, "error")
		return nil
	}
	c.warn("xLyra Cloud push sync rejected", "device", deviceID.String(), "status", status, "code", code)
	return nil
}

func (c *Client) handleRevoked(ctx context.Context) {
	_ = c.repo.ClearBinding(ctx, store.CloudStatusRevoked, revokedMessage)
	c.clearDeveloperCache()
	c.markDevicesUnsynced(ctx)
	c.warn("xLyra Cloud binding revoked")
}

func (c *Client) markDevicesUnsynced(ctx context.Context) {
	if c.devices == nil {
		return
	}
	if err := c.devices.MarkAllPushUnsynced(ctx); err != nil {
		c.warn("xLyra Cloud push devices were not marked unsynced", "error", err.Error())
	}
}

func (c *Client) markOneUnsynced(ctx context.Context, deviceID uuid.UUID) {
	if c.devices == nil {
		return
	}
	if err := c.devices.MarkPushUnsynced(ctx, deviceID); err != nil {
		c.warn("xLyra Cloud push device was not marked unsynced", "device", deviceID.String(), "error", err.Error())
	}
}

func (c *Client) notePushState(ctx context.Context, deviceID uuid.UUID, state string) {
	if c.devices == nil {
		return
	}
	if err := c.devices.SetPushState(ctx, deviceID, state, c.now()); err != nil {
		c.warn("xLyra Cloud push state was not saved", "device", deviceID.String(), "error", err.Error())
	}
}
