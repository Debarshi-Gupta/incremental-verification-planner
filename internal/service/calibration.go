package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/audit"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/persistence"
)

// CalibrationReport is the LLD 10.6 calibration metric set of a repository
// together with its scheduled-audit state (LLD 10.2).
type CalibrationReport struct {
	RepositoryID uuid.UUID                               `json:"repository_id"`
	Metrics      map[string]audit.MetricValue            `json:"metrics"`
	Breakdowns   map[string]map[string]audit.MetricValue `json:"breakdowns,omitempty"`
	Scheduled    ScheduledAuditState                     `json:"scheduled_audit"`
	GeneratedAt  time.Time                               `json:"generated_at"`
}

// ScheduledAuditState is the scheduled-audit state of one repository: when it
// last ran a full verification and whether the next one is due.
type ScheduledAuditState = persistence.ScheduledAuditState

// auditSchedulePolicy maps the effective policy onto the audit schedule
// policy the orchestrator uses.
func auditSchedulePolicy(policy domain.Policy) audit.SchedulePolicy {
	return audit.SchedulePolicy{
		Enabled:            policy.AuditSchedule.Cadence > 0,
		Cadence:            policy.AuditSchedule.Cadence,
		AcknowledgementSLA: policy.AuditSchedule.AcknowledgementSLA,
	}
}

// scheduledAuditState computes the scheduled-audit state of a repository.
func (b *Backend) scheduledAuditState(ctx context.Context, tenantID, repositoryID uuid.UUID, policy domain.Policy, now time.Time) (ScheduledAuditState, error) {
	state := ScheduledAuditState{CadenceHours: int(policy.AuditSchedule.Cadence.Hours())}
	repositories, err := b.DB.ScheduledAuditRepositories(ctx, tenantID)
	if err != nil {
		return state, err
	}
	schedule := auditSchedulePolicy(policy)
	for _, repository := range repositories {
		if repository.RepositoryID != repositoryID {
			continue
		}
		if !repository.LastFullVerification.IsZero() {
			last := repository.LastFullVerification
			state.LastFullVerification = &last
			next := last.Add(schedule.Cadence)
			state.NextDueAt = &next
		}
		state.Due = schedule.ScheduledAuditDue(repository.LastFullVerification, now)
		return state, nil
	}
	return state, nil
}

// scheduledAuditDue reports whether the repository must run a full
// verification now (LLD 10.2).
func (b *Backend) scheduledAuditDue(ctx context.Context, tenantID, repositoryID uuid.UUID, policy domain.Policy, now time.Time) (bool, error) {
	state, err := b.scheduledAuditState(ctx, tenantID, repositoryID, policy, now)
	if err != nil {
		return false, err
	}
	return state.Due, nil
}

// calibrationMetrics computes the LLD 10.6 metric set of a repository from the
// persisted evidence, with the adapter, risk-level and algorithm-version
// breakdowns the dashboards need.
func (b *Backend) calibrationMetrics(ctx context.Context, tenantID, repositoryID uuid.UUID) (map[string]audit.MetricValue, map[string]map[string]audit.MetricValue, error) {
	evidence, err := b.DB.CalibrationEvidence(ctx, tenantID, repositoryID)
	if err != nil {
		return nil, nil, err
	}
	runs := make([]audit.ComparableRunEvidence, 0, len(evidence.Runs))
	for _, run := range evidence.Runs {
		runs = append(runs, audit.ComparableRunEvidence{
			Adapter: run.Adapter, RiskLevel: run.RiskLevel, AlgorithmVersion: run.AlgorithmVersion,
			SelectedRuntimeMillis: run.SelectedRuntimeMillis, FullRuntimeMillis: run.FullRuntimeMillis,
			SelectedCPUMillis: run.SelectedCPUMillis, FullCPUMillis: run.FullCPUMillis,
		})
	}
	failures := make([]audit.FailureEvidence, 0, len(evidence.Failures))
	for _, failure := range evidence.Failures {
		failures = append(failures, audit.FailureEvidence{
			Adapter: failure.Adapter, RiskLevel: failure.RiskLevel, AlgorithmVersion: failure.AlgorithmVersion,
			ObservedFailureKeys: failure.ObservedFailureKeys, MissedFailureKeys: failure.MissedFailureKeys,
			UnrelatedFailureKeys: failure.UnrelatedFailureKeys,
		})
	}
	plans := make([]audit.PlanEvidence, 0, len(evidence.Plans))
	for _, plan := range evidence.Plans {
		plans = append(plans, audit.PlanEvidence{
			Adapter: plan.Adapter, RiskLevel: plan.RiskLevel, AlgorithmVersion: plan.AlgorithmVersion,
			Escalated: plan.Escalated, DiscoveryError: plan.DiscoveryError, ReportParsingError: plan.ReportParsingError,
		})
	}
	units := make([]audit.PlanUnitEvidence, 0, len(evidence.Units))
	for _, unit := range evidence.Units {
		units = append(units, audit.PlanUnitEvidence{
			Adapter: unit.Adapter, StableKey: unit.StableKey, ReasonCodes: unit.ReasonCodes,
		})
	}
	metrics := map[string]audit.MetricValue{
		"runtime_reduction":         audit.RuntimeReduction(runs),
		"compute_reduction":         audit.ComputeReduction(runs),
		"regression_recall":         audit.RegressionRecall(failures),
		"selection_precision":       audit.SelectionPrecision(units),
		"fallback_escalation_rate":  audit.FallbackEscalationRate(plans),
		"discovery_error_rate":      audit.TestDiscoveryErrorRate(plans),
		"report_parsing_error_rate": audit.ReportParsingErrorRate(plans),
	}
	breakdowns := map[string]map[string]audit.MetricValue{
		"fallback_escalation_rate": audit.BreakdownBy(plans, func(plan audit.PlanEvidence) string { return plan.Adapter }, audit.FallbackEscalationRate),
		"runtime_reduction":        audit.BreakdownBy(runs, func(run audit.ComparableRunEvidence) string { return run.Adapter }, audit.RuntimeReduction),
	}
	return metrics, breakdowns, nil
}

// calibrationReport assembles the calibration report of a repository.
func (b *Backend) calibrationReport(ctx context.Context, tenantID, repositoryID uuid.UUID, policy domain.Policy, now time.Time) (CalibrationReport, error) {
	metrics, breakdowns, err := b.calibrationMetrics(ctx, tenantID, repositoryID)
	if err != nil {
		return CalibrationReport{}, err
	}
	state, err := b.scheduledAuditState(ctx, tenantID, repositoryID, policy, now)
	if err != nil {
		return CalibrationReport{}, err
	}
	return CalibrationReport{RepositoryID: repositoryID, Metrics: metrics, Breakdowns: breakdowns, Scheduled: state, GeneratedAt: now}, nil
}

// recordAuditMissAge publishes the age of the oldest unacknowledged audit
// miss and the number of misses past the response SLA (LLD 15.3).
func (b *Backend) recordAuditMissAge(ctx context.Context, tenantID uuid.UUID, policy domain.Policy, now time.Time) {
	if b == nil || b.Metrics == nil {
		return
	}
	count, age, err := b.DB.UnacknowledgedMisses(ctx, tenantID, now)
	if err != nil {
		return
	}
	b.Metrics.AuditMissUnacknowledged.Set(age.Hours())
	breaches := 0
	if count > 0 && policy.AuditSchedule.AcknowledgementSLA > 0 && age >= policy.AuditSchedule.AcknowledgementSLA {
		breaches = count
	}
	b.Metrics.AuditMissUnacknowledgedBreaches.Set(float64(breaches))
}

// calibrationEvidenceFor returns the persisted calibration evidence, exposed
// for the dashboards that read the repository metrics.
func calibrationEvidenceFor(evidence persistence.CalibrationEvidence) persistence.CalibrationEvidence {
	return evidence
}
