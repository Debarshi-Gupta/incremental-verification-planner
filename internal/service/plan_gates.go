package service

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
)

// evidenceGate is the outcome of the evidence comparability gates that run
// before the planner sees the evidence of a snapshot.
//
// The gates exist because LLD 18.4 and LLD 23 require that evidence which
// cannot be shown to be comparable never justifies a selective plan:
//
//   - a coverage report that belongs to a previous dependency version is
//     stale (LLD 23 scenario 6);
//   - adapter-specific evidence produced by an adapter version that changed
//     in an incompatible way is invalidated unless the compatibility check
//     proves it remains comparable (LLD 18.4).
//
// Invalidated evidence is excluded from the planner input and always escalates
// the plan to the authoritative full-suite fallback.
type evidenceGate struct {
	// Units is the test-unit evidence handed to the planner.
	Units []domain.TestUnit
	// Coverage is the coverage evidence handed to the planner.
	Coverage []domain.CoverageObservation
	// Escalations records why the discovery evidence was invalidated. Invalid
	// discovery evidence leaves the plan without the units it needs, so the
	// plan escalates to the authoritative full-suite fallback.
	Escalations []domain.EscalationReason
	// CoverageInvalidations records the coverage reports that were marked
	// stale (LLD 23 scenario 6, LLD 18.4). A stale report is excluded from
	// the evidence and recorded on the plan; the plan itself escalates only
	// when the remaining evidence cannot meet the coverage and confidence
	// thresholds (LLD 6.3 rule 6).
	CoverageInvalidations []domain.EscalationReason
	// Changes records the detected adapter version changes.
	Changes []domain.AdapterVersionChange
}

// invalidated reports whether the gate removed any evidence.
func (g evidenceGate) invalidated() bool {
	return len(g.Escalations) > 0 || len(g.CoverageInvalidations) > 0
}

// applyEvidenceGates removes evidence that cannot be shown to be comparable
// with the current environment, dependency version, and adapter versions.
//
// currentAdapterVersions holds the adapter versions reported by the client
// that requests the plan. The recorded versions come from the coverage
// provenance (the version that produced the report) and from the snapshot
// (the versions that produced the adapter-specific discovery evidence).
func applyEvidenceGates(snapshot domain.RepositorySnapshot, units []domain.TestUnit, coverage []domain.CoverageObservation, currentAdapterVersions map[string]string) evidenceGate {
	gate := evidenceGate{Units: units, Coverage: coverage}
	adapterByUnit := make(map[string]string, len(units))
	for _, unit := range units {
		adapterByUnit[unit.StableKey] = unit.Adapter
	}
	invalidatedAdapters := map[string]domain.AdapterVersionChange{}
	keptCoverage := make([]domain.CoverageObservation, 0, len(coverage))
	for _, observation := range coverage {
		if domain.CoverageDependencyStale(observation, snapshot) {
			gate.CoverageInvalidations = append(gate.CoverageInvalidations, domain.EscalationReason{
				Code:    domain.EscCoverageStale,
				Detail:  fmt.Sprintf("coverage report %s belongs to a previous dependency version and is stale", observation.ReportHash),
				Checked: true,
			})
			continue
		}
		adapter := adapterByUnit[observation.TestUnitKey]
		recorded := observation.AdapterVersion
		if recorded == "" {
			recorded = snapshot.AdapterVersions[adapter]
		}
		change := domain.AdapterVersionChanged(adapter, recorded, currentAdapterVersions[adapter])
		if change.Detected && !change.Comparable {
			if _, seen := invalidatedAdapters[adapter]; !seen {
				invalidatedAdapters[adapter] = change
				gate.Changes = append(gate.Changes, change)
			}
			gate.CoverageInvalidations = append(gate.CoverageInvalidations, domain.EscalationReason{
				Code:    domain.EscCoverageStale,
				Detail:  fmt.Sprintf("coverage produced by adapter %q version %q is not comparable with version %q: %s", change.Adapter, change.Recorded, change.Current, change.Reason),
				Checked: true,
			})
			continue
		}
		keptCoverage = append(keptCoverage, observation)
	}
	gate.Coverage = keptCoverage
	keptUnits := make([]domain.TestUnit, 0, len(units))
	for _, unit := range units {
		change := domain.AdapterVersionChanged(unit.Adapter, snapshot.AdapterVersions[unit.Adapter], currentAdapterVersions[unit.Adapter])
		if change.Detected && !change.Comparable {
			if _, seen := invalidatedAdapters[unit.Adapter]; !seen {
				invalidatedAdapters[unit.Adapter] = change
				gate.Changes = append(gate.Changes, change)
			}
			gate.Escalations = append(gate.Escalations, domain.EscalationReason{
				Code:    domain.EscGraphStale,
				Detail:  fmt.Sprintf("discovery evidence produced by adapter %q version %q is not comparable with version %q: %s", change.Adapter, change.Recorded, change.Current, change.Reason),
				Checked: true,
			})
			// The unit stays in the audit trail but can no longer be selected:
			// its evidence is invalidated.
			unit.Selectable = false
		}
		keptUnits = append(keptUnits, unit)
	}
	gate.Units = keptUnits
	return gate
}

// recordInvalidatedCoverage marks the stale coverage reports on the plan
// without escalating it: the reports are excluded from the evidence, and the
// plan escalates only when the remaining evidence cannot meet the coverage
// and confidence thresholds (LLD 6.3 rule 6).
func recordInvalidatedCoverage(plan *domain.VerificationPlan, invalidations []domain.EscalationReason) {
	if plan == nil || len(invalidations) == 0 {
		return
	}
	for _, invalidation := range invalidations {
		plan.HardRuleChecks = append(plan.HardRuleChecks, domain.HardRuleCheck{Code: invalidation.Code, Triggered: true, Detail: invalidation.Detail})
	}
	sort.Slice(plan.HardRuleChecks, func(i, j int) bool { return plan.HardRuleChecks[i].Code < plan.HardRuleChecks[j].Code })
	plan.Explanation += " Stale coverage evidence was excluded from this plan (" + escalationSummary(invalidations) + ")."
}

// escalateInvalidatedEvidence downgrades a plan to the authoritative
// full-suite fallback when the evidence comparability gates invalidated
// evidence. A failed comparison never continues silently.
func escalateInvalidatedEvidence(plan *domain.VerificationPlan, policy domain.Policy, escalations []domain.EscalationReason) {
	if plan == nil || len(escalations) == 0 {
		return
	}
	plan.Status = domain.PlanFullRequired
	plan.EscalationReasons = append(plan.EscalationReasons, escalations...)
	for _, escalation := range escalations {
		plan.HardRuleChecks = append(plan.HardRuleChecks, domain.HardRuleCheck{Code: escalation.Code, Triggered: true, Detail: escalation.Detail})
	}
	sort.Slice(plan.HardRuleChecks, func(i, j int) bool { return plan.HardRuleChecks[i].Code < plan.HardRuleChecks[j].Code })
	fallback := domain.PlanUnit{UnitID: uuid.New(), StableKey: "FULL_SUITE", UnitType: domain.UnitProjectSuite,
		ReasonCodes: []domain.SelectedReasonCode{domain.ReasonFallbackFullSuite}, Mandatory: true,
		TargetRef: policy.FullSuiteTargetRef, SelectionGranularity: "PROJECT_SUITE",
		Explanation: "safety escalation requires the locally configured authoritative full-suite command"}
	plan.SelectedUnits = append([]domain.PlanUnit{fallback}, plan.MandatoryUnits...)
	plan.Explanation += " Evidence comparability checks invalidated part of the planned evidence (" + escalationSummary(escalations) +
		"), so the selective recommendation was replaced by the authoritative full-suite fallback."
}

// escalationSummary renders a short, deterministic summary of the escalation
// reasons that invalidated the evidence.
func escalationSummary(escalations []domain.EscalationReason) string {
	parts := make([]string, 0, len(escalations))
	for _, escalation := range escalations {
		parts = append(parts, string(escalation.Code))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// effectivePlanMode applies the LLD 18.4 upgrade rule: after a major
// algorithm or policy change the CI selective eligibility is reset to OBSERVE
// until the repository is re-calibrated. The returned reason is empty when the
// requested mode may be used unchanged.
func effectivePlanMode(requested domain.PlanMode, state domain.UpgradeState, policy domain.Policy) (domain.PlanMode, string) {
	if requested != domain.ModeRiskBasedCI {
		return requested, ""
	}
	if !state.Reset() {
		return requested, ""
	}
	if state.Recalibrated(policy.ObservedCalibrationRuns, policy.ObservedAuditRuns, policy) {
		return requested, ""
	}
	return domain.ModeObserve, state.ResetReason(policy.ObservedCalibrationRuns, policy.ObservedAuditRuns, policy)
}
