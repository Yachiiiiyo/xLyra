package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	JSPluginUpdatePolicyOff    = "off"
	JSPluginUpdatePolicyNotify = "notify"
	JSPluginUpdatePolicyAuto   = "auto"

	JSPluginAutoUpdateApplied    = "applied"
	JSPluginAutoUpdateRolledBack = "rolled_back"
	JSPluginAutoUpdateFailed     = "failed"
)

type JSPluginMarketSubscription struct {
	PluginID            string `gorm:"primaryKey"`
	UpdatePolicy        string
	PausedReason        string
	NotifiedVersion     string
	NotifiedRevocations string
	UpdatedAt           time.Time
}

func (JSPluginMarketSubscription) TableName() string { return "js_plugin_market_subscriptions" }

type JSPluginMarketState struct {
	ID                       int `gorm:"primaryKey"`
	RootSeq                  int64
	RootPayloadSHA256        string
	RootEnvelope             []byte
	RootETag                 string `gorm:"column:root_etag"`
	IndexSeq                 int64
	IndexPayloadSHA256       string
	IndexEnvelope            []byte
	IndexETag                string `gorm:"column:index_etag"`
	RevocationsSeq           int64
	RevocationsPayloadSHA256 string
	RevocationsEnvelope      []byte
	RevocationsETag          string `gorm:"column:revocations_etag"`
	IndexSyncedAt            *time.Time
	RevocationsSyncedAt      *time.Time
	LastAttemptAt            *time.Time
	LastError                string
	SourceURL                string
	LeaseOwner               string
	LeaseUntil               *time.Time
}

func (JSPluginMarketState) TableName() string { return "js_plugin_market_state" }

type JSPluginAutoUpdate struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey"`
	PluginID    string
	FromVersion string
	ToVersion   string
	Status      string
	Reason      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (JSPluginAutoUpdate) TableName() string { return "js_plugin_auto_updates" }

func (r JSPluginRepository) GetMarketSubscription(ctx context.Context, pluginID string) (JSPluginMarketSubscription, error) {
	var row JSPluginMarketSubscription
	err := r.db.WithContext(ctx).Where("plugin_id = ?", pluginID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return JSPluginMarketSubscription{PluginID: pluginID, UpdatePolicy: JSPluginUpdatePolicyNotify}, nil
	}
	return row, err
}

func (r JSPluginRepository) UpsertMarketSubscription(ctx context.Context, row JSPluginMarketSubscription) error {
	if row.UpdatePolicy == "" {
		row.UpdatePolicy = JSPluginUpdatePolicyNotify
	}
	var paused any
	if row.PausedReason != "" {
		paused = row.PausedReason
	}
	return r.db.WithContext(ctx).Exec(`
		INSERT INTO js_plugin_market_subscriptions (plugin_id, update_policy, paused_reason, updated_at)
		VALUES (?, ?, ?, NOW())
		ON CONFLICT (plugin_id) DO UPDATE SET
			update_policy = EXCLUDED.update_policy,
			paused_reason = EXCLUDED.paused_reason,
			updated_at = NOW()
	`, row.PluginID, row.UpdatePolicy, paused).Error
}

func (r JSPluginRepository) SetMarketPolicy(ctx context.Context, pluginID, policy string) error {
	if policy == "" {
		policy = JSPluginUpdatePolicyNotify
	}
	return r.db.WithContext(ctx).Exec(`
		INSERT INTO js_plugin_market_subscriptions (plugin_id, update_policy, paused_reason, updated_at)
		VALUES (?, ?, NULL, NOW())
		ON CONFLICT (plugin_id) DO UPDATE SET
			update_policy = EXCLUDED.update_policy,
			paused_reason = NULL,
			updated_at = NOW()
	`, pluginID, policy).Error
}

func (r JSPluginRepository) SetMarketPausedReason(ctx context.Context, pluginID, reason string) error {
	var paused any
	if reason != "" {
		paused = reason
	}
	return r.db.WithContext(ctx).Exec(`
		INSERT INTO js_plugin_market_subscriptions (plugin_id, update_policy, paused_reason, updated_at)
		VALUES (?, 'notify', ?, NOW())
		ON CONFLICT (plugin_id) DO UPDATE SET
			paused_reason = EXCLUDED.paused_reason,
			updated_at = NOW()
	`, pluginID, paused).Error
}

func (r JSPluginRepository) SetMarketNotifiedVersion(ctx context.Context, pluginID, version string) error {
	return r.db.WithContext(ctx).Exec(`
		INSERT INTO js_plugin_market_subscriptions (plugin_id, update_policy, notified_version, updated_at)
		VALUES (?, 'notify', ?, NOW())
		ON CONFLICT (plugin_id) DO UPDATE SET
			notified_version = EXCLUDED.notified_version,
			updated_at = NOW()
	`, pluginID, version).Error
}

func (r JSPluginRepository) RememberMarketRevocation(ctx context.Context, pluginID, entryID string) error {
	entryID = strings.TrimSpace(entryID)
	if pluginID == "" || entryID == "" {
		return nil
	}
	return r.db.WithContext(ctx).Exec(`
		INSERT INTO js_plugin_market_subscriptions (plugin_id, update_policy, notified_revocations, updated_at)
		VALUES (?, 'notify', ?, NOW())
		ON CONFLICT (plugin_id) DO UPDATE SET
			notified_revocations = CASE
				WHEN js_plugin_market_subscriptions.notified_revocations = '' THEN EXCLUDED.notified_revocations
				WHEN position(',' || EXCLUDED.notified_revocations || ',' in ',' || js_plugin_market_subscriptions.notified_revocations || ',') > 0
					THEN js_plugin_market_subscriptions.notified_revocations
				ELSE js_plugin_market_subscriptions.notified_revocations || ',' || EXCLUDED.notified_revocations
			END,
			updated_at = NOW()
	`, pluginID, entryID).Error
}

func (r JSPluginRepository) ListRolledBackAutoUpdates(ctx context.Context) ([]JSPluginAutoUpdate, error) {
	var rows []JSPluginAutoUpdate
	err := r.db.WithContext(ctx).Where("status = ?", JSPluginAutoUpdateRolledBack).Find(&rows).Error
	return rows, err
}

func (r JSPluginRepository) LatestAutoUpdate(ctx context.Context, pluginID, toVersion string) (JSPluginAutoUpdate, error) {
	var row JSPluginAutoUpdate
	err := r.db.WithContext(ctx).
		Where("plugin_id = ? AND to_version = ?", pluginID, toVersion).
		Order("created_at DESC").
		Take(&row).Error
	return row, err
}

func (r JSPluginRepository) ListMarketSubscriptions(ctx context.Context) ([]JSPluginMarketSubscription, error) {
	var rows []JSPluginMarketSubscription
	err := r.db.WithContext(ctx).Order("plugin_id ASC").Find(&rows).Error
	return rows, err
}

func (r JSPluginRepository) GetMarketState(ctx context.Context) (JSPluginMarketState, error) {
	var row JSPluginMarketState
	err := r.db.WithContext(ctx).Where("id = ?", 1).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return JSPluginMarketState{}, nil
	}
	return row, err
}

func (r JSPluginRepository) GetMarketTrustState(ctx context.Context) (JSPluginMarketState, error) {
	var row JSPluginMarketState
	err := r.db.WithContext(ctx).
		Select("root_seq", "root_payload_sha256", "root_envelope", "revocations_seq", "revocations_payload_sha256", "revocations_envelope").
		Where("id = ?", 1).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return JSPluginMarketState{}, nil
	}
	return row, err
}

func (r JSPluginRepository) SaveMarketState(ctx context.Context, row JSPluginMarketState) error {
	return r.db.WithContext(ctx).Exec(`
		INSERT INTO js_plugin_market_state (
			id,
			root_seq, root_payload_sha256, root_envelope, root_etag,
			index_seq, index_payload_sha256, index_envelope, index_etag,
			revocations_seq, revocations_payload_sha256, revocations_envelope, revocations_etag,
			index_synced_at, revocations_synced_at, last_attempt_at,
			last_error, source_url
		) VALUES (
			1,
			?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?,
			?, ?
		)
		ON CONFLICT (id) DO UPDATE SET
			root_seq = EXCLUDED.root_seq,
			root_payload_sha256 = EXCLUDED.root_payload_sha256,
			root_envelope = EXCLUDED.root_envelope,
			root_etag = EXCLUDED.root_etag,
			index_seq = EXCLUDED.index_seq,
			index_payload_sha256 = EXCLUDED.index_payload_sha256,
			index_envelope = EXCLUDED.index_envelope,
			index_etag = EXCLUDED.index_etag,
			revocations_seq = EXCLUDED.revocations_seq,
			revocations_payload_sha256 = EXCLUDED.revocations_payload_sha256,
			revocations_envelope = EXCLUDED.revocations_envelope,
			revocations_etag = EXCLUDED.revocations_etag,
			index_synced_at = EXCLUDED.index_synced_at,
			revocations_synced_at = EXCLUDED.revocations_synced_at,
			last_attempt_at = EXCLUDED.last_attempt_at,
			last_error = EXCLUDED.last_error,
			source_url = EXCLUDED.source_url
	`,
		row.RootSeq, row.RootPayloadSHA256, nullableBytes(row.RootEnvelope), row.RootETag,
		row.IndexSeq, row.IndexPayloadSHA256, nullableBytes(row.IndexEnvelope), row.IndexETag,
		row.RevocationsSeq, row.RevocationsPayloadSHA256, nullableBytes(row.RevocationsEnvelope), row.RevocationsETag,
		nullableTime(row.IndexSyncedAt), nullableTime(row.RevocationsSyncedAt), nullableTime(row.LastAttemptAt),
		row.LastError, row.SourceURL,
	).Error
}

func (r JSPluginRepository) TryAcquireMarketLease(ctx context.Context, owner string, ttl time.Duration) (bool, error) {
	var id int
	err := r.db.WithContext(ctx).Raw(`
		INSERT INTO js_plugin_market_state (id, lease_owner, lease_until)
		VALUES (1, ?, NOW() + make_interval(secs => ?))
		ON CONFLICT (id) DO UPDATE
		SET lease_owner = EXCLUDED.lease_owner,
			lease_until = EXCLUDED.lease_until
		WHERE js_plugin_market_state.lease_until IS NULL
			OR js_plugin_market_state.lease_until < NOW()
			OR js_plugin_market_state.lease_owner = EXCLUDED.lease_owner
		RETURNING id
	`, owner, ttl.Seconds()).Row().Scan(&id)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return id == 1, nil
}

func (r JSPluginRepository) ReleaseMarketLease(ctx context.Context, owner string) error {
	return r.db.WithContext(ctx).Exec(`
		UPDATE js_plugin_market_state
		SET lease_owner = '', lease_until = NULL
		WHERE id = 1 AND lease_owner = ?
	`, owner).Error
}

func (r JSPluginRepository) CreateAutoUpdate(ctx context.Context, row JSPluginAutoUpdate) (JSPluginAutoUpdate, error) {
	err := r.db.WithContext(ctx).Raw(`
		INSERT INTO js_plugin_auto_updates (plugin_id, from_version, to_version, status, reason)
		VALUES (?, ?, ?, ?, ?)
		RETURNING id, plugin_id, from_version, to_version, status, reason, created_at, updated_at
	`, row.PluginID, row.FromVersion, row.ToVersion, row.Status, row.Reason).Scan(&row).Error
	return row, err
}

func (r JSPluginRepository) UpdateAutoUpdateStatus(ctx context.Context, id uuid.UUID, status, reason string) error {
	result := r.db.WithContext(ctx).Exec(`
		UPDATE js_plugin_auto_updates
		SET status = ?, reason = ?, updated_at = NOW()
		WHERE id = ?
	`, status, reason, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r JSPluginRepository) FindRecentAppliedAutoUpdate(ctx context.Context, pluginID, toVersion string, within time.Duration) (JSPluginAutoUpdate, error) {
	var row JSPluginAutoUpdate
	err := r.db.WithContext(ctx).
		Where("plugin_id = ? AND to_version = ? AND status = ? AND created_at >= NOW() - make_interval(secs => ?)",
			pluginID, toVersion, JSPluginAutoUpdateApplied, within.Seconds()).
		Order("created_at DESC").
		Take(&row).Error
	return row, err
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
