package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

const (
	JSPluginStorageScopeGlobal     = "global"
	JSPluginStorageScopeSite       = "site"
	JSPluginStorageScopeCredential = "credential"

	JSPluginStorageMaxKeysPerScope  = 64
	JSPluginStorageMaxBytesPerScope = 64 << 10
)

var (
	ErrJSPluginStorageKeyLimit  = errors.New("plugin storage key limit exceeded")
	ErrJSPluginStorageByteLimit = errors.New("plugin storage capacity limit exceeded")
)

type JSPluginStorageEntry struct {
	PluginID  string `gorm:"primaryKey"`
	ScopeType string `gorm:"primaryKey"`
	ScopeID   string `gorm:"primaryKey"`
	Key       string `gorm:"primaryKey"`
	Value     JSON   `gorm:"type:jsonb"`
	ByteSize  int
	UpdatedAt time.Time
}

func (JSPluginStorageEntry) TableName() string { return "js_plugin_storage" }

type JSPluginStorageRepository struct {
	db *gorm.DB
}

func NewJSPluginStorageRepository(db *gorm.DB) JSPluginStorageRepository {
	return JSPluginStorageRepository{db: db}
}

func (r JSPluginStorageRepository) Get(ctx context.Context, pluginID, scopeType, scopeID, key string) (JSON, bool, error) {
	var row JSPluginStorageEntry
	err := r.db.WithContext(ctx).
		Where("plugin_id = ? AND scope_type = ? AND scope_id = ? AND key = ?", pluginID, scopeType, scopeID, key).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return row.Value, true, nil
}

func (r JSPluginStorageRepository) Keys(ctx context.Context, pluginID, scopeType, scopeID string) ([]string, error) {
	var keys []string
	err := r.db.WithContext(ctx).
		Model(&JSPluginStorageEntry{}).
		Where("plugin_id = ? AND scope_type = ? AND scope_id = ?", pluginID, scopeType, scopeID).
		Order("key ASC").
		Pluck("key", &keys).Error
	return keys, err
}

func (r JSPluginStorageRepository) Set(ctx context.Context, pluginID, scopeType, scopeID, key string, value JSON) error {
	entryBytes := len(key) + len(value)
	if entryBytes > JSPluginStorageMaxBytesPerScope {
		return ErrJSPluginStorageByteLimit
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []struct {
			Key      string
			ByteSize int
		}
		if err := tx.Raw(`
			SELECT key, byte_size
			FROM js_plugin_storage
			WHERE plugin_id = ? AND scope_type = ? AND scope_id = ?
			FOR UPDATE
		`, pluginID, scopeType, scopeID).Scan(&rows).Error; err != nil {
			return err
		}
		totalBytes := 0
		exists := false
		for _, row := range rows {
			if row.Key == key {
				exists = true
				continue
			}
			totalBytes += row.ByteSize
		}
		if !exists && len(rows) >= JSPluginStorageMaxKeysPerScope {
			return ErrJSPluginStorageKeyLimit
		}
		if totalBytes+entryBytes > JSPluginStorageMaxBytesPerScope {
			return ErrJSPluginStorageByteLimit
		}
		return tx.Exec(`
			INSERT INTO js_plugin_storage (plugin_id, scope_type, scope_id, key, value, byte_size, updated_at)
			VALUES (?, ?, ?, ?, ?::jsonb, ?, ?)
			ON CONFLICT (plugin_id, scope_type, scope_id, key) DO UPDATE SET
				value = EXCLUDED.value,
				byte_size = EXCLUDED.byte_size,
				updated_at = EXCLUDED.updated_at
		`, pluginID, scopeType, scopeID, key, string(value), entryBytes, time.Now().UTC()).Error
	})
}

func (r JSPluginStorageRepository) Delete(ctx context.Context, pluginID, scopeType, scopeID, key string) (bool, error) {
	res := r.db.WithContext(ctx).
		Where("plugin_id = ? AND scope_type = ? AND scope_id = ? AND key = ?", pluginID, scopeType, scopeID, key).
		Delete(&JSPluginStorageEntry{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}
