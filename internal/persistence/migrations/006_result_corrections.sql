-- +goose Up
-- LLD 12.4: results are append-only. Correcting an incorrectly parsed
-- outcome creates a superseding record carrying the audit reason; the
-- original row is never overwritten or deleted.
CREATE TABLE result_corrections (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  execution_id uuid NOT NULL, test_unit_key text NOT NULL, attempt integer NOT NULL CHECK(attempt > 0),
  superseded_status text NOT NULL, superseding_status text NOT NULL,
  reason text NOT NULL CHECK(length(reason) > 0), corrected_by uuid REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(id,tenant_id),
  FOREIGN KEY(execution_id,tenant_id) REFERENCES executions(id,tenant_id) ON DELETE CASCADE
);
CREATE INDEX result_corrections_by_execution ON result_corrections(execution_id,test_unit_key,attempt,created_at DESC);

-- A correction is append-only evidence: it can be added, never changed or
-- removed, exactly like the result rows it supersedes.
CREATE TRIGGER immutable_row BEFORE UPDATE OR DELETE ON result_corrections FOR EACH ROW EXECUTE FUNCTION ivp_reject_immutable_mutation();

-- Every tenant-owned table carries tenant_id and enforces it in PostgreSQL as
-- defense in depth.
ALTER TABLE result_corrections ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON result_corrections USING (tenant_id = nullif(current_setting('ivp.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = nullif(current_setting('ivp.tenant_id', true), '')::uuid);

-- +goose Down
DROP POLICY IF EXISTS tenant_isolation ON result_corrections;
DROP TRIGGER IF EXISTS immutable_row ON result_corrections;
DROP INDEX IF EXISTS result_corrections_by_execution;
DROP TABLE IF EXISTS result_corrections;
