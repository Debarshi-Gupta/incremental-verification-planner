-- +goose Up
-- LLD 23 scenario 6: a coverage report that belongs to a previous dependency
-- version must be marked stale. The dependency version (lockfile digest) is
-- therefore part of the snapshot identity and of every coverage observation.
ALTER TABLE repository_snapshots ADD COLUMN dependency_digest text NOT NULL DEFAULT '';
ALTER TABLE repository_snapshots ADD COLUMN adapter_versions_json jsonb NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE coverage_observations ADD COLUMN dependency_digest text NOT NULL DEFAULT '';
ALTER TABLE coverage_observations ADD COLUMN adapter_version text NOT NULL DEFAULT '';

-- The snapshot identity now includes the dependency version and the adapter
-- versions that produced the snapshot's adapter-specific evidence: the same
-- tree indexed against a different dependency set is a different snapshot.
DROP INDEX IF EXISTS repository_snapshot_identity;
CREATE UNIQUE INDEX repository_snapshot_identity ON repository_snapshots
  (repository_id,commit_sha,base_sha,tree_hash,worktree_hash,submodule_digest,toolchain_digest,config_digest,test_config_digest,adapter_digest,dependency_digest,adapter_versions_json,graph_version,completeness_json);

-- LLD 18.4: a major algorithm or policy change resets CI selective
-- eligibility to OBSERVE until the repository is re-calibrated. The reset
-- state and the calibration counters observed at reset time are recorded per
-- repository so that pre-change evidence cannot re-enable selective gating.
CREATE TABLE repository_upgrade_state (
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  repository_id uuid NOT NULL,
  algorithm_version text NOT NULL DEFAULT '',
  policy_version bigint NOT NULL DEFAULT 0,
  reset_at timestamptz,
  calibration_runs_at_reset integer NOT NULL DEFAULT 0 CHECK(calibration_runs_at_reset >= 0),
  audit_runs_at_reset integer NOT NULL DEFAULT 0 CHECK(audit_runs_at_reset >= 0),
  reason text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(tenant_id,repository_id),
  FOREIGN KEY(repository_id,tenant_id) REFERENCES repositories(id,tenant_id) ON DELETE CASCADE
);

-- +goose StatementBegin
ALTER TABLE repository_upgrade_state ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON repository_upgrade_state USING (tenant_id = nullif(current_setting('ivp.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = nullif(current_setting('ivp.tenant_id', true), '')::uuid);
-- +goose StatementEnd

-- +goose Down
DROP POLICY IF EXISTS tenant_isolation ON repository_upgrade_state;
DROP TABLE IF EXISTS repository_upgrade_state;

DROP INDEX IF EXISTS repository_snapshot_identity;
CREATE UNIQUE INDEX repository_snapshot_identity ON repository_snapshots
  (repository_id,commit_sha,base_sha,tree_hash,worktree_hash,submodule_digest,toolchain_digest,config_digest,test_config_digest,adapter_digest,graph_version,completeness_json);

ALTER TABLE coverage_observations DROP COLUMN IF EXISTS adapter_version;
ALTER TABLE coverage_observations DROP COLUMN IF EXISTS dependency_digest;
ALTER TABLE repository_snapshots DROP COLUMN IF EXISTS adapter_versions_json;
ALTER TABLE repository_snapshots DROP COLUMN IF EXISTS dependency_digest;
