//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/audit"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/persistence"
	"github.com/jackc/pgx/v5"
)

// seedAuditScenario creates a tenant, maintainer, repository, snapshot, plan,
// one selected unit, one omitted unit, and the selected/full execution pair an
// audit compares. The selected run passed while the authoritative full suite
// failed, which is the precondition for a possible or confirmed miss.
func seedAuditScenario(t *testing.T, db *persistence.DB) (tenant, user, repo, planID, selectedExecution, fullExecution uuid.UUID, selectedKey, omittedKey string) {
	t.Helper()
	ctx := context.Background()
	tenant, user = uuid.New(), uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,$2)`, tenant, "audit-scenario-tests"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO users(id,oidc_subject,email_hash) VALUES($1,$2,$3)`, user, "subject-"+user.String(), "hash-"+user.String()); err != nil {
		t.Fatal(err)
	}
	registered, err := db.RegisterRepository(ctx, tenant, user, domain.Repository{Slug: "audit-" + user.String()[:8], CanonicalURL: "https://git.example.test/audit-scenario"})
	if err != nil {
		t.Fatal(err)
	}
	repo = registered.ID
	selectedKey, omittedKey = "test/selected.py", "test/omitted.py"
	snapshotID, err := db.SaveSnapshot(ctx, persistence.SnapshotBundle{TenantID: tenant, Snapshot: domain.RepositorySnapshot{
		RepositoryID: repo, CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BaseSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		TreeHash: "tree-audit", WorktreeHash: "worktree-audit", ToolchainDigest: "toolchain", BuildConfigDigest: "build",
		AdapterSetDigest: "adapter-set", GraphVersion: 1, EvidenceGeneratedAt: time.Now().UTC(), IsComplete: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	planID, selectedExecution, fullExecution = uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO verification_plans(id,tenant_id,repository_id,snapshot_id,base_sha,head_sha,worktree_hash,mode,status,risk_score,risk_level,selection_confidence,weighted_coverage,algorithm_version,policy_version,estimated_duration_ms,expires_at)
			VALUES($1,$2,$3,$4,'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','worktree','FAST_FEEDBACK','READY',10,'LOW',1,1,'test-v1',1,1000,$5)`, planID, tenant, repo, snapshotID, now.Add(time.Hour)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO verification_plan_units(tenant_id,plan_id,test_unit_key,selection_order,mandatory) VALUES($1,$2,$3,1,false)`, tenant, planID, selectedKey); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO omitted_plan_units(tenant_id,plan_id,test_unit_key) VALUES($1,$2,$3)`, tenant, planID, omittedKey); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO executions(id,tenant_id,plan_id,status,environment_digest,outcome,completed_at,elapsed_ms)
			VALUES($1,$2,$3,'COMPLETED','environment','TARGETED_PASS',$4,500)`, selectedExecution, tenant, planID, now); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO executions(id,tenant_id,plan_id,status,environment_digest,outcome,completed_at,elapsed_ms,summary_json)
			VALUES($1,$2,$3,'COMPLETED','environment','FULL_VERIFICATION_FAIL',$4,900,'{"full_suite":true}')`, fullExecution, tenant, planID, now)
		if err != nil {
			return err
		}
		// The audit validator requires every reported failure to be a
		// persisted FAILED/ERROR unit result of the authoritative full run.
		_, err = tx.Exec(ctx, `INSERT INTO execution_unit_results(tenant_id,execution_id,test_unit_key,attempt,status,duration_ms)
			VALUES($1,$2,$3,1,'FAILED',900)`, tenant, fullExecution, omittedKey)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return tenant, user, repo, planID, selectedExecution, fullExecution, selectedKey, omittedKey
}

func comparableFingerprints() audit.Fingerprint {
	return audit.Fingerprint{SourceTreeHash: "tree-audit", DependencyDigest: "deps", BuildConfigDigest: "build",
		ToolchainDigest: "toolchain", TestConfigDigest: "tests", EnvironmentClass: "linux-ci", RequiredServicesDigest: "services"}
}

// LLD 23 scenario 11: selected checks pass but the audit full suite finds an
// omitted failing integration test; a quarantine must be created in the same
// transaction and must gate selective planning for the adapter's scope.
//
// LLD 23 scenario 12: the audit full suite ran with a different dependency
// digest; the audit must be recorded INCOMPARABLE, must not create a
// quarantine of its own, and must not satisfy the clean-audit gate that lifts
// the existing quarantine.
func TestAuditScenariosQuarantineOnConfirmedMissAndIncomparableDigest(t *testing.T) {
	ctx := context.Background()
	db := openServiceDB(t)
	backend := NewBackend(db, nil)
	tenant, user, repo, planID, selectedExecution, fullExecution, selectedKey, omittedKey := seedAuditScenario(t, db)

	// Scenario 11: equivalent environments, selected run passed, the omitted
	// integration test fails in the authoritative full suite.
	fingerprints := comparableFingerprints()
	confirmed, err := backend.Audit.Process(ctx, audit.Input{
		TenantID: tenant, RepositoryID: repo, PlanID: planID,
		SelectedExecutionID: selectedExecution, FullExecutionID: fullExecution,
		Adapter: "pytest", SelectedOutcome: domain.OutcomeTargetedPass, FullOutcome: domain.OutcomeFullVerificationFail,
		SelectedKeys:        []string{selectedKey},
		Failures:            []audit.Failure{{TestUnitKey: omittedKey, Reproduced: true, RelatedToChange: true}},
		SelectedFingerprint: fingerprints, FullFingerprint: fingerprints,
	})
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Disposition != audit.DispositionConfirmed || !confirmed.Quarantine || confirmed.Record.Outcome != domain.AuditConfirmedMiss {
		t.Fatalf("scenario 11 audit not classified as a confirmed miss: %+v", confirmed)
	}
	if scope, active := backend.quarantined(ctx, tenant, repo, "pytest"); !active || scope != "pytest/all" {
		t.Fatalf("confirmed miss did not quarantine pytest/all: scope=%q active=%v", scope, active)
	}

	// Scenario 12: the audit full suite ran against a different dependency
	// digest, so it cannot confirm or clear anything about the quarantine.
	changed := fingerprints
	changed.DependencyDigest = "deps-v2"
	incomparable, err := backend.Audit.Process(ctx, audit.Input{
		TenantID: tenant, RepositoryID: repo, PlanID: planID,
		SelectedExecutionID: selectedExecution, FullExecutionID: fullExecution,
		Adapter: "pytest", SelectedOutcome: domain.OutcomeTargetedPass, FullOutcome: domain.OutcomeFullVerificationFail,
		SelectedKeys:        []string{selectedKey},
		Failures:            []audit.Failure{{TestUnitKey: omittedKey, Reproduced: true, RelatedToChange: true}},
		SelectedFingerprint: fingerprints, FullFingerprint: changed,
	})
	if err != nil {
		t.Fatal(err)
	}
	if incomparable.Disposition != audit.DispositionIncomparable || incomparable.Quarantine || incomparable.Record.Outcome != domain.AuditIncomparable {
		t.Fatalf("scenario 12 audit not marked incomparable: %+v", incomparable)
	}
	var comparability string
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT comparability FROM audit_records WHERE tenant_id=$1 AND id=$2`, tenant, incomparable.Record.ID).Scan(&comparability)
	}); err != nil {
		t.Fatal(err)
	}
	if comparability != "INCOMPARABLE" {
		t.Fatalf("audit comparability=%q, want INCOMPARABLE", comparability)
	}
	if scope, active := backend.quarantined(ctx, tenant, repo, "pytest"); !active || scope != "pytest/all" {
		t.Fatalf("incomparable audit released the quarantine: scope=%q active=%v", scope, active)
	}

	// The incomparable audit must not satisfy the clear gate either: after
	// acknowledgement and once the expiry window has passed, clearing still
	// fails because no clean comparable audit exists.
	if err := db.AcknowledgeQuarantine(ctx, tenant, user, repo, "pytest/all", "acknowledged for repair"); err != nil {
		t.Fatal(err)
	}
	if err := db.ClearEligibleQuarantine(ctx, tenant, user, repo, "pytest/all", "attempted clear with incomparable audit", time.Now().UTC().Add(8*24*time.Hour)); err == nil {
		t.Fatal("an incomparable audit allowed the quarantine to be cleared")
	}
}

// ADR 0015: an audit reclassifies the audited selected execution's stored
// outcome, and a later audit may still declare the originating pass. The
// stored outcome must walk AUDIT_MISS -> AUDIT_POSSIBLE_MISS ->
// AUDIT_INCOMPARABLE across three re-audits of the same execution pair.
func TestAuditReclassificationChainUpdatesExecutionOutcome(t *testing.T) {
	ctx := context.Background()
	db := openServiceDB(t)
	backend := NewBackend(db, nil)
	tenant, _, repo, planID, selectedExecution, fullExecution, selectedKey, omittedKey := seedAuditScenario(t, db)

	storedOutcome := func() domain.ExecutionOutcome {
		t.Helper()
		var got domain.ExecutionOutcome
		if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT outcome FROM executions WHERE tenant_id=$1 AND id=$2`, tenant, selectedExecution).Scan(&got)
		}); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := storedOutcome(); got != domain.OutcomeTargetedPass {
		t.Fatalf("seeded selected execution outcome = %s, want TARGETED_PASS", got)
	}

	fingerprints := comparableFingerprints()
	process := func(name string, in audit.Input) audit.Result {
		t.Helper()
		result, err := backend.Audit.Process(ctx, in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return result
	}
	base := func() audit.Input {
		return audit.Input{
			TenantID: tenant, RepositoryID: repo, PlanID: planID,
			SelectedExecutionID: selectedExecution, FullExecutionID: fullExecution,
			Adapter: "pytest", FullOutcome: domain.OutcomeFullVerificationFail,
			SelectedKeys: []string{selectedKey},
			// Every audit declares the outcome the execution held before any
			// reclassification; AUDIT_* and its originating pass are equivalent.
			SelectedOutcome:     domain.OutcomeTargetedPass,
			SelectedFingerprint: fingerprints, FullFingerprint: fingerprints,
		}
	}

	first := base()
	first.Failures = []audit.Failure{{TestUnitKey: omittedKey, Reproduced: true, RelatedToChange: true}}
	if result := process("confirmed miss audit", first); result.Disposition != audit.DispositionConfirmed || result.Record.Outcome != domain.AuditConfirmedMiss {
		t.Fatalf("first audit not classified as a confirmed miss: %+v", result)
	}
	if got := storedOutcome(); got != domain.OutcomeAuditMiss {
		t.Fatalf("outcome after confirmed audit = %s, want AUDIT_MISS", got)
	}

	second := base()
	if result := process("possible miss audit", second); result.Disposition != audit.DispositionPossible || result.Record.Outcome != domain.AuditPossibleMiss {
		t.Fatalf("second audit not classified as a possible miss: %+v", result)
	}
	if got := storedOutcome(); got != domain.OutcomeAuditPossibleMiss {
		t.Fatalf("outcome after possible-miss audit = %s, want AUDIT_POSSIBLE_MISS", got)
	}

	third := base()
	changed := fingerprints
	changed.DependencyDigest = "deps-v2"
	third.FullFingerprint = changed
	if result := process("incomparable audit", third); result.Disposition != audit.DispositionIncomparable || result.Record.Outcome != domain.AuditIncomparable {
		t.Fatalf("third audit not classified as incomparable: %+v", result)
	}
	if got := storedOutcome(); got != domain.OutcomeAuditIncomparable {
		t.Fatalf("outcome after incomparable audit = %s, want AUDIT_INCOMPARABLE", got)
	}
}
