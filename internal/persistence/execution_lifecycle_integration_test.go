//go:build integration

package persistence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
	"github.com/jackc/pgx/v5"
)

// seedExecutionLifecycleRepo creates the tenant, user, and repository the
// execution lifecycle tests attach their snapshots and plans to.
func seedExecutionLifecycleRepo(t *testing.T, db *DB) (tenant, user, repo uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tenant, user = uuid.New(), uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,$2)`, tenant, "lifecycle-tests"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO users(id,oidc_subject,email_hash) VALUES($1,$2,$3)`, user, "subject-"+user.String(), "hash-"+user.String()); err != nil {
		t.Fatal(err)
	}
	registered, err := db.RegisterRepository(ctx, tenant, user, domain.Repository{Slug: "lifecycle-" + user.String()[:8], CanonicalURL: "https://git.example.test/lifecycle"})
	if err != nil {
		t.Fatal(err)
	}
	return tenant, user, registered.ID
}

// seedLifecycleSnapshot stores a snapshot with the dependency and adapter
// version identity of LLD 23 and LLD 18.4.
func seedLifecycleSnapshot(t *testing.T, db *DB, tenant, repo uuid.UUID, tree, dependencyDigest string, adapterVersions map[string]string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	snapshotID, err := db.SaveSnapshot(ctx, SnapshotBundle{TenantID: tenant, Snapshot: domain.RepositorySnapshot{
		RepositoryID: repo, CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BaseSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		TreeHash: tree, WorktreeHash: "worktree-" + tree, ToolchainDigest: "toolchain", BuildConfigDigest: "build",
		TestConfigDigest: "test-config", AdapterSetDigest: "adapter-set", DependencyDigest: dependencyDigest,
		AdapterVersions: adapterVersions, GraphVersion: 1, EvidenceGeneratedAt: time.Now().UTC(), IsComplete: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return snapshotID
}

// seedLifecyclePlan stores a plan with the given environment fingerprint.
func seedLifecyclePlan(t *testing.T, db *DB, tenant, repo, snapshotID uuid.UUID, mode domain.PlanMode, environmentFingerprint string, units []domain.PlanUnit) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	plan := domain.VerificationPlan{ID: uuid.New(), RepositoryID: repo, SnapshotID: snapshotID, BaseSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", WorktreeHash: "worktree", Mode: mode, Status: domain.PlanReady,
		RiskScore: 10, RiskLevel: domain.RiskLow, SelectionConfidence: 1, WeightedCoverage: 1, AlgorithmVersion: "ivp-planner-v1.0.0",
		PolicyVersion: 1, EnvironmentFingerprint: environmentFingerprint, AdapterCapabilities: map[string]string{"pytest": "pytest-8.2"},
		SelectedUnits: units, MandatoryUnits: []domain.PlanUnit{}, OmittedUnits: []domain.OmittedUnit{},
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)}
	if err := db.SavePlan(ctx, tenant, plan); err != nil {
		t.Fatal(err)
	}
	return plan.ID
}

// TestSnapshotIdentityIncludesDependencyAndAdapterVersions proves that the
// dependency version and the adapter versions are part of the immutable
// snapshot identity (LLD 23 and LLD 18.4).
func TestSnapshotIdentityIncludesDependencyAndAdapterVersions(t *testing.T) {
	ctx := context.Background()
	db := openIntegrationDB(t)
	tenant, _, repo := seedExecutionLifecycleRepo(t, db)
	first := seedLifecycleSnapshot(t, db, tenant, repo, "tree-a", "dep-1", map[string]string{"pytest": "pytest-8.2"})
	second := seedLifecycleSnapshot(t, db, tenant, repo, "tree-a", "dep-2", map[string]string{"pytest": "pytest-8.2"})
	if first == second {
		t.Fatal("a different dependency version must produce a different snapshot")
	}
	bundle, err := db.LoadSnapshot(ctx, tenant, repo, second)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Snapshot.DependencyDigest != "dep-2" {
		t.Fatalf("the dependency digest was not stored: %+v", bundle.Snapshot)
	}
	if bundle.Snapshot.AdapterVersions["pytest"] != "pytest-8.2" {
		t.Fatalf("the adapter versions were not stored: %+v", bundle.Snapshot.AdapterVersions)
	}
}

// TestCoverageHistoryExposesPreviousDependencyVersion proves that coverage
// produced under a previous dependency version stays visible to the planner
// together with its dependency provenance, so the planner can mark it stale
// (LLD 23 scenario 6).
func TestCoverageHistoryExposesPreviousDependencyVersion(t *testing.T) {
	ctx := context.Background()
	db := openIntegrationDB(t)
	tenant, user, repo := seedExecutionLifecycleRepo(t, db)
	now := time.Now().UTC()
	oldSnapshot := seedLifecycleSnapshot(t, db, tenant, repo, "tree-old", "dep-old", map[string]string{"pytest": "pytest-8.2"})
	newSnapshot := seedLifecycleSnapshot(t, db, tenant, repo, "tree-new", "dep-new", map[string]string{"pytest": "pytest-8.2"})

	// The snapshot that produced the coverage carries one discovered unit and
	// one covered source file.
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		unitID, nodeID := uuid.New(), uuid.New()
		if _, err := tx.Exec(ctx, `INSERT INTO graph_nodes(id,tenant_id,snapshot_id,stable_key,node_type,path,confidence,evidence_source,observed_at)
			VALUES($1,$2,$3,'file:src/main.go','SOURCE_FILE','src/main.go',1,'fixture',$4)`, nodeID, tenant, oldSnapshot, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO test_units(id,tenant_id,snapshot_id,stable_key,unit_type,adapter,selectable,granularity,metadata_json)
			VALUES($1,$2,$3,'test:unit','TEST_CASE','pytest',true,'DECLARED_UNIT','{"test_config_digest":"test-config"}')`, unitID, tenant, oldSnapshot); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	planID := seedLifecyclePlan(t, db, tenant, repo, oldSnapshot, domain.ModeFastFeedback, "env-1",
		[]domain.PlanUnit{{StableKey: "test:unit", UnitType: domain.UnitTestCase, TargetRef: "test:unit", SelectionGranularity: "DECLARED_UNIT"}})
	execution, err := db.StartExecution(ctx, tenant, user, planID, "coverage-1", "worktree", "env-1", false, now)
	if err != nil {
		t.Fatal(err)
	}
	completed := now.Add(time.Second)
	submission := ResultSubmission{Outcome: domain.OutcomeTargetedPass, CompletedAt: completed, ElapsedMillis: 10, ComputeMillis: 10,
		UnitResults: []UnitResult{{TestUnitKey: "test:unit", Attempt: 1, Status: "PASSED", DurationMillis: 10}},
		Coverage: []domain.CoverageObservation{{TestUnitKey: "test:unit", CoveredStableKey: "file:src/main.go", SourceType: "pytest",
			ReportHash: "sha256:report", RunID: execution.ID, ObservedAt: completed, EnvironmentFingerprint: "env-1",
			BuildConfigDigest: "build", TestConfigDigest: "test-config", DependencyDigest: "dep-old", AdapterVersion: "pytest-8.2",
			Precision: domain.CoverageLine, LineStart: 1, LineEnd: 2, TestSpecific: true, EvidenceConfidence: 1}}}
	if err := db.SubmitExecutionResult(ctx, tenant, execution.ID, submission); err != nil {
		t.Fatalf("the coverage submission failed: %v", err)
	}

	// The planner reads the coverage history of the repository: the report is
	// visible together with the dependency version that produced it.
	history, err := db.LoadCoverageHistory(ctx, tenant, repo, "env-1", "build", "test-config", []string{"test:unit"})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("expected one coverage observation, got %d", len(history))
	}
	if history[0].DependencyDigest != "dep-old" || history[0].AdapterVersion != "pytest-8.2" {
		t.Fatalf("the coverage provenance was not stored: %+v", history[0])
	}
	bundle, err := db.LoadSnapshot(ctx, tenant, repo, newSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !domain.CoverageDependencyStale(history[0], bundle.Snapshot) {
		t.Fatal("coverage from a previous dependency version must be stale for the current snapshot")
	}
	if stale, reason := domain.CoverageAdapterStale(history[0], "pytest-9.0"); !stale || reason == "" {
		t.Fatalf("coverage from an incompatible adapter version must be stale: %v %q", stale, reason)
	}
}

// TestStartExecutionRejectsChangedEnvironmentFingerprint proves the LLD 9.4
// rule at execution start: a selective execution whose environment fingerprint
// differs from the plan fingerprint is refused and the plan is downgraded to
// the authoritative full-suite fallback.
func TestStartExecutionRejectsChangedEnvironmentFingerprint(t *testing.T) {
	ctx := context.Background()
	db := openIntegrationDB(t)
	tenant, user, repo := seedExecutionLifecycleRepo(t, db)
	now := time.Now().UTC()
	snapshotID := seedLifecycleSnapshot(t, db, tenant, repo, "tree-env", "dep-1", nil)
	planID := seedLifecyclePlan(t, db, tenant, repo, snapshotID, domain.ModeFastFeedback, "env-planned", nil)

	_, err := db.StartExecution(ctx, tenant, user, planID, "env-mismatch", "worktree", "env-other", false, now)
	if !errors.Is(err, ErrEnvironmentMismatch) {
		t.Fatalf("a changed environment fingerprint must refuse the selective execution, got %v", err)
	}
	plan, err := db.GetPlan(ctx, tenant, planID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanFullRequired {
		t.Fatalf("the invalidated plan must require full verification, got %s", plan.Status)
	}
	// The authoritative full-suite fallback may still run.
	if _, err := db.StartExecution(ctx, tenant, user, planID, "env-full", "worktree", "env-other", true, now); err != nil {
		t.Fatalf("the full-suite fallback must remain executable: %v", err)
	}
}

// TestSubmitExecutionResultMarksTargetedResultStaleOnEnvironmentMismatch
// proves the defense in depth of LLD 9.4: a selective result produced in an
// environment that differs from the plan fingerprint is never recorded as a
// targeted pass.
func TestSubmitExecutionResultMarksTargetedResultStaleOnEnvironmentMismatch(t *testing.T) {
	ctx := context.Background()
	db := openIntegrationDB(t)
	tenant, _, repo := seedExecutionLifecycleRepo(t, db)
	now := time.Now().UTC()
	snapshotID := seedLifecycleSnapshot(t, db, tenant, repo, "tree-stale", "dep-1", nil)
	planID := seedLifecyclePlan(t, db, tenant, repo, snapshotID, domain.ModeFastFeedback, "env-planned", nil)
	executionID := uuid.New()
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO executions(id,tenant_id,plan_id,status,environment_digest,started_at,summary_json)
			VALUES($1,$2,$3,'RUNNING','env-other',$4,'{"full_suite":false}')`, executionID, tenant, planID, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	submission := ResultSubmission{Outcome: domain.OutcomeTargetedPass, CompletedAt: now.Add(time.Second), ElapsedMillis: 10, ComputeMillis: 10}
	if err := db.SubmitExecutionResult(ctx, tenant, executionID, submission); err != nil {
		t.Fatalf("the result submission failed: %v", err)
	}
	execution, err := db.GetExecution(ctx, tenant, executionID)
	if err != nil {
		t.Fatal(err)
	}
	if execution.Outcome != domain.OutcomeStale {
		t.Fatalf("a selective result from a different environment must be stale, got %s", execution.Outcome)
	}
	plan, err := db.GetPlan(ctx, tenant, planID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanStale {
		t.Fatalf("the plan of a stale result must be stale, got %s", plan.Status)
	}
}

// TestObserveUpgradeResetsCISelectiveEligibility proves the LLD 18.4 upgrade
// rule against the database: a major algorithm or policy change resets the
// repository, and the calibration counters observed at that moment are stored
// so that pre-change evidence cannot re-enable selective gating.
func TestObserveUpgradeResetsCISelectiveEligibility(t *testing.T) {
	ctx := context.Background()
	db := openIntegrationDB(t)
	tenant, _, repo := seedExecutionLifecycleRepo(t, db)
	now := time.Now().UTC()

	state, err := db.ObserveUpgrade(ctx, tenant, repo, "ivp-planner-v1.0.0", 1, 0, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if state.Reset() {
		t.Fatal("the first observation must not reset the repository")
	}
	state, err = db.ObserveUpgrade(ctx, tenant, repo, "ivp-planner-v1.0.1", 1, 0, 0, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if state.Reset() {
		t.Fatal("a patch release must not reset the repository")
	}
	state, err = db.ObserveUpgrade(ctx, tenant, repo, "ivp-planner-v2.0.0", 1, 4, 2, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !state.Reset() {
		t.Fatal("a major algorithm change must reset the repository")
	}
	if state.CalibrationRunsAtReset != 4 || state.AuditRunsAtReset != 2 {
		t.Fatalf("the reset must record the observed calibration counters: %+v", state)
	}
	if state.Reason == "" {
		t.Fatal("the reset must explain itself")
	}
	// A new policy version resets the repository again.
	state, err = db.ObserveUpgrade(ctx, tenant, repo, "ivp-planner-v2.0.0", 2, 5, 3, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !state.Reset() || state.CalibrationRunsAtReset != 5 {
		t.Fatalf("a new policy version must reset the repository: %+v", state)
	}
	stored, err := db.UpgradeState(ctx, tenant, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Reset() || stored.PolicyVersion != 2 {
		t.Fatalf("the recorded upgrade state was not persisted: %+v", stored)
	}
}
