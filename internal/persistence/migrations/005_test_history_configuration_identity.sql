-- +goose Up
ALTER TABLE test_history ADD COLUMN build_config_digest text NOT NULL DEFAULT '';
ALTER TABLE test_history ADD COLUMN test_config_digest text NOT NULL DEFAULT '';

-- Historical rows predate configuration identity and cannot be assigned a
-- trustworthy digest retroactively. They remain available for audit reports
-- but are excluded from planning estimates until new comparable runs arrive.
CREATE INDEX test_history_comparable_key_time ON test_history
  (tenant_id,repository_id,environment_digest,test_config_digest,stable_test_key,started_at DESC);

-- +goose Down
DROP INDEX IF EXISTS test_history_comparable_key_time;
ALTER TABLE test_history DROP COLUMN IF EXISTS test_config_digest;
ALTER TABLE test_history DROP COLUMN IF EXISTS build_config_digest;
