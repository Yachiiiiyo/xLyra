package cloudclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"xlyra/server/internal/crypto"
	"xlyra/server/internal/httpclient"
	"xlyra/server/internal/store"
	"xlyra/server/internal/version"
)

const (
	MaxPollSeconds     = 600
	revokedMessage     = "已在账号侧解除连接"
	clockSkewMessage   = "系统时间偏差过大"
	defaultHTTPTimeout = 5 * time.Second
)

var ErrAlreadyBound = errors.New("实例已连接")

// CloudError is a structured error returned by the Cloud.
type CloudError struct {
	Status     int
	Code       string
	Message    string
	ServerTime string
}

func (e *CloudError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return "xLyra Cloud 请求失败"
}

type Options struct {
	AllowInsecureLocal     bool
	HeartbeatInterval      time.Duration
	FirstHeartbeatDelay    time.Duration
	HasFirstHeartbeatDelay bool
	BackoffMin             time.Duration
	BackoffMax             time.Duration
	PollInterval           time.Duration
	LeaseRetry             time.Duration
	LeaseTTL               time.Duration
	LeaseRefresh           time.Duration
	HTTPTimeout            time.Duration
	Logger                 *slog.Logger
	Audit                  func(context.Context, string, map[string]any)
	Now                    func() time.Time
	// Devices 提供心跳里的设备清单和推送同步。nil 时不输出 devices、也不做推送同步。
	Devices DeviceStore
}

// DeviceStore 是推送同步需要的设备读写。实现见 store.AdminDeviceRepository。
// 列出的行可能带加密 token, 调用方解密, 且不得把 token 写进日志。
type DeviceStore interface {
	// ListActiveRecent 按最近活跃倒序返回未吊销设备, 最多 limit 台。
	ListActiveRecent(ctx context.Context, limit int) ([]store.AdminDevice, error)
	// ListUnsyncedPush 返回未吊销且 push_synced_at 为空的设备（含已清除 token 的）。
	ListUnsyncedPush(ctx context.Context) ([]store.AdminDevice, error)
	MarkPushSynced(ctx context.Context, id uuid.UUID, at time.Time) error
	SetPushState(ctx context.Context, id uuid.UUID, state string, at time.Time) error
	// MarkPushUnsynced 把一台设备标成未同步。
	MarkPushUnsynced(ctx context.Context, id uuid.UUID) error
	// MarkAllPushUnsynced 把全部设备标成未同步。
	MarkAllPushUnsynced(ctx context.Context) error
}

type PairingInfo struct {
	UserCode  string `json:"user_code"`
	ExpiresAt string `json:"expires_at"`
}

type Status struct {
	Status          string       `json:"status"`
	InstanceName    string       `json:"instance_name"`
	UserDisplayName string       `json:"user_display_name"`
	CloudURL        string       `json:"cloud_url"`
	BoundAt         *string      `json:"bound_at"`
	LastSyncAt      *string      `json:"last_sync_at"`
	LastError       string       `json:"last_error"`
	LastErrorAt     *string      `json:"last_error_at"`
	Pairing         *PairingInfo `json:"pairing"`
	MaxPollSeconds  int          `json:"max_poll_seconds"`
}

type pendingPair struct {
	userCode  string
	expiresAt time.Time
	interval  time.Duration
}

type Client struct {
	repo       store.CloudBindingRepository
	plugins    store.JSPluginRepository
	masterKey  string
	cloudURL   func(context.Context) (string, error)
	http       *http.Client
	allowLocal bool
	owner      string
	logger     *slog.Logger
	audit      func(context.Context, string, map[string]any)
	now        func() time.Time

	heartbeatEvery time.Duration
	firstDelay     time.Duration
	hasFirstDelay  bool
	backoffMin     time.Duration
	backoffMax     time.Duration
	pollEvery      time.Duration
	leaseRetry     time.Duration
	leaseTTL       time.Duration
	leaseRefresh   time.Duration

	devices DeviceStore
	// wakeHeartbeatGap 是被唤醒时两次心跳的最短间隔，要大于 Hub 的心跳限速（每分钟 1 次）。
	wakeHeartbeatGap time.Duration

	mu              sync.Mutex
	pending         *pendingPair
	developer       developerCache
	submissions     map[string]Submission
	developerWarnAt time.Time
	wakePush        chan struct{}
}

const maxReportedDevices = 8

func New(repo store.CloudBindingRepository, plugins store.JSPluginRepository, masterKey string, cloudURL func(context.Context) (string, error), opts Options) *Client {
	if opts.HeartbeatInterval <= 0 {
		opts.HeartbeatInterval = 15 * time.Minute
	}
	if opts.BackoffMin <= 0 {
		opts.BackoffMin = 30 * time.Second
	}
	if opts.BackoffMax <= 0 {
		opts.BackoffMax = time.Hour
	}
	if opts.LeaseRetry <= 0 {
		opts.LeaseRetry = 15 * time.Second
	}
	if opts.LeaseTTL <= 0 {
		opts.LeaseTTL = 2 * time.Minute
	}
	if opts.LeaseRefresh <= 0 {
		opts.LeaseRefresh = 30 * time.Second
	}
	if opts.HTTPTimeout <= 0 {
		opts.HTTPTimeout = defaultHTTPTimeout
	}
	if cloudURL == nil {
		cloudURL = func(context.Context) (string, error) {
			return "", errors.New("未缓存已验签的市场 root")
		}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		repo:             repo,
		plugins:          plugins,
		masterKey:        masterKey,
		cloudURL:         cloudURL,
		http:             newHTTPClient(opts.HTTPTimeout, opts.AllowInsecureLocal),
		allowLocal:       opts.AllowInsecureLocal,
		owner:            newOwner(),
		logger:           opts.Logger,
		audit:            opts.Audit,
		now:              now,
		heartbeatEvery:   opts.HeartbeatInterval,
		firstDelay:       opts.FirstHeartbeatDelay,
		hasFirstDelay:    opts.HasFirstHeartbeatDelay,
		backoffMin:       opts.BackoffMin,
		backoffMax:       opts.BackoffMax,
		pollEvery:        opts.PollInterval,
		leaseRetry:       opts.LeaseRetry,
		leaseTTL:         opts.LeaseTTL,
		leaseRefresh:     opts.LeaseRefresh,
		devices:          opts.Devices,
		wakeHeartbeatGap: 65 * time.Second,
		wakePush:         make(chan struct{}, 1),
	}
}

// WakePushSync 让持有租约的循环立刻再发一次心跳并同步推送。
// 非阻塞: 已经有一次未处理的唤醒时, 再唤醒会被合并掉。
func (c *Client) WakePushSync() {
	if c == nil || c.wakePush == nil {
		return
	}
	select {
	case c.wakePush <- struct{}{}:
	default:
	}
}

func newHTTPClient(timeout time.Duration, allowPrivate bool) *http.Client {
	dialer := httpclient.NewGuardedDialer(timeout)
	if !allowPrivate {
		base := dialer.Control
		dialer.Control = func(network, address string, c syscall.RawConn) error {
			if base != nil {
				if err := base(network, address, c); err != nil {
					return err
				}
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("blocked dial to %q: %w", address, err)
			}
			ip := net.ParseIP(host)
			if ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
				return fmt.Errorf("blocked connection to private address %s", ip)
			}
			return nil
		}
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			ResponseHeaderTimeout: timeout,
			DialContext:           dialer.DialContext,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func newOwner() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "unknown"
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return host
	}
	return host + ":" + strconv.Itoa(os.Getpid()) + ":" + base64.RawURLEncoding.EncodeToString(nonce[:])
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	row, err := c.repo.Get(ctx)
	if err != nil {
		return Status{}, err
	}
	view := statusFromRow(row)
	code := strings.TrimSpace(row.PairingCodeHint)
	if row.Status == store.CloudStatusPairing && code != "" && row.PairingExpiresAt != nil && c.now().Before(*row.PairingExpiresAt) {
		exp := row.PairingExpiresAt.UTC().Format(time.RFC3339)
		view.Pairing = &PairingInfo{UserCode: code, ExpiresAt: exp}
	}
	if _, cloudErr := c.cloudURL(ctx); cloudErr != nil && view.LastError == "" {
		view.LastError = cloudErr.Error()
	}
	return view, nil
}

func (c *Client) Report(ctx context.Context) (Report, error) {
	plugins, err := c.plugins.ListPlugins(ctx)
	if err != nil {
		return Report{}, err
	}
	versions, err := c.plugins.ListVersionHeaders(ctx)
	if err != nil {
		return Report{}, err
	}
	report := BuildReport(version.Current().Version, currentPlatform(), SelectPlugins(plugins, versions), c.now())
	devices, err := c.reportDevices(ctx)
	if err != nil {
		return Report{}, err
	}
	report.Devices = devices
	report.withDevices = devices != nil
	return report, nil
}

// reportDevices returns nil when there is no device source (the JSON key stays absent)
// and a non-nil slice, possibly empty, when a source is configured.
func (c *Client) reportDevices(ctx context.Context) ([]DeviceItem, error) {
	if c.devices == nil {
		return nil, nil
	}
	rows, err := c.devices.ListActiveRecent(ctx, maxReportedDevices)
	if err != nil {
		return nil, err
	}
	items := make([]DeviceItem, 0, len(rows))
	for _, row := range rows {
		if !strings.EqualFold(strings.TrimSpace(row.Platform), "ios") {
			continue
		}
		lastActive := ""
		if row.LastSeenAt.Valid && !row.LastSeenAt.Time.IsZero() {
			lastActive = row.LastSeenAt.Time.UTC().Format(time.RFC3339)
		}
		items = append(items, DeviceItem{
			Ref:            row.ID.String(),
			Name:           row.Name,
			Platform:       "ios",
			LastActiveAt:   lastActive,
			PushRegistered: strings.TrimSpace(row.APNSTokenEnc) != "",
			PushEnv:        row.APNSEnv,
		})
	}
	return items, nil
}

func currentPlatform() Platform {
	return Platform{OS: runtime.GOOS, Arch: runtime.GOARCH, Runtime: runtime.Version()}
}

// StartPairing calls the Cloud once. It is the only synchronous Cloud call on a request path.
func (c *Client) StartPairing(ctx context.Context, name string) (Status, error) {
	row, err := c.repo.Get(ctx)
	if err != nil {
		return Status{}, err
	}
	if row.Status == store.CloudStatusBound {
		return Status{}, ErrAlreadyBound
	}
	cloud, err := c.cloudURL(ctx)
	if err != nil {
		return Status{}, err
	}
	identity, err := c.repo.EnsureIdentity(ctx, c.generateIdentity)
	if err != nil {
		return Status{}, err
	}
	name = defaultName(name)
	info := version.Current()
	body, err := json.Marshal(map[string]any{
		"publicKey":    identity.PublicKey,
		"name":         name,
		"xlyraVersion": info.Version,
		"platform":     currentPlatform(),
	})
	if err != nil {
		return Status{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultHTTPTimeout)
	defer cancel()
	status, raw, err := c.post(callCtx, cloud, "/api/instance/pair/start", body, nil)
	if err != nil {
		return Status{}, err
	}
	if status < 200 || status >= 300 {
		return Status{}, cloudError(status, raw)
	}
	var resp struct {
		UserCode        string `json:"userCode"`
		PollToken       string `json:"pollToken"`
		ExpiresAt       string `json:"expiresAt"`
		IntervalSeconds int    `json:"intervalSeconds"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || resp.UserCode == "" || resp.PollToken == "" {
		return Status{}, errors.New("xLyra Cloud 返回的配对响应无效")
	}
	expires, err := time.Parse(time.RFC3339, resp.ExpiresAt)
	if err != nil {
		return Status{}, errors.New("xLyra Cloud 返回的配对过期时间无效")
	}
	enc, err := crypto.EncryptSecret(c.masterKey, resp.PollToken)
	if err != nil {
		return Status{}, err
	}
	if err := c.repo.SavePairing(ctx, cloud, enc, name, resp.UserCode, expires); err != nil {
		return Status{}, err
	}
	interval := time.Duration(resp.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 3 * time.Second
	}
	c.mu.Lock()
	c.pending = &pendingPair{userCode: resp.UserCode, expiresAt: expires, interval: interval}
	c.mu.Unlock()
	return c.Status(ctx)
}

func (c *Client) CancelPairing(ctx context.Context) error {
	c.mu.Lock()
	c.pending = nil
	c.mu.Unlock()
	return c.repo.ClearPairing(ctx, "")
}

// Unbind asks the Cloud to drop the instance, then clears the local link either way.
func (c *Client) Unbind(ctx context.Context) error {
	row, err := c.repo.Get(ctx)
	if err != nil {
		return err
	}
	if row.Status == store.CloudStatusBound && row.InstanceID != "" {
		c.bestEffortUnbind(ctx, row)
	}
	c.mu.Lock()
	c.pending = nil
	c.developer = developerCache{}
	c.submissions = nil
	c.mu.Unlock()
	return c.repo.ClearBinding(ctx, store.CloudStatusUnbound, "")
}

func (c *Client) bestEffortUnbind(ctx context.Context, row store.CloudBinding) {
	priv, err := c.privateKey(row)
	if err != nil {
		return
	}
	cloud := row.CloudURL
	if cloud == "" {
		cloud, err = c.cloudURL(ctx)
		if err != nil {
			return
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultHTTPTimeout)
	defer cancel()
	_, _, _ = c.post(callCtx, cloud, "/api/instance/unbind", nil, &signedRequest{
		instanceID: row.InstanceID,
		privateKey: priv,
	})
}

func (c *Client) generateIdentity() (string, string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	enc, err := crypto.EncryptSecret(c.masterKey, base64.StdEncoding.EncodeToString(priv))
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(pub), enc, nil
}

func (c *Client) privateKey(row store.CloudBinding) (ed25519.PrivateKey, error) {
	plain, err := crypto.DecryptSecret(c.masterKey, row.PrivateKeyEnc)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(plain)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, errors.New("实例私钥无效")
	}
	return ed25519.PrivateKey(raw), nil
}

type signedRequest struct {
	instanceID string
	privateKey ed25519.PrivateKey
}

func (c *Client) post(ctx context.Context, cloud, path string, body []byte, signed *signedRequest) (int, []byte, error) {
	contentType := ""
	if body != nil {
		contentType = "application/json"
	}
	return c.exchange(ctx, http.MethodPost, cloud, path, "", body, contentType, signed)
}

func (c *Client) exchange(ctx context.Context, method, cloud, path, rawQuery string, body []byte, contentType string, signed *signedRequest) (int, []byte, error) {
	status, raw, _, err := c.exchangeHeaders(ctx, method, cloud, path, rawQuery, body, contentType, signed)
	return status, raw, err
}

func (c *Client) exchangeHeaders(ctx context.Context, method, cloud, path, rawQuery string, body []byte, contentType string, signed *signedRequest) (int, []byte, http.Header, error) {
	endpoint, err := c.endpoint(cloud, path)
	if err != nil {
		return 0, nil, nil, err
	}
	if rawQuery != "" {
		endpoint += "?" + rawQuery
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if signed != nil {
		unix := c.now().Unix()
		message := CanonicalMessage(method, path, unix, body)
		req.Header.Set("X-XLyra-Instance", signed.instanceID)
		req.Header.Set("X-XLyra-Timestamp", strconv.FormatInt(unix, 10))
		req.Header.Set("X-XLyra-Signature", Sign(signed.privateKey, message))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return 0, nil, nil, errors.New("连接 xLyra Cloud 超时")
		}
		return 0, nil, nil, errors.New("连接 xLyra Cloud 失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return 0, nil, nil, errors.New("xLyra Cloud 重定向被拒绝")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return resp.StatusCode, nil, resp.Header, errors.New("读取 xLyra Cloud 响应失败")
	}
	return resp.StatusCode, raw, resp.Header, nil
}

func (c *Client) endpoint(cloud, path string) (string, error) {
	if err := c.validateCloud(cloud); err != nil {
		return "", err
	}
	u, err := url.Parse(cloud)
	if err != nil {
		return "", errors.New("xLyra Cloud 地址无效")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return u.Scheme + "://" + u.Host + path, nil
}

func (c *Client) validateCloud(cloud string) error {
	u, err := url.Parse(strings.TrimSpace(cloud))
	if err != nil || u.Host == "" || u.User != nil {
		return errors.New("xLyra Cloud 地址无效")
	}
	host := u.Hostname()
	if u.Scheme == "https" {
		return nil
	}
	if c.allowLocal && u.Scheme == "http" && (host == "127.0.0.1" || host == "localhost" || host == "::1") {
		return nil
	}
	return errors.New("xLyra Cloud 地址必须是 https")
}

func cloudError(status int, raw []byte) error {
	var body struct {
		ServerTime string `json:"serverTime"`
		Error      struct {
			Code       string `json:"code"`
			Message    string `json:"message"`
			ServerTime string `json:"serverTime"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &body)
	code := body.Error.Code
	if code == "" {
		code = "invalid_request"
	}
	message := body.Error.Message
	if message == "" {
		message = "xLyra Cloud 请求失败"
	}
	serverTime := body.Error.ServerTime
	if serverTime == "" {
		serverTime = body.ServerTime
	}
	return &CloudError{Status: status, Code: code, Message: message, ServerTime: serverTime}
}

func statusFromRow(row store.CloudBinding) Status {
	status := row.Status
	if status == "" {
		status = store.CloudStatusUnbound
	}
	return Status{
		Status:          status,
		InstanceName:    row.InstanceName,
		UserDisplayName: row.UserDisplayName,
		CloudURL:        row.CloudURL,
		BoundAt:         formatTime(row.BoundAt),
		LastSyncAt:      formatTime(row.LastSyncAt),
		LastError:       row.LastError,
		LastErrorAt:     formatTime(row.LastErrorAt),
		MaxPollSeconds:  MaxPollSeconds,
	}
}

func formatTime(value *time.Time) *string {
	if value == nil || value.IsZero() {
		return nil
	}
	text := value.UTC().Format(time.RFC3339)
	return &text
}

func defaultName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		host, err := os.Hostname()
		if err != nil || strings.TrimSpace(host) == "" {
			return "xLyra"
		}
		name = strings.TrimSpace(host)
	}
	if len(name) > 80 {
		name = name[:80]
	}
	return name
}

func (c *Client) warn(message string, args ...any) {
	if c.logger != nil {
		c.logger.Warn(message, args...)
	}
}

func (c *Client) recordAudit(ctx context.Context, action string, metadata map[string]any) {
	if c.audit == nil {
		return
	}
	c.audit(ctx, action, metadata)
}

func (c *Client) noteError(ctx context.Context, message string) {
	if message == "" {
		return
	}
	if err := c.repo.RecordError(ctx, message, c.now()); err != nil && ctx.Err() == nil {
		c.warn("xLyra Cloud binding error was not saved", "error", err.Error())
	}
}

func (c *Client) sleep(ctx context.Context, wait time.Duration) error {
	if wait < 0 {
		wait = 0
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func panicError(rec any) string {
	return fmt.Sprintf("%v\n%s", rec, debug.Stack())
}
