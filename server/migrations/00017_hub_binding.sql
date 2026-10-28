-- +goose Up
CREATE TABLE hub_binding (
  id SMALLINT PRIMARY KEY CHECK (id = 1),
  public_key TEXT NOT NULL,
  private_key_enc TEXT NOT NULL,
  instance_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK (status IN ('unbound', 'pairing', 'bound', 'revoked')),
  pairing_code_hint TEXT NOT NULL DEFAULT '',
  poll_token_enc TEXT NOT NULL DEFAULT '',
  pairing_expires_at TIMESTAMPTZ,
  hub_url TEXT NOT NULL DEFAULT '',
  user_display_name TEXT NOT NULL DEFAULT '',
  instance_name TEXT NOT NULL DEFAULT '',
  bound_at TIMESTAMPTZ,
  last_sync_at TIMESTAMPTZ,
  last_error TEXT NOT NULL DEFAULT '',
  last_error_at TIMESTAMPTZ,
  lease_owner TEXT NOT NULL DEFAULT '',
  lease_until TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS hub_binding;
