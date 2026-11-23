package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BindingEntityConflictError means an object picked for a binding already
// belongs to another binding of the same scope, which holds one per object.
type BindingEntityConflictError struct {
	EntityType string
	EntityID   string
	Binding    JSPluginBinding
}

func (e *BindingEntityConflictError) Error() string {
	return "the object is already bound in this scope"
}

// JSPluginVersionState is the status of one installed version, without the package.
type JSPluginVersionState struct {
	PluginID string
	Version  string
	Status   string
	SelfTest JSON `gorm:"column:selftest;type:jsonb"`
}

// ListBindingsByScopeType returns every binding of one scope type, oldest first.
func (r JSPluginRepository) ListBindingsByScopeType(ctx context.Context, scopeType string) ([]JSPluginBinding, error) {
	var items []JSPluginBinding
	err := r.db.WithContext(ctx).Where("scope_type = ?", scopeType).Order("created_at ASC, id ASC").Find(&items).Error
	return items, err
}

// ListPluginBindingsByScope returns one plugin's bindings of one scope type, oldest first.
func (r JSPluginRepository) ListPluginBindingsByScope(ctx context.Context, pluginID, scopeType string) ([]JSPluginBinding, error) {
	var items []JSPluginBinding
	err := r.db.WithContext(ctx).Where("plugin_id = ? AND scope_type = ?", pluginID, scopeType).
		Order("created_at ASC, id ASC").Find(&items).Error
	return items, err
}

// ListBindingInputsByScopeType returns the picked objects of every binding of one scope type.
func (r JSPluginRepository) ListBindingInputsByScopeType(ctx context.Context, scopeType string) ([]JSPluginBindingInput, error) {
	var items []JSPluginBindingInput
	err := r.db.WithContext(ctx).Table("js_plugin_binding_inputs AS i").
		Select("i.*").
		Joins("JOIN js_plugin_bindings b ON b.id = i.binding_id").
		Where("b.scope_type = ?", scopeType).
		Order("i.binding_id ASC, i.input_name ASC, i.entity_id ASC").
		Find(&items).Error
	return items, err
}

// EntityBindingOwners maps each object of one type picked in a scope to the
// binding holding it.
func (r JSPluginRepository) EntityBindingOwners(ctx context.Context, scopeType, entityType string) (map[string]uuid.UUID, error) {
	var rows []struct {
		EntityID  string
		BindingID uuid.UUID
	}
	err := r.db.WithContext(ctx).Table("js_plugin_binding_inputs AS i").
		Select("i.entity_id, i.binding_id").
		Joins("JOIN js_plugin_bindings b ON b.id = i.binding_id").
		Where("b.scope_type = ? AND i.entity_type = ?", scopeType, entityType).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]uuid.UUID, len(rows))
	for _, row := range rows {
		out[row.EntityID] = row.BindingID
	}
	return out, nil
}

// CreateScopedBinding stores a binding of its own scope with its picked
// objects in one transaction. Its scope id and slot are its id. When
// exclusiveEntityType is set, an object of that type already picked by another
// binding of the scope is refused with *BindingEntityConflictError.
func (r JSPluginRepository) CreateScopedBinding(ctx context.Context, binding JSPluginBinding, inputs []JSPluginBindingInput, exclusiveEntityType string) (JSPluginBinding, error) {
	if binding.ID == uuid.Nil {
		binding.ID = uuid.New()
	}
	binding.ScopeID = binding.ID.String()
	binding.Slot = binding.ID.String()
	if len(binding.Config) == 0 {
		binding.Config = JSON("{}")
	}
	binding.Events = JSON("[]")
	binding.State = JSON("{}")
	binding.NextTickAt = nil
	if binding.CreatedAt.IsZero() {
		binding.CreatedAt = time.Now().UTC()
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockScopedEntities(tx, binding.ScopeType, uuid.Nil, inputs, exclusiveEntityType); err != nil {
			return err
		}
		if err := tx.Create(&binding).Error; err != nil {
			return err
		}
		return replaceInputs(tx, binding.ID, inputs)
	})
	return binding, err
}

// UpdateScopedBinding replaces a binding's config and picked objects, with the
// same exclusive check as CreateScopedBinding.
func (r JSPluginRepository) UpdateScopedBinding(ctx context.Context, pluginID string, id uuid.UUID, scopeType string, config JSON, inputs []JSPluginBindingInput, exclusiveEntityType string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockScopedEntities(tx, scopeType, id, inputs, exclusiveEntityType); err != nil {
			return err
		}
		result := tx.Model(&JSPluginBinding{}).Where("id = ? AND plugin_id = ? AND scope_type = ?", id, pluginID, scopeType).
			Update("config", config)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return replaceInputs(tx, id, inputs)
	})
}

// DeleteScopedBinding removes one binding of a plugin in a scope, with its inputs.
func (r JSPluginRepository) DeleteScopedBinding(ctx context.Context, pluginID string, id uuid.UUID, scopeType string) error {
	result := r.db.WithContext(ctx).Where("id = ? AND plugin_id = ? AND scope_type = ?", id, pluginID, scopeType).Delete(&JSPluginBinding{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// SetBindingConfig replaces the config of one binding.
func (r JSPluginRepository) SetBindingConfig(ctx context.Context, id uuid.UUID, config JSON) error {
	return r.db.WithContext(ctx).Model(&JSPluginBinding{}).Where("id = ?", id).Update("config", config).Error
}

// ListVersionStates returns the status and self-test record of every installed version.
func (r JSPluginRepository) ListVersionStates(ctx context.Context) ([]JSPluginVersionState, error) {
	var items []JSPluginVersionState
	err := r.db.WithContext(ctx).Table("js_plugin_versions").
		Select("plugin_id", "version", "status", "selftest").
		Find(&items).Error
	return items, err
}

// lockScopedEntities serialises writers of one scope and refuses objects of
// exclusiveEntityType that another binding of the scope already picked.
func lockScopedEntities(tx *gorm.DB, scopeType string, self uuid.UUID, inputs []JSPluginBindingInput, exclusiveEntityType string) error {
	if exclusiveEntityType == "" {
		return nil
	}
	if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, "js_plugin_bindings:"+scopeType).Error; err != nil {
		return err
	}
	var ids []string
	for _, input := range inputs {
		if input.EntityType == exclusiveEntityType {
			ids = append(ids, input.EntityID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var taken struct {
		EntityID  string
		BindingID uuid.UUID
	}
	err := tx.Table("js_plugin_binding_inputs AS i").
		Select("i.entity_id, i.binding_id").
		Joins("JOIN js_plugin_bindings b ON b.id = i.binding_id").
		Where("b.scope_type = ? AND i.entity_type = ? AND i.entity_id IN ? AND b.id <> ?", scopeType, exclusiveEntityType, ids, self).
		Order("i.entity_id ASC").
		Limit(1).
		Scan(&taken).Error
	if err != nil {
		return err
	}
	if taken.EntityID == "" {
		return nil
	}
	var owner JSPluginBinding
	if err := tx.Where("id = ?", taken.BindingID).First(&owner).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return &BindingEntityConflictError{EntityType: exclusiveEntityType, EntityID: taken.EntityID, Binding: owner}
}
