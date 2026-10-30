package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
	"github.com/jackc/pgx/v5"
)

// executablePlanUnits returns the deterministic de-duplicated union consumed
// by both executor implementations. Mandatory checks remain explicit in the
// immutable plan DTO while sharing the persistence membership table.
func executablePlanUnits(plan domain.VerificationPlan) ([]domain.PlanUnit, error) {
	units := make([]domain.PlanUnit, 0, len(plan.SelectedUnits)+len(plan.MandatoryUnits))
	index := make(map[string]int, cap(units))
	for _, unit := range plan.SelectedUnits {
		if unit.StableKey == "" {
			return nil, errors.New("plan unit stable key is required")
		}
		if _, exists := index[unit.StableKey]; exists {
			return nil, errors.New("selected plan units contain duplicate stable keys")
		}
		index[unit.StableKey] = len(units)
		units = append(units, unit)
	}
	for _, unit := range plan.MandatoryUnits {
		if unit.StableKey == "" {
			return nil, errors.New("mandatory plan unit stable key is required")
		}
		if i, exists := index[unit.StableKey]; exists {
			units[i].Mandatory = true
			seenReasons := make(map[domain.SelectedReasonCode]struct{}, len(units[i].ReasonCodes))
			for _, code := range units[i].ReasonCodes {
				seenReasons[code] = struct{}{}
			}
			for _, code := range unit.ReasonCodes {
				if _, ok := seenReasons[code]; !ok {
					units[i].ReasonCodes = append(units[i].ReasonCodes, code)
					seenReasons[code] = struct{}{}
				}
			}
			continue
		}
		index[unit.StableKey] = len(units)
		unit.Mandatory = true
		units = append(units, unit)
	}
	return units, nil
}

func (d *DB) SavePlan(ctx context.Context, tenantID uuid.UUID, plan domain.VerificationPlan) error {
	if plan.ID == uuid.Nil || plan.RepositoryID == uuid.Nil || plan.SnapshotID == uuid.Nil {
		return errors.New("plan, repository, and snapshot ids are required")
	}
	if plan.SelectionConfidence < 0 || plan.SelectionConfidence > 1 || plan.WeightedCoverage < 0 || plan.WeightedCoverage > 1 || plan.RiskScore < 0 || plan.RiskScore > 100 {
		return errors.New("plan scores are out of range")
	}
	executableUnits, err := executablePlanUnits(plan)
	if err != nil {
		return err
	}
	executableKeys := make(map[string]struct{}, len(executableUnits))
	for _, unit := range executableUnits {
		executableKeys[unit.StableKey] = struct{}{}
	}
	omittedKeys := make(map[string]struct{}, len(plan.OmittedUnits))
	for _, unit := range plan.OmittedUnits {
		if unit.StableKey == "" {
			return errors.New("omitted plan unit stable key is required")
		}
		if _, duplicate := omittedKeys[unit.StableKey]; duplicate {
			return errors.New("omitted plan units contain duplicate stable keys")
		}
		if _, overlap := executableKeys[unit.StableKey]; overlap {
			return errors.New("plan unit cannot be both executable and omitted")
		}
		omittedKeys[unit.StableKey] = struct{}{}
	}
	explanation, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	return d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO verification_plans(id,tenant_id,repository_id,snapshot_id,base_sha,head_sha,worktree_hash,mode,status,risk_score,risk_level,selection_confidence,weighted_coverage,algorithm_version,policy_version,estimated_duration_ms,explanation_json,created_at,expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`, plan.ID, tenantID, plan.RepositoryID, plan.SnapshotID, plan.BaseSHA, plan.HeadSHA, plan.WorktreeHash, plan.Mode, plan.Status, plan.RiskScore, plan.RiskLevel, plan.SelectionConfidence, plan.WeightedCoverage, plan.AlgorithmVersion, plan.PolicyVersion, plan.EstimatedDurationMillis, explanation, plan.CreatedAt, plan.ExpiresAt)
		if err != nil {
			return err
		}
		for i, u := range executableUnits {
			codes, e := json.Marshal(u.ReasonCodes)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO verification_plan_units(tenant_id,plan_id,test_unit_key,selection_order,mandatory,reason_codes_json,estimated_duration_ms,coverage_contribution,risk_contribution) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, tenantID, plan.ID, u.StableKey, i, u.Mandatory, codes, u.EstimatedDurationMillis, u.CoverageContribution, u.RiskContribution); e != nil {
				return e
			}
		}
		for _, u := range plan.OmittedUnits {
			codes, e := json.Marshal(u.ReasonCodes)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO omitted_plan_units(tenant_id,plan_id,test_unit_key,omission_reason_codes_json) VALUES($1,$2,$3,$4)`, tenantID, plan.ID, u.StableKey, codes); e != nil {
				return e
			}
		}
		return nil
	})
}

func (d *DB) GetPlan(ctx context.Context, tenantID, planID uuid.UUID) (domain.VerificationPlan, error) {
	var raw []byte
	var currentStatus domain.PlanStatus
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT explanation_json,status FROM verification_plans WHERE tenant_id=$1 AND id=$2`, tenantID, planID).Scan(&raw, &currentStatus)
	})
	var plan domain.VerificationPlan
	if err != nil {
		return plan, err
	}
	err = json.Unmarshal(raw, &plan)
	plan.Status = currentStatus
	return plan, err
}

func (d *DB) StartExecution(ctx context.Context, tenantID, executorID uuid.UUID, planID uuid.UUID, idempotencyKey, worktreeHash, environmentDigest string, fullSuite bool, now time.Time) (domain.Execution, error) {
	result := domain.Execution{ID: uuid.New(), PlanID: planID, StartedAt: now, EnvironmentFingerprint: environmentDigest}
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		var status domain.PlanStatus
		var mode domain.PlanMode
		var expires time.Time
		var expectedWorktree string
		if err := tx.QueryRow(ctx, `SELECT status,mode,expires_at,worktree_hash FROM verification_plans WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, planID).Scan(&status, &mode, &expires, &expectedWorktree); err != nil {
			return err
		}
		if expectedWorktree != worktreeHash || !expires.After(now) {
			return errors.New("plan is stale or has expired")
		}
		if status != domain.PlanReady && !(status == domain.PlanFullRequired && fullSuite) && !(status == domain.PlanPassed && fullSuite) {
			return errors.New("plan is not executable under the requested verification mode")
		}
		if mode == domain.ModeFull && !fullSuite {
			return errors.New("FULL mode requires a full-suite execution")
		}
		metadata, _ := json.Marshal(map[string]bool{"full_suite": fullSuite})
		_, err := tx.Exec(ctx, `INSERT INTO executions(id,tenant_id,plan_id,idempotency_key,status,executor_id,environment_digest,started_at,summary_json) VALUES($1,$2,$3,$4,'RUNNING',$5,$6,$7,$8)`, result.ID, tenantID, planID, idempotencyKey, executorID, environmentDigest, now, metadata)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE verification_plans SET status=$3 WHERE tenant_id=$1 AND id=$2`, tenantID, planID, domain.PlanExecuting)
		return err
	})
	return result, err
}

type UnitResult struct {
	TestUnitKey        string          `json:"test_unit_key"`
	Attempt            int             `json:"attempt"`
	Status             string          `json:"status"`
	DurationMillis     int64           `json:"duration_ms"`
	FailureFingerprint string          `json:"failure_fingerprint,omitempty"`
	ReportHash         string          `json:"report_hash,omitempty"`
	Summary            json.RawMessage `json:"summary,omitempty"`
}
type ResultSubmission struct {
	Outcome       domain.ExecutionOutcome `json:"outcome"`
	ExitCode      *int                    `json:"exit_code,omitempty"`
	CompletedAt   time.Time               `json:"completed_at"`
	ElapsedMillis int64                   `json:"elapsed_ms"`
	ComputeMillis int64                   `json:"compute_ms"`
	Summary       json.RawMessage         `json:"summary,omitempty"`
	UnitResults   []UnitResult            `json:"unit_results,omitempty"`
}

func (d *DB) SubmitExecutionResult(ctx context.Context, tenantID, executionID uuid.UUID, result ResultSubmission) error {
	if !validOutcome(result.Outcome) {
		return errors.New("invalid execution outcome")
	}
	if result.CompletedAt.IsZero() {
		result.CompletedAt = time.Now().UTC()
	}
	if len(result.UnitResults) > 100_000 {
		return errors.New("too many unit results")
	}
	return d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		var planID uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT plan_id FROM executions WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, executionID).Scan(&planID); err != nil {
			return err
		}
		var plan domain.VerificationPlan
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT explanation_json FROM verification_plans WHERE tenant_id=$1 AND id=$2`, tenantID, planID).Scan(&raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &plan); err != nil {
			return err
		}
		var executionSummary []byte
		var executionState string
		if err := tx.QueryRow(ctx, `SELECT summary_json,status FROM executions WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, executionID).Scan(&executionSummary, &executionState); err != nil {
			return err
		}
		if executionState != "RUNNING" {
			return errors.New("execution result is already finalized")
		}
		var executionMeta struct {
			FullSuite bool `json:"full_suite"`
		}
		if err := json.Unmarshal(executionSummary, &executionMeta); err != nil {
			return err
		}
		executableUnits, err := executablePlanUnits(plan)
		if err != nil {
			return err
		}
		allowed := make(map[string]struct{}, len(executableUnits))
		for _, unit := range executableUnits {
			allowed[unit.StableKey] = struct{}{}
		}
		if executionMeta.FullSuite {
			for _, unit := range plan.OmittedUnits {
				allowed[unit.StableKey] = struct{}{}
			}
		}
		for _, unit := range result.UnitResults {
			if _, ok := allowed[unit.TestUnitKey]; !ok {
				return fmt.Errorf("result includes unit outside immutable plan: %s", unit.TestUnitKey)
			}
			if unit.Attempt < 1 || unit.DurationMillis < 0 {
				return errors.New("invalid unit result attempt or duration")
			}
			summary := unit.Summary
			if len(summary) == 0 {
				summary = json.RawMessage(`{}`)
			}
			if !json.Valid(summary) {
				return errors.New("unit result summary must be valid JSON")
			}
			if _, err := tx.Exec(ctx, `INSERT INTO execution_unit_results(tenant_id,execution_id,test_unit_key,attempt,status,duration_ms,failure_fingerprint,report_hash,summary_json) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, tenantID, executionID, unit.TestUnitKey, unit.Attempt, unit.Status, unit.DurationMillis, unit.FailureFingerprint, unit.ReportHash, summary); err != nil {
				return err
			}
		}
		summary := result.Summary
		if len(summary) == 0 {
			summary = json.RawMessage(`{}`)
		}
		if !json.Valid(summary) {
			return errors.New("execution summary must be valid JSON")
		}
		var summaryMap map[string]any
		if err := json.Unmarshal(summary, &summaryMap); err != nil {
			return err
		}
		if summaryMap == nil {
			summaryMap = map[string]any{}
		}
		summaryMap["full_suite"] = executionMeta.FullSuite
		summaryJSON, err := json.Marshal(summaryMap)
		if err != nil {
			return err
		}
		commandStatus := "COMPLETED"
		planStatus := domain.PlanPassed
		if result.Outcome == domain.OutcomeTargetedFail || result.Outcome == domain.OutcomeFullVerificationFail {
			planStatus = domain.PlanFailed
		}
		if result.Outcome == domain.OutcomeStale {
			commandStatus = "STALE"
			planStatus = domain.PlanStale
		}
		if result.Outcome == domain.OutcomeInfraFailure {
			commandStatus = "ERROR"
			planStatus = domain.PlanError
		}
		if result.Outcome == domain.OutcomeTargetedPass && (plan.Status == domain.PlanFullRequired || executionMeta.FullSuite) {
			return errors.New("full verification execution cannot be labeled targeted pass")
		}
		if result.Outcome == domain.OutcomeFullVerificationPass || result.Outcome == domain.OutcomeFullVerificationFail {
			if !executionMeta.FullSuite {
				return errors.New("full verification outcome requires an execution registered as full-suite")
			}
		}
		if (result.Outcome == domain.OutcomeTargetedPass || result.Outcome == domain.OutcomeTargetedFail) && plan.Mode == domain.ModeFull {
			return errors.New("full mode cannot be labeled as a targeted execution")
		}
		if result.Outcome == domain.OutcomeTargetedPass && plan.Status != domain.PlanReady {
			return errors.New("only a ready selective plan may be labeled targeted pass")
		}
		if result.Outcome == domain.OutcomeTargetedFail && (plan.Status == domain.PlanFullRequired || executionMeta.FullSuite) {
			return errors.New("full verification execution cannot be labeled targeted failure")
		}
		if result.Outcome == domain.OutcomeFullVerificationFail && !executionMeta.FullSuite {
			return errors.New("full verification failure requires a full-suite execution")
		}
		if result.Outcome == domain.OutcomeFullVerificationPass && !executionMeta.FullSuite {
			return errors.New("full verification pass requires a full-suite execution")
		}
		if result.Outcome == domain.OutcomeStale || result.Outcome == domain.OutcomeInfraFailure {
			// Infrastructure/staleness states can terminate either execution type.
		}
		if _, err := tx.Exec(ctx, `UPDATE executions SET status=$3,completed_at=$4,elapsed_ms=$5,compute_ms=$6,exit_code=$7,outcome=$8,summary_json=$9 WHERE tenant_id=$1 AND id=$2`, tenantID, executionID, commandStatus, result.CompletedAt, result.ElapsedMillis, result.ComputeMillis, result.ExitCode, result.Outcome, summaryJSON); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE verification_plans SET status=$3 WHERE tenant_id=$1 AND id=$2`, tenantID, planID, planStatus)
		return err
	})
}

func validOutcome(o domain.ExecutionOutcome) bool {
	switch o {
	case domain.OutcomeTargetedPass, domain.OutcomeFullVerificationPass, domain.OutcomeTargetedFail, domain.OutcomeFullVerificationFail, domain.OutcomeInfraFailure, domain.OutcomeStale:
		return true
	default:
		return false
	}
}
