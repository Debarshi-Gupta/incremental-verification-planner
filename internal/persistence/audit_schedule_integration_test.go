//go:build integration

package persistence

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// LLD 10.2 and 15.3: the scheduled-audit orchestration reads the last full
// verification of every repository and the unacknowledged audit misses from
// the database.
func TestScheduledAuditRepositoriesAndUnacknowledgedMisses(t *testing.T) {
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
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	tenant := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,'schedule')`, tenant); err != nil {
		t.Fatal(err)
	}
	verifiedRepo := uuid.New()
	freshRepo := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO repositories(id,tenant_id,slug,canonical_url) VALUES($1,$2,'verified','https://git.example.test/verified'),($3,$4,'fresh','https://git.example.test/fresh')`,
		verifiedRepo, tenant, freshRepo, tenant); err != nil {
		t.Fatal(err)
	}
	// The verified repository ran a FULL-mode plan yesterday.
	snapshotID := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO repository_snapshots(id,tenant_id,repository_id,commit_sha,base_sha,tree_hash,worktree_hash,submodule_digest,toolchain_digest,config_digest,test_config_digest,adapter_digest,dependency_digest,adapter_versions_json,graph_version,completeness_json)
		VALUES($1,$2,$3,'head','base','tree','worktree','','toolchain','config','test-config','adapter','','{}'::jsonb,1,'{}'::jsonb)`, snapshotID, tenant, verifiedRepo); err != nil {
		t.Fatal(err)
	}
	planID := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO verification_plans(id,tenant_id,repository_id,snapshot_id,base_sha,head_sha,mode,status,risk_score,risk_level,selection_confidence,weighted_coverage,algorithm_version,policy_version,expires_at)
		VALUES($1,$2,$3,$4,'base','head','FULL','READY',10,'LOW',1,1,'v1',1,now()+interval '1 hour')`, planID, tenant, verifiedRepo, snapshotID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO executions(id,tenant_id,plan_id,status,completed_at,outcome)
		VALUES($1,$2,$3,'COMPLETED',now()-interval '1 day','FULL_VERIFICATION_PASS')`, uuid.New(), tenant, planID); err != nil {
		t.Fatal(err)
	}

	repositories, err := db.ScheduledAuditRepositories(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 2 {
		t.Fatalf("scheduled audit listed %d repositories, want 2", len(repositories))
	}
	byID := map[uuid.UUID]ScheduledAuditRepository{}
	for _, repository := range repositories {
		byID[repository.RepositoryID] = repository
	}
	verified, ok := byID[verifiedRepo]
	if !ok {
		t.Fatalf("the verified repository is missing: %+v", repositories)
	}
	if verified.LastFullVerification.IsZero() {
		t.Fatal("the last full verification was not reported")
	}
	// A repository that never ran a full verification has no evidence at all.
	fresh, ok := byID[freshRepo]
	if !ok {
		t.Fatalf("the fresh repository is missing: %+v", repositories)
	}
	if !fresh.LastFullVerification.IsZero() {
		t.Fatalf("a repository without a full verification reported %s", fresh.LastFullVerification)
	}

	// An unacknowledged quarantine is an unacknowledged audit miss.
	if _, err := db.Pool.Exec(ctx, `INSERT INTO quarantines(id,tenant_id,repository_id,scope_key,reason_code,status,created_at)
		VALUES($1,$2,$3,'pytest/all','AUDIT_MISS','QUARANTINED',now()-interval '2 days')`, uuid.New(), tenant, verifiedRepo); err != nil {
		t.Fatal(err)
	}
	count, age, err := db.UnacknowledgedMisses(ctx, tenant, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("unacknowledged misses = %d, want 1", count)
	}
	if age < 47*time.Hour {
		t.Fatalf("unacknowledged miss age = %s, want about two days", age)
	}
	// Acknowledging the miss clears the signal.
	if _, err := db.Pool.Exec(ctx, `UPDATE quarantines SET status='ACKNOWLEDGED', acknowledged_at=now() WHERE tenant_id=$1`, tenant); err != nil {
		t.Fatal(err)
	}
	count, _, err = db.UnacknowledgedMisses(ctx, tenant, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unacknowledged misses after acknowledgement = %d, want 0", count)
	}
}
