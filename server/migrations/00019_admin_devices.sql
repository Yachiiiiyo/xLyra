-- +goose Up
CREATE TABLE admin_devices (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  admin_id UUID NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
  name TEXT NOT NULL DEFAULT '',
  platform TEXT NOT NULL DEFAULT '',
  token_hash TEXT NOT NULL UNIQUE,
  last_seen_at TIMESTAMPTZ,
  last_ip INET,
  last_user_agent TEXT NOT NULL DEFAULT '',
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX admin_devices_admin_id_idx ON admin_devices (admin_id);
CREATE INDEX admin_devices_revoked_at_idx ON admin_devices (revoked_at);

CREATE TABLE admin_device_pairings (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  admin_id UUID NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
  code_hash TEXT NOT NULL UNIQUE,
  expires_at TIMESTAMPTZ NOT NULL,
  used_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX admin_device_pairings_admin_id_idx ON admin_device_pairings (admin_id);
CREATE INDEX admin_device_pairings_expires_at_idx ON admin_device_pairings (expires_at);

CREATE TRIGGER admin_devices_set_updated_at
BEFORE UPDATE ON admin_devices
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE IF EXISTS admin_device_pairings;
DROP TABLE IF EXISTS admin_devices;
