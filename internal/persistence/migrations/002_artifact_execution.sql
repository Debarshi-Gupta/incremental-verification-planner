-- +goose Up
ALTER TABLE artifacts ADD COLUMN execution_id uuid;
ALTER TABLE artifacts ADD CONSTRAINT artifacts_execution_tenant_fk
  FOREIGN KEY(execution_id,tenant_id) REFERENCES executions(id,tenant_id) ON DELETE RESTRICT;
CREATE INDEX artifacts_execution ON artifacts(tenant_id,execution_id,created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS artifacts_execution;
ALTER TABLE artifacts DROP CONSTRAINT IF EXISTS artifacts_execution_tenant_fk;
ALTER TABLE artifacts DROP COLUMN IF EXISTS execution_id;
