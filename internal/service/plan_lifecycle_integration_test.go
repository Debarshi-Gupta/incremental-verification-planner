//go:build integration

package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/api"
	"github.com/ivp/ivp/internal/config"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/persistence"
	"github.com/ivp/ivp/internal/security"
	"github.com/jackc/pgx/v5"
)

// seedLifecycleRepository creates the tenant, maintainer, and repository the
// plan lifecycle tests attach their snapshots to.
func seedLifecycleRepository(t *testing.T, db *persistence.DB) (tenant, user, repo uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tenant, user = uuid.New(), uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,$2)`, tenant, "plan-lifecycle-tests"); err != nil {
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

// storeLifecyclePolicy stores an effective policy with a configured local
// full-suite fallback. The stored policy is the server-owned baseline the
// planner and the upgrade rule work with.
func storeLifecyclePolicy(t *testing.T, db *persistence.DB, tenant, user, repo uuid.UUID, riskBasedCI bool) int64 {
	t.Helper()
	ctx := context.Background()
	policy := config.ToDomainPolicy(config.Defaults(), 1, nil)
	policy.FullSuiteTargetRef = "full-suite"
	policy.RiskBasedCIEnabled = riskBasedCI
	policy.RiskBasedCIApproved = riskBasedCI
	policy.MinimumCalibrationRuns = 1
	policy.MinimumAuditRuns = 1
	encoded, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := security.CanonicalJSONHash(encoded)
	if err != nil {
		t.Fatal(err)
	}
	version, err := db.SavePolicy(ctx, tenant, user, repo, policy, hash)
	if err != nil {
		t.Fatal(err)
	}
	return version
}

// seedLifecycleEvidence stores one immutable snapshot with two discovered
// units, a covered source file, and one coverage observation whose dependency
// and adapter provenance are chosen by the caller.
func seedLifecycleEvidence(t *testing.T, db *persistence.DB, tenant, repo uuid.UUID, tree, dependencyDigest, coverageDependencyDigest, adapterVersion string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	snapshotID, sourceID, testID, unitID, otherUnitID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	snapshot := domain.RepositorySnapshot{ID: snapshotID, RepositoryID: repo, CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BaseSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", TreeHash: tree, WorktreeHash: "worktree-" + tree,
		ToolchainDigest: "toolchain", BuildConfigDigest: "build", TestConfigDigest: "test-config", AdapterSetDigest: "adapter-set",
		DependencyDigest: dependencyDigest, AdapterVersions: map[string]string{"pytest": "pytest-8.2"},
		GraphVersion: 1, EvidenceGeneratedAt: now, IsComplete: true}
	unit := domain.TestUnit{ID: unitID, SnapshotID: snapshotID, StableKey: "test:unit", Adapter: "pytest", UnitType: domain.UnitTestCase,
		EstimatedMillis: 100, DurationP50Millis: 100, DurationP95Millis: 100, DurationSampleCount: 5, Selectable: true,
		SelectionGranularity: "DECLARED_UNIT", TargetRef: "test:unit", SourcePaths: []string{"src/main.go"},
		Metadata: map[string]any{"test_config_digest": "test-config"}}
	// A second, unrelated unit keeps the full-suite estimate above the
	// selective runtime ceiling, so the plan economics stay comparable.
	other := domain.TestUnit{ID: otherUnitID, SnapshotID: snapshotID, StableKey: "test:other", Adapter: "pytest", UnitType: domain.UnitTestCase,
		EstimatedMillis: 100_000, DurationP50Millis: 100_000, DurationP95Millis: 100_000, DurationSampleCount: 5, Selectable: true,
		SelectionGranularity: "DECLARED_UNIT", TargetRef: "test:other", SourcePaths: []string{"src/other.go"},
		Metadata: map[string]any{"test_config_digest": "test-config"}}
	observation := domain.CoverageObservation{TestUnitKey: unit.StableKey, CoveredStableKey: "file:src/main.go", CoveredPath: "src/main.go",
		SourceType: "pytest", ReportHash: "sha256:report", RunID: uuid.New(), ObservedAt: now, EnvironmentFingerprint: "toolchain",
		BuildConfigDigest: "build", TestConfigDigest: "test-config", DependencyDigest: coverageDependencyDigest, AdapterVersion: adapterVersion,
		Precision: domain.CoverageLine, LineStart: 1, LineEnd: 1, TestSpecific: true, EvidenceConfidence: 1}
	// The policy's mandatory checks must be discovered, otherwise the planner
	// escalates every plan with MANDATORY_CHECK_UNRESOLVED and the fixture
	// would not exercise the rule under test.
	units := []domain.TestUnit{unit, other}
	for _, checkID := range config.Defaults().Safety.AlwaysRunCheckIDs {
		units = append(units, domain.TestUnit{ID: uuid.New(), SnapshotID: snapshotID, StableKey: checkID, Adapter: "pytest",
			UnitType: domain.UnitCheck, EstimatedMillis: 10, DurationP50Millis: 10, DurationP95Millis: 10, DurationSampleCount: 5,
			Selectable: true, SelectionGranularity: "DECLARED_UNIT", TargetRef: checkID,
			Metadata: map[string]any{"test_config_digest": "test-config"}})
	}
	bundle := persistence.SnapshotBundle{TenantID: tenant, Snapshot: snapshot,
		Nodes: []domain.GraphNode{
			{ID: sourceID, SnapshotID: snapshotID, NodeType: domain.NodeSourceFile, StableKey: "file:src/main.go", Path: "src/main.go", Confidence: 1, EvidenceSource: "fixture", ObservedAt: now},
			{ID: testID, SnapshotID: snapshotID, NodeType: domain.NodeTestTarget, StableKey: "test:unit", Confidence: 1, EvidenceSource: "fixture", ObservedAt: now}},
		Edges:     []domain.GraphEdge{{ID: uuid.New(), SnapshotID: snapshotID, FromNodeID: testID, ToNodeID: sourceID, EdgeType: domain.EdgeTestCovers, Confidence: 1, EvidenceSource: "fixture", ObservedAt: now}},
		TestUnits: units, Coverage: []domain.CoverageObservation{observation}}
	if _, err := db.SaveSnapshot(ctx, bundle); err != nil {
		t.Fatal(err)
	}
	// The repository ran a full verification just now, so the LLD 10.2
	// scheduled-audit cadence is not reached and the plans under test are not
	// forced to full verification.
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		fullPlanID, fullExecutionID := uuid.New(), uuid.New()
		if _, err := tx.Exec(ctx, `INSERT INTO verification_plans(id,tenant_id,repository_id,snapshot_id,base_sha,head_sha,worktree_hash,mode,status,risk_score,risk_level,selection_confidence,weighted_coverage,algorithm_version,policy_version,estimated_duration_ms,expires_at)
			VALUES($1,$2,$3,$4,'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',$5,'FULL','READY',10,'LOW',1,1,'test-v1',1,1000,$6)`, fullPlanID, tenant, repo, snapshotID, "worktree-"+tree, now.Add(time.Hour)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO executions(id,tenant_id,plan_id,status,environment_digest,outcome,completed_at,elapsed_ms)
			VALUES($1,$2,$3,'COMPLETED','toolchain','FULL_VERIFICATION_PASS',$4,500)`, fullExecutionID, tenant, fullPlanID, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return snapshotID
}

// createLifecyclePlan requests a plan through the application service.
func createLifecyclePlan(t *testing.T, backend *Backend, tenant, user, repo, snapshotID uuid.UUID, tree string, mode domain.PlanMode, adapterVersion string) (domain.VerificationPlan, int, error) {
	t.Helper()
	request := api.PlanRequest{SnapshotID: snapshotID.String(), BaseSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", WorktreeHash: "worktree-" + tree,
		ChangedFiles: []domain.ChangedFile{{Path: "src/main.go", Status: domain.ChangeModified}}, Mode: mode,
		ClientCapabilities: api.ClientCapabilities{OS: "linux", Arch: "amd64", Adapters: []string{"pytest"},
			AdapterVersions: map[string]string{"pytest": adapterVersion}},
		ExecutionContext: api.ExecutionContext{ToolchainDigest: "toolchain", EnvironmentClass: "linux-ci"}}
	response, status, err := backend.Execute(context.Background(), api.Action{Operation: "plans.create",
		Principal:  security.Principal{Subject: "sub", TenantID: tenant.String(), UserID: user.String(), Role: security.RoleExecutor},
		ResourceID: repo.String(), Payload: request})
	if err != nil {
		return domain.VerificationPlan{}, status, err
	}
	plan, ok := response.(domain.VerificationPlan)
	if !ok {
		t.Fatalf("unexpected plan response: %T", response)
	}
	return plan, status, nil
}

func planHasEscalation(plan domain.VerificationPlan, code domain.EscalationReasonCode) bool {
	for _, escalation := range plan.EscalationReasons {
		if escalation.Code == code {
			return true
		}
	}
	return false
}

// TestPlanCreationMarksCoverageFromPreviousDependencyVersionStale proves the
// LLD 23 scenario 6 behaviour against a real database: identical evidence
// produces an executable plan while the coverage report belongs to a previous
// dependency version, and an escalated plan when it does not.
func TestPlanCreationMarksCoverageFromPreviousDependencyVersionStale(t *testing.T) {
	db := openServiceDB(t)
	backend := NewBackend(db, nil)
	tenant, user, repo := seedLifecycleRepository(t, db)
	storeLifecyclePolicy(t, db, tenant, user, repo, false)
	comparable := seedLifecycleEvidence(t, db, tenant, repo, "tree-comparable", "dep-current", "dep-current", "pytest-8.2")
	previous := seedLifecycleEvidence(t, db, tenant, repo, "tree-previous", "dep-current", "dep-previous", "pytest-8.2")

	plan, status, err := createLifecyclePlan(t, backend, tenant, user, repo, comparable, "tree-comparable", domain.ModeFastFeedback, "pytest-8.2")
	if err != nil {
		t.Fatal(err)
	}
	if status != 201 || plan.Status != domain.PlanReady {
		t.Fatalf("comparable coverage must produce an executable plan: status=%d plan=%s reasons=%+v", status, plan.Status, plan.EscalationReasons)
	}
	plan, status, err = createLifecyclePlan(t, backend, tenant, user, repo, previous, "tree-previous", domain.ModeFastFeedback, "pytest-8.2")
	if err != nil {
		t.Fatal(err)
	}
	if status != 201 {
		t.Fatalf("the plan request must succeed: %d", status)
	}
	// LLD 23 scenario 6: the report that belongs to a previous dependency
	// version is marked stale and excluded from the evidence. The plan itself
	// escalates only when the remaining evidence cannot meet the coverage and
	// confidence thresholds (LLD 6.3 rule 6); here the comparable history
	// still covers the changed file, so the plan stays executable.
	marked := false
	for _, check := range plan.HardRuleChecks {
		if check.Code == domain.EscCoverageStale && check.Triggered {
			marked = true
		}
	}
	if !marked {
		t.Fatalf("the plan must mark the stale coverage: %+v", plan.HardRuleChecks)
	}
	if len(plan.SelectedUnits) == 0 {
		t.Fatalf("the plan must select its verification units: %+v", plan.SelectedUnits)
	}
}

// TestPlanCreationInvalidatesAdapterEvidenceAfterIncompatibleVersionChange
// proves the LLD 18.4 adapter rule against a real database.
func TestPlanCreationInvalidatesAdapterEvidenceAfterIncompatibleVersionChange(t *testing.T) {
	db := openServiceDB(t)
	backend := NewBackend(db, nil)
	tenant, user, repo := seedLifecycleRepository(t, db)
	storeLifecyclePolicy(t, db, tenant, user, repo, false)
	snapshotID := seedLifecycleEvidence(t, db, tenant, repo, "tree-adapter", "dep-current", "dep-current", "pytest-8.2")

	plan, _, err := createLifecyclePlan(t, backend, tenant, user, repo, snapshotID, "tree-adapter", domain.ModeFastFeedback, "pytest-8.2")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanReady {
		t.Fatalf("an unchanged adapter version must keep the plan executable: %s %+v", plan.Status, plan.EscalationReasons)
	}
	plan, _, err = createLifecyclePlan(t, backend, tenant, user, repo, snapshotID, "tree-adapter", domain.ModeFastFeedback, "pytest-9.0")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanFullRequired {
		t.Fatalf("an incompatible adapter version change must escalate the plan, got %s", plan.Status)
	}
	if !planHasEscalation(plan, domain.EscCoverageStale) && !planHasEscalation(plan, domain.EscGraphStale) {
		t.Fatalf("the plan must report the invalidated adapter evidence: %+v", plan.EscalationReasons)
	}
}

// TestExecutionStartRejectsChangedEnvironmentFingerprint proves the LLD 9.4
// rule at the API boundary against a real database.
func TestExecutionStartRejectsChangedEnvironmentFingerprint(t *testing.T) {
	ctx := context.Background()
	db := openServiceDB(t)
	backend := NewBackend(db, nil)
	tenant, user, repo := seedLifecycleRepository(t, db)
	storeLifecyclePolicy(t, db, tenant, user, repo, false)
	snapshotID := seedLifecycleEvidence(t, db, tenant, repo, "tree-execution", "dep-current", "dep-current", "pytest-8.2")
	plan, _, err := createLifecyclePlan(t, backend, tenant, user, repo, snapshotID, "tree-execution", domain.ModeFastFeedback, "pytest-8.2")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanReady {
		t.Fatalf("the fixture must produce an executable plan: %s %+v", plan.Status, plan.EscalationReasons)
	}
	if plan.EnvironmentFingerprint != "toolchain" {
		t.Fatalf("the plan must record its environment fingerprint: %+v", plan)
	}
	_, _, err = backend.Execute(ctx, api.Action{Operation: "executions.start",
		Principal:  security.Principal{Subject: "sub", TenantID: tenant.String(), UserID: user.String(), Role: security.RoleExecutor},
		ResourceID: plan.ID.String(), IdempotencyKey: "env-mismatch",
		Payload: api.ExecutionStartRequest{WorktreeHash: plan.WorktreeHash, EnvironmentDigest: "toolchain-other", FullSuite: false}})
	if err == nil {
		t.Fatal("a changed environment fingerprint must refuse the selective execution")
	}
	apiErr, ok := err.(*api.APIError)
	if !ok || apiErr.Code != "ENVIRONMENT_MISMATCH" || apiErr.Status != 409 {
		t.Fatalf("unexpected error for the changed environment fingerprint: %v", err)
	}
	stored, err := db.GetPlan(ctx, tenant, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.PlanFullRequired {
		t.Fatalf("the invalidated plan must require full verification, got %s", stored.Status)
	}
}

// TestRiskBasedCIResetsToObserveAfterMajorPolicyChange proves the LLD 18.4
// upgrade rule against a real database: after a new policy version the CI
// selective eligibility is reset to OBSERVE until the repository is
// re-calibrated.
func TestRiskBasedCIResetsToObserveAfterMajorPolicyChange(t *testing.T) {
	ctx := context.Background()
	db := openServiceDB(t)
	backend := NewBackend(db, nil)
	tenant, user, repo := seedLifecycleRepository(t, db)
	storeLifecyclePolicy(t, db, tenant, user, repo, true)
	snapshotID := seedLifecycleEvidence(t, db, tenant, repo, "tree-upgrade", "dep-current", "dep-current", "pytest-8.2")
	// One completed OBSERVE run and one comparable audit calibrate the
	// repository for the stored policy thresholds.
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		planID, executionID := uuid.New(), uuid.New()
		if _, err := tx.Exec(ctx, `INSERT INTO verification_plans(id,tenant_id,repository_id,snapshot_id,base_sha,head_sha,worktree_hash,mode,status,risk_score,risk_level,selection_confidence,weighted_coverage,algorithm_version,policy_version,estimated_duration_ms,expires_at)
			VALUES($1,$2,$3,$4,'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','worktree-tree-upgrade','OBSERVE','READY',10,'LOW',1,1,'test-v1',1,1000,$5)`, planID, tenant, repo, snapshotID, time.Now().UTC().Add(time.Hour)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO executions(id,tenant_id,plan_id,status,environment_digest,outcome,completed_at,elapsed_ms)
			VALUES($1,$2,$3,'COMPLETED','toolchain','TARGETED_PASS',$4,500)`, executionID, tenant, planID, time.Now().UTC()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO audit_records(tenant_id,execution_id,comparability,outcome,details_json)
			VALUES($1,$2,'COMPARABLE',$3,'{"scope_key":"pytest/all"}')`, tenant, executionID, domain.AuditNoMissObserved)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// The first plan establishes the baseline of the recorded algorithm and
	// policy versions.
	plan, _, err := createLifecyclePlan(t, backend, tenant, user, repo, snapshotID, "tree-upgrade", domain.ModeRiskBasedCI, "pytest-8.2")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != domain.ModeRiskBasedCI {
		t.Fatalf("without a reset the requested mode must be kept, got %s", plan.Mode)
	}
	// A new policy version is a major policy change: the CI selective
	// eligibility is reset to OBSERVE.
	storeLifecyclePolicy(t, db, tenant, user, repo, true)
	plan, _, err = createLifecyclePlan(t, backend, tenant, user, repo, snapshotID, "tree-upgrade", domain.ModeRiskBasedCI, "pytest-8.2")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != domain.ModeObserve {
		t.Fatalf("a major policy change must reset the CI selective eligibility to OBSERVE, got %s", plan.Mode)
	}
	if !strings.Contains(plan.Explanation, "reset to OBSERVE") {
		t.Fatalf("the plan must explain the reset: %s", plan.Explanation)
	}
}
