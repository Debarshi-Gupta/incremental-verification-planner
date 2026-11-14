package audit

import (
	"testing"
	"time"
)

// LLD 10.6: the calibration metric set. Every metric states its denominator,
// reports whether it is defined, and never divides by zero.
func TestCalibrationMetricsComputeFromEvidence(t *testing.T) {
	t.Run("runtime reduction", func(t *testing.T) {
		metric := RuntimeReduction([]ComparableRunEvidence{
			{SelectedRuntimeMillis: 2_000, FullRuntimeMillis: 10_000},
			{SelectedRuntimeMillis: 3_000, FullRuntimeMillis: 10_000},
		})
		if !metric.Defined || metric.Name != "runtime_reduction" {
			t.Fatalf("runtime reduction is not defined: %+v", metric)
		}
		if metric.Value != 0.75 || metric.Denominator != 20_000 || metric.Numerator != 15_000 || metric.SampleSize != 2 {
			t.Fatalf("runtime reduction = %+v, want 0.75 over 2 samples", metric)
		}
		// A run without a measured full-suite runtime is excluded, not counted
		// as a zero-cost run.
		partial := RuntimeReduction([]ComparableRunEvidence{
			{SelectedRuntimeMillis: 1_000, FullRuntimeMillis: 4_000},
			{SelectedRuntimeMillis: 1_000},
		})
		if partial.SampleSize != 1 || partial.Value != 0.75 {
			t.Fatalf("runtime reduction over one measurable run = %+v", partial)
		}
		// No evidence is never presented as a measured zero.
		empty := RuntimeReduction(nil)
		if empty.Defined || empty.NoDataReason == "" || empty.SampleSize != 0 {
			t.Fatalf("runtime reduction without evidence = %+v", empty)
		}
	})

	t.Run("compute reduction", func(t *testing.T) {
		metric := ComputeReduction([]ComparableRunEvidence{
			{SelectedCPUMillis: 1_000, FullCPUMillis: 4_000},
			{SelectedCPUMillis: 1_000, FullCPUMillis: 4_000},
		})
		if !metric.Defined || metric.Value != 0.75 || metric.SampleSize != 2 {
			t.Fatalf("compute reduction = %+v, want 0.75 over 2 samples", metric)
		}
		// A run without CPU metrics is excluded from both sides.
		partial := ComputeReduction([]ComparableRunEvidence{
			{SelectedCPUMillis: 1_000, FullCPUMillis: 4_000},
			{SelectedRuntimeMillis: 10, FullRuntimeMillis: 40},
		})
		if partial.SampleSize != 1 || partial.Value != 0.75 {
			t.Fatalf("compute reduction over one measurable run = %+v", partial)
		}
		if empty := ComputeReduction(nil); empty.Defined || empty.NoDataReason == "" {
			t.Fatalf("compute reduction without evidence = %+v", empty)
		}
	})

	t.Run("regression recall", func(t *testing.T) {
		metric := RegressionRecall([]FailureEvidence{{
			ObservedFailureKeys:  []string{"test:a", "test:b", "test:c", "test:d"},
			MissedFailureKeys:    []string{"test:b", "test:c"},
			UnrelatedFailureKeys: []string{"test:d"},
		}})
		if !metric.Defined {
			t.Fatalf("regression recall is not defined: %+v", metric)
		}
		// The unrelated failure is excluded from the relevance denominator:
		// two missed relevant failures over three relevant failures.
		if metric.Value != 2.0/3.0 || metric.Denominator != 3 || metric.Numerator != 2 {
			t.Fatalf("regression recall = %+v, want 2/3", metric)
		}
		if empty := RegressionRecall(nil); empty.Defined || empty.NoDataReason == "" {
			t.Fatalf("regression recall without evidence = %+v", empty)
		}
	})

	t.Run("selection precision", func(t *testing.T) {
		metric := SelectionPrecision([]PlanUnitEvidence{
			{StableKey: "test:a", ReasonCodes: []string{"COVERS_CHANGED_LINE"}},
			{StableKey: "test:b", ReasonCodes: []string{"MANDATORY_POLICY_CHECK"}},
			{StableKey: "test:c", ReasonCodes: []string{"AFFECTED_BUILD_TARGET"}},
		})
		if !metric.Defined {
			t.Fatalf("selection precision is not defined: %+v", metric)
		}
		// Two of the three selected units carry relevance evidence.
		if metric.Value != 2.0/3.0 || metric.Numerator != 2 || metric.Denominator != 3 {
			t.Fatalf("selection precision = %+v, want 2/3", metric)
		}
		if empty := SelectionPrecision(nil); empty.Defined || empty.NoDataReason == "" {
			t.Fatalf("selection precision without evidence = %+v", empty)
		}
	})

	t.Run("fallback escalation rate", func(t *testing.T) {
		metric := FallbackEscalationRate([]PlanEvidence{
			{Escalated: false},
			{Escalated: true},
			{Escalated: true},
			{Escalated: false},
		})
		if !metric.Defined || metric.Value != 0.5 || metric.SampleSize != 4 {
			t.Fatalf("fallback escalation rate = %+v, want 0.5 over 4 plans", metric)
		}
		if empty := FallbackEscalationRate(nil); empty.Defined || empty.NoDataReason == "" {
			t.Fatalf("fallback escalation rate without evidence = %+v", empty)
		}
	})

	t.Run("discovery and report parsing error rates", func(t *testing.T) {
		discovery := TestDiscoveryErrorRate([]PlanEvidence{
			{DiscoveryError: true}, {DiscoveryError: false}, {DiscoveryError: false}, {DiscoveryError: false},
		})
		if !discovery.Defined || discovery.Value != 0.25 || discovery.SampleSize != 4 {
			t.Fatalf("discovery error rate = %+v, want 0.25 over 4 plans", discovery)
		}
		parsing := ReportParsingErrorRate([]PlanEvidence{
			{ReportParsingError: true}, {ReportParsingError: true}, {ReportParsingError: false},
		})
		if !parsing.Defined || parsing.Value != 2.0/3.0 {
			t.Fatalf("report parsing error rate = %+v, want 2/3", parsing)
		}
		if empty := TestDiscoveryErrorRate(nil); empty.Defined || empty.NoDataReason == "" {
			t.Fatalf("discovery error rate without evidence = %+v", empty)
		}
		if empty := ReportParsingErrorRate(nil); empty.Defined || empty.NoDataReason == "" {
			t.Fatalf("report parsing error rate without evidence = %+v", empty)
		}
	})

	t.Run("breakdowns", func(t *testing.T) {
		breakdown := BreakdownBy([]PlanEvidence{
			{Adapter: "gradle", Escalated: true},
			{Adapter: "gradle", Escalated: false},
			{Adapter: "pytest", Escalated: true},
		}, func(plan PlanEvidence) string { return plan.Adapter }, FallbackEscalationRate)
		if len(breakdown) != 2 {
			t.Fatalf("breakdown = %+v, want two adapters", breakdown)
		}
		if gradle, ok := breakdown["gradle"]; !ok || gradle.Value != 0.5 {
			t.Fatalf("gradle breakdown = %+v, want 0.5", breakdown)
		}
		if pytest, ok := breakdown["pytest"]; !ok || pytest.Value != 1 {
			t.Fatalf("pytest breakdown = %+v, want 1", breakdown)
		}
		if keys := SortedBreakdownKeys(breakdown); len(keys) != 2 || keys[0] != "gradle" || keys[1] != "pytest" {
			t.Fatalf("breakdown keys are not sorted: %v", keys)
		}
	})
}

// A selected unit carries relevance evidence only when one of its reason codes
// ties it to the change under verification; a mandatory gate and the fallback
// unit do not.
func TestHasRelevanceEvidenceUsesTheRelevanceVocabulary(t *testing.T) {
	for _, code := range []string{"DIRECTLY_CHANGED_TEST", "COVERS_CHANGED_LINE", "AFFECTED_BUILD_TARGET", "INTEGRATION_BOUNDARY"} {
		if !HasRelevanceEvidence([]string{code}) {
			t.Errorf("reason code %q was not treated as relevance evidence", code)
		}
	}
	for _, code := range []string{"MANDATORY_POLICY_CHECK", "FALLBACK_FULL_SUITE", "UNKNOWN_CODE"} {
		if HasRelevanceEvidence([]string{code}) {
			t.Errorf("reason code %q was treated as relevance evidence", code)
		}
	}
	if HasRelevanceEvidence(nil) {
		t.Error("a unit without reason codes was treated as relevance evidence")
	}
}

// LLD 10.2 and 15.3: the scheduled-audit cadence and the acknowledgement SLA.
func TestSchedulePolicyDecidesDueRunsAndOverdueMisses(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	policy := SchedulePolicy{Enabled: true, Cadence: 24 * time.Hour, AcknowledgementSLA: 24 * time.Hour}
	if err := policy.Validate(); err != nil {
		t.Fatalf("a valid schedule policy was rejected: %v", err)
	}
	// A repository that ran a full verification inside the cadence is not due.
	if policy.ScheduledAuditDue(now.Add(-time.Hour), now) {
		t.Error("a repository inside the cadence was reported as due")
	}
	// A repository whose last full verification is older than the cadence is due.
	if !policy.ScheduledAuditDue(now.Add(-25*time.Hour), now) {
		t.Error("a repository past the cadence was not reported as due")
	}
	// A repository that never ran a full verification is due immediately.
	if !policy.ScheduledAuditDue(time.Time{}, now) {
		t.Error("a repository without any full verification was not reported as due")
	}
	// A miss that is still unacknowledged past the SLA is overdue.
	if !policy.AcknowledgementOverdue(now.Add(-25*time.Hour), time.Time{}, now) {
		t.Error("an unacknowledged miss past the SLA was not reported as overdue")
	}
	// An acknowledged miss is never overdue, and a miss inside the SLA is not.
	if policy.AcknowledgementOverdue(now.Add(-25*time.Hour), now.Add(-time.Hour), now) {
		t.Error("an acknowledged miss was reported as overdue")
	}
	if policy.AcknowledgementOverdue(now.Add(-time.Hour), time.Time{}, now) {
		t.Error("a miss inside the SLA was reported as overdue")
	}
	for name, invalid := range map[string]SchedulePolicy{
		"zero cadence":     {Cadence: 0, AcknowledgementSLA: time.Hour},
		"zero SLA":         {Cadence: time.Hour, AcknowledgementSLA: 0},
		"negative cadence": {Cadence: -time.Hour, AcknowledgementSLA: time.Hour},
	} {
		if err := invalid.Validate(); err == nil {
			t.Errorf("%s was accepted by the schedule policy", name)
		}
	}
}
