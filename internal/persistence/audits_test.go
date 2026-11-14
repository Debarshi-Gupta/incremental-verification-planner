package persistence

import (
	"testing"

	"github.com/ivp/ivp/internal/audit"
	"github.com/ivp/ivp/internal/domain"
)

// ADR 0015: an audit may reclassify only a passing execution outcome (or one
// an earlier audit already reclassified). Failing, infra-failed, and stale
// executions keep the outcome their run produced.
func TestAuditOutcomeTransition(t *testing.T) {
	cases := []struct {
		name        string
		current     domain.ExecutionOutcome
		disposition audit.Disposition
		want        domain.ExecutionOutcome
		wantOK      bool
	}{
		{name: "confirmed from targeted pass", current: domain.OutcomeTargetedPass, disposition: audit.DispositionConfirmed, want: domain.OutcomeAuditMiss, wantOK: true},
		{name: "possible from targeted pass", current: domain.OutcomeTargetedPass, disposition: audit.DispositionPossible, want: domain.OutcomeAuditPossibleMiss, wantOK: true},
		{name: "incomparable from targeted pass", current: domain.OutcomeTargetedPass, disposition: audit.DispositionIncomparable, want: domain.OutcomeAuditIncomparable, wantOK: true},
		{name: "confirmed from full pass", current: domain.OutcomeFullVerificationPass, disposition: audit.DispositionConfirmed, want: domain.OutcomeAuditMiss, wantOK: true},
		{name: "re-audit confirmed over possible miss", current: domain.OutcomeAuditPossibleMiss, disposition: audit.DispositionConfirmed, want: domain.OutcomeAuditMiss, wantOK: true},
		{name: "re-audit possible over audit miss", current: domain.OutcomeAuditMiss, disposition: audit.DispositionPossible, want: domain.OutcomeAuditPossibleMiss, wantOK: true},
		{name: "re-audit incomparable over audit miss", current: domain.OutcomeAuditMiss, disposition: audit.DispositionIncomparable, want: domain.OutcomeAuditIncomparable, wantOK: true},
		{name: "targeted fail keeps outcome", current: domain.OutcomeTargetedFail, disposition: audit.DispositionConfirmed, want: domain.OutcomeTargetedFail, wantOK: false},
		{name: "full verification fail keeps outcome", current: domain.OutcomeFullVerificationFail, disposition: audit.DispositionConfirmed, want: domain.OutcomeFullVerificationFail, wantOK: false},
		{name: "infra failure keeps outcome", current: domain.OutcomeInfraFailure, disposition: audit.DispositionPossible, want: domain.OutcomeInfraFailure, wantOK: false},
		{name: "stale keeps outcome", current: domain.OutcomeStale, disposition: audit.DispositionIncomparable, want: domain.OutcomeStale, wantOK: false},
		{name: "clean audit does not reclassify", current: domain.OutcomeTargetedPass, disposition: audit.DispositionNoMiss, want: domain.OutcomeTargetedPass, wantOK: false},
		{name: "not attributable does not reclassify", current: domain.OutcomeTargetedPass, disposition: audit.DispositionNotAttributable, want: domain.OutcomeTargetedPass, wantOK: false},
		{name: "failed audit does not reclassify", current: domain.OutcomeTargetedPass, disposition: audit.DispositionAuditFailed, want: domain.OutcomeTargetedPass, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := AuditOutcomeTransition(tc.current, tc.disposition)
			if got != tc.want || ok != tc.wantOK {
				t.Fatalf("AuditOutcomeTransition(%s, %s) = (%s, %v), want (%s, %v)",
					tc.current, tc.disposition, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// LLD 7.3: flake classification may only be set to FLAKY once the test has a
// minimum number of comparable observations. Below the minimum an audit
// repeat records no classification rather than claiming flakiness from
// insufficient evidence; reproduced repeats keep REPEATED, which records
// retry reproducibility (not flakiness) and is not gated. The vocabulary
// (empty, REPEATED, FLAKY) is unchanged.
func TestFlakeClassificationGatesFLAKYOnComparableObservations(t *testing.T) {
	cases := []struct {
		name            string
		flaky           bool
		observations    int64
		minObservations int
		want            string
	}{
		{name: "flaky repeat at the minimum classifies FLAKY", flaky: true, observations: 5, minObservations: 5, want: "FLAKY"},
		{name: "flaky repeat above the minimum classifies FLAKY", flaky: true, observations: 9, minObservations: 5, want: "FLAKY"},
		{name: "flaky repeat one below the minimum stays unclassified", flaky: true, observations: 4, minObservations: 5, want: ""},
		{name: "single flaky observation never classifies FLAKY", flaky: true, observations: 1, minObservations: 5, want: ""},
		{name: "no observations never classifies FLAKY", flaky: true, observations: 0, minObservations: 5, want: ""},
		{name: "reproduced repeat above the minimum records REPEATED not FLAKY", flaky: false, observations: 9, minObservations: 5, want: "REPEATED"},
		{name: "reproduced repeat below the minimum records REPEATED not FLAKY", flaky: false, observations: 2, minObservations: 5, want: "REPEATED"},
		{name: "flaky repeat with a lower minimum at its boundary classifies FLAKY", flaky: true, observations: 2, minObservations: 2, want: "FLAKY"},
		{name: "non-positive minimum fails closed", flaky: true, observations: 100, minObservations: 0, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := flakeClassification(tc.flaky, tc.observations, tc.minObservations)
			if got != tc.want {
				t.Fatalf("flakeClassification(%v, %d, %d) = %q, want %q", tc.flaky, tc.observations, tc.minObservations, got, tc.want)
			}
		})
	}
}

// A re-audit may present the outcome the execution held before an earlier
// audit reclassified it, so an AUDIT_* outcome and its originating passing
// outcome are equivalent for matching.
func TestAuditOutcomeMatchesTreatsAuditAndPassAsEquivalent(t *testing.T) {
	cases := []struct {
		name      string
		persisted domain.ExecutionOutcome
		declared  domain.ExecutionOutcome
		want      bool
	}{
		{name: "identical pass", persisted: domain.OutcomeTargetedPass, declared: domain.OutcomeTargetedPass, want: true},
		{name: "identical audit outcome", persisted: domain.OutcomeAuditMiss, declared: domain.OutcomeAuditMiss, want: true},
		{name: "reclassified persisted, pass declared", persisted: domain.OutcomeAuditPossibleMiss, declared: domain.OutcomeTargetedPass, want: true},
		{name: "pass persisted, audit declared", persisted: domain.OutcomeTargetedPass, declared: domain.OutcomeAuditIncomparable, want: true},
		{name: "both audit outcomes", persisted: domain.OutcomeAuditMiss, declared: domain.OutcomeAuditPossibleMiss, want: true},
		{name: "persisted audit, declared fail", persisted: domain.OutcomeAuditMiss, declared: domain.OutcomeTargetedFail, want: false},
		{name: "persisted fail, declared audit", persisted: domain.OutcomeTargetedFail, declared: domain.OutcomeAuditMiss, want: false},
		{name: "different passing runs", persisted: domain.OutcomeTargetedPass, declared: domain.OutcomeFullVerificationPass, want: false},
		{name: "infra failure never matches", persisted: domain.OutcomeInfraFailure, declared: domain.OutcomeAuditMiss, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := auditOutcomeMatches(tc.persisted, tc.declared); got != tc.want {
				t.Fatalf("auditOutcomeMatches(%s, %s) = %v, want %v", tc.persisted, tc.declared, got, tc.want)
			}
		})
	}
}
