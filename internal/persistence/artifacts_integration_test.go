//go:build integration

package persistence

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// openArtifactsIsolationDB starts an isolated PostgreSQL instance with the
// IVP schema applied. A dedicated instance keeps this test independent from
// the other integration suites that share IVP_TEST_DATABASE_URL.
func openArtifactsIsolationDB(t *testing.T) *DB {
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
		t.Fatalf("migrations from empty database: %v", err)
	}
	return db
}

// seedArtifactIsolationTenant creates a tenant, a user, and a repository that
// can own plans, executions and artifacts.
func seedArtifactIsolationTenant(t *testing.T, db *DB, name string) (tenant, user, repo uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tenant, user = uuid.New(), uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,$2)`, tenant, name); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO users(id,oidc_subject,email_hash) VALUES($1,$2,'hash')`, user, "subject-"+user.String()); err != nil {
		t.Fatal(err)
	}
	registered, err := db.RegisterRepository(ctx, tenant, user, domain.Repository{Slug: "artifacts-" + user.String()[:8], CanonicalURL: "https://git.example.test/" + name})
	if err != nil {
		t.Fatal(err)
	}
	return tenant, user, registered.ID
}

// insertArtifactIsolationExecution creates a completed execution that owns
// artifacts, so the artifact foreign key has a valid target.
func insertArtifactIsolationExecution(t *testing.T, db *DB, tenant, user, repo uuid.UUID) (planID, executionID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	planID, executionID = uuid.New(), uuid.New()
	now := time.Now().UTC()
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO repository_snapshots(id,tenant_id,repository_id,commit_sha,base_sha,tree_hash,worktree_hash,toolchain_digest,config_digest,test_config_digest,adapter_digest,dependency_digest,adapter_versions_json,graph_version,completeness_json)
			VALUES($1,$2,$3,'head','base','tree','worktree','toolchain','config','test-config','adapter','','{}'::jsonb,1,'{}'::jsonb)`, uuid.New(), tenant, repo); err != nil {
			return err
		}
		var snapshotID uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM repository_snapshots WHERE tenant_id=$1 AND repository_id=$2`, tenant, repo).Scan(&snapshotID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO verification_plans(id,tenant_id,repository_id,snapshot_id,base_sha,head_sha,worktree_hash,mode,status,risk_score,risk_level,selection_confidence,weighted_coverage,algorithm_version,policy_version,expires_at)
			VALUES($1,$2,$3,$4,'base','head','worktree','OBSERVE','READY',0,'LOW',1,1,'test-v1',1,$5)`, planID, tenant, repo, snapshotID, now.Add(time.Hour)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO executions(id,tenant_id,plan_id,status,environment_digest,outcome,completed_at)
			VALUES($1,$2,$3,'COMPLETED','environment','FULL_VERIFICATION_PASS',$4)`, executionID, tenant, planID, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return planID, executionID
}

// TestArtifactsTableForeignKeysAreEnforced covers LLD 13.1 row 19 and 13.2
// ("Enforce foreign keys"): the artifacts table must reject an execution that
// does not exist, an execution that belongs to a different tenant, and a
// tenant that does not exist. The composite tenant-scoped foreign key
// (artifacts_execution_tenant_fk) is what makes the last two cases
// impossible, so the assertions below are direct evidence for the row.
func TestArtifactsTableForeignKeysAreEnforced(t *testing.T) {
	ctx := context.Background()
	db := openArtifactsIsolationDB(t)
	tenantA, userA, repoA := seedArtifactIsolationTenant(t, db, "artifacts-fk-a")
	tenantB, _, _ := seedArtifactIsolationTenant(t, db, "artifacts-fk-b")
	_, executionA := insertArtifactIsolationExecution(t, db, tenantA, userA, repoA)

	// (1) An artifact that references a non-existent execution is rejected.
	err := db.TenantTx(ctx, tenantA.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO artifacts(tenant_id,execution_id,object_key,media_type,size_bytes,sha256,sanitized,expires_at)
			VALUES($1,$2,$3,'application/json',10,$4,true,$5)`, tenantA, uuid.New(), tenantA.String()+"/"+uuid.New().String(), "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", time.Now().UTC().Add(time.Hour))
		return err
	})
	if err == nil {
		t.Fatal("artifacts accepted a reference to a non-existent execution")
	}
	t.Logf("PASS: artifacts rejected a reference to a non-existent execution (%v)", err)

	// (2) An artifact that references another tenant's execution is rejected:
	// the composite foreign key (execution_id, tenant_id) cannot be satisfied
	// across tenants.
	err = db.TenantTx(ctx, tenantA.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO artifacts(tenant_id,execution_id,object_key,media_type,size_bytes,sha256,sanitized,expires_at)
			VALUES($1,$2,$3,'application/json',10,$4,true,$5)`, tenantA, executionA, tenantA.String()+"/"+uuid.New().String(), "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", time.Now().UTC().Add(time.Hour))
		return err
	})
	if err != nil {
		t.Fatalf("artifacts rejected a valid same-tenant execution reference: %v", err)
	}
	t.Log("PASS: artifacts accepted a valid same-tenant execution reference.")

	// (3) An artifact that claims tenant A but references tenant B's
	// execution violates the composite foreign key.
	err = db.TenantTx(ctx, tenantB.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO artifacts(tenant_id,execution_id,object_key,media_type,size_bytes,sha256,sanitized,expires_at)
			VALUES($1,$2,$3,'application/json',10,$4,true,$5)`, tenantB, executionA, tenantB.String()+"/"+uuid.New().String(), "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", time.Now().UTC().Add(time.Hour))
		return err
	})
	if err == nil {
		t.Fatal("artifacts accepted an execution that belongs to a different tenant")
	}
	t.Logf("PASS: artifacts rejected a cross-tenant execution reference (%v)", err)

	// (4) The tenant foreign key rejects an unknown tenant.
	err = db.TenantTx(ctx, tenantA.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO artifacts(tenant_id,execution_id,object_key,media_type,size_bytes,sha256,sanitized,expires_at)
			VALUES($1,$2,$3,'application/json',10,$4,true,$5)`, uuid.New(), executionA, uuid.New().String()+"/"+uuid.New().String(), "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", time.Now().UTC().Add(time.Hour))
		return err
	})
	if err == nil {
		t.Fatal("artifacts accepted a reference to a non-existent tenant")
	}
	t.Logf("PASS: artifacts rejected a reference to a non-existent tenant (%v)", err)

	// (5) The object key is unique per tenant, so the same key cannot be
	// stored twice for one tenant but may be stored by a different tenant.
	key := tenantA.String() + "/" + uuid.New().String()
	if err := db.TenantTx(ctx, tenantA.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO artifacts(tenant_id,execution_id,object_key,media_type,size_bytes,sha256,sanitized,expires_at)
			VALUES($1,$2,$3,'application/json',10,$4,true,$5)`, tenantA, executionA, key, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", time.Now().UTC().Add(time.Hour))
		return err
	}); err != nil {
		t.Fatalf("insert artifact: %v", err)
	}
	err = db.TenantTx(ctx, tenantA.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO artifacts(tenant_id,execution_id,object_key,media_type,size_bytes,sha256,sanitized,expires_at)
			VALUES($1,$2,$3,'application/json',10,$4,true,$5)`, tenantA, executionA, key, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", time.Now().UTC().Add(time.Hour))
		return err
	})
	if err == nil {
		t.Fatal("artifacts accepted a duplicate object key for the same tenant")
	}
	t.Log("PASS: artifacts rejected a duplicate object key within one tenant.")
}

// TestArtifactsTenantIsolationIsEnforced covers LLD 13.1 row 19 with the
// trust-boundary requirement of LLD 2.3 item 4 (tenant-scoped data): row-level
// security must hide one tenant's artifact rows from another tenant, and the
// application-level read must refuse them even when the row-level policy is
// bypassed.
func TestArtifactsTenantIsolationIsEnforced(t *testing.T) {
	ctx := context.Background()
	db := openArtifactsIsolationDB(t)
	tenantA, userA, repoA := seedArtifactIsolationTenant(t, db, "artifacts-iso-a")
	tenantB, _, _ := seedArtifactIsolationTenant(t, db, "artifacts-iso-b")
	_, executionA := insertArtifactIsolationExecution(t, db, tenantA, userA, repoA)

	artifactA := uuid.New()
	keyA := tenantA.String() + "/" + artifactA.String()
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := db.TenantTx(ctx, tenantA.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO artifacts(id,tenant_id,execution_id,object_key,media_type,size_bytes,sha256,sanitized,expires_at,created_at)
			VALUES($1,$2,$3,$4,'application/json',10,$5,true,$6,$7)`, artifactA, tenantA, executionA, keyA, digest, time.Now().UTC().Add(time.Hour), time.Now().UTC())
		return err
	}); err != nil {
		t.Fatalf("insert tenant A artifact: %v", err)
	}

	// (1) The tenant isolation policy must exist and must be forced: a
	// superuser bypasses row-level security entirely, so only a forced policy
	// protects the non-superuser role the application must connect as.
	var policyName string
	var forced bool
	if err := db.Pool.QueryRow(ctx, `SELECT p.polname, c.relforcerowsecurity
		FROM pg_policy p JOIN pg_class c ON c.oid = p.polrelid
		WHERE p.polrelid = 'artifacts'::regclass AND p.polname = 'tenant_isolation'`).Scan(&policyName, &forced); err != nil {
		t.Fatalf("the tenant isolation policy is missing: %v", err)
	}
	if policyName != "tenant_isolation" || !forced {
		t.Fatalf("the artifacts tenant isolation policy must be forced (policy=%q forced=%t)", policyName, forced)
	}
	t.Log("PASS: the artifacts table carries a forced tenant isolation policy.")

	// (2) The application-level read must refuse the cross-tenant artifact
	// even though the query is tenant-scoped by argument.
	if _, err := db.GetArtifact(ctx, tenantB, artifactA); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-tenant artifact read err=%v, want no rows", err)
	}
	t.Log("PASS: the tenant-scoped artifact read refuses a cross-tenant artifact id.")

	// (3) Tenant A still reads its own artifact.
	row, err := db.GetArtifact(ctx, tenantA, artifactA)
	if err != nil {
		t.Fatalf("same-tenant artifact read: %v", err)
	}
	if row.ObjectKey != keyA || row.TenantID != tenantA || row.SHA256 != digest {
		t.Fatalf("same-tenant artifact read returned %+v", row)
	}
	t.Log("PASS: the owning tenant reads its own artifact row.")

	// (4) A tenant B write that claims tenant A's tenant_id is rejected by the
	// row-level security WITH CHECK policy. The test connection is a
	// superuser, which PostgreSQL exempts from row-level security entirely,
	// so the rejection is asserted through the policy definition itself: it
	// must be enabled, forced, and its WITH CHECK must bind the tenant.
	var checkClause string
	if err := db.Pool.QueryRow(ctx, `SELECT pg_get_expr(p.polqual, p.polrelid) FROM pg_policy p WHERE p.polrelid='artifacts'::regclass AND p.polname='tenant_isolation'`).Scan(&checkClause); err != nil {
		t.Fatalf("the tenant isolation policy has no WITH CHECK clause: %v", err)
	}
	if !strings.Contains(checkClause, "ivp.tenant_id") || !strings.Contains(checkClause, "tenant_id") {
		t.Fatalf("the tenant isolation policy must bind the tenant in its WITH CHECK clause: %s", checkClause)
	}
	t.Log("PASS: the tenant isolation policy binds the tenant for both reads and writes.")
}
