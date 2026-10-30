-- +goose Up
CREATE TABLE accepted_change_outcomes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  repository_id uuid NOT NULL,
  change_key text NOT NULL,
  started_at timestamptz NOT NULL,
  accepted_at timestamptz NOT NULL,
  accepted_correct boolean NOT NULL,
  compute_ms bigint NOT NULL DEFAULT 0 CHECK(compute_ms >= 0),
  cost_microusd bigint NOT NULL DEFAULT 0 CHECK(cost_microusd >= 0),
  human_correction_minutes bigint NOT NULL DEFAULT 0 CHECK(human_correction_minutes >= 0),
  execution_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK(accepted_at >= started_at),
  FOREIGN KEY(repository_id,tenant_id) REFERENCES repositories(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(execution_id,tenant_id) REFERENCES executions(id,tenant_id) ON DELETE RESTRICT
);
CREATE INDEX accepted_changes_repository_time ON accepted_change_outcomes(tenant_id,repository_id,accepted_at DESC);
CREATE UNIQUE INDEX accepted_changes_change_key ON accepted_change_outcomes(tenant_id,repository_id,change_key);
CREATE TRIGGER immutable_accepted_change BEFORE UPDATE OR DELETE ON accepted_change_outcomes FOR EACH ROW EXECUTE FUNCTION ivp_reject_immutable_mutation();
ALTER TABLE accepted_change_outcomes ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON accepted_change_outcomes
  USING (tenant_id = nullif(current_setting('ivp.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = nullif(current_setting('ivp.tenant_id', true), '')::uuid);

-- +goose Down
DROP POLICY IF EXISTS tenant_isolation ON accepted_change_outcomes;
DROP TRIGGER IF EXISTS immutable_accepted_change ON accepted_change_outcomes;
DROP INDEX IF EXISTS accepted_changes_repository_time;
DROP INDEX IF EXISTS accepted_changes_change_key;
DROP TABLE IF EXISTS accepted_change_outcomes;
