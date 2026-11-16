package jsplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"xlyra/server/internal/store"
)

const (
	PermissionStorage = "storage"

	StorageScopeGlobal     = store.JSPluginStorageScopeGlobal
	StorageScopeSite       = store.JSPluginStorageScopeSite
	StorageScopeCredential = store.JSPluginStorageScopeCredential

	maxStorageKeyBytes = 128
)

var (
	// ErrPermissionDenied is thrown to JS as permission_denied when a plugin
	// calls a capability it did not declare.
	ErrPermissionDenied = errors.New("permission_denied")
	// ErrSecretLeakRejected is thrown when storage.set receives a value that
	// contains a secret from the active SecretBag.
	ErrSecretLeakRejected = errors.New("secret_leak_rejected")
)

func permissionDeniedError(detail string) error {
	return fmt.Errorf("%w: %s", ErrPermissionDenied, detail)
}

func validateManifestPermissions(permissions []string) error {
	if len(permissions) == 0 {
		return nil
	}
	if len(permissions) > 32 {
		return fmt.Errorf("permissions must list at most 32 entries")
	}
	seen := make(map[string]struct{}, len(permissions))
	for i, raw := range permissions {
		perm := strings.TrimSpace(raw)
		if perm == "" || perm != raw {
			return fmt.Errorf("permissions[%d] %q is invalid", i, raw)
		}
		if _, dup := seen[perm]; dup {
			return fmt.Errorf("permissions: duplicate entry %q", perm)
		}
		seen[perm] = struct{}{}
		if !validManifestPermission(perm) {
			return fmt.Errorf("permissions: unsupported permission %q", perm)
		}
	}
	return nil
}

func validManifestPermission(perm string) bool {
	switch perm {
	case PermissionStorage, "secret.use", "http", "schedule":
		return true
	}
	if host, ok := strings.CutPrefix(perm, "net:"); ok {
		host = strings.TrimPrefix(host, "*.")
		return validSiteProviderHost(host) == nil
	}
	if event, ok := strings.CutPrefix(perm, "event:"); ok {
		return event != "" && len(event) <= 64 && !strings.ContainsAny(event, " \t\r\n")
	}
	return false
}

// StorageBackend backs the synchronous JS storage.* host API for one hook call.
type StorageBackend interface {
	Get(ctx context.Context, key string) (any, error)
	Set(ctx context.Context, key string, value any) error
	Delete(ctx context.Context, key string) (bool, error)
	Keys(ctx context.Context) ([]string, error)
}

func validateStorageKey(key string) error {
	if key == "" || len(key) > maxStorageKeyBytes || !utf8.ValidString(key) {
		return fmt.Errorf("storage key must be 1..%d valid utf-8 bytes", maxStorageKeyBytes)
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return errors.New("storage key must not contain control characters")
		}
	}
	return nil
}

func encodeStorageValue(value any, bag *SecretBag) ([]byte, error) {
	if value == nil {
		return []byte("null"), nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("storage value must be JSON: %w", err)
	}
	if len(encoded) > MaxStorageBytesPerScope {
		return nil, fmt.Errorf("storage value exceeds %d bytes", MaxStorageBytesPerScope)
	}
	if bag != nil && bag.ContainsSecret(string(encoded)) {
		return nil, fmt.Errorf("%w: storage value contains a credential secret", ErrSecretLeakRejected)
	}
	return encoded, nil
}

// MemoryStorage is an isolated in-memory StorageBackend used by fixtures and tests.
type MemoryStorage struct {
	mu      sync.Mutex
	entries map[string][]byte
}

func NewMemoryStorage(initial map[string]any) (*MemoryStorage, error) {
	ms := &MemoryStorage{entries: map[string][]byte{}}
	for key, val := range initial {
		if err := ms.Set(context.Background(), key, val); err != nil {
			return nil, err
		}
	}
	return ms, nil
}

func (m *MemoryStorage) Get(_ context.Context, key string) (any, error) {
	if err := validateStorageKey(key); err != nil {
		return nil, err
	}
	m.mu.Lock()
	raw, ok := m.entries[key]
	m.mu.Unlock()
	if !ok {
		return nil, nil
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (m *MemoryStorage) Set(_ context.Context, key string, value any) error {
	if err := validateStorageKey(key); err != nil {
		return err
	}
	encoded, err := encodeStorageValue(value, nil)
	if err != nil {
		return err
	}
	entryBytes := len(key) + len(encoded)
	if entryBytes > MaxStorageBytesPerScope {
		return store.ErrJSPluginStorageByteLimit
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, exists := m.entries[key]
	if !exists && len(m.entries) >= MaxStorageKeysPerScope {
		return store.ErrJSPluginStorageKeyLimit
	}
	total := 0
	for k, v := range m.entries {
		if k == key {
			continue
		}
		total += len(k) + len(v)
	}
	if total+entryBytes > MaxStorageBytesPerScope {
		return store.ErrJSPluginStorageByteLimit
	}
	m.entries[key] = encoded
	return nil
}

func (m *MemoryStorage) Delete(_ context.Context, key string) (bool, error) {
	if err := validateStorageKey(key); err != nil {
		return false, err
	}
	m.mu.Lock()
	_, exists := m.entries[key]
	delete(m.entries, key)
	m.mu.Unlock()
	return exists, nil
}

func (m *MemoryStorage) Keys(_ context.Context) ([]string, error) {
	m.mu.Lock()
	out := make([]string, 0, len(m.entries))
	for k := range m.entries {
		out = append(out, k)
	}
	m.mu.Unlock()
	sort.Strings(out)
	return out, nil
}

// Snapshot returns a decoded copy of every stored entry for fixture assertions.
func (m *MemoryStorage) Snapshot() (map[string]any, error) {
	if m == nil {
		return map[string]any{}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]any, len(m.entries))
	for k, raw := range m.entries {
		var val any
		if err := json.Unmarshal(raw, &val); err != nil {
			return nil, err
		}
		out[k] = val
	}
	return out, nil
}

// GuardStorage wraps inner so every Set checks value against bag for secret leaks.
func GuardStorage(inner StorageBackend, bag *SecretBag) StorageBackend {
	if inner == nil || bag == nil {
		return inner
	}
	return guardedStorage{inner: inner, bag: bag}
}

type guardedStorage struct {
	inner StorageBackend
	bag   *SecretBag
}

func (g guardedStorage) Get(ctx context.Context, key string) (any, error) {
	return g.inner.Get(ctx, key)
}

func (g guardedStorage) Set(ctx context.Context, key string, value any) error {
	if _, err := encodeStorageValue(value, g.bag); err != nil {
		return err
	}
	return g.inner.Set(ctx, key, value)
}

func (g guardedStorage) Delete(ctx context.Context, key string) (bool, error) {
	return g.inner.Delete(ctx, key)
}

func (g guardedStorage) Keys(ctx context.Context) ([]string, error) {
	return g.inner.Keys(ctx)
}

type StoreStorage struct {
	repo      store.JSPluginStorageRepository
	pluginID  string
	scopeType string
	scopeID   string
}

func NewStoreStorage(repo store.JSPluginStorageRepository, pluginID, scopeType, scopeID string) *StoreStorage {
	if scopeType == "" {
		scopeType = StorageScopeGlobal
	}
	return &StoreStorage{
		repo:      repo,
		pluginID:  pluginID,
		scopeType: scopeType,
		scopeID:   scopeID,
	}
}

func (s *StoreStorage) Get(ctx context.Context, key string) (any, error) {
	if err := validateStorageKey(key); err != nil {
		return nil, err
	}
	raw, ok, err := s.repo.Get(ctx, s.pluginID, s.scopeType, s.scopeID, key)
	if err != nil || !ok {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *StoreStorage) Set(ctx context.Context, key string, value any) error {
	if err := validateStorageKey(key); err != nil {
		return err
	}
	encoded, err := encodeStorageValue(value, nil)
	if err != nil {
		return err
	}
	return s.repo.Set(ctx, s.pluginID, s.scopeType, s.scopeID, key, store.JSON(encoded))
}

func (s *StoreStorage) Delete(ctx context.Context, key string) (bool, error) {
	if err := validateStorageKey(key); err != nil {
		return false, err
	}
	return s.repo.Delete(ctx, s.pluginID, s.scopeType, s.scopeID, key)
}

func (s *StoreStorage) Keys(ctx context.Context) ([]string, error) {
	return s.repo.Keys(ctx, s.pluginID, s.scopeType, s.scopeID)
}
