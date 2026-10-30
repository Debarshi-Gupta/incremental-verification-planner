package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (d *DB) GetExecution(ctx context.Context, tenantID, executionID uuid.UUID) (domain.Execution, error) {
	var execution domain.Execution
	var exitStatus *int
	var completed *time.Time
	var summary []byte
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT e.id,e.plan_id,e.outcome,e.started_at,e.completed_at,e.exit_code,e.environment_digest,e.summary_json
			FROM executions e JOIN verification_plans p ON p.id=e.plan_id AND p.tenant_id=e.tenant_id
			WHERE e.tenant_id=$1 AND e.id=$2`, tenantID, executionID).Scan(&execution.ID, &execution.PlanID, &execution.Outcome, &execution.StartedAt, &completed, &exitStatus, &execution.EnvironmentFingerprint, &summary)
	})
	if err != nil {
		return execution, err
	}
	if completed != nil {
		execution.CompletedAt = *completed
	}
	execution.ExitStatus = exitStatus
	var metadata struct {
		CancellationReason   string   `json:"cancellation_reason"`
		AdapterVersion       string   `json:"adapter_version"`
		ArgvTemplateIDs      []string `json:"argv_template_ids"`
		EscalationPerformed  bool     `json:"escalation_performed"`
		OutputHashes         []string `json:"output_hashes"`
		SanitizedExcerptRefs []string `json:"sanitized_excerpt_refs"`
	}
	if len(summary) > 0 {
		if err := json.Unmarshal(summary, &metadata); err != nil {
			return execution, err
		}
	}
	execution.CancellationReason = metadata.CancellationReason
	execution.AdapterVersion = metadata.AdapterVersion
	execution.ArgvTemplateIDs = metadata.ArgvTemplateIDs
	execution.EscalationPerformed = metadata.EscalationPerformed
	execution.OutputHashes = metadata.OutputHashes
	execution.SanitizedExcerptRefs = metadata.SanitizedExcerptRefs
	return execution, nil
}

type RepositoryMetrics struct {
	RepositoryID             uuid.UUID      `json:"repository_id"`
	WindowStart              time.Time      `json:"window_start"`
	AcceptedChanges          int64          `json:"accepted_changes"`
	AcceptedCorrectChanges   int64          `json:"accepted_correct_changes"`
	MeanTimeToAcceptedCorrectMillis float64 `json:"mean_time_to_accepted_correct_ms"`
	MeanCostPerAcceptedCorrectMicrousd float64 `json:"mean_cost_per_accepted_correct_microusd"`
	MeanHumanCorrectionMinutes float64     `json:"mean_human_correction_minutes"`
	CalibrationBreakdown      []CalibrationBreakdown `json:"calibration_breakdown"`
	ComparableAudits         int64          `json:"comparable_audits"`
	IncomparableAudits       int64          `json:"incomparable_audits"`
	ConfirmedMisses          int64          `json:"confirmed_misses"`
	PossibleMisses           int64          `json:"possible_misses"`
	ObservedMissRate         float64        `json:"observed_miss_rate"`
	MissRateCI95Lower        float64        `json:"miss_rate_ci95_lower"`
	MissRateCI95Upper        float64        `json:"miss_rate_ci95_upper"`
	TargetedPasses           int64          `json:"targeted_passes"`
	FullVerificationPasses   int64          `json:"full_verification_passes"`
	ExecutionCount           int64          `json:"execution_count"`
	MeanEstimatedActualRatio float64        `json:"mean_estimated_actual_ratio"`
	Daily                    []DailyMetrics `json:"daily"`
	NextCursor               string         `json:"next_cursor,omitempty"`
}
type DailyMetrics struct {
	Day                time.Time `json:"day"`
	ComparableAudits   int64     `json:"comparable_audits"`
	Misses             int64     `json:"misses"`
	IncomparableAudits int64     `json:"incomparable_audits"`
}

type CalibrationBreakdown struct {
	Mode                      domain.PlanMode  `json:"mode"`
	RiskLevel                 domain.RiskLevel `json:"risk_level"`
	ExecutionCount            int64            `json:"execution_count"`
	TargetedPasses            int64            `json:"targeted_passes"`
	FullVerificationPasses    int64            `json:"full_verification_passes"`
	ComparableAudits          int64            `json:"comparable_audits"`
	Misses                    int64            `json:"misses"`
	MeanEstimatedActualRatio  float64          `json:"mean_estimated_actual_ratio"`
	MeanSelectionConfidence   float64          `json:"mean_selection_confidence"`
	MeanWeightedCoverage      float64          `json:"mean_weighted_coverage"`
}

// CalibrationCounts returns observed, completed executions with usable timing
// data and comparable audits for the repository. These server-owned counts
// gate RISK_BASED_CI; a repository policy cannot self-report them.
func (d *DB) CalibrationCounts(ctx context.Context, tenantID, repositoryID uuid.UUID) (int64, int64, error) {
	var executions, audits int64
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*)
			FROM executions e JOIN verification_plans p ON p.id=e.plan_id AND p.tenant_id=e.tenant_id
			WHERE e.tenant_id=$1 AND p.repository_id=$2 AND e.completed_at IS NOT NULL AND e.elapsed_ms>0
			AND e.outcome IN ('TARGETED_PASS','TARGETED_FAIL','FULL_VERIFICATION_PASS','FULL_VERIFICATION_FAIL')
			AND p.estimated_duration_ms>0`, tenantID, repositoryID).Scan(&executions); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*)
			FROM audit_records ar JOIN executions e ON e.id=ar.execution_id AND e.tenant_id=ar.tenant_id
			JOIN verification_plans p ON p.id=e.plan_id AND p.tenant_id=e.tenant_id
			WHERE ar.tenant_id=$1 AND p.repository_id=$2 AND ar.comparability='COMPARABLE'`, tenantID, repositoryID).Scan(&audits)
	})
	return executions, audits, err
}

func (d *DB) GetExecutionUnitResults(ctx context.Context, tenantID, executionID uuid.UUID) ([]UnitResult, error) {
	results := make([]UnitResult, 0)
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT test_unit_key,attempt,status,duration_ms,failure_fingerprint,report_hash,summary_json FROM execution_unit_results WHERE tenant_id=$1 AND execution_id=$2 ORDER BY test_unit_key,attempt`, tenantID, executionID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item UnitResult
			if err := rows.Scan(&item.TestUnitKey, &item.Attempt, &item.Status, &item.DurationMillis, &item.FailureFingerprint, &item.ReportHash, &item.Summary); err != nil {
				return err
			}
			results = append(results, item)
		}
		return rows.Err()
	})
	return results, err
}

func (d *DB) GetRepositoryMetrics(ctx context.Context, tenantID, repositoryID uuid.UUID, limit int, cursor string) (RepositoryMetrics, error) {
	if limit < 1 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	now := time.Now().UTC()
	start := now.AddDate(0, 0, -90)
	report := RepositoryMetrics{RepositoryID: repositoryID, WindowStart: start, Daily: make([]DailyMetrics, 0), CalibrationBreakdown: make([]CalibrationBreakdown, 0)}
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		var auditTotals, incomparable, confirmed, possible int64
		err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE comparability='COMPARABLE'),count(*) FILTER(WHERE comparability='INCOMPARABLE'),count(*) FILTER(WHERE outcome=$3),count(*) FILTER(WHERE outcome=$4)
			FROM audit_records ar JOIN executions e ON e.id=ar.execution_id AND e.tenant_id=ar.tenant_id JOIN verification_plans p ON p.id=e.plan_id AND p.tenant_id=e.tenant_id
			WHERE ar.tenant_id=$1 AND p.repository_id=$2 AND ar.created_at >= $5`, tenantID, repositoryID, domain.AuditConfirmedMiss, domain.AuditPossibleMiss, start).Scan(&auditTotals, &incomparable, &confirmed, &possible)
		if err != nil {
			return err
		}
		report.ComparableAudits = auditTotals
		report.IncomparableAudits = incomparable
		report.ConfirmedMisses = confirmed
		report.PossibleMisses = possible
		report.ObservedMissRate, report.MissRateCI95Lower, report.MissRateCI95Upper = wilson(confirmed+possible, auditTotals)
		var targeted, full, executions int64
		var ratio float64
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE e.outcome=$3),count(*) FILTER(WHERE e.outcome=$4),count(*),COALESCE(avg(p.estimated_duration_ms::double precision/NULLIF(e.elapsed_ms,0)),0)
			FROM executions e JOIN verification_plans p ON p.id=e.plan_id AND p.tenant_id=e.tenant_id WHERE e.tenant_id=$1 AND p.repository_id=$2 AND e.completed_at >= $5`, tenantID, repositoryID, domain.OutcomeTargetedPass, domain.OutcomeFullVerificationPass, start).Scan(&targeted, &full, &executions, &ratio); err != nil {
			return err
		}
		report.TargetedPasses = targeted
		report.FullVerificationPasses = full
		report.ExecutionCount = executions
		report.MeanEstimatedActualRatio = ratio
		if err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE accepted_correct),
			COALESCE(avg(EXTRACT(EPOCH FROM (accepted_at-started_at))*1000) FILTER(WHERE accepted_correct),0),
			COALESCE(avg(cost_microusd::double precision) FILTER(WHERE accepted_correct),0),
			COALESCE(avg(human_correction_minutes::double precision) FILTER(WHERE accepted_correct),0)
			FROM accepted_change_outcomes WHERE tenant_id=$1 AND repository_id=$2 AND accepted_at >= $3`, tenantID, repositoryID, start).Scan(
			&report.AcceptedChanges, &report.AcceptedCorrectChanges, &report.MeanTimeToAcceptedCorrectMillis,
			&report.MeanCostPerAcceptedCorrectMicrousd, &report.MeanHumanCorrectionMinutes); err != nil {
			return err
		}
		breakdowns, err := tx.Query(ctx, `WITH audit_by_plan AS (
			SELECT e.plan_id,count(*) FILTER(WHERE ar.comparability='COMPARABLE') AS comparable,
			count(*) FILTER(WHERE ar.outcome IN ($3,$4)) AS misses
			FROM audit_records ar JOIN executions e ON e.id=ar.execution_id AND e.tenant_id=ar.tenant_id
			WHERE ar.tenant_id=$1 AND ar.created_at >= $5 GROUP BY e.plan_id
		), execution_by_plan AS (
			SELECT p.id,p.mode,p.risk_level,count(*) AS executions,
			count(*) FILTER(WHERE e.outcome=$6) AS targeted_passes,
			count(*) FILTER(WHERE e.outcome=$7) AS full_passes,
			avg(p.estimated_duration_ms::double precision/NULLIF(e.elapsed_ms,0)) AS estimate_ratio,
			avg(p.selection_confidence) AS selection_confidence,avg(p.weighted_coverage) AS weighted_coverage
			FROM executions e JOIN verification_plans p ON p.id=e.plan_id AND p.tenant_id=e.tenant_id
			WHERE e.tenant_id=$1 AND p.repository_id=$2 AND e.completed_at >= $5
			GROUP BY p.id,p.mode,p.risk_level
		)
		SELECT e.mode,e.risk_level,sum(e.executions),sum(e.targeted_passes),sum(e.full_passes),
		COALESCE(sum(a.comparable),0),COALESCE(sum(a.misses),0),COALESCE(avg(e.estimate_ratio),0),
		COALESCE(avg(e.selection_confidence),0),COALESCE(avg(e.weighted_coverage),0)
		FROM execution_by_plan e LEFT JOIN audit_by_plan a ON a.plan_id=e.id
		GROUP BY e.mode,e.risk_level ORDER BY e.mode,e.risk_level`, tenantID, repositoryID, domain.AuditConfirmedMiss, domain.AuditPossibleMiss, start,
			domain.OutcomeTargetedPass, domain.OutcomeFullVerificationPass)
		if err != nil {
			return err
		}
		defer breakdowns.Close()
		for breakdowns.Next() {
			var item CalibrationBreakdown
			if err := breakdowns.Scan(&item.Mode, &item.RiskLevel, &item.ExecutionCount, &item.TargetedPasses, &item.FullVerificationPasses,
				&item.ComparableAudits, &item.Misses, &item.MeanEstimatedActualRatio, &item.MeanSelectionConfidence, &item.MeanWeightedCoverage); err != nil {
				return err
			}
			report.CalibrationBreakdown = append(report.CalibrationBreakdown, item)
		}
		if err := breakdowns.Err(); err != nil {
			return err
		}
		var before any
		if cursor != "" {
			day, err := time.Parse("2006-01-02", cursor)
			if err != nil {
				return errors.New("metrics cursor must be a YYYY-MM-DD date")
			}
			before = day
		}
		rows, err := tx.Query(ctx, `SELECT date_trunc('day',ar.created_at)::date AS day,count(*) FILTER(WHERE ar.comparability='COMPARABLE') AS comparable,
			count(*) FILTER(WHERE ar.comparability='INCOMPARABLE') AS incomparable,
			count(*) FILTER(WHERE ar.outcome IN ($3,$4)) AS misses
			FROM audit_records ar JOIN executions e ON e.id=ar.execution_id AND e.tenant_id=ar.tenant_id JOIN verification_plans p ON p.id=e.plan_id AND p.tenant_id=e.tenant_id
			WHERE ar.tenant_id=$1 AND p.repository_id=$2 AND ar.created_at >= $5 AND ($6::date IS NULL OR date_trunc('day',ar.created_at)::date < $6::date)
			GROUP BY day ORDER BY day DESC LIMIT $7`, tenantID, repositoryID, domain.AuditConfirmedMiss, domain.AuditPossibleMiss, start, before, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var day DailyMetrics
			if err := rows.Scan(&day.Day, &day.ComparableAudits, &day.IncomparableAudits, &day.Misses); err != nil {
				return err
			}
			report.Daily = append(report.Daily, day)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(report.Daily) > limit {
			report.Daily = report.Daily[:limit]
			report.NextCursor = report.Daily[len(report.Daily)-1].Day.Format("2006-01-02")
		}
		return nil
	})
	return report, err
}

func wilson(successes, total int64) (rate, lower, upper float64) {
	if total <= 0 {
		return 0, 0, 0
	}
	n := float64(total)
	p := float64(successes) / n
	z := 1.959963984540054
	denom := 1 + z*z/n
	center := (p + z*z/(2*n)) / denom
	margin := z * math.Sqrt((p*(1-p)+z*z/(4*n))/n) / denom
	return p, math.Max(0, center-margin), math.Min(1, center+margin)
}
