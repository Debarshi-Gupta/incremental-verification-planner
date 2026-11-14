//go:build integration

package persistence

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func openIntegrationDB(t *testing.T) *DB {
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
		t.Fatal(err)
	}
	return db
}

// seedQuarantineRepo creates a tenant, a maintainer user, and a repository the
// quarantine lifecycle tests attach quarantines and audits to. Schema-mutating
// seeds run through TenantTx because quarantines, executions, plans and audit
// records all carry row-level security.
func seedQuarantineRepo(t *testing.T, db *DB) (tenant, user, repo uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tenant, user = uuid.New(), uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,$2)`, tenant, "quarantine-tests"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO users(id,oidc_subject,email_hash) VALUES($1,$2,$3)`, user, "subject-"+user.String(), "hash-"+user.String()); err != nil {
		t.Fatal(err)
	}
	registered, err := db.RegisterRepository(ctx, tenant, user, domain.Repository{Slug: "quarantine-" + user.String()[:8], CanonicalURL: "https://git.example.test/quarantine"})
	if err != nil {
		t.Fatal(err)
	}
	return tenant, user, registered.ID
}

// insertCleanAudit writes a comparable NO_MISS_OBSERVED audit for scope. The
// clean-audit join in ClearEligibleQuarantine requires a persisted execution
// under a plan of the repository, so this seeds the snapshot, plan, and
// execution chain the audit row hangs off.
func insertCleanAudit(t *testing.T, db *DB, tenant, repo uuid.UUID, scope string) {
	t.Helper()
	ctx := context.Background()
	snapshotID, err := db.SaveSnapshot(ctx, SnapshotBundle{TenantID: tenant, Snapshot: domain.RepositorySnapshot{
		RepositoryID: repo, CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BaseSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		TreeHash: "tree-" + scope, WorktreeHash: "worktree-" + scope, ToolchainDigest: "toolchain", BuildConfigDigest: "build",
		AdapterSetDigest: "adapter-set", GraphVersion: 1, EvidenceGeneratedAt: time.Now().UTC(), IsComplete: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	planID, executionID := uuid.New(), uuid.New()
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO verification_plans(id,tenant_id,repository_id,snapshot_id,base_sha,head_sha,worktree_hash,mode,status,risk_score,risk_level,selection_confidence,weighted_coverage,algorithm_version,policy_version,estimated_duration_ms,expires_at)
			VALUES($1,$2,$3,$4,'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','worktree','FAST_FEEDBACK','READY',10,'LOW',1,1,'test-v1',1,1000,$5)`, planID, tenant, repo, snapshotID, time.Now().UTC().Add(time.Hour)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO executions(id,tenant_id,plan_id,status,environment_digest,outcome,completed_at,elapsed_ms)
			VALUES($1,$2,$3,'COMPLETED','environment','TARGETED_PASS',$4,500)`, executionID, tenant, planID, time.Now().UTC())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO audit_records(tenant_id,execution_id,comparability,outcome,details_json)
			VALUES($1,$2,'COMPARABLE',$3,$4)`, tenant, executionID, domain.AuditNoMissObserved, `{"scope_key":"`+scope+`"}`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func quarantineStatus(t *testing.T, db *DB, tenant, repo uuid.UUID, scope string) (status string, clearedAt *time.Time) {
	t.Helper()
	err := db.TenantTx(context.Background(), tenant.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT status,cleared_at FROM quarantines WHERE tenant_id=$1 AND repository_id=$2 AND scope_key=$3`, tenant, repo, scope).Scan(&status, &clearedAt)
	})
	if err != nil {
		t.Fatal(err)
	}
	return status, clearedAt
}

// P0-3 regression: clearing requires maintainer acknowledgement plus a clean
// comparable audit (LLD 10.5). A never-expiring quarantine (expires_at IS
// NULL) was impossible to clear because the eligibility gate demanded expiry;
// meanwhile expiry alone must never suffice without acknowledgement, and an
// unexpired quarantine must wait out its window even after acknowledgement
// plus a clean audit.
func TestClearEligibleQuarantineGatesOnAcknowledgementAndCleanAudit(t *testing.T) {
	ctx := context.Background()
	db := openIntegrationDB(t)
	tenant, user, repo := seedQuarantineRepo(t, db)
	now := time.Now().UTC()

	// Never-expiring quarantine, not acknowledged: clearing must fail.
	if err := db.CreateQuarantine(ctx, tenant, user, repo, "pytest/all", "AUDIT_POSSIBLE_MISS", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.ClearEligibleQuarantine(ctx, tenant, user, repo, "pytest/all", "attempted clear", now); err == nil {
		t.Fatal("unacknowledged quarantine was clearable")
	}

	// Acknowledged but without a clean comparable audit: clearing must fail.
	if err := db.AcknowledgeQuarantine(ctx, tenant, user, repo, "pytest/all", "acknowledged for repair"); err != nil {
		t.Fatal(err)
	}
	if err := db.ClearEligibleQuarantine(ctx, tenant, user, repo, "pytest/all", "attempted clear", now); err == nil {
		t.Fatal("quarantine without a clean comparable audit was clearable")
	}

	// Acknowledged plus a clean comparable audit: the never-expiring
	// quarantine becomes clearable (this path was unreachable before P0-3).
	insertCleanAudit(t, db, tenant, repo, "pytest/all")
	if err := db.ClearEligibleQuarantine(ctx, tenant, user, repo, "pytest/all", "clean audit observed", now); err != nil {
		t.Fatalf("acknowledged non-expiring quarantine with a clean audit was not clearable: %v", err)
	}
	if status, clearedAt := quarantineStatus(t, db, tenant, repo, "pytest/all"); status != "CLEARED" || clearedAt == nil {
		t.Fatalf("quarantine status=%s cleared_at=%v, want CLEARED with timestamp", status, clearedAt)
	}

	// Unexpired quarantine: acknowledgement plus a clean audit is still not
	// enough before the expiry window passes.
	futureExpiry := now.Add(time.Hour)
	if err := db.CreateQuarantine(ctx, tenant, user, repo, "jest/all", "AUDIT_POSSIBLE_MISS", nil, &futureExpiry); err != nil {
		t.Fatal(err)
	}
	if err := db.AcknowledgeQuarantine(ctx, tenant, user, repo, "jest/all", "acknowledged for repair"); err != nil {
		t.Fatal(err)
	}
	insertCleanAudit(t, db, tenant, repo, "jest/all")
	if err := db.ClearEligibleQuarantine(ctx, tenant, user, repo, "jest/all", "attempted clear", now); err == nil {
		t.Fatal("unexpired quarantine was clearable before its expiry window")
	}
	if err := db.ClearEligibleQuarantine(ctx, tenant, user, repo, "jest/all", "expiry window elapsed", now.Add(2*time.Hour)); err != nil {
		t.Fatalf("expired acknowledged quarantine with a clean audit was not clearable: %v", err)
	}

	// Expired but never acknowledged: clearing must fail regardless of expiry.
	pastExpiry := now.Add(-time.Hour)
	if err := db.CreateQuarantine(ctx, tenant, user, repo, "gradle/all", "AUDIT_POSSIBLE_MISS", nil, &pastExpiry); err != nil {
		t.Fatal(err)
	}
	insertCleanAudit(t, db, tenant, repo, "gradle/all")
	if err := db.ClearEligibleQuarantine(ctx, tenant, user, repo, "gradle/all", "attempted clear", now); err == nil {
		t.Fatal("expired but unacknowledged quarantine was clearable without acknowledgement")
	}
}

// P0-4a regression: CalibrationCounts gates RISK_BASED_CI enablement and must
// count only OBSERVE-mode calibration runs (LLD 7.2); executions under other
// plan modes previously inflated the server-owned calibration count.
func TestCalibrationCountsScopedToObserveModeExecutions(t *testing.T) {
	ctx := context.Background()
	db := openIntegrationDB(t)
	tenant, _, repo := seedQuarantineRepo(t, db)
	now := time.Now().UTC()

	snapshotID, err := db.SaveSnapshot(ctx, SnapshotBundle{TenantID: tenant, Snapshot: domain.RepositorySnapshot{
		RepositoryID: repo, CommitSHA: "cccccccccccccccccccccccccccccccccccccccc", BaseSHA: "dddddddddddddddddddddddddddddddddddddddd",
		TreeHash: "tree-calibration", WorktreeHash: "worktree-calibration", ToolchainDigest: "toolchain", BuildConfigDigest: "build",
		AdapterSetDigest: "adapter-set", GraphVersion: 1, EvidenceGeneratedAt: now, IsComplete: true,
	}})
	if err != nil {
		t.Fatal(err)
	}

	// Exactly one row qualifies: a completed OBSERVE execution with usable
	// timing on a plan with a duration estimate. The rest vary one qualifying
	// dimension at a time: plan mode, completion, outcome, or estimate.
	type planSeed struct {
		mode        string
		elapsedMs   int64
		completed   bool
		outcome     string
		estimatedMs int64
		qualifies   bool
	}
	seeds := []planSeed{
		{mode: "OBSERVE", elapsedMs: 500, completed: true, outcome: "TARGETED_PASS", estimatedMs: 1000, qualifies: true},
		{mode: "FULL", elapsedMs: 900, completed: true, outcome: "FULL_VERIFICATION_PASS", estimatedMs: 1000},
		{mode: "FAST_FEEDBACK", elapsedMs: 400, completed: true, outcome: "TARGETED_FAIL", estimatedMs: 1000},
		{mode: "RISK_BASED_CI", elapsedMs: 300, completed: true, outcome: "TARGETED_PASS", estimatedMs: 1000},
		{mode: "OBSERVE", elapsedMs: 500, completed: false, outcome: "", estimatedMs: 1000},
		{mode: "OBSERVE", elapsedMs: 500, completed: true, outcome: "INFRA_FAILURE", estimatedMs: 1000},
		{mode: "OBSERVE", elapsedMs: 500, completed: true, outcome: "TARGETED_PASS", estimatedMs: 0},
	}
	var auditExecution uuid.UUID
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		for i, seed := range seeds {
			planID, executionID := uuid.New(), uuid.New()
			if _, err := tx.Exec(ctx, `INSERT INTO verification_plans(id,tenant_id,repository_id,snapshot_id,base_sha,head_sha,worktree_hash,mode,status,risk_score,risk_level,selection_confidence,weighted_coverage,algorithm_version,policy_version,estimated_duration_ms,expires_at)
				VALUES($1,$2,$3,$4,'dddddddddddddddddddddddddddddddddddddddd','cccccccccccccccccccccccccccccccccccccccc','worktree',$5,'READY',10,'LOW',1,1,'test-v1',1,$6,$7)`, planID, tenant, repo, snapshotID, seed.mode, seed.estimatedMs, now.Add(time.Hour)); err != nil {
				return err
			}
			var completed any
			if seed.completed {
				completed = now
			}
			if _, err := tx.Exec(ctx, `INSERT INTO executions(id,tenant_id,plan_id,status,environment_digest,outcome,completed_at,elapsed_ms)
				VALUES($1,$2,$3,'COMPLETED','environment',$4,$5,$6)`, executionID, tenant, planID, seed.outcome, completed, seed.elapsedMs); err != nil {
				return err
			}
			if i == 0 {
				auditExecution = executionID
			}
		}
		// One comparable and one incomparable audit: only the comparable one
		// counts toward the calibration audit total.
		if _, err := tx.Exec(ctx, `INSERT INTO audit_records(tenant_id,execution_id,comparability,outcome,details_json)
			VALUES($1,$2,'COMPARABLE',$3,'{"scope_key":"pytest/all"}')`, tenant, auditExecution, domain.AuditNoMissObserved); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO audit_records(tenant_id,execution_id,comparability,outcome,details_json)
			VALUES($1,$2,'INCOMPARABLE',$3,'{"scope_key":"pytest/all"}')`, tenant, auditExecution, domain.AuditIncomparable)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	executions, audits, err := db.CalibrationCounts(ctx, tenant, repo)
	if err != nil {
		t.Fatal(err)
	}
	if executions != 1 {
		t.Fatalf("calibration execution count=%d, want 1 (only the completed OBSERVE execution with usable timing)", executions)
	}
	if audits != 1 {
		t.Fatalf("calibration audit count=%d, want 1 (only the comparable audit)", audits)
	}
}
