-- +goose Up
-- A gateway pipeline binding attaches a plugin to downstream API keys. It is a
-- binding of its own scope: its slot and scope id are the binding id, and the
-- picked keys are rows of js_plugin_binding_inputs.
ALTER TABLE js_plugin_bindings DROP CONSTRAINT js_plugin_bindings_scope_type_check;
ALTER TABLE js_plugin_bindings ADD CONSTRAINT js_plugin_bindings_scope_type_check
  CHECK (scope_type IN ('endpoint', 'automation', 'gateway_pipeline'));
CREATE INDEX js_plugin_binding_inputs_entity_idx
  ON js_plugin_binding_inputs (entity_type, entity_id);

-- +goose Down
DROP INDEX IF EXISTS js_plugin_binding_inputs_entity_idx;
DELETE FROM js_plugin_bindings WHERE scope_type = 'gateway_pipeline';
ALTER TABLE js_plugin_bindings DROP CONSTRAINT js_plugin_bindings_scope_type_check;
ALTER TABLE js_plugin_bindings ADD CONSTRAINT js_plugin_bindings_scope_type_check
  CHECK (scope_type IN ('endpoint', 'automation'));
