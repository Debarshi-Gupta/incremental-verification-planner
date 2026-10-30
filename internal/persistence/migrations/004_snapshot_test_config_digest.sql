-- +goose Up
ALTER TABLE repository_snapshots ADD COLUMN test_config_digest text NOT NULL DEFAULT '';
UPDATE repository_snapshots SET test_config_digest=config_digest WHERE test_config_digest='';

DO $$
DECLARE item record;
BEGIN
  FOR item IN
    SELECT c.conname FROM pg_constraint c
    WHERE c.conrelid='repository_snapshots'::regclass AND c.contype='u'
      AND (SELECT array_agg(a.attname ORDER BY array_position(c.conkey,a.attnum))
           FROM pg_attribute a WHERE a.attrelid=c.conrelid AND a.attnum=ANY(c.conkey)) =
          ARRAY['repository_id','commit_sha','base_sha','tree_hash','worktree_hash','submodule_digest','toolchain_digest','config_digest','adapter_digest','graph_version','completeness_json']::name[]
  LOOP
    EXECUTE format('ALTER TABLE repository_snapshots DROP CONSTRAINT %I', item.conname);
  END LOOP;
END $$;

CREATE UNIQUE INDEX repository_snapshot_identity ON repository_snapshots
  (repository_id,commit_sha,base_sha,tree_hash,worktree_hash,submodule_digest,toolchain_digest,config_digest,test_config_digest,adapter_digest,graph_version,completeness_json);

-- +goose Down
DROP INDEX IF EXISTS repository_snapshot_identity;
ALTER TABLE repository_snapshots DROP COLUMN IF EXISTS test_config_digest;
ALTER TABLE repository_snapshots ADD CONSTRAINT repository_snapshots_evidence_identity UNIQUE
  (repository_id,commit_sha,base_sha,tree_hash,worktree_hash,submodule_digest,toolchain_digest,config_digest,adapter_digest,graph_version,completeness_json);
