package hubclient

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"time"

	"xlyra/server/internal/crypto"
	"xlyra/server/internal/store"
)

var errRevoked = errors.New("instance revoked")
var errLeaseLost = errors.New("hub send lease lost")

// Run drives pairing polls and heartbeats until ctx is cancelled.
// A panic is logged and the loop continues. Only the lease holder sends.
func (c *Client) Run(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	for ctx.Err() == nil {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					if c.logger != nil {
						c.logger.Error("hub binding loop panic", "error", panicError(rec))
					}
				}
			}()
			if err := c.runOnce(ctx); err != nil && ctx.Err() == nil && !errors.Is(err, errLeaseLost) {
				c.warn("hub binding loop", "error", err.Error())
			}
		}()
		if ctx.Err() != nil {
			return
		}
		if err := c.sleep(ctx, c.leaseRetry); err != nil {
			return
		}
	}
}

func (c *Client) runOnce(ctx context.Context) error {
	ok, err := c.repo.TryAcquireSendLease(ctx, c.owner, c.leaseTTL)
	if err != nil {
		return err
	}
	if !ok {
		return errLeaseLost
	}
	defer func() {
		_ = c.repo.ReleaseSendLease(context.WithoutCancel(ctx), c.owner)
	}()
	return c.drive(ctx)
}

func (c *Client) holdLease(ctx context.Context) bool {
	ok, err := c.repo.RefreshSendLease(ctx, c.owner, c.leaseTTL)
	if err != nil {
		c.warn("hub binding lease refresh failed", "error", err.Error())
		return false
	}
	return ok
}

func (c *Client) drive(ctx context.Context) error {
	var nextHeartbeat time.Time
	armed := false
	backoff := time.Duration(0)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !c.holdLease(ctx) {
			return errLeaseLost
		}
		row, err := c.repo.Get(ctx)
		if err != nil {
			return err
		}
		wait := c.leaseRefresh
		justBound := false
		if row.Status == store.HubStatusPairing {
			armed = false
			if row.PairingExpiresAt != nil && !c.now().Before(row.PairingExpiresAt.UTC()) {
				c.finishPairing(ctx, "expired", "", "", "")
			} else {
				c.poll(ctx, row)
				row, err = c.repo.Get(ctx)
				if err != nil {
					return err
				}
				justBound = row.Status == store.HubStatusBound
			}
		}
		switch row.Status {
		case store.HubStatusPairing:
			wait = c.pollWait()
		case store.HubStatusBound:
			if !armed {
				armed = true
				backoff = 0
				delay := c.initialDelay()
				if justBound {
					delay = 0
				}
				nextHeartbeat = c.now().Add(delay)
			}
			if !c.now().Before(nextHeartbeat) {
				err := c.heartbeat(ctx, row)
				if errors.Is(err, errRevoked) {
					armed = false
					wait = c.leaseRefresh
					break
				}
				if err != nil {
					backoff = nextBackoff(backoff, c.backoffMin, c.backoffMax)
					nextHeartbeat = c.now().Add(backoff)
				} else {
					backoff = 0
					nextHeartbeat = c.now().Add(c.heartbeatEvery)
				}
			}
			if remain := nextHeartbeat.Sub(c.now()); remain < wait {
				wait = remain
			}
		default:
			armed = false
		}
		if wait > c.leaseRefresh {
			wait = c.leaseRefresh
		}
		if err := c.sleep(ctx, wait); err != nil {
			return err
		}
	}
}

func (c *Client) initialDelay() time.Duration {
	if c.hasFirstDelay {
		if c.firstDelay < 0 {
			return 0
		}
		return c.firstDelay
	}
	span := int64(30 * time.Second)
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 45 * time.Second
	}
	n := int64(buf[0]) | int64(buf[1])<<8 | int64(buf[2])<<16 | int64(buf[3])<<24
	return 30*time.Second + time.Duration(n%span)
}

func (c *Client) pollWait() time.Duration {
	if c.pollEvery > 0 {
		return c.pollEvery
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending != nil && c.pending.interval > 0 {
		return c.pending.interval
	}
	return 3 * time.Second
}

func (c *Client) poll(ctx context.Context, row store.HubBinding) {
	if row.PollTokenEnc == "" {
		return
	}
	token, err := crypto.DecryptSecret(c.masterKey, row.PollTokenEnc)
	if err != nil {
		c.noteError(ctx, "配对状态无法读取")
		return
	}
	hub := row.HubURL
	if hub == "" {
		hub, err = c.hubURL(ctx)
		if err != nil {
			c.noteError(ctx, err.Error())
			return
		}
	}
	body, err := json.Marshal(map[string]string{"pollToken": token})
	if err != nil {
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultHTTPTimeout)
	defer cancel()
	status, raw, err := c.post(callCtx, hub, "/api/instance/pair/poll", body, nil)
	if err != nil {
		c.noteError(ctx, err.Error())
		c.warn("hub pairing poll failed")
		return
	}
	if status < 200 || status >= 300 {
		hubErr, ok := hubError(status, raw).(*HubError)
		if ok && (hubErr.Code == "pairing_not_found" || status == 404) {
			c.finishPairing(ctx, "expired", "", "", "")
			return
		}
		message := "配对轮询失败"
		if ok && hubErr.Code != "" {
			message = "配对轮询失败: " + hubErr.Code
		}
		c.noteError(ctx, message)
		c.warn("hub pairing poll rejected", "code", codeOf(hubErr))
		return
	}
	var resp struct {
		Status          string `json:"status"`
		InstanceID      string `json:"instanceId"`
		InstanceName    string `json:"instanceName"`
		UserDisplayName string `json:"userDisplayName"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		c.noteError(ctx, "Hub 返回的轮询响应无效")
		return
	}
	c.finishPairing(ctx, resp.Status, resp.InstanceID, resp.InstanceName, resp.UserDisplayName)
}

func (c *Client) finishPairing(ctx context.Context, pollStatus, instanceID, instanceName, userDisplayName string) {
	next, lastError, done := applyPollStatus(pollStatus)
	if !done {
		return
	}
	if next == store.HubStatusBound {
		if instanceID == "" {
			c.noteError(ctx, "Hub 未返回实例 id")
			return
		}
		if err := c.repo.MarkBound(ctx, instanceID, instanceName, userDisplayName, c.now()); err != nil {
			c.warn("hub binding was not saved", "error", err.Error())
			return
		}
		c.mu.Lock()
		c.pending = nil
		c.mu.Unlock()
		c.recordAudit(ctx, "hub_binding.bound", map[string]any{"instanceId": instanceID})
		return
	}
	c.mu.Lock()
	c.pending = nil
	c.mu.Unlock()
	if err := c.repo.ClearPairing(ctx, lastError); err != nil {
		c.warn("hub pairing was not cleared", "error", err.Error())
	}
}

func (c *Client) heartbeat(ctx context.Context, row store.HubBinding) error {
	priv, err := c.privateKey(row)
	if err != nil {
		c.noteError(ctx, "实例私钥无法读取")
		c.warn("hub heartbeat skipped")
		return err
	}
	hub := row.HubURL
	if hub == "" {
		hub, err = c.hubURL(ctx)
		if err != nil {
			c.noteError(ctx, err.Error())
			return err
		}
	}
	report, err := c.Report(ctx)
	if err != nil {
		c.noteError(ctx, "无法构造上报内容")
		return err
	}
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultHTTPTimeout)
	defer cancel()
	status, raw, err := c.post(callCtx, hub, "/api/instance/heartbeat", body, &signedRequest{
		instanceID: row.InstanceID,
		privateKey: priv,
	})
	if err != nil {
		c.noteError(ctx, err.Error())
		c.warn("hub heartbeat failed")
		return err
	}
	if status < 200 || status >= 300 {
		hubErr, _ := hubError(status, raw).(*HubError)
		if hubErr != nil && hubErr.Code == "instance_revoked" {
			_ = c.repo.ClearBinding(ctx, store.HubStatusRevoked, revokedMessage)
			c.clearDeveloperCache()
			c.warn("hub binding revoked")
			return errRevoked
		}
		if hubErr != nil && hubErr.Code == "clock_skew" {
			message := clockSkewMessage
			if hubErr.ServerTime != "" {
				message = clockSkewMessage + "（服务器时间 " + hubErr.ServerTime + "）"
			}
			c.noteError(ctx, message)
			c.warn("hub heartbeat clock skew")
			return errors.New(message)
		}
		message := "心跳失败"
		if hubErr != nil && hubErr.Code != "" {
			message = "心跳失败: " + hubErr.Code
		}
		c.noteError(ctx, message)
		c.warn("hub heartbeat rejected", "code", codeOf(hubErr))
		return errors.New(message)
	}
	var resp struct {
		InstanceName    string `json:"instanceName"`
		UserDisplayName string `json:"userDisplayName"`
	}
	_ = json.Unmarshal(raw, &resp)
	if err := c.repo.RecordSync(ctx, resp.InstanceName, resp.UserDisplayName, c.now()); err != nil {
		c.warn("hub heartbeat sync was not saved", "error", err.Error())
		return err
	}
	return nil
}

func codeOf(err *HubError) string {
	if err == nil {
		return ""
	}
	return err.Code
}
