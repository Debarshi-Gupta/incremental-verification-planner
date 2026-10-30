//go:build integration

package persistence

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestPostgresMigrationsAndTenantIsolation(t *testing.T) {
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
		t.Fatalf("migrations from empty database: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("repeated migration run: %v", err)
	}
	tenantA, tenantB, user := uuid.New(), uuid.New(), uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,'A'),($2,'B')`, tenantA, tenantB); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO users(id,oidc_subject,email_hash) VALUES($1,'subject-a','hash')`, user); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO tenant_memberships(tenant_id,user_id,role) VALUES($1,$2,'maintainer')`, tenantA, user); err != nil {
		t.Fatal(err)
	}
	membership, err := db.ResolveMembership(ctx, "subject-a", tenantA.String())
	if err != nil {
		t.Fatal(err)
	}
	if membership.Role != "maintainer" || membership.Disabled {
		t.Fatalf("unexpected membership: %+v", membership)
	}
	if _, err := db.ResolveMembership(ctx, "subject-a", tenantB.String()); err == nil {
		t.Fatal("cross-tenant membership unexpectedly resolved")
	}
	repo, err := db.RegisterRepository(ctx, tenantA, user, domain.Repository{Slug: "service", CanonicalURL: "https://git.example.test/service"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetRepository(ctx, tenantB, repo.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-tenant repository read err=%v, want no rows", err)
	}
	page, err := db.ListRepositories(ctx, tenantB, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("tenant B received tenant A repositories: %+v", page.Items)
	}
	snapshotID, err := db.SaveSnapshot(ctx, SnapshotBundle{TenantID: tenantA, Snapshot: domain.RepositorySnapshot{
		RepositoryID: repo.ID, CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BaseSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		TreeHash: "tree-hash", WorktreeHash: "worktree-hash", ToolchainDigest: "toolchain", BuildConfigDigest: "build",
		AdapterSetDigest: "adapter-set", GraphVersion: 1, EvidenceGeneratedAt: time.Now().UTC(), IsComplete: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.LoadSnapshot(ctx, tenantB, repo.ID, snapshotID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-tenant snapshot read err=%v, want no rows", err)
	}
	now := time.Now().UTC()
	planID, executionID, artifactID := uuid.New(), uuid.New(), uuid.New()
	if err := db.TenantTx(ctx, tenantA.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO verification_plans(id,tenant_id,repository_id,snapshot_id,base_sha,head_sha,worktree_hash,mode,status,risk_score,risk_level,selection_confidence,weighted_coverage,algorithm_version,policy_version,expires_at)
			VALUES($1,$2,$3,$4,'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','worktree','FULL','READY',0,'LOW',1,1,'test-v1',1,$5)`, planID, tenantA, repo.ID, snapshotID, now.Add(time.Hour)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO executions(id,tenant_id,plan_id,status,environment_digest,outcome,completed_at)
			VALUES($1,$2,$3,'COMPLETED','environment','FULL_VERIFICATION_PASS',$4)`, executionID, tenantA, planID, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	artifactRow := ArtifactMetadata{ID: artifactID, TenantID: tenantA, ExecutionID: executionID, ObjectKey: tenantA.String() + "/" + artifactID.String(), MediaType: "application/xml", SizeBytes: 12, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Sanitized: true, ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	if err := db.SaveArtifact(ctx, user, artifactRow); err != nil {
		t.Fatalf("save tenant artifact: %v", err)
	}
	if got, err := db.GetArtifact(ctx, tenantA, artifactID); err != nil || got.ObjectKey != artifactRow.ObjectKey {
		t.Fatalf("same-tenant artifact read = %+v, %v", got, err)
	}
	if _, err := db.GetArtifact(ctx, tenantB, artifactID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-tenant artifact read err=%v, want no rows", err)
	}
	var expiredArtifactID uuid.UUID
	err = db.TenantTx(ctx, tenantA.String(), func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO artifacts(tenant_id,object_key,media_type,size_bytes,sha256,sanitized,expires_at) VALUES($1,'expired/test-report','application/json',12,'sha256',true,$2) RETURNING id`, tenantA, now.Add(-time.Hour)).Scan(&expiredArtifactID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO test_history(tenant_id,repository_id,stable_test_key,adapter,source_snapshot_hash,environment_digest,started_at,outcome) VALUES($1,$2,'test','generic','tree','env',$3,'PASS')`, tenantA, repo.ID, now.Add(-100*24*time.Hour))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.PruneExpiredWithArtifacts(ctx, now, now.Add(-90*24*time.Hour), func(context.Context, string) error {
		return errors.New("artifact store unavailable")
	}); err == nil {
		t.Fatal("retention ignored an artifact object deletion failure")
	}
	if _, err := db.GetArtifact(ctx, tenantA, expiredArtifactID); err != nil {
		t.Fatalf("expired metadata was deleted before its object: %v", err)
	}
	var deletedObjects []string
	pruned, err := db.PruneExpiredWithArtifacts(ctx, now, now.Add(-90*24*time.Hour), func(_ context.Context, key string) error {
		deletedObjects = append(deletedObjects, key)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if pruned.ArtifactsDeleted != 1 || pruned.HistoryRowsDeleted != 1 {
		t.Fatalf("retention result = %+v, want one expired artifact and one old history row", pruned)
	}
	if len(deletedObjects) != 1 || deletedObjects[0] != "expired/test-report" {
		t.Fatalf("retention deleted objects = %v, want the expired report object", deletedObjects)
	}
	err = db.TenantTx(ctx, tenantA.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE repository_snapshots SET tree_hash='mutated' WHERE tenant_id=$1 AND id=$2`, tenantA, snapshotID)
		return err
	})
	if err == nil {
		t.Fatal("immutable snapshot update unexpectedly succeeded")
	}
}
