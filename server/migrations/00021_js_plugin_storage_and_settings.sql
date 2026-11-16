-- +goose Up
ALTER TABLE js_plugins
  ADD COLUMN IF NOT EXISTS settings JSONB NOT NULL DEFAULT '{}'::jsonb;

CREATE TABLE js_plugin_storage (
  plugin_id TEXT NOT NULL REFERENCES js_plugins(id) ON DELETE CASCADE,
  scope_type TEXT NOT NULL CHECK (scope_type IN ('global', 'site', 'credential')),
  scope_id TEXT NOT NULL DEFAULT '',
  key TEXT NOT NULL,
  value JSONB NOT NULL,
  byte_size INTEGER NOT NULL CHECK (byte_size >= 0),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (plugin_id, scope_type, scope_id, key)
);

CREATE INDEX js_plugin_storage_scope_idx
  ON js_plugin_storage (plugin_id, scope_type, scope_id);

-- +goose Down
DROP TABLE IF EXISTS js_plugin_storage;
ALTER TABLE js_plugins DROP COLUMN IF EXISTS settings;
