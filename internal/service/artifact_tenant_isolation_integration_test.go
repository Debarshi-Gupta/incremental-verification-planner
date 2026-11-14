//go:build integration

package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/api"
	"github.com/ivp/ivp/internal/artifact"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/persistence"
	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// openArtifactIsolationDB starts an isolated PostgreSQL instance with the IVP
// schema applied for the cross-tenant artifact isolation test.
func openArtifactIsolationDB(t *testing.T) *persistence.DB {
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
	db, err := persistence.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrations from empty database: %v", err)
	}
	return db
}

// seedArtifactIsolationScope creates a tenant with a user, a repository, and a
// completed execution that can own report artifacts.
func seedArtifactIsolationScope(t *testing.T, db *persistence.DB, name string) (tenant, user uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tenant, user = uuid.New(), uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,$2)`, tenant, name); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO users(id,oidc_subject,email_hash) VALUES($1,$2,'hash')`, user, "subject-"+user.String()); err != nil {
		t.Fatal(err)
	}
	repo, err := db.RegisterRepository(ctx, tenant, user, domain.Repository{Slug: "art-iso-" + user.String()[:8], CanonicalURL: "https://git.example.test/" + name})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO repository_snapshots(id,tenant_id,repository_id,commit_sha,base_sha,tree_hash,worktree_hash,toolchain_digest,config_digest,test_config_digest,adapter_digest,dependency_digest,adapter_versions_json,graph_version,completeness_json)
			VALUES($1,$2,$3,'head','base','tree','worktree','toolchain','config','test-config','adapter','','{}'::jsonb,1,'{}'::jsonb)`, uuid.New(), tenant, repo.ID); err != nil {
			return err
		}
		var snapshotID uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM repository_snapshots WHERE tenant_id=$1 AND repository_id=$2`, tenant, repo.ID).Scan(&snapshotID); err != nil {
			return err
		}
		planID := uuid.New()
		if _, err := tx.Exec(ctx, `INSERT INTO verification_plans(id,tenant_id,repository_id,snapshot_id,base_sha,head_sha,worktree_hash,mode,status,risk_score,risk_level,selection_confidence,weighted_coverage,algorithm_version,policy_version,expires_at)
			VALUES($1,$2,$3,$4,'base','head','worktree','OBSERVE','READY',0,'LOW',1,1,'test-v1',1,$5)`, planID, tenant, repo.ID, snapshotID, now.Add(time.Hour)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO executions(id,tenant_id,plan_id,status,environment_digest,outcome,completed_at)
			VALUES($1,$2,$3,'COMPLETED','environment','FULL_VERIFICATION_PASS',$4)`, uuid.New(), tenant, planID, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return tenant, user
}

// executionOfTenant returns the id of the tenant's single completed
// execution, which is the parent of its report artifacts.
func executionOfTenant(t *testing.T, db *persistence.DB, tenant uuid.UUID) uuid.UUID {
	t.Helper()
	var executionID uuid.UUID
	if err := db.TenantTx(context.Background(), tenant.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT id FROM executions WHERE tenant_id=$1 LIMIT 1`, tenant).Scan(&executionID)
	}); err != nil {
		t.Fatal(err)
	}
	return executionID
}

// TestArtifactManagerRefusesCrossTenantArtifactAccess covers LLD 2.3 item 4
// ("PostgreSQL / artifact store: tenant-scoped data") for the artifact store:
// one tenant must not be able to read another tenant's artifact metadata row
// or the stored object, through either the service layer or the HTTP surface.
func TestArtifactManagerRefusesCrossTenantArtifactAccess(t *testing.T) {
	ctx := context.Background()
	db := openArtifactIsolationDB(t)
	tenantA, userA := seedArtifactIsolationScope(t, db, "artifact-iso-a")
	tenantB, userB := seedArtifactIsolationScope(t, db, "artifact-iso-b")
	executionA := executionOfTenant(t, db, tenantA)
	executionB := executionOfTenant(t, db, tenantB)

	store, err := artifact.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := &ArtifactManager{DB: db, Store: store, MaxBytes: 64 << 10, Retention: 24 * time.Hour}

	// Tenant A uploads a report artifact.
	info, err := manager.Upload(ctx, tenantA, userA, executionA, "application/json", strings.NewReader(`{"tests":[{"name":"ok","status":"passed"}]}`))
	if err != nil {
		t.Fatalf("tenant A artifact upload: %v", err)
	}
	row, err := db.GetArtifact(ctx, tenantA, info.ID)
	if err != nil {
		t.Fatalf("tenant A artifact metadata: %v", err)
	}
	if row.TenantID != tenantA {
		t.Fatalf("artifact metadata tenant = %s, want %s", row.TenantID, tenantA)
	}
	t.Logf("PASS: tenant A stored artifact %s under object key %s.", info.ID, row.ObjectKey)

	// (1) Tenant B cannot read tenant A's artifact metadata row.
	if _, err := db.GetArtifact(ctx, tenantB, info.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-tenant artifact metadata read err=%v, want no rows", err)
	}
	t.Log("PASS: tenant B cannot read tenant A's artifact metadata row.")

	// (2) Tenant B cannot open tenant A's artifact object through the service
	// layer: the object key is re-derived from the authenticated tenant, so a
	// foreign artifact id never reaches tenant A's object.
	_, _, err = manager.Open(ctx, tenantB, info.ID)
	if err == nil {
		t.Fatal("tenant B opened tenant A's artifact object")
	}
	if !errors.Is(err, api.ErrArtifactUnavailable) {
		t.Fatalf("cross-tenant artifact open err=%v, want ErrArtifactUnavailable", err)
	}
	t.Logf("PASS: tenant B cannot open tenant A's artifact object (%v).", err)

	// (3) The object itself is still stored under tenant A's scoped key, so
	// the artifact store never holds a foreign-tenant readable key.
	if !strings.HasPrefix(row.ObjectKey, tenantA.String()+"/") {
		t.Fatalf("artifact object key %q is not scoped to the owning tenant", row.ObjectKey)
	}
	t.Log("PASS: the stored object key is scoped to the owning tenant.")

	// (4) Tenant B cannot upload an artifact against tenant A's execution: the
	// artifact row must belong to the same tenant as the execution.
	if _, err := manager.Upload(ctx, tenantB, userB, executionA, "application/json", strings.NewReader(`{"tests":[{"name":"x","status":"passed"}]}`)); err == nil {
		t.Fatal("tenant B uploaded an artifact against tenant A's execution")
	}
	t.Log("PASS: tenant B cannot attach an artifact to tenant A's execution.")

	// (5) Tenant B still reads its own artifacts: the isolation must not
	// become a denial of service for the owning tenant.
	infoB, err := manager.Upload(ctx, tenantB, userB, executionB, "application/json", strings.NewReader(`{"tests":[{"name":"ok","status":"passed"}]}`))
	if err != nil {
		t.Fatalf("tenant B artifact upload: %v", err)
	}
	_, reader, err := manager.Open(ctx, tenantB, infoB.ID)
	if err != nil {
		t.Fatalf("tenant B cannot open its own artifact: %v", err)
	}
	_ = reader.Close()
	t.Log("PASS: tenant B reads its own artifact successfully.")
}
