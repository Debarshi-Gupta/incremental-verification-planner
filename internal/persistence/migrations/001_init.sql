-- +goose Up
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE tenants (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200), created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE users (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), oidc_subject text NOT NULL UNIQUE, email_hash text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(), disabled_at timestamptz
);
CREATE TABLE tenant_memberships (
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role text NOT NULL CHECK (role IN ('reader','executor','maintainer','auditor','admin')), created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id,user_id)
);
CREATE TABLE repositories (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  slug text NOT NULL CHECK (length(slug) BETWEEN 1 AND 200), canonical_url text NOT NULL, default_branch text NOT NULL DEFAULT 'main',
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE (tenant_id,slug), UNIQUE (id,tenant_id)
);
CREATE TABLE repository_policies (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  repository_id uuid NOT NULL, version bigint NOT NULL CHECK(version > 0), policy_json jsonb NOT NULL,
  policy_hash text NOT NULL, created_by uuid REFERENCES users(id), created_at timestamptz NOT NULL DEFAULT now(), is_active boolean NOT NULL DEFAULT false,
  FOREIGN KEY(repository_id,tenant_id) REFERENCES repositories(id,tenant_id) ON DELETE CASCADE,
  UNIQUE(repository_id,version), UNIQUE(id,repository_id)
);
CREATE UNIQUE INDEX repository_one_active_policy ON repository_policies(repository_id) WHERE is_active;
CREATE TABLE repository_snapshots (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, repository_id uuid NOT NULL,
  commit_sha text NOT NULL, base_sha text NOT NULL DEFAULT '', tree_hash text NOT NULL, worktree_hash text NOT NULL DEFAULT '',
  submodule_digest text NOT NULL DEFAULT '', toolchain_digest text NOT NULL, config_digest text NOT NULL, adapter_digest text NOT NULL,
  graph_version bigint NOT NULL, completeness_json jsonb NOT NULL DEFAULT '{}'::jsonb, created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(repository_id,tenant_id) REFERENCES repositories(id,tenant_id) ON DELETE CASCADE,
  UNIQUE(id,repository_id), UNIQUE(id,tenant_id),
  UNIQUE(repository_id,commit_sha,base_sha,tree_hash,worktree_hash,submodule_digest,toolchain_digest,config_digest,adapter_digest,graph_version,completeness_json)
);
CREATE TABLE graph_nodes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  snapshot_id uuid NOT NULL, stable_key text NOT NULL, node_type text NOT NULL, path text NOT NULL DEFAULT '', display_name text NOT NULL DEFAULT '',
  confidence double precision NOT NULL CHECK(confidence >= 0 AND confidence <= 1), evidence_source text NOT NULL,
  observed_at timestamptz NOT NULL, valid_until timestamptz, attributes_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  FOREIGN KEY(snapshot_id,tenant_id) REFERENCES repository_snapshots(id,tenant_id) ON DELETE CASCADE,
  UNIQUE(id,snapshot_id), UNIQUE(snapshot_id,node_type,stable_key)
);
CREATE TABLE graph_edges (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  snapshot_id uuid NOT NULL, from_node_id uuid NOT NULL, to_node_id uuid NOT NULL, edge_type text NOT NULL,
  confidence double precision NOT NULL CHECK(confidence >= 0 AND confidence <= 1), evidence_source text NOT NULL,
  observed_at timestamptz NOT NULL, valid_until timestamptz, attributes_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  FOREIGN KEY(snapshot_id,tenant_id) REFERENCES repository_snapshots(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(from_node_id,snapshot_id) REFERENCES graph_nodes(id,snapshot_id) ON DELETE CASCADE,
  FOREIGN KEY(to_node_id,snapshot_id) REFERENCES graph_nodes(id,snapshot_id) ON DELETE CASCADE,
  UNIQUE(snapshot_id,from_node_id,to_node_id,edge_type)
);
CREATE INDEX graph_edges_outbound ON graph_edges(snapshot_id,from_node_id,edge_type);
CREATE INDEX graph_edges_inbound ON graph_edges(snapshot_id,to_node_id,edge_type);
CREATE TABLE test_units (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  snapshot_id uuid NOT NULL, stable_key text NOT NULL, unit_type text NOT NULL, adapter text NOT NULL, module_key text NOT NULL DEFAULT '',
  mandatory boolean NOT NULL DEFAULT false, risk_labels_json jsonb NOT NULL DEFAULT '[]'::jsonb, selectable boolean NOT NULL DEFAULT true,
  granularity text NOT NULL DEFAULT '', metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  FOREIGN KEY(snapshot_id,tenant_id) REFERENCES repository_snapshots(id,tenant_id) ON DELETE CASCADE,
  UNIQUE(id,snapshot_id), UNIQUE(snapshot_id,stable_key)
);
CREATE TABLE coverage_observations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  snapshot_id uuid NOT NULL, test_unit_id uuid NOT NULL, covered_node_id uuid NOT NULL, precision text NOT NULL,
  source text NOT NULL, report_hash text NOT NULL, run_id text NOT NULL DEFAULT '', environment_digest text NOT NULL,
	 build_config_digest text NOT NULL DEFAULT '', test_config_digest text NOT NULL DEFAULT '', line_start integer NOT NULL DEFAULT 0,
	 line_end integer NOT NULL DEFAULT 0, suite_level boolean NOT NULL DEFAULT false, test_specific boolean NOT NULL DEFAULT false,
	 evidence_confidence double precision NOT NULL DEFAULT 0 CHECK(evidence_confidence >= 0 AND evidence_confidence <= 1),
	 observed_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(test_unit_id,snapshot_id) REFERENCES test_units(id,snapshot_id) ON DELETE CASCADE,
  FOREIGN KEY(covered_node_id,snapshot_id) REFERENCES graph_nodes(id,snapshot_id) ON DELETE CASCADE,
  FOREIGN KEY(snapshot_id,tenant_id) REFERENCES repository_snapshots(id,tenant_id) ON DELETE CASCADE
);
CREATE TABLE test_history (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  repository_id uuid NOT NULL, stable_test_key text NOT NULL, adapter text NOT NULL, source_snapshot_hash text NOT NULL,
  environment_digest text NOT NULL, started_at timestamptz NOT NULL, duration_ms bigint NOT NULL DEFAULT 0 CHECK(duration_ms >= 0),
  cpu_ms bigint NOT NULL DEFAULT 0 CHECK(cpu_ms >= 0), outcome text NOT NULL, failure_fingerprint text NOT NULL DEFAULT '',
  retry_count integer NOT NULL DEFAULT 0 CHECK(retry_count >= 0), flake_classification text NOT NULL DEFAULT '',
  FOREIGN KEY(repository_id,tenant_id) REFERENCES repositories(id,tenant_id) ON DELETE CASCADE
);
CREATE INDEX test_history_key_time ON test_history(repository_id,stable_test_key,started_at DESC);
CREATE INDEX test_history_outcome_time ON test_history(repository_id,outcome,started_at DESC);
CREATE TABLE verification_plans (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  repository_id uuid NOT NULL, snapshot_id uuid NOT NULL, base_sha text NOT NULL, head_sha text NOT NULL, worktree_hash text NOT NULL DEFAULT '',
  mode text NOT NULL, status text NOT NULL, risk_score integer NOT NULL CHECK(risk_score BETWEEN 0 AND 100), risk_level text NOT NULL,
  selection_confidence double precision NOT NULL CHECK(selection_confidence BETWEEN 0 AND 1), weighted_coverage double precision NOT NULL CHECK(weighted_coverage BETWEEN 0 AND 1),
  algorithm_version text NOT NULL, policy_version bigint NOT NULL, estimated_duration_ms bigint NOT NULL DEFAULT 0,
  explanation_json jsonb NOT NULL DEFAULT '{}'::jsonb, created_at timestamptz NOT NULL DEFAULT now(), expires_at timestamptz NOT NULL,
  FOREIGN KEY(repository_id,tenant_id) REFERENCES repositories(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(snapshot_id,repository_id) REFERENCES repository_snapshots(id,repository_id) ON DELETE RESTRICT,
  UNIQUE(id,tenant_id)
);
CREATE INDEX plans_by_repository_status_time ON verification_plans(repository_id,status,created_at DESC);
CREATE TABLE verification_plan_units (
	 tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, plan_id uuid NOT NULL,
  test_unit_key text NOT NULL, selection_order integer NOT NULL, mandatory boolean NOT NULL,
  reason_codes_json jsonb NOT NULL DEFAULT '[]'::jsonb, estimated_duration_ms bigint NOT NULL DEFAULT 0,
  coverage_contribution double precision NOT NULL DEFAULT 0, risk_contribution double precision NOT NULL DEFAULT 0,
	 PRIMARY KEY(plan_id,test_unit_key), FOREIGN KEY(plan_id,tenant_id) REFERENCES verification_plans(id,tenant_id) ON DELETE CASCADE
);
CREATE TABLE omitted_plan_units (
	 tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, plan_id uuid NOT NULL,
  test_unit_key text NOT NULL, omission_reason_codes_json jsonb NOT NULL DEFAULT '[]'::jsonb, risk_contribution double precision NOT NULL DEFAULT 0,
	 PRIMARY KEY(plan_id,test_unit_key), FOREIGN KEY(plan_id,tenant_id) REFERENCES verification_plans(id,tenant_id) ON DELETE CASCADE
);
CREATE TABLE executions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  plan_id uuid NOT NULL, idempotency_key text NOT NULL DEFAULT '', status text NOT NULL, executor_id uuid REFERENCES users(id),
  environment_digest text NOT NULL DEFAULT '', started_at timestamptz NOT NULL DEFAULT now(), completed_at timestamptz,
  elapsed_ms bigint NOT NULL DEFAULT 0, compute_ms bigint NOT NULL DEFAULT 0, exit_code integer,
  outcome text NOT NULL DEFAULT '', summary_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  FOREIGN KEY(plan_id,tenant_id) REFERENCES verification_plans(id,tenant_id) ON DELETE CASCADE,
  UNIQUE(id,tenant_id)
);
CREATE TABLE execution_unit_results (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
	 execution_id uuid NOT NULL, test_unit_key text NOT NULL, attempt integer NOT NULL CHECK(attempt > 0),
  status text NOT NULL, duration_ms bigint NOT NULL DEFAULT 0, failure_fingerprint text NOT NULL DEFAULT '', report_hash text NOT NULL DEFAULT '',
	 summary_json jsonb NOT NULL DEFAULT '{}'::jsonb, created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(execution_id,test_unit_key,attempt),
	 FOREIGN KEY(execution_id,tenant_id) REFERENCES executions(id,tenant_id) ON DELETE CASCADE
);
CREATE TABLE audit_records (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
	 execution_id uuid NOT NULL, full_execution_id uuid,
  comparability text NOT NULL, outcome text NOT NULL, missed_test_keys_json jsonb NOT NULL DEFAULT '[]'::jsonb,
	 details_json jsonb NOT NULL DEFAULT '{}'::jsonb, created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(id,tenant_id),
	 FOREIGN KEY(execution_id,tenant_id) REFERENCES executions(id,tenant_id) ON DELETE RESTRICT,
	 FOREIGN KEY(full_execution_id,tenant_id) REFERENCES executions(id,tenant_id) ON DELETE RESTRICT,
  adjudicated_by uuid REFERENCES users(id), adjudication_reason text NOT NULL DEFAULT ''
);
CREATE TABLE quarantines (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
	 repository_id uuid NOT NULL, scope_key text NOT NULL, reason_code text NOT NULL, source_audit_id uuid,
  status text NOT NULL CHECK(status IN ('QUARANTINED','ACKNOWLEDGED','CLEARED')), created_at timestamptz NOT NULL DEFAULT now(), expires_at timestamptz,
  acknowledged_by uuid REFERENCES users(id), acknowledged_at timestamptz, cleared_at timestamptz,
	 FOREIGN KEY(repository_id,tenant_id) REFERENCES repositories(id,tenant_id) ON DELETE CASCADE,
	 FOREIGN KEY(source_audit_id,tenant_id) REFERENCES audit_records(id,tenant_id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX active_quarantine_scope ON quarantines(repository_id,scope_key) WHERE status IN ('QUARANTINED','ACKNOWLEDGED');
CREATE TABLE artifacts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  object_key text NOT NULL, media_type text NOT NULL, size_bytes bigint NOT NULL CHECK(size_bytes >= 0), sha256 text NOT NULL,
  sanitized boolean NOT NULL DEFAULT false, expires_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(tenant_id,object_key)
);
CREATE TABLE audit_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  actor_id uuid REFERENCES users(id), action text NOT NULL, resource_type text NOT NULL, resource_id text NOT NULL,
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE idempotency_requests (
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, endpoint text NOT NULL, idempotency_key text NOT NULL,
  request_hash text NOT NULL, response_status integer, response_json jsonb, created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(tenant_id,endpoint,idempotency_key)
);
CREATE INDEX artifacts_expiration ON artifacts(expires_at);
CREATE INDEX coverage_by_snapshot_unit ON coverage_observations(snapshot_id,test_unit_id,observed_at DESC);

-- Snapshot evidence and audit history are append-only. Plan composition and
-- version metadata are immutable while its lifecycle status may advance.
CREATE FUNCTION ivp_reject_immutable_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'IVP evidence and audit rows are immutable';
  END IF;
  IF TG_TABLE_NAME = 'verification_plans' THEN
    IF (to_jsonb(OLD) - 'status') IS DISTINCT FROM (to_jsonb(NEW) - 'status') THEN
      RAISE EXCEPTION 'IVP plan composition and version metadata are immutable';
    END IF;
  ELSIF TG_TABLE_NAME = 'repository_policies' THEN
    IF (to_jsonb(OLD) - 'is_active') IS DISTINCT FROM (to_jsonb(NEW) - 'is_active') THEN
      RAISE EXCEPTION 'IVP policy versions are immutable';
    END IF;
  ELSE
    RAISE EXCEPTION 'IVP evidence and audit rows are immutable';
  END IF;
  RETURN NEW;
END;
$$;

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['repository_snapshots','graph_nodes','graph_edges','test_units','coverage_observations','execution_unit_results','audit_records','audit_events'] LOOP
    EXECUTE format('CREATE TRIGGER immutable_row BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION ivp_reject_immutable_mutation()', t);
  END LOOP;
  EXECUTE 'CREATE TRIGGER immutable_policy_version BEFORE UPDATE OR DELETE ON repository_policies FOR EACH ROW EXECUTE FUNCTION ivp_reject_immutable_mutation()';
  EXECUTE 'CREATE TRIGGER immutable_plan_composition BEFORE UPDATE OR DELETE ON verification_plans FOR EACH ROW EXECUTE FUNCTION ivp_reject_immutable_mutation()';
END $$;

-- Every tenant-owned table carries tenant_id and enforces it in PostgreSQL as
-- defense in depth. Application queries additionally bind tenant_id explicitly.
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['tenant_memberships','repositories','repository_policies','repository_snapshots','graph_nodes','graph_edges','test_units','coverage_observations','test_history','verification_plans','verification_plan_units','omitted_plan_units','executions','execution_unit_results','audit_records','quarantines','artifacts','audit_events','idempotency_requests'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = nullif(current_setting(''ivp.tenant_id'', true), '''')::uuid) WITH CHECK (tenant_id = nullif(current_setting(''ivp.tenant_id'', true), '''')::uuid)', t);
  END LOOP;
END $$;

