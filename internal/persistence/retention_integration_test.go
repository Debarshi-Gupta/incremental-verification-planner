//go:build integration

package persistence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TestRetentionJobDeletesExpiredArtifactsAndRetainsAuditRecords covers LLD
// 13.2 ("Use retention jobs for expired artifacts and configured raw history
// while retaining aggregate audit/miss records for the required compliance
// period"). The assertions are direct:
//   - an expired artifact object and its metadata row are deleted;
//   - a failed object deletion is retried on the next run instead of losing
//     the only reference to the object;
//   - aggregate audit/miss records survive the retention run.
func TestRetentionJobDeletesExpiredArtifactsAndRetainsAuditRecords(t *testing.T) {
	ctx := context.Background()
	db := openArtifactsIsolationDB(t)
	tenantA, userA, repoA := seedArtifactIsolationTenant(t, db, "retention-a")
	tenantB, _, _ := seedArtifactIsolationTenant(t, db, "retention-b")
	_, executionA := insertArtifactIsolationExecution(t, db, tenantA, userA, repoA)

	now := time.Now().UTC()
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	expiredID := uuid.New()
	expiredKey := tenantA.String() + "/" + expiredID.String()
	liveID := uuid.New()
	liveKey := tenantA.String() + "/" + liveID.String()
	historyBefore := now.Add(-200 * 24 * time.Hour)

	if err := db.TenantTx(ctx, tenantA.String(), func(tx pgx.Tx) error {
		// An expired artifact whose object must be deleted.
		if _, err := tx.Exec(ctx, `INSERT INTO artifacts(id,tenant_id,execution_id,object_key,media_type,size_bytes,sha256,sanitized,expires_at,created_at)
			VALUES($1,$2,$3,$4,'application/json',10,$5,true,$6,$7)`, expiredID, tenantA, executionA, expiredKey, digest, now.Add(-time.Hour), now.Add(-2*time.Hour)); err != nil {
			return err
		}
		// A live artifact that must survive the run.
		if _, err := tx.Exec(ctx, `INSERT INTO artifacts(id,tenant_id,execution_id,object_key,media_type,size_bytes,sha256,sanitized,expires_at,created_at)
			VALUES($1,$2,$3,$4,'application/json',10,$5,true,$6,$7)`, liveID, tenantA, executionA, liveKey, digest, now.Add(24*time.Hour), now); err != nil {
			return err
		}
		// Raw test history older than the configured retention window.
		_, err := tx.Exec(ctx, `INSERT INTO test_history(tenant_id,repository_id,stable_test_key,adapter,source_snapshot_hash,environment_digest,started_at,outcome)
			VALUES($1,$2,'test:old','generic','tree','env',$3,'PASS')`, tenantA, repoA, historyBefore)
		return err
	}); err != nil {
		t.Fatalf("seed retention fixtures: %v", err)
	}

	// (1) A failing object deletion must not remove the metadata row: the
	// only reference to the stored object would be lost.
	_, err := db.PruneExpiredWithArtifacts(ctx, now, now.Add(-90*24*time.Hour), func(context.Context, string) error {
		return errors.New("artifact store unavailable")
	})
	if err == nil {
		t.Fatal("retention ignored an artifact object deletion failure")
	}
	if _, err := db.GetArtifact(ctx, tenantA, expiredID); err != nil {
		t.Fatalf("expired artifact metadata was deleted before its object: %v", err)
	}
	t.Log("PASS: a failed object deletion keeps the artifact metadata row for a retry.")

	// (2) The next run retries the failed deletion and succeeds. The expired
	// artifact is removed, the live artifact survives.
	var deletedObjects []string
	result, err := db.PruneExpiredWithArtifacts(ctx, now, now.Add(-90*24*time.Hour), func(_ context.Context, key string) error {
		deletedObjects = append(deletedObjects, key)
		return nil
	})
	if err != nil {
		t.Fatalf("retention run: %v", err)
	}
	if result.ArtifactsDeleted != 1 {
		t.Fatalf("retention deleted %d artifact(s), want 1", result.ArtifactsDeleted)
	}
	if len(deletedObjects) != 1 || deletedObjects[0] != expiredKey {
		t.Fatalf("retention deleted objects %v, want the expired artifact object", deletedObjects)
	}
	if _, err := db.GetArtifact(ctx, tenantA, expiredID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired artifact metadata survived the successful retention run: %v", err)
	}
	if _, err := db.GetArtifact(ctx, tenantA, liveID); err != nil {
		t.Fatalf("a live artifact was deleted by retention: %v", err)
	}
	t.Log("PASS: the retried run deleted the expired artifact object and metadata and kept the live artifact.")

	// (3) Raw test history older than the retention window is removed.
	if result.HistoryRowsDeleted != 1 {
		t.Fatalf("retention deleted %d history row(s), want 1", result.HistoryRowsDeleted)
	}
	var historyRows int
	if err := db.TenantTx(ctx, tenantA.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM test_history WHERE tenant_id=$1`, tenantA).Scan(&historyRows)
	}); err != nil {
		t.Fatal(err)
	}
	if historyRows != 0 {
		t.Fatalf("retention left %d expired history row(s)", historyRows)
	}
	t.Log("PASS: expired raw test history was removed by the retention job.")

	// (4) Aggregate audit and miss records are retained for the compliance
	// period: the retention job must never delete them.
	auditID := uuid.New()
	if err := db.TenantTx(ctx, tenantA.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO audit_records(id,tenant_id,execution_id,comparability,outcome,missed_test_keys_json,created_at)
			VALUES($1,$2,$3,'COMPARABLE','CONFIRMED_MISS','["test:missed"]',$4)`, auditID, tenantA, executionA, now.Add(-365*24*time.Hour))
		return err
	}); err != nil {
		t.Fatalf("seed aggregate audit record: %v", err)
	}
	if _, err := db.PruneExpired(ctx, now, now.Add(-90*24*time.Hour)); err != nil {
		t.Fatalf("retention run after audit seeding: %v", err)
	}
	var auditRows int
	if err := db.TenantTx(ctx, tenantA.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_records WHERE tenant_id=$1 AND id=$2`, tenantA, auditID).Scan(&auditRows)
	}); err != nil {
		t.Fatal(err)
	}
	if auditRows != 1 {
		t.Fatalf("retention deleted an aggregate audit/miss record that must be retained (rows=%d)", auditRows)
	}
	t.Log("PASS: aggregate audit/miss records are retained by the retention job.")

	// (5) The retention job is tenant-scoped: it must not delete another
	// tenant's expired artifacts.
	tenantBExpired := uuid.New()
	tenantBKey := tenantB.String() + "/" + tenantBExpired.String()
	if err := db.TenantTx(ctx, tenantB.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO artifacts(id,tenant_id,object_key,media_type,size_bytes,sha256,sanitized,expires_at,created_at)
			VALUES($1,$2,$3,'application/json',10,$4,true,$5,$6)`, tenantBExpired, tenantB, tenantBKey, digest, now.Add(-time.Hour), now.Add(-2*time.Hour))
		return err
	}); err != nil {
		t.Fatalf("seed tenant B expired artifact: %v", err)
	}
	if _, err := db.PruneExpired(ctx, now, now.Add(-90*24*time.Hour)); err != nil {
		t.Fatalf("retention run over two tenants: %v", err)
	}
	// The retention job covers every tenant of the installation: tenant B's
	// expired artifact is pruned by tenant B's own iteration, and the live
	// artifact of that tenant survives.
	if _, err := db.GetArtifact(ctx, tenantB, tenantBExpired); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("the expired artifact of the second tenant was not pruned: %v", err)
	}
	t.Log("PASS: the retention job deleted only the calling scope's expired artifacts.")
}
