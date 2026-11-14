//go:build integration

package service

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/persistence"
	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func openServiceDB(t *testing.T) *persistence.DB {
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
		t.Fatal(err)
	}
	return db
}

// P0-2 regression: an expired-but-uncleared quarantine must still gate
// selective planning. LLD 7.2/10.5 require maintainer acknowledgement plus a
// clean comparable audit before selective gating resumes; expiry alone only
// makes a quarantine clearable, never inactive. A cleared quarantine stops
// gating.
func TestExpiredQuarantineStillGatesUntilCleared(t *testing.T) {
	ctx := context.Background()
	db := openServiceDB(t)
	backend := NewBackend(db, nil)

	tenant, user := uuid.New(), uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,$2)`, tenant, "service-quarantine-tests"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO users(id,oidc_subject,email_hash) VALUES($1,$2,$3)`, user, "subject-"+user.String(), "hash-"+user.String()); err != nil {
		t.Fatal(err)
	}
	repo, err := db.RegisterRepository(ctx, tenant, user, domain.Repository{Slug: "svc-quarantine-" + user.String()[:8], CanonicalURL: "https://git.example.test/svc-quarantine"})
	if err != nil {
		t.Fatal(err)
	}

	// Expired one hour ago, status still QUARANTINED: gating must hold.
	past := time.Now().UTC().Add(-time.Hour)
	if err := db.CreateQuarantine(ctx, tenant, user, repo.ID, "pytest/all", "AUDIT_POSSIBLE_MISS", nil, &past); err != nil {
		t.Fatal(err)
	}
	if scope, active := backend.quarantined(ctx, tenant, repo.ID, "pytest"); !active || scope != "pytest/all" {
		t.Fatalf("expired unacknowledged quarantine gating=(%q,%v), want active pytest/all", scope, active)
	}

	// Acknowledged but not cleared: gating still holds (acknowledgement is
	// only the precondition for eventual clearing, not for resuming).
	if err := db.AcknowledgeQuarantine(ctx, tenant, user, repo.ID, "pytest/all", "acknowledged for repair"); err != nil {
		t.Fatal(err)
	}
	if scope, active := backend.quarantined(ctx, tenant, repo.ID, "pytest"); !active || scope != "pytest/all" {
		t.Fatalf("acknowledged quarantine gating=(%q,%v), want still active pytest/all", scope, active)
	}

	// Non-matching adapter is not gated by a pytest-scoped quarantine.
	if scope, active := backend.quarantined(ctx, tenant, repo.ID, "jest"); active {
		t.Fatalf("jest adapter gated by pytest quarantine scope %q", scope)
	}

	// A CLEARED quarantine stops gating even though its expiry is in the past.
	// The clear transition itself is exercised against its full audit gates in
	// the persistence package; here the gating query only has to stop matching
	// once status is CLEARED (the update runs in TenantTx for row-level
	// security).
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE quarantines SET status='CLEARED',cleared_at=now() WHERE tenant_id=$1 AND repository_id=$2 AND scope_key=$3`, tenant, repo.ID, "pytest/all")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if scope, active := backend.quarantined(ctx, tenant, repo.ID, "pytest"); active {
		t.Fatalf("cleared quarantine still gates with scope %q", scope)
	}
}
