package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
)

const (
	HubStatusUnbound = "unbound"
	HubStatusPairing = "pairing"
	HubStatusBound   = "bound"
	HubStatusRevoked = "revoked"

	hubIdentityLock int64 = 8273645101
)

// HubBinding is the single local row that remembers this instance's Hub identity.
// pairing_code_hint stores the user code while a pairing is in progress.
// poll_token_enc and private_key_enc are ciphertext.
type HubBinding struct {
	ID               int `gorm:"primaryKey"`
	PublicKey        string
	PrivateKeyEnc    string
	InstanceID       string
	Status           string
	PairingCodeHint  string
	PollTokenEnc     string
	PairingExpiresAt *time.Time
	HubURL           string
	UserDisplayName  string
	InstanceName     string
	BoundAt          *time.Time
	LastSyncAt       *time.Time
	LastError        string
	LastErrorAt      *time.Time
	LeaseOwner       string
	LeaseUntil       *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (HubBinding) TableName() string { return "hub_binding" }

type HubBindingRepository struct {
	db *gorm.DB
}

func NewHubBindingRepository(db *gorm.DB) HubBindingRepository {
	return HubBindingRepository{db: db}
}

func (r HubBindingRepository) postgres() bool {
	return r.db != nil && r.db.Dialector != nil && r.db.Dialector.Name() == "postgres"
}

func (r HubBindingRepository) Get(ctx context.Context) (HubBinding, error) {
	var row HubBinding
	err := r.db.WithContext(ctx).Where("id = ?", 1).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return HubBinding{Status: HubStatusUnbound}, nil
	}
	if err != nil {
		return HubBinding{}, err
	}
	if row.Status == "" {
		row.Status = HubStatusUnbound
	}
	return row, nil
}

// EnsureIdentity returns the saved key pair, generating one only when the row is missing.
// Concurrent callers serialize on Postgres with a transaction advisory lock; the insert is
// ON CONFLICT DO NOTHING so a loser keeps the winner's keys. Non-Postgres (SQLite) is a single
// process and relies on the conflict clause.
func (r HubBindingRepository) EnsureIdentity(ctx context.Context, generate func() (publicKey, privateKeyEnc string, err error)) (HubBinding, error) {
	var row HubBinding
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if r.postgres() {
			if err := tx.Exec(`SELECT pg_advisory_xact_lock(?)`, hubIdentityLock).Error; err != nil {
				return err
			}
		}
		var existing HubBinding
		err := tx.Where("id = ?", 1).Take(&existing).Error
		if err == nil && existing.PublicKey != "" && existing.PrivateKeyEnc != "" {
			row = existing
			return nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		publicKey, privateKeyEnc, err := generate()
		if err != nil {
			return err
		}
		if publicKey == "" || privateKeyEnc == "" {
			return errors.New("hub identity is empty")
		}
		now := time.Now().UTC()
		res := tx.Exec(`
			INSERT INTO hub_binding (id, public_key, private_key_enc, status, created_at, updated_at)
			VALUES (1, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO NOTHING
		`, publicKey, privateKeyEnc, HubStatusUnbound, now, now)
		if res.Error != nil {
			return res.Error
		}
		return tx.Where("id = ?", 1).Take(&row).Error
	})
	if err != nil {
		return HubBinding{}, err
	}
	if row.Status == "" {
		row.Status = HubStatusUnbound
	}
	return row, nil
}

func (r HubBindingRepository) SavePairing(ctx context.Context, hubURL, pollTokenEnc, instanceName, userCode string, expiresAt time.Time) error {
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).Exec(`
		UPDATE hub_binding SET
			status = ?,
			instance_id = '',
			pairing_code_hint = ?,
			poll_token_enc = ?,
			pairing_expires_at = ?,
			hub_url = ?,
			user_display_name = '',
			instance_name = ?,
			bound_at = NULL,
			last_error = '',
			last_error_at = NULL,
			updated_at = ?
		WHERE id = 1
	`, HubStatusPairing, userCode, pollTokenEnc, expiresAt.UTC(), hubURL, instanceName, now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ClearPairing returns a pairing row to unbound and forgets the poll token. A bound row is left alone.
func (r HubBindingRepository) ClearPairing(ctx context.Context, lastError string) error {
	now := time.Now().UTC()
	var lastErrAt any
	if lastError != "" {
		lastErrAt = now
	}
	res := r.db.WithContext(ctx).Exec(`
		UPDATE hub_binding SET
			status = ?,
			pairing_code_hint = '',
			poll_token_enc = '',
			pairing_expires_at = NULL,
			instance_name = '',
			last_error = ?,
			last_error_at = ?,
			updated_at = ?
		WHERE id = 1 AND status = ?
	`, HubStatusUnbound, lastError, lastErrAt, now, HubStatusPairing)
	return res.Error
}

func (r HubBindingRepository) MarkBound(ctx context.Context, instanceID, instanceName, userDisplayName string, boundAt time.Time) error {
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).Exec(`
		UPDATE hub_binding SET
			status = ?,
			instance_id = ?,
			instance_name = CASE WHEN ? <> '' THEN ? ELSE instance_name END,
			user_display_name = ?,
			bound_at = ?,
			pairing_code_hint = '',
			poll_token_enc = '',
			pairing_expires_at = NULL,
			last_error = '',
			last_error_at = NULL,
			updated_at = ?
		WHERE id = 1
	`, HubStatusBound, instanceID, instanceName, instanceName, userDisplayName, boundAt.UTC(), now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r HubBindingRepository) RecordSync(ctx context.Context, instanceName, userDisplayName string, at time.Time) error {
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).Exec(`
		UPDATE hub_binding SET
			instance_name = CASE WHEN ? <> '' THEN ? ELSE instance_name END,
			user_display_name = CASE WHEN ? <> '' THEN ? ELSE user_display_name END,
			last_sync_at = ?,
			last_error = '',
			last_error_at = NULL,
			updated_at = ?
		WHERE id = 1 AND status = ?
	`, instanceName, instanceName, userDisplayName, userDisplayName, at.UTC(), now, HubStatusBound)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r HubBindingRepository) RecordError(ctx context.Context, message string, at time.Time) error {
	res := r.db.WithContext(ctx).Exec(`
		UPDATE hub_binding SET
			last_error = ?,
			last_error_at = ?,
			updated_at = ?
		WHERE id = 1
	`, message, at.UTC(), time.Now().UTC())
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ClearBinding drops the account link and keeps the key pair.
func (r HubBindingRepository) ClearBinding(ctx context.Context, status, lastError string) error {
	if status == "" {
		status = HubStatusUnbound
	}
	now := time.Now().UTC()
	var lastErrAt any
	if lastError != "" {
		lastErrAt = now
	}
	res := r.db.WithContext(ctx).Exec(`
		UPDATE hub_binding SET
			instance_id = '',
			status = ?,
			pairing_code_hint = '',
			poll_token_enc = '',
			pairing_expires_at = NULL,
			user_display_name = '',
			instance_name = '',
			bound_at = NULL,
			last_error = ?,
			last_error_at = ?,
			lease_owner = '',
			lease_until = NULL,
			updated_at = ?
		WHERE id = 1
	`, status, lastError, lastErrAt, now)
	return res.Error
}

// TryAcquireSendLease claims the single sender slot. SQLite is treated as one process and always wins.
// The lease lives on the binding row so it stays inside the schema; a crashed owner expires with lease_until.
func (r HubBindingRepository) TryAcquireSendLease(ctx context.Context, owner string, ttl time.Duration) (bool, error) {
	if !r.postgres() {
		return true, nil
	}
	var id int
	err := r.db.WithContext(ctx).Raw(`
		UPDATE hub_binding
		SET lease_owner = ?, lease_until = NOW() + make_interval(secs => ?)
		WHERE id = 1
			AND (lease_until IS NULL OR lease_until < NOW() OR lease_owner = ?)
		RETURNING id
	`, owner, ttl.Seconds(), owner).Row().Scan(&id)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return id == 1, nil
}

func (r HubBindingRepository) RefreshSendLease(ctx context.Context, owner string, ttl time.Duration) (bool, error) {
	if !r.postgres() {
		return true, nil
	}
	var id int
	err := r.db.WithContext(ctx).Raw(`
		UPDATE hub_binding
		SET lease_until = NOW() + make_interval(secs => ?)
		WHERE id = 1 AND lease_owner = ?
		RETURNING id
	`, ttl.Seconds(), owner).Row().Scan(&id)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return id == 1, nil
}

func (r HubBindingRepository) ReleaseSendLease(ctx context.Context, owner string) error {
	if !r.postgres() {
		return nil
	}
	return r.db.WithContext(ctx).Exec(`
		UPDATE hub_binding SET lease_owner = '', lease_until = NULL
		WHERE id = 1 AND lease_owner = ?
	`, owner).Error
}
