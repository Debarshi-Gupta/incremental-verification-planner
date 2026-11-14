package analysis

import (
	"testing"

	"github.com/ivp/ivp/internal/domain"
)

// LLD 6.3 rule 5 and 15.3: a repository in quarantine after a confirmed or
// unresolved regression miss must not get a selective plan - the scope falls
// back to full verification.
func TestQuarantineEscalatesThePlanToFullVerification(t *testing.T) {
	req, evidence, policy := readyFixture(t)
	policy.Quarantined = true
	policy.QuarantineScope = "pytest/all"

	plan, err := CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanFullRequired {
		t.Fatalf("a quarantined repository must not get a selective plan, got %s (%+v)", plan.Status, plan.EscalationReasons)
	}
	if !hasReason(plan, domain.EscQuarantineActive) {
		t.Fatalf("the quarantine escalation is missing: %+v", plan.EscalationReasons)
	}
	check, ok := hardRuleCheck(plan, domain.EscQuarantineActive)
	if !ok || !check.Triggered {
		t.Fatalf("the quarantine hard rule must be recorded as triggered: %+v", plan.HardRuleChecks)
	}
}

// LLD 6.3: a test that historically failed on the changed entity (or on a
// related dependency edge) is relevant evidence: it carries the
// HISTORICAL_FAILURE_RELEVANCE reason and it contributes to the risk score,
// clamped to the documented 0-10 points.
func TestHistoricalFailureRelevanceIsAppliedAndClamped(t *testing.T) {
	req, evidence, policy := readyFixture(t)
	evidence.History = []domain.HistoryObservation{{
		TestUnitKey: "test:unit", RelatedStableKey: "src/main.go", Comparable: true,
		Outcome: "FAIL", FailureCount: 9, SampleCount: 5, ObservedAt: req.Now,
		Environment: req.EnvironmentFingerprint,
	}}

	plan, err := CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.SelectedUnits) == 0 {
		t.Fatalf("the plan selected no unit: %+v", plan.EscalationReasons)
	}
	relevant := false
	for _, unit := range plan.SelectedUnits {
		if unit.StableKey == "test:unit" && containsReason(unit.ReasonCodes, domain.ReasonHistoricalFailureRelevance) {
			relevant = true
		}
	}
	if !relevant {
		t.Fatalf("the historically failing unit must carry the historical-failure reason: %+v", plan.SelectedUnits)
	}
	points := -1
	for _, contribution := range plan.RiskContributions {
		if contribution.Feature == "regression_history" {
			points = contribution.Points
		}
	}
	if points < 0 {
		t.Fatalf("the regression-history risk contribution is missing: %+v", plan.RiskContributions)
	}
	// Nine comparable failures contribute min(9*2, 10) = 10 points.
	if points != 10 {
		t.Fatalf("the regression-history contribution = %d points, want the clamped 10", points)
	}
}

// LLD 4.5: once a unit has at least five duration samples, the P95 estimate
// is preferred over the mean for the plan's runtime estimate.
func TestP95IsPreferredOverTheMeanOnceFiveSamplesExist(t *testing.T) {
	req, evidence, policy := readyFixture(t)
	for i := range evidence.Units {
		if evidence.Units[i].StableKey != "test:unit" {
			continue
		}
		evidence.Units[i].EstimatedMillis = 500
		evidence.Units[i].DurationSampleCount = 5
		evidence.Units[i].DurationP50Millis = 1_000
		evidence.Units[i].DurationP95Millis = 2_000
	}
	plan, err := CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if plan.EstimatedDurationP95Millis < 2_000 {
		t.Fatalf("the plan must use the P95 estimate of the selected unit, got %d ms", plan.EstimatedDurationP95Millis)
	}
}
