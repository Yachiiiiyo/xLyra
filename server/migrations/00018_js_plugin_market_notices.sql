-- +goose Up
ALTER TABLE js_plugin_market_subscriptions
  ADD COLUMN notified_version TEXT NOT NULL DEFAULT '',
  ADD COLUMN notified_revocations TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE js_plugin_market_subscriptions DROP COLUMN IF EXISTS notified_revocations;
ALTER TABLE js_plugin_market_subscriptions DROP COLUMN IF EXISTS notified_version;
