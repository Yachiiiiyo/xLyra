-- +goose Up
ALTER TABLE admin_devices
  ADD COLUMN apns_token_enc TEXT NOT NULL DEFAULT '',
  ADD COLUMN apns_env TEXT NOT NULL DEFAULT '',
  ADD COLUMN push_prefs JSONB NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN push_registered_at TIMESTAMPTZ,
  ADD COLUMN push_synced_at TIMESTAMPTZ,
  ADD COLUMN push_state TEXT NOT NULL DEFAULT '',
  ADD COLUMN push_state_at TIMESTAMPTZ;

ALTER TABLE admin_devices
  ADD CONSTRAINT admin_devices_apns_env_check CHECK (apns_env IN ('', 'sandbox', 'production')),
  ADD CONSTRAINT admin_devices_push_state_check CHECK (push_state IN ('', 'ok', 'invalid', 'error'));

-- +goose Down
ALTER TABLE admin_devices DROP CONSTRAINT IF EXISTS admin_devices_push_state_check;
ALTER TABLE admin_devices DROP CONSTRAINT IF EXISTS admin_devices_apns_env_check;
ALTER TABLE admin_devices
  DROP COLUMN IF EXISTS push_state_at,
  DROP COLUMN IF EXISTS push_state,
  DROP COLUMN IF EXISTS push_synced_at,
  DROP COLUMN IF EXISTS push_registered_at,
  DROP COLUMN IF EXISTS push_prefs,
  DROP COLUMN IF EXISTS apns_env,
  DROP COLUMN IF EXISTS apns_token_enc;
