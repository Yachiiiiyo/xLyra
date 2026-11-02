-- +goose Up
ALTER TABLE js_plugins DROP CONSTRAINT IF EXISTS js_plugins_source_check;
ALTER TABLE js_plugins ADD CONSTRAINT js_plugins_source_check CHECK (source IN ('builtin', 'uploaded', 'market'));

ALTER TABLE js_plugin_versions
  ADD COLUMN origin TEXT NOT NULL DEFAULT 'manual' CHECK (origin IN ('manual', 'market')),
  ADD COLUMN attestation JSONB;

CREATE TABLE js_plugin_market_subscriptions (
  plugin_id TEXT PRIMARY KEY REFERENCES js_plugins(id) ON DELETE CASCADE,
  update_policy TEXT NOT NULL DEFAULT 'notify' CHECK (update_policy IN ('off', 'notify', 'auto')),
  paused_reason TEXT,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE js_plugin_market_state (
  id SMALLINT PRIMARY KEY CHECK (id = 1),
  root_seq BIGINT NOT NULL DEFAULT 0,
  root_payload_sha256 TEXT NOT NULL DEFAULT '',
  root_envelope BYTEA,
  root_etag TEXT NOT NULL DEFAULT '',
  index_seq BIGINT NOT NULL DEFAULT 0,
  index_payload_sha256 TEXT NOT NULL DEFAULT '',
  index_envelope BYTEA,
  index_etag TEXT NOT NULL DEFAULT '',
  revocations_seq BIGINT NOT NULL DEFAULT 0,
  revocations_payload_sha256 TEXT NOT NULL DEFAULT '',
  revocations_envelope BYTEA,
  revocations_etag TEXT NOT NULL DEFAULT '',
  index_synced_at TIMESTAMPTZ,
  revocations_synced_at TIMESTAMPTZ,
  last_attempt_at TIMESTAMPTZ,
  last_error TEXT NOT NULL DEFAULT '',
  source_url TEXT NOT NULL DEFAULT '',
  lease_owner TEXT NOT NULL DEFAULT '',
  lease_until TIMESTAMPTZ
);

CREATE TABLE js_plugin_auto_updates (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  plugin_id TEXT NOT NULL REFERENCES js_plugins(id) ON DELETE CASCADE,
  from_version TEXT NOT NULL,
  to_version TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('applied', 'rolled_back', 'failed')),
  reason TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX js_plugin_auto_updates_plugin_created_idx
  ON js_plugin_auto_updates (plugin_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS js_plugin_auto_updates;
DROP TABLE IF EXISTS js_plugin_market_subscriptions;
DROP TABLE IF EXISTS js_plugin_market_state;

ALTER TABLE js_plugin_versions DROP COLUMN IF EXISTS attestation;
ALTER TABLE js_plugin_versions DROP COLUMN IF EXISTS origin;

ALTER TABLE js_plugins DROP CONSTRAINT IF EXISTS js_plugins_source_check;
ALTER TABLE js_plugins ADD CONSTRAINT js_plugins_source_check CHECK (source IN ('builtin', 'uploaded'));
