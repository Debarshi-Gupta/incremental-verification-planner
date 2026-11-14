//go:build integration

package persistence

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// openEvidenceDB starts an isolated PostgreSQL instance with the schema
// applied.
func openEvidenceDB(t *testing.T) *DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dsn := os.Getenv("IVP_TEST_DATABASE_URL")
	if dsn == "" {
		container, err := postgres.Run(ctx, "postgres:16.6-alpine",
			postgres.WithDatabase("ivp"), postgres.WithUsername("ivp"), postgres.WithPassword("ivp-test"),
			postgres.BasicWaitStrategies())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = container.Terminate(context.Background()) })
		dsn, err = container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			t.Fatal(err)
		}
	}
	db, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return db
}

// seedEvidenceRepository creates a tenant, a user and a repository.
func seedEvidenceRepository(t *testing.T, db *DB, slug string) (tenant, user, repo uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tenant, user = uuid.New(), uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,$2)`, tenant, "evidence-tests"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO users(id,oidc_subject,email_hash) VALUES($1,$2,$3)`, user, "subject-"+user.String(), "hash-"+user.String()); err != nil {
		t.Fatal(err)
	}
	registered, err := db.RegisterRepository(ctx, tenant, user, domain.Repository{Slug: slug, CanonicalURL: "https://git.example.test/" + slug})
	if err != nil {
		t.Fatal(err)
	}
	return tenant, user, registered.ID
}

// LLD 13.1 and 13.2: the schema carries the documented tables and columns, the
// uniqueness constraints and the composite tenant foreign keys, and the
// application-managed tenant filtering is enforced by the queries.
func TestSchemaColumnsUniquenessAndTenantForeignKeys(t *testing.T) {
	ctx := context.Background()
	db := openEvidenceDB(t)

	for table, columns := range map[string][]string{
		"tenants":      {"id", "name", "created_at"},
		"users":        {"id", "oidc_subject", "email_hash", "created_at", "disabled_at"},
		"repositories": {"id", "tenant_id", "slug", "canonical_url", "default_branch", "created_at", "updated_at"},
	} {
		var present []string
		rows, err := db.Pool.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_name=$1`, table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			present = append(present, name)
		}
		rows.Close()
		for _, column := range columns {
			found := false
			for _, name := range present {
				if name == column {
					found = true
				}
			}
			if !found {
				t.Fatalf("the %s table is missing the column %q (present: %v)", table, column, present)
			}
		}
	}

	tenant, user, repo := seedEvidenceRepository(t, db, "unique-slug")
	// The unique (tenant_id, slug) constraint is enforced by the database.
	if _, err := db.Pool.Exec(ctx, `INSERT INTO repositories(id,tenant_id,slug,canonical_url) VALUES($1,$2,'unique-slug','https://git.example.test/duplicate')`,
		uuid.New(), tenant); err == nil {
		t.Fatal("the database accepted a duplicate (tenant_id, slug)")
	}
	// The same slug in another tenant is allowed (the constraint is per tenant).
	otherTenant, otherUser, _ := seedEvidenceRepository(t, db, "unique-slug")
	if otherTenant == tenant || otherUser == user {
		t.Fatal("the second tenant was not created")
	}

	// The composite tenant foreign key rejects a graph node of another tenant.
	snapshotID, err := db.SaveSnapshot(ctx, SnapshotBundle{TenantID: tenant, Snapshot: domain.RepositorySnapshot{
		RepositoryID: repo, CommitSHA: "cccccccccccccccccccccccccccccccccccccccc", BaseSHA: "dddddddddddddddddddddddddddddddddddddddd",
		TreeHash: "tree-schema", WorktreeHash: "worktree-schema", ToolchainDigest: "toolchain", BuildConfigDigest: "build",
		TestConfigDigest: "test-config", AdapterSetDigest: "adapter-set", GraphVersion: 1, EvidenceGeneratedAt: time.Now().UTC(), IsComplete: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	err = db.TenantTx(ctx, otherTenant.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO graph_nodes(id,tenant_id,snapshot_id,node_type,stable_key) VALUES($1,$2,$3,'SOURCE_FILE','file:foreign.go')`,
			uuid.New(), otherTenant, snapshotID)
		return err
	})
	if err == nil {
		t.Fatal("the database accepted a graph node that references another tenant's snapshot")
	}
}

// LLD 3.2, 4.2 and 13.3: the stored graph is the immutable source of truth -
// a snapshot, a graph row and an audit row cannot be updated or deleted.
func TestStoredGraphAndAuditHistoryAreImmutable(t *testing.T) {
	ctx := context.Background()
	db := openEvidenceDB(t)
	tenant, _, repo := seedEvidenceRepository(t, db, "immutable")
	snapshotID, err := db.SaveSnapshot(ctx, SnapshotBundle{TenantID: tenant, Snapshot: domain.RepositorySnapshot{
		RepositoryID: repo, CommitSHA: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", BaseSHA: "ffffffffffffffffffffffffffffffffffffffff",
		TreeHash: "tree-immutable", WorktreeHash: "worktree-immutable", ToolchainDigest: "toolchain", BuildConfigDigest: "build",
		TestConfigDigest: "test-config", AdapterSetDigest: "adapter-set", GraphVersion: 1, EvidenceGeneratedAt: time.Now().UTC(), IsComplete: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	nodeID := uuid.New()
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO graph_nodes(id,tenant_id,snapshot_id,node_type,stable_key,path,confidence,evidence_source,observed_at) VALUES($1,$2,$3,'SOURCE_FILE','file:src/app.go','src/app.go',1,'fixture',now())`,
			nodeID, tenant, snapshotID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// A snapshot row cannot be updated.
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE repository_snapshots SET tree_hash='tampered' WHERE tenant_id=$1 AND id=$2`, tenant, snapshotID)
		return err
	}); err == nil {
		t.Fatal("the database accepted an update of an immutable snapshot")
	}
	// A graph node cannot be updated or deleted.
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE graph_nodes SET stable_key='file:tampered.go' WHERE tenant_id=$1 AND id=$2`, tenant, nodeID)
		return err
	}); err == nil {
		t.Fatal("the database accepted an update of an immutable graph node")
	}
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM graph_nodes WHERE tenant_id=$1 AND id=$2`, tenant, nodeID)
		return err
	}); err == nil {
		t.Fatal("the database accepted a delete of an immutable graph node")
	}
	// The unique graph identity deduplicates an ingested node.
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO graph_nodes(id,tenant_id,snapshot_id,node_type,stable_key,path,confidence,evidence_source,observed_at) VALUES($1,$2,$3,'SOURCE_FILE','file:src/app.go','src/app.go',1,'fixture',now())`,
			uuid.New(), tenant, snapshotID)
		return err
	}); err == nil {
		t.Fatal("the database accepted a duplicate graph node identity")
	}
}

// LLD Appendix-C: the snapshot ownership is validated - a tenant cannot read
// another tenant's snapshot.
func TestCrossTenantSnapshotOwnershipIsDenied(t *testing.T) {
	ctx := context.Background()
	db := openEvidenceDB(t)
	tenantA, _, repoA := seedEvidenceRepository(t, db, "owner-a")
	tenantB, _, _ := seedEvidenceRepository(t, db, "owner-b")
	snapshotID, err := db.SaveSnapshot(ctx, SnapshotBundle{TenantID: tenantA, Snapshot: domain.RepositorySnapshot{
		RepositoryID: repoA, CommitSHA: "1111111111111111111111111111111111111111", BaseSHA: "2222222222222222222222222222222222222222",
		TreeHash: "tree-owner", WorktreeHash: "worktree-owner", ToolchainDigest: "toolchain", BuildConfigDigest: "build",
		TestConfigDigest: "test-config", AdapterSetDigest: "adapter-set", GraphVersion: 1, EvidenceGeneratedAt: time.Now().UTC(), IsComplete: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.LoadSnapshot(ctx, tenantA, repoA, snapshotID); err != nil {
		t.Fatalf("the owning tenant cannot read its snapshot: %v", err)
	}
	if _, err := db.LoadSnapshot(ctx, tenantB, repoA, snapshotID); err == nil {
		t.Fatal("another tenant read a snapshot it does not own")
	}
}

// LLD 7.1 and Appendix-D: the server refuses a mislabelled result. A
// full-suite execution may not be labelled TARGETED_PASS, and a selective
// execution may not be labelled FULL_VERIFICATION_PASS - the label must state
// what actually ran.
func TestMislabelledResultIsRejected(t *testing.T) {
	ctx := context.Background()
	db := openEvidenceDB(t)
	tenant, _, repo := seedEvidenceRepository(t, db, "label-rejection")
	snapshotID, err := db.SaveSnapshot(ctx, SnapshotBundle{TenantID: tenant, Snapshot: domain.RepositorySnapshot{
		RepositoryID: repo, CommitSHA: "3333333333333333333333333333333333333333", BaseSHA: "4444444444444444444444444444444444444444",
		TreeHash: "tree-label", WorktreeHash: "worktree-label", ToolchainDigest: "toolchain", BuildConfigDigest: "build",
		TestConfigDigest: "test-config", AdapterSetDigest: "adapter-set", GraphVersion: 1, EvidenceGeneratedAt: time.Now().UTC(), IsComplete: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	planID, executionID := uuid.New(), uuid.New()
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO verification_plans(id,tenant_id,repository_id,snapshot_id,base_sha,head_sha,worktree_hash,mode,status,risk_score,risk_level,selection_confidence,weighted_coverage,algorithm_version,policy_version,estimated_duration_ms,expires_at)
			VALUES($1,$2,$3,$4,'4444444444444444444444444444444444444444','3333333333333333333333333333333333333333','worktree-label','FAST_FEEDBACK','READY',10,'LOW',1,1,'test-v1',1,1000,$5)`, planID, tenant, repo, snapshotID, now.Add(time.Hour)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO executions(id,tenant_id,plan_id,status,environment_digest,outcome,started_at,completed_at,elapsed_ms,summary_json)
			VALUES($1,$2,$3,'RUNNING','environment','',$4,$5,500,'{"full_suite":true}')`, executionID, tenant, planID, now, now.Add(time.Second))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	exitCode := 0
	err = db.SubmitExecutionResult(ctx, tenant, executionID, ResultSubmission{
		Outcome: domain.OutcomeTargetedPass, ExitCode: &exitCode, CompletedAt: now.Add(time.Second),
		ElapsedMillis: 500, ComputeMillis: 500, Summary: json.RawMessage(`{"full_suite":true}`),
	})
	if err == nil {
		t.Fatal("the server accepted a targeted pass for a full-suite execution")
	}
	if !strings.Contains(err.Error(), "cannot be labeled targeted pass") {
		t.Fatalf("unexpected rejection: %v", err)
	}
}

// LLD 4.5: the flake rate is computed over a configurable rolling window and
// the sample size is recorded with it.
func TestFlakeRateUsesTheConfiguredRollingWindow(t *testing.T) {
	ctx := context.Background()
	db := openEvidenceDB(t)
	tenant, _, repo := seedEvidenceRepository(t, db, "flake-window")
	now := time.Now().UTC()
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		// The observation outside the window is a plain failure; the two inside
		// it are the flaky ones the rate is computed from.
		for _, row := range []struct {
			started time.Time
			flaky   string
		}{
			{now.Add(-72 * time.Hour), ""},
			{now.Add(-2 * time.Hour), "FLAKY"},
			{now.Add(-time.Hour), "FLAKY"},
		} {
			if _, err := tx.Exec(ctx, `INSERT INTO test_history(tenant_id,repository_id,stable_test_key,adapter,source_snapshot_hash,environment_digest,build_config_digest,test_config_digest,started_at,duration_ms,cpu_ms,outcome,failure_fingerprint,flake_classification)
				VALUES($1,$2,'test:flaky','pytest','tree-flake','env','build','test-config',$3,100,10,'FAIL','fp',$4)`, tenant, repo, row.started, row.flaky); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	units := []domain.TestUnit{{ID: uuid.New(), StableKey: "test:flaky", Adapter: "pytest", EstimatedMillis: 100, DurationSampleCount: 0}}
	updated, err := db.ApplyTestHistory(ctx, tenant, repo, "env", "test-config", 24*time.Hour, 1, units)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 1 {
		t.Fatalf("the history was not applied: %+v", updated)
	}
	// Only the two failures inside the 24 hour window count, and the sample
	// size is recorded with the rate.
	if updated[0].DurationSampleCount != 2 {
		t.Fatalf("the sample size = %d, want the 2 observations inside the window", updated[0].DurationSampleCount)
	}
	if updated[0].FlakeRate != 1 {
		t.Fatalf("the flake rate = %v, want 1 (every observation in the window was classified flaky)", updated[0].FlakeRate)
	}
	if updated[0].HistoricalFailures != 2 {
		t.Fatalf("the historical failures = %d, want the 2 failures inside the window", updated[0].HistoricalFailures)
	}
	// A window that excludes the observations yields no sample at all.
	outside, err := db.ApplyTestHistory(ctx, tenant, repo, "env", "test-config", time.Hour, 1, units)
	if err != nil {
		t.Fatal(err)
	}
	if outside[0].DurationSampleCount != 0 || outside[0].FlakeRate != 0 {
		t.Fatalf("a window without observations must yield no sample, got %+v", outside[0])
	}
}
