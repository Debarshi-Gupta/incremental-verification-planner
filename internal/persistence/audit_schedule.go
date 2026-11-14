package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
	"github.com/jackc/pgx/v5"
)

// ScheduledAuditState is the scheduled-audit state of one repository: when it
// last ran a full verification and whether the next one is due.
type ScheduledAuditState struct {
	CadenceHours         int        `json:"cadence_hours"`
	LastFullVerification *time.Time `json:"last_full_verification,omitempty"`
	Due                  bool       `json:"due"`
	NextDueAt            *time.Time `json:"next_due_at,omitempty"`
}

// ScheduledAuditRepository is one repository with the time of its last
// completed full verification (zero when it never ran one). LLD 10.2 requires
// every active repository to run full verification at least once per cadence,
// even when no random audit was sampled.
type ScheduledAuditRepository struct {
	RepositoryID         uuid.UUID
	Slug                 string
	LastFullVerification time.Time
}

// ScheduledAuditRepositories lists the repositories of a tenant together with
// the completion time of their last FULL-mode execution.
func (d *DB) ScheduledAuditRepositories(ctx context.Context, tenantID uuid.UUID) ([]ScheduledAuditRepository, error) {
	rows := make([]ScheduledAuditRepository, 0)
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		result, err := tx.Query(ctx, `SELECT r.id, r.slug, MAX(e.completed_at)
			FROM repositories r
			LEFT JOIN verification_plans p ON p.repository_id = r.id AND p.tenant_id = r.tenant_id AND p.mode = 'FULL'
			LEFT JOIN executions e ON e.plan_id = p.id AND e.tenant_id = r.tenant_id AND e.status = 'COMPLETED'
			WHERE r.tenant_id = $1
			GROUP BY r.id, r.slug
			ORDER BY r.slug`, tenantID)
		if err != nil {
			return err
		}
		defer result.Close()
		for result.Next() {
			var repository ScheduledAuditRepository
			var completed *time.Time
			if err := result.Scan(&repository.RepositoryID, &repository.Slug, &completed); err != nil {
				return err
			}
			if completed != nil {
				repository.LastFullVerification = completed.UTC()
			}
			rows = append(rows, repository)
		}
		return result.Err()
	})
	return rows, err
}

// UnacknowledgedMisses reports how many audit misses are still unacknowledged
// and the age of the oldest one. A miss that stays unacknowledged past the
// configured response SLA is an operational incident (LLD 15.3).
func (d *DB) UnacknowledgedMisses(ctx context.Context, tenantID uuid.UUID, now time.Time) (int, time.Duration, error) {
	count := 0
	var oldest *time.Time
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		var created *time.Time
		if err := tx.QueryRow(ctx, `SELECT COUNT(*), MIN(q.created_at)
			FROM quarantines q
			WHERE q.tenant_id = $1 AND q.status = 'QUARANTINED' AND q.acknowledged_at IS NULL`, tenantID).Scan(&count, &created); err != nil {
			return err
		}
		oldest = created
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	if oldest == nil {
		return count, 0, nil
	}
	age := now.Sub(oldest.UTC())
	if age < 0 {
		age = 0
	}
	return count, age, nil
}

// CalibrationRun is one comparable selective/full execution pair with the
// runtime and compute the run consumed (LLD 10.6).
type CalibrationRun struct {
	Adapter               string
	RiskLevel             string
	AlgorithmVersion      string
	SelectedRuntimeMillis int64
	FullRuntimeMillis     int64
	SelectedCPUMillis     int64
	FullCPUMillis         int64
}

// CalibrationFailure is one full-suite audit's observed failures.
type CalibrationFailure struct {
	Adapter              string
	RiskLevel            string
	AlgorithmVersion     string
	ObservedFailureKeys  []string
	MissedFailureKeys    []string
	UnrelatedFailureKeys []string
}

// CalibrationPlan is one plan's calibration-relevant outcome.
type CalibrationPlan struct {
	Adapter            string
	RiskLevel          string
	AlgorithmVersion   string
	Escalated          bool
	DiscoveryError     bool
	ReportParsingError bool
}

// CalibrationUnit is one selected unit with the reason codes that justify it.
type CalibrationUnit struct {
	Adapter     string
	StableKey   string
	ReasonCodes []string
}

// CalibrationEvidence is the persisted evidence the calibration metric set is
// computed from.
type CalibrationEvidence struct {
	Runs     []CalibrationRun
	Failures []CalibrationFailure
	Plans    []CalibrationPlan
	Units    []CalibrationUnit
}

// CalibrationEvidence gathers the calibration evidence of one repository.
func (d *DB) CalibrationEvidence(ctx context.Context, tenantID, repositoryID uuid.UUID) (CalibrationEvidence, error) {
	evidence := CalibrationEvidence{}
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		// Comparable selective/full pairs: the audit recorded the pair as
		// comparable, so the selective result can be attributed to it.
		runRows, err := tx.Query(ctx, `SELECT p.mode, p.risk_level, p.algorithm_version,
				selected.elapsed_ms, full.elapsed_ms, selected.compute_ms, full.compute_ms
			FROM audit_records a
			JOIN executions selected ON selected.id = a.execution_id AND selected.tenant_id = a.tenant_id
			JOIN executions full ON full.id = a.full_execution_id AND full.tenant_id = a.tenant_id
			JOIN verification_plans p ON p.id = selected.plan_id AND p.tenant_id = selected.tenant_id
			JOIN repositories r ON r.id = p.repository_id AND r.tenant_id = p.tenant_id
			WHERE a.tenant_id = $1 AND r.id = $2 AND a.comparability = 'COMPARABLE'
			ORDER BY a.created_at`, tenantID, repositoryID)
		if err != nil {
			return err
		}
		for runRows.Next() {
			var run CalibrationRun
			if err := runRows.Scan(&run.Adapter, &run.RiskLevel, &run.AlgorithmVersion, &run.SelectedRuntimeMillis,
				&run.FullRuntimeMillis, &run.SelectedCPUMillis, &run.FullCPUMillis); err != nil {
				runRows.Close()
				return err
			}
			evidence.Runs = append(evidence.Runs, run)
		}
		runRows.Close()
		if err := runRows.Err(); err != nil {
			return err
		}

		// Observed and missed failures of every comparable audit.
		failureRows, err := tx.Query(ctx, `SELECT p.risk_level, p.algorithm_version, a.missed_test_keys_json,
				COALESCE((SELECT jsonb_agg(ur.test_unit_key) FROM execution_unit_results ur
					WHERE ur.tenant_id = a.tenant_id AND ur.execution_id = a.full_execution_id AND ur.status IN ('FAILED','ERROR')), '[]'::jsonb)
			FROM audit_records a
			JOIN verification_plans p ON p.id = (SELECT plan_id FROM executions e WHERE e.id = a.execution_id AND e.tenant_id = a.tenant_id) AND p.tenant_id = a.tenant_id
			JOIN repositories r ON r.id = p.repository_id AND r.tenant_id = p.tenant_id
			WHERE a.tenant_id = $1 AND r.id = $2 AND a.comparability = 'COMPARABLE'
			ORDER BY a.created_at`, tenantID, repositoryID)
		if err != nil {
			return err
		}
		for failureRows.Next() {
			var failure CalibrationFailure
			var missed, observed []byte
			if err := failureRows.Scan(&failure.RiskLevel, &failure.AlgorithmVersion, &missed, &observed); err != nil {
				failureRows.Close()
				return err
			}
			failure.MissedFailureKeys = decodeStringList(missed)
			failure.ObservedFailureKeys = decodeStringList(observed)
			failure.UnrelatedFailureKeys = unrelatedFailures(failure.ObservedFailureKeys, failure.MissedFailureKeys)
			evidence.Failures = append(evidence.Failures, failure)
		}
		failureRows.Close()
		if err := failureRows.Err(); err != nil {
			return err
		}

		// Plan outcomes and the selected units with their reason codes.
		planRows, err := tx.Query(ctx, `SELECT p.risk_level, p.algorithm_version, p.status, p.explanation_json
			FROM verification_plans p
			JOIN repositories r ON r.id = p.repository_id AND r.tenant_id = p.tenant_id
			WHERE p.tenant_id = $1 AND r.id = $2
			ORDER BY p.created_at`, tenantID, repositoryID)
		if err != nil {
			return err
		}
		for planRows.Next() {
			var plan CalibrationPlan
			var status string
			var explanation []byte
			if err := planRows.Scan(&plan.RiskLevel, &plan.AlgorithmVersion, &status, &explanation); err != nil {
				planRows.Close()
				return err
			}
			plan.Escalated = status != string(domain.PlanReady)
			plan.DiscoveryError = explanationMentions(explanation, "DISCOVERY")
			plan.ReportParsingError = explanationMentions(explanation, "REPORT_PARSE")
			evidence.Plans = append(evidence.Plans, plan)
		}
		planRows.Close()
		if err := planRows.Err(); err != nil {
			return err
		}

		unitRows, err := tx.Query(ctx, `SELECT u.test_unit_key, u.reason_codes_json
			FROM verification_plan_units u
			JOIN verification_plans p ON p.id = u.plan_id AND p.tenant_id = u.tenant_id
			JOIN repositories r ON r.id = p.repository_id AND r.tenant_id = p.tenant_id
			WHERE u.tenant_id = $1 AND r.id = $2
			ORDER BY u.plan_id, u.selection_order`, tenantID, repositoryID)
		if err != nil {
			return err
		}
		for unitRows.Next() {
			var unit CalibrationUnit
			var codes []byte
			if err := unitRows.Scan(&unit.StableKey, &codes); err != nil {
				unitRows.Close()
				return err
			}
			unit.ReasonCodes = decodeStringList(codes)
			evidence.Units = append(evidence.Units, unit)
		}
		unitRows.Close()
		return unitRows.Err()
	})
	if err != nil {
		return evidence, fmt.Errorf("calibration evidence: %w", err)
	}
	return evidence, nil
}

// decodeStringList decodes a JSON array of strings, ignoring malformed
// entries: evidence that cannot be read is never turned into a claim.
func decodeStringList(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil
	}
	return values
}

// unrelatedFailures returns the observed failures that the audit did not miss
// and that the plan did not select either; they are excluded from the
// relevance denominator (LLD 10.4 NOT_ATTRIBUTABLE).
func unrelatedFailures(observed, missed []string) []string {
	missedSet := make(map[string]bool, len(missed))
	for _, key := range missed {
		missedSet[key] = true
	}
	var unrelated []string
	for _, key := range observed {
		if !missedSet[key] {
			unrelated = append(unrelated, key)
		}
	}
	return unrelated
}

// explanationMentions reports whether a plan explanation names a failure
// class, so a discovery or report-parsing failure is visible to the error-rate
// metrics.
func explanationMentions(explanation []byte, needle string) bool {
	if len(explanation) == 0 {
		return false
	}
	var decoded map[string]any
	if err := json.Unmarshal(explanation, &decoded); err != nil {
		return false
	}
	text, err := json.Marshal(decoded)
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(text)), strings.ToLower(needle))
}
