package service

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
)

func gateUnit(adapter string) domain.TestUnit {
	return domain.TestUnit{ID: uuid.New(), StableKey: "unit::" + adapter, Adapter: adapter, UnitType: domain.UnitTestCase,
		Selectable: true, SelectionGranularity: "TEST_CASE", TargetRef: "unit::" + adapter}
}

func gateCoverage(unit domain.TestUnit, reportHash, dependencyDigest, adapterVersion string) domain.CoverageObservation {
	return domain.CoverageObservation{TestUnitKey: unit.StableKey, CoveredStableKey: "file:src/app.go", SourceType: "pytest",
		ReportHash: reportHash, RunID: uuid.New(), EnvironmentFingerprint: "sha256:env", BuildConfigDigest: "sha256:build",
		TestConfigDigest: "sha256:test", DependencyDigest: dependencyDigest, AdapterVersion: adapterVersion,
		Precision: domain.CoverageLine, TestSpecific: true, EvidenceConfidence: 0.9}
}

func TestApplyEvidenceGatesMarksPreviousDependencyVersionStale(t *testing.T) {
	unit := gateUnit("pytest")
	snapshot := domain.RepositorySnapshot{DependencyDigest: "sha256:dep-current"}
	current := gateCoverage(unit, "sha256:report-current", "sha256:dep-current", "pytest-8.2")
	previous := gateCoverage(unit, "sha256:report-previous", "sha256:dep-previous", "pytest-8.2")
	gate := applyEvidenceGates(snapshot, []domain.TestUnit{unit}, []domain.CoverageObservation{current, previous}, map[string]string{"pytest": "pytest-8.2"})
	if len(gate.Coverage) != 1 || gate.Coverage[0].ReportHash != "sha256:report-current" {
		t.Fatalf("coverage from a previous dependency version must be removed: %+v", gate.Coverage)
	}
	if !gate.invalidated() {
		t.Fatal("a stale coverage report must invalidate the evidence")
	}
	found := false
	for _, invalidation := range gate.CoverageInvalidations {
		if invalidation.Code == domain.EscCoverageStale && strings.Contains(invalidation.Detail, "sha256:report-previous") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the stale coverage report must be reported as stale: %+v", gate.CoverageInvalidations)
	}
	// A stale coverage report is marked and excluded; it does not by itself
	// invalidate the discovery evidence (LLD 6.3 rule 6 escalates only when the
	// remaining evidence cannot meet the coverage thresholds).
	if len(gate.Escalations) != 0 {
		t.Fatalf("a stale coverage report must not invalidate the discovery evidence: %+v", gate.Escalations)
	}
}

func TestApplyEvidenceGatesInvalidatesIncompatibleAdapterEvidence(t *testing.T) {
	unit := gateUnit("pytest")
	snapshot := domain.RepositorySnapshot{AdapterVersions: map[string]string{"pytest": "pytest-8.2"}}
	coverage := gateCoverage(unit, "sha256:report", "", "pytest-8.2")
	gate := applyEvidenceGates(snapshot, []domain.TestUnit{unit}, []domain.CoverageObservation{coverage}, map[string]string{"pytest": "pytest-9.0"})
	if len(gate.Coverage) != 0 {
		t.Fatalf("coverage from an incompatible adapter version must be removed: %+v", gate.Coverage)
	}
	if len(gate.Units) != 1 || gate.Units[0].Selectable {
		t.Fatalf("units of an incompatible adapter version must no longer be selectable: %+v", gate.Units)
	}
	codes := map[domain.EscalationReasonCode]bool{}
	for _, invalidation := range gate.CoverageInvalidations {
		codes[invalidation.Code] = true
	}
	for _, escalation := range gate.Escalations {
		codes[escalation.Code] = true
	}
	if !codes[domain.EscCoverageStale] || !codes[domain.EscGraphStale] {
		t.Fatalf("the adapter version change must be reported as stale coverage and stale evidence: %+v", gate)
	}
	if len(gate.Changes) != 1 || gate.Changes[0].Adapter != "pytest" || gate.Changes[0].Comparable {
		t.Fatalf("the detected adapter version change must be recorded: %+v", gate.Changes)
	}
}

func TestApplyEvidenceGatesKeepsComparableAdapterEvidence(t *testing.T) {
	unit := gateUnit("pytest")
	snapshot := domain.RepositorySnapshot{AdapterVersions: map[string]string{"pytest": "pytest-8.2"}}
	coverage := gateCoverage(unit, "sha256:report", "", "pytest-8.2")
	gate := applyEvidenceGates(snapshot, []domain.TestUnit{unit}, []domain.CoverageObservation{coverage}, map[string]string{"pytest": "pytest-8.2.1"})
	if gate.invalidated() {
		t.Fatalf("a compatible adapter patch release must not invalidate evidence: %+v", gate.Escalations)
	}
	if len(gate.Coverage) != 1 || len(gate.Units) != 1 || !gate.Units[0].Selectable {
		t.Fatalf("compatible adapter evidence must be preserved: %+v", gate)
	}
}

func TestEffectivePlanModeResetsCISelectiveEligibilityToObserve(t *testing.T) {
	policy := domain.Policy{MinimumCalibrationRuns: 2, MinimumAuditRuns: 2, ObservedCalibrationRuns: 1, ObservedAuditRuns: 1}
	state := domain.UpgradeState{AlgorithmVersion: "ivp-planner-v2.0.0", PolicyVersion: 4, ResetAt: time.Now().UTC(), Reason: "the selection algorithm major version changed"}
	mode, reason := effectivePlanMode(domain.ModeRiskBasedCI, state, policy)
	if mode != domain.ModeObserve {
		t.Fatalf("a reset repository must fall back to OBSERVE, got %s", mode)
	}
	if reason == "" {
		t.Fatal("the reset must be explained")
	}
	policy.ObservedCalibrationRuns, policy.ObservedAuditRuns = 5, 5
	mode, reason = effectivePlanMode(domain.ModeRiskBasedCI, state, policy)
	if mode != domain.ModeRiskBasedCI || reason != "" {
		t.Fatalf("a re-calibrated repository must keep its requested mode: mode=%s reason=%q", mode, reason)
	}
	mode, reason = effectivePlanMode(domain.ModeFastFeedback, state, policy)
	if mode != domain.ModeFastFeedback || reason != "" {
		t.Fatalf("modes other than CI selective gating must not be rewritten: mode=%s reason=%q", mode, reason)
	}
}

func TestEscalateInvalidatedEvidenceReplacesSelectivePlanWithFullSuite(t *testing.T) {
	policy := domain.Policy{FullSuiteTargetRef: "full-suite"}
	plan := domain.VerificationPlan{ID: uuid.New(), Status: domain.PlanReady, SelectedUnits: []domain.PlanUnit{{StableKey: "unit::a", Mandatory: false}},
		MandatoryUnits: []domain.PlanUnit{{StableKey: "unit::mandatory", Mandatory: true}}, OmittedUnits: []domain.OmittedUnit{{StableKey: "unit::b"}}}
	escalations := []domain.EscalationReason{{Code: domain.EscCoverageStale, Detail: "coverage report is stale", Checked: true}}
	escalateInvalidatedEvidence(&plan, policy, escalations)
	if plan.Status != domain.PlanFullRequired {
		t.Fatalf("invalidated evidence must escalate the plan, got %s", plan.Status)
	}
	if len(plan.SelectedUnits) != 2 || plan.SelectedUnits[0].StableKey != "FULL_SUITE" || plan.SelectedUnits[0].TargetRef != "full-suite" {
		t.Fatalf("the escalated plan must run the configured full-suite fallback: %+v", plan.SelectedUnits)
	}
	if plan.SelectedUnits[1].StableKey != "unit::mandatory" {
		t.Fatalf("mandatory units must survive the escalation: %+v", plan.SelectedUnits)
	}
	if len(plan.EscalationReasons) != 1 || plan.EscalationReasons[0].Code != domain.EscCoverageStale {
		t.Fatalf("the escalation reason must be recorded on the plan: %+v", plan.EscalationReasons)
	}
	if len(plan.HardRuleChecks) != 1 || !plan.HardRuleChecks[0].Triggered {
		t.Fatalf("the hard rule check must record the escalation: %+v", plan.HardRuleChecks)
	}
	if !strings.Contains(plan.Explanation, "COVERAGE_STALE") {
		t.Fatalf("the explanation must record the invalidation: %s", plan.Explanation)
	}
	if len(plan.OmittedUnits) != 1 {
		t.Fatalf("the omission audit trail must survive the escalation: %+v", plan.OmittedUnits)
	}
}
