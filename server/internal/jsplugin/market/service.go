package market

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"xlyra/server/internal/auth"
	"xlyra/server/internal/config"
	"xlyra/server/internal/httpclient"
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/marketproto"
	"xlyra/server/internal/store"
)

const (
	marketLeaseTTL = 5 * time.Minute
	trustViewTTL   = 10 * time.Second

	errNoRootKeys      = "未配置市场根公钥"
	errBusy            = "其他实例正在同步"
	errEndpointBinding = "root.json 声明的 registry 地址不包含当前拉取地址（端点绑定）: endpoint_binding"
)

type Service struct {
	repo                store.JSPluginRepository
	clients             *httpclient.Manager
	cfg                 config.PluginMarketConfig
	rootKeys            []ed25519.PublicKey
	registries          []string
	owner               string
	fixture             *CatalogFixture
	trustMu             sync.Mutex
	trustCached         *cachedTrust
	icons               *iconLoader
	skipEndpointBinding bool
	runtime             pluginRuntime
	auditor             auditRecorder
}

type pluginRuntime interface {
	Enable(ctx context.Context, pluginID, version string, opts jsplugin.EnableOptions) error
	Disable(ctx context.Context, pluginID string) error
}

type auditRecorder interface {
	RecordAudit(ctx context.Context, input auth.AuditInput)
}

func (s *Service) SetRuntime(rt pluginRuntime) {
	if s == nil {
		return
	}
	s.runtime = rt
}

func (s *Service) SetAuditor(auditor auditRecorder) {
	if s == nil {
		return
	}
	s.auditor = auditor
}

func (s *Service) SetConfig(cfg config.PluginMarketConfig) {
	if s == nil {
		return
	}
	s.cfg = cfg
}

type cachedTrust struct {
	at          time.Time
	root        *marketproto.Root
	revocations *marketproto.Revocations
}

func NewService(repo store.JSPluginRepository, clients *httpclient.Manager, conf *config.ConfigFile, rootKeys []ed25519.PublicKey, registries []string) (*Service, error) {
	if rootKeys == nil {
		parsed, err := parseRootKeys(BuiltinRootKeys)
		if err != nil {
			return nil, err
		}
		rootKeys = parsed
	}
	if registries == nil {
		registries = append([]string(nil), BuiltinRegistryURLs...)
	}
	if clients == nil {
		clients = httpclient.NewManager(conf)
	}
	return &Service{
		repo:       repo,
		clients:    clients,
		cfg:        config.DefaultPluginMarketConfig(),
		rootKeys:   rootKeys,
		registries: registries,
		owner:      newOwner(),
		icons:      newIconLoader(),
	}, nil
}

func newOwner() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "unknown"
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		now := uint64(time.Now().UnixNano())
		for i := range nonce {
			nonce[i] = byte(now >> (8 * i))
		}
	}
	return host + ":" + strconv.Itoa(os.Getpid()) + ":" + hex.EncodeToString(nonce[:])
}

func parseRootKeys(encoded []string) ([]ed25519.PublicKey, error) {
	keys := make([]ed25519.PublicKey, 0, len(encoded))
	for _, item := range encoded {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		pub, err := marketproto.ParsePublicKey(item)
		if err != nil {
			return nil, fmt.Errorf("market root key: %w", err)
		}
		keys = append(keys, pub)
	}
	return keys, nil
}

type DocStatus struct {
	Seq         int64      `json:"seq"`
	SyncedAt    *time.Time `json:"synced_at"`
	NotModified bool       `json:"not_modified"`
}

type Status struct {
	SourceURL   string    `json:"source_url"`
	Root        DocStatus `json:"root"`
	Index       DocStatus `json:"index"`
	Revocations DocStatus `json:"revocations"`
	Error       string    `json:"error,omitempty"`
}

type Snapshot struct {
	Root               *marketproto.Root
	Index              *marketproto.Index
	Revocations        *marketproto.Revocations
	IndexExpired       bool
	RevocationsExpired bool
}

type CacheStatus struct {
	Enabled             bool       `json:"enabled"`
	RootKeysConfigured  bool       `json:"root_keys_configured"`
	SourceURL           string     `json:"source_url"`
	RootSeq             int64      `json:"root_seq"`
	IndexSeq            int64      `json:"index_seq"`
	RevocationsSeq      int64      `json:"revocations_seq"`
	IndexSyncedAt       *time.Time `json:"index_synced_at"`
	RevocationsSyncedAt *time.Time `json:"revocations_synced_at"`
	LastAttemptAt       *time.Time `json:"last_attempt_at"`
	LastError           string     `json:"last_error"`
	IndexExpired        bool       `json:"index_expired"`
	RevocationsExpired  bool       `json:"revocations_expired"`
}

func (s *Service) SyncOnce(ctx context.Context, force bool) (Status, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg := s.cfg
	if !cfg.Enabled {
		return Status{}, nil
	}
	if len(s.rootKeys) == 0 {
		if err := s.recordFailure(ctx, errNoRootKeys); err != nil {
			return Status{Error: errNoRootKeys}, err
		}
		return Status{Error: errNoRootKeys}, errors.New(errNoRootKeys)
	}
	ok, err := s.repo.TryAcquireMarketLease(ctx, s.owner, marketLeaseTTL)
	if err != nil {
		return Status{}, err
	}
	if !ok {
		return Status{Error: errBusy}, nil
	}
	defer func() {
		_ = s.repo.ReleaseMarketLease(context.WithoutCancel(ctx), s.owner)
	}()

	state, err := s.repo.GetMarketState(ctx)
	if err != nil {
		return Status{}, err
	}
	client, err := s.httpClient(cfg.ProxyID)
	if err != nil {
		return s.fail(ctx, state, err.Error())
	}
	bases := candidateBases(cfg.SourceURL, s.cachedRegistries(state), s.registries)
	if len(bases) == 0 {
		return s.fail(ctx, state, "没有可用的市场基地址")
	}
	var failures []string
	for _, base := range bases {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err.Error())
			break
		}
		next, status, err := s.syncBase(ctx, client, base, state, cfg, force)
		if err != nil {
			failures = append(failures, base+": "+err.Error())
			continue
		}
		if err := s.repo.SaveMarketState(context.WithoutCancel(ctx), next); err != nil {
			return Status{}, err
		}
		s.clearTrustView()
		if err := s.applyPending(context.WithoutCancel(ctx), next); err != nil {
			slog.Warn("plugin market follow-up failed", "error", err)
		}
		return status, nil
	}
	return s.fail(ctx, state, strings.Join(failures, "; "))
}

func (s *Service) fail(ctx context.Context, state store.JSPluginMarketState, msg string) (Status, error) {
	if err := s.recordFailure(ctx, msg); err != nil {
		return Status{SourceURL: state.SourceURL, Error: msg}, err
	}
	return Status{SourceURL: state.SourceURL, Error: msg}, nil
}

func (s *Service) recordFailure(ctx context.Context, msg string) error {
	ctx = context.WithoutCancel(ctx)
	state, err := s.repo.GetMarketState(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	state.LastError = msg
	state.LastAttemptAt = &now
	return s.repo.SaveMarketState(ctx, state)
}

func (s *Service) httpClient(proxyID string) (*http.Client, error) {
	return s.clients.Client(httpclient.Profile{
		ProxyID:        proxyID,
		RequestTimeout: 30 * time.Second,
	})
}

func (s *Service) syncBase(ctx context.Context, client *http.Client, base string, state store.JSPluginMarketState, cfg config.PluginMarketConfig, force bool) (store.JSPluginMarketState, Status, error) {
	now := time.Now().UTC()
	rootRaw, rootETag, root304, err := s.fetchDocument(ctx, client, base, "root.json", state.RootETag, state.RootEnvelope, marketproto.MaxRootBytes)
	if err != nil {
		return store.JSPluginMarketState{}, Status{}, err
	}
	root, err := marketproto.VerifyRoot(rootRaw, s.rootKeys, marketproto.State{
		Seq: state.RootSeq, PayloadSHA256: state.RootPayloadSHA256,
	}, now)
	if err != nil {
		return store.JSPluginMarketState{}, Status{}, err
	}
	if strings.TrimSpace(cfg.SourceURL) == "" && !s.skipEndpointBinding && !rootCoversBase(root, base) {
		return store.JSPluginMarketState{}, Status{}, errors.New(errEndpointBinding)
	}
	rootSHA, err := payloadSHA256(rootRaw)
	if err != nil {
		return store.JSPluginMarketState{}, Status{}, err
	}

	revRaw, revETag, rev304, err := s.fetchDocument(ctx, client, base, "revocations.json", state.RevocationsETag, state.RevocationsEnvelope, marketproto.MaxRevocationsBytes)
	if err != nil {
		return store.JSPluginMarketState{}, Status{}, err
	}
	rev, err := marketproto.VerifyRevocations(revRaw, root, marketproto.State{
		Seq: state.RevocationsSeq, PayloadSHA256: state.RevocationsPayloadSHA256,
	}, now)
	if err != nil {
		return store.JSPluginMarketState{}, Status{}, err
	}
	revSHA, err := payloadSHA256(revRaw)
	if err != nil {
		return store.JSPluginMarketState{}, Status{}, err
	}

	next := state
	next.RootSeq = root.Seq
	next.RootPayloadSHA256 = rootSHA
	next.RootEnvelope = rootRaw
	next.RootETag = rootETag
	next.RevocationsSeq = rev.Seq
	next.RevocationsPayloadSHA256 = revSHA
	next.RevocationsEnvelope = revRaw
	next.RevocationsETag = revETag
	next.RevocationsSyncedAt = &now
	next.LastAttemptAt = &now
	next.LastError = ""
	next.SourceURL = base

	index304 := false
	if indexDue(state, cfg.IndexSyncHours, force, now) {
		idxRaw, idxETag, notModified, err := s.fetchDocument(ctx, client, base, "index.json", state.IndexETag, state.IndexEnvelope, marketproto.MaxIndexBytes)
		if err != nil {
			return store.JSPluginMarketState{}, Status{}, err
		}
		idx, err := marketproto.VerifyIndex(idxRaw, root, marketproto.State{
			Seq: state.IndexSeq, PayloadSHA256: state.IndexPayloadSHA256,
		}, now)
		if err != nil {
			return store.JSPluginMarketState{}, Status{}, err
		}
		idxSHA, err := payloadSHA256(idxRaw)
		if err != nil {
			return store.JSPluginMarketState{}, Status{}, err
		}
		next.IndexSeq = idx.Seq
		next.IndexPayloadSHA256 = idxSHA
		next.IndexEnvelope = idxRaw
		next.IndexETag = idxETag
		next.IndexSyncedAt = &now
		index304 = notModified
	}

	status := Status{
		SourceURL: base,
		Root: DocStatus{
			Seq: next.RootSeq, SyncedAt: next.LastAttemptAt, NotModified: root304,
		},
		Index: DocStatus{
			Seq: next.IndexSeq, SyncedAt: next.IndexSyncedAt, NotModified: index304,
		},
		Revocations: DocStatus{
			Seq: next.RevocationsSeq, SyncedAt: next.RevocationsSyncedAt, NotModified: rev304,
		},
	}
	return next, status, nil
}

func (s *Service) fetchDocument(ctx context.Context, client *http.Client, base, name, etag string, cached []byte, maxBytes int) ([]byte, string, bool, error) {
	body, newETag, notModified, err := fetch(ctx, client, documentURL(base, name), etag, maxBytes)
	if err != nil {
		return nil, "", false, err
	}
	if !notModified {
		return body, newETag, false, nil
	}
	if len(cached) == 0 {
		return nil, "", false, fmt.Errorf("%s: not modified without a cached copy", name)
	}
	if newETag == "" {
		newETag = etag
	}
	return bytes.Clone(cached), newETag, true, nil
}

func (s *Service) cachedRegistries(state store.JSPluginMarketState) []string {
	if len(state.RootEnvelope) == 0 || len(s.rootKeys) == 0 {
		return nil
	}
	root, err := marketproto.VerifyRoot(state.RootEnvelope, s.rootKeys, marketproto.State{
		Seq: state.RootSeq, PayloadSHA256: state.RootPayloadSHA256,
	}, time.Now())
	if err != nil {
		return nil
	}
	return root.Endpoints.Registry
}

func (s *Service) Snapshot(ctx context.Context) (Snapshot, error) {
	state, err := s.repo.GetMarketState(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	return snapshotFrom(state, s.rootKeys, time.Now())
}

func (s *Service) CacheStatus(ctx context.Context) (CacheStatus, error) {
	if s.fixture != nil {
		return CacheStatus{
			Enabled:            s.fixture.Enabled,
			RootKeysConfigured: s.fixture.RootKeysConfigured,
			SourceURL:          s.fixture.SourceURL,
			IndexSyncedAt:      s.fixture.IndexSyncedAt,
			LastError:          s.fixture.LastError,
			IndexExpired:       s.fixture.Snapshot.IndexExpired,
			RevocationsExpired: s.fixture.Snapshot.RevocationsExpired,
		}, nil
	}
	cfg := s.cfg
	state, err := s.repo.GetMarketState(ctx)
	if err != nil {
		return CacheStatus{}, err
	}
	snap, err := snapshotFrom(state, s.rootKeys, time.Now())
	if err != nil {
		return CacheStatus{}, err
	}
	return CacheStatus{
		Enabled:             cfg.Enabled,
		RootKeysConfigured:  len(s.rootKeys) > 0,
		SourceURL:           state.SourceURL,
		RootSeq:             state.RootSeq,
		IndexSeq:            state.IndexSeq,
		RevocationsSeq:      state.RevocationsSeq,
		IndexSyncedAt:       state.IndexSyncedAt,
		RevocationsSyncedAt: state.RevocationsSyncedAt,
		LastAttemptAt:       state.LastAttemptAt,
		LastError:           state.LastError,
		IndexExpired:        snap.IndexExpired,
		RevocationsExpired:  snap.RevocationsExpired,
	}, nil
}

func snapshotFrom(state store.JSPluginMarketState, rootKeys []ed25519.PublicKey, now time.Time) (Snapshot, error) {
	if len(state.RootEnvelope) == 0 && len(state.IndexEnvelope) == 0 && len(state.RevocationsEnvelope) == 0 {
		return Snapshot{}, nil
	}
	var snap Snapshot
	if len(state.RootEnvelope) == 0 {
		return Snapshot{}, fmt.Errorf("market cache is missing root")
	}
	root, err := marketproto.VerifyRoot(state.RootEnvelope, rootKeys, marketproto.State{
		Seq: state.RootSeq, PayloadSHA256: state.RootPayloadSHA256,
	}, now)
	if err != nil {
		return Snapshot{}, err
	}
	snap.Root = root
	if len(state.IndexEnvelope) > 0 {
		idx, expired, err := verifyIndexSnapshot(state, root, now)
		if err != nil {
			return Snapshot{}, err
		}
		snap.Index = idx
		snap.IndexExpired = expired
	}
	if len(state.RevocationsEnvelope) > 0 {
		rev, expired, err := verifyRevocationsSnapshot(state, root, now)
		if err != nil {
			return Snapshot{}, err
		}
		snap.Revocations = rev
		snap.RevocationsExpired = expired
	}
	return snap, nil
}

func verifyIndexSnapshot(state store.JSPluginMarketState, root *marketproto.Root, now time.Time) (*marketproto.Index, bool, error) {
	st := marketproto.State{Seq: state.IndexSeq, PayloadSHA256: state.IndexPayloadSHA256}
	idx, err := marketproto.VerifyIndex(state.IndexEnvelope, root, st, now)
	if marketproto.Code(err) != marketproto.CodeExpired {
		return idx, false, err
	}
	gen, genErr := documentGeneratedAt(state.IndexEnvelope)
	if genErr != nil {
		return nil, true, genErr
	}
	idx, err = marketproto.VerifyIndex(state.IndexEnvelope, root, st, gen)
	return idx, true, err
}

func verifyRevocationsSnapshot(state store.JSPluginMarketState, root *marketproto.Root, now time.Time) (*marketproto.Revocations, bool, error) {
	st := marketproto.State{Seq: state.RevocationsSeq, PayloadSHA256: state.RevocationsPayloadSHA256}
	rev, err := marketproto.VerifyRevocations(state.RevocationsEnvelope, root, st, now)
	if marketproto.Code(err) != marketproto.CodeExpired {
		return rev, false, err
	}
	gen, genErr := documentGeneratedAt(state.RevocationsEnvelope)
	if genErr != nil {
		return nil, true, genErr
	}
	rev, err = marketproto.VerifyRevocations(state.RevocationsEnvelope, root, st, gen)
	return rev, true, err
}

func rootCoversBase(root *marketproto.Root, base string) bool {
	if root == nil {
		return false
	}
	want := normalizeRegistryBase(base)
	if want == "" {
		return false
	}
	for _, item := range root.Endpoints.Registry {
		if normalizeRegistryBase(item) == want {
			return true
		}
	}
	return false
}

func normalizeRegistryBase(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if scheme, rest, ok := strings.Cut(s, "://"); ok {
		host, path, found := strings.Cut(rest, "/")
		s = strings.ToLower(scheme) + "://" + strings.ToLower(host)
		if found {
			s += "/" + path
		}
	}
	return strings.TrimRight(s, "/") + "/"
}

func candidateBases(sourceURL string, cached, fallbacks []string) []string {
	if strings.TrimSpace(sourceURL) != "" {
		return []string{strings.TrimSpace(sourceURL)}
	}
	merged := make([]string, 0, len(cached)+len(fallbacks))
	merged = append(merged, cached...)
	merged = append(merged, fallbacks...)
	return dedupe(merged)
}

func dedupe(urls []string) []string {
	seen := make(map[string]struct{}, len(urls))
	out := make([]string, 0, len(urls))
	for _, item := range urls {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func indexDue(state store.JSPluginMarketState, hours int, force bool, now time.Time) bool {
	if force || state.IndexSyncedAt == nil || len(state.IndexEnvelope) == 0 {
		return true
	}
	if hours < 1 {
		hours = config.DefaultPluginMarketConfig().IndexSyncHours
	}
	return state.IndexSyncedAt.Before(now.Add(-time.Duration(hours) * time.Hour))
}

func documentURL(base, name string) string {
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base + name
}

func payloadSHA256(raw []byte) (string, error) {
	payload, err := envelopePayload(raw)
	if err != nil {
		return "", err
	}
	return marketproto.FileSHA256(payload), nil
}

func envelopePayload(raw []byte) ([]byte, error) {
	var env marketproto.Envelope
	if err := json.Unmarshal(bytes.TrimSpace(raw), &env); err != nil {
		return nil, err
	}
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

// CloudURL returns the cloud endpoint from the cached, already-verified root.
// It does not fetch root.json.
func (s *Service) CloudURL(ctx context.Context) (string, error) {
	if s == nil {
		return "", errors.New("未缓存已验签的市场 root")
	}
	root, _, err := s.trustView(ctx)
	if err != nil || root == nil {
		return "", errors.New("未缓存已验签的市场 root")
	}
	if root.Endpoints.Cloud == nil || strings.TrimSpace(*root.Endpoints.Cloud) == "" {
		return "", errors.New("已验签的 root 未声明 xLyra Cloud 地址")
	}
	return strings.TrimRight(strings.TrimSpace(*root.Endpoints.Cloud), "/"), nil
}

func documentGeneratedAt(raw []byte) (time.Time, error) {
	payload, err := envelopePayload(raw)
	if err != nil {
		return time.Time{}, err
	}
	var probe struct {
		GeneratedAt string `json:"generatedAt"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return time.Time{}, err
	}
	ts, err := time.Parse(time.RFC3339, probe.GeneratedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("generatedAt: %w", err)
	}
	return ts, nil
}
