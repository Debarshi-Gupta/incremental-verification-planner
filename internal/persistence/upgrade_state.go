package persistence

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
	"github.com/jackc/pgx/v5"
)

// UpgradeState returns the recorded algorithm and policy versions of a
// repository together with the reset state that LLD 18.4 requires. A
// repository without a recorded state has never been reset.
func (d *DB) UpgradeState(ctx context.Context, tenantID, repositoryID uuid.UUID) (domain.UpgradeState, error) {
	state := domain.UpgradeState{}
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT algorithm_version,policy_version,reset_at,calibration_runs_at_reset,audit_runs_at_reset,reason
			FROM repository_upgrade_state WHERE tenant_id=$1 AND repository_id=$2`, tenantID, repositoryID).
			Scan(&state.AlgorithmVersion, &state.PolicyVersion, &state.ResetAt, &state.CalibrationRunsAtReset, &state.AuditRunsAtReset, &state.Reason)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.UpgradeState{}, nil
	}
	return state, err
}

// ObserveUpgrade records the algorithm and policy versions observed for a
// repository. When a major algorithm or policy change is detected the CI
// selective eligibility is reset: the reset time and the calibration counters
// observed at that moment are stored so that evidence from before the change
// can never re-enable selective gating (LLD 18.4).
func (d *DB) ObserveUpgrade(ctx context.Context, tenantID, repositoryID uuid.UUID, algorithmVersion string, policyVersion int64, calibrationRuns, auditRuns int, now time.Time) (domain.UpgradeState, error) {
	state := domain.UpgradeState{}
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		var previousAlgorithm string
		var previousPolicy int64
		var resetAt *time.Time
		var calibrationAtReset, auditAtReset int
		var reason string
		err := tx.QueryRow(ctx, `SELECT algorithm_version,policy_version,reset_at,calibration_runs_at_reset,audit_runs_at_reset,reason
			FROM repository_upgrade_state WHERE tenant_id=$1 AND repository_id=$2 FOR UPDATE`, tenantID, repositoryID).
			Scan(&previousAlgorithm, &previousPolicy, &resetAt, &calibrationAtReset, &auditAtReset, &reason)
		if errors.Is(err, pgx.ErrNoRows) {
			// Baseline: no version was recorded before, so no change was
			// detected and the repository is not reset.
			if _, err := tx.Exec(ctx, `INSERT INTO repository_upgrade_state(tenant_id,repository_id,algorithm_version,policy_version,reset_at,calibration_runs_at_reset,audit_runs_at_reset,reason,updated_at)
				VALUES($1,$2,$3,$4,NULL,$5,$6,'',$7)`, tenantID, repositoryID, algorithmVersion, policyVersion, calibrationRuns, auditRuns, now); err != nil {
				return err
			}
			state = domain.UpgradeState{AlgorithmVersion: algorithmVersion, PolicyVersion: policyVersion}
			return nil
		}
		if err != nil {
			return err
		}
		algorithmChanged, algorithmReason := domain.MajorAlgorithmChange(previousAlgorithm, algorithmVersion)
		policyChanged, policyReason := domain.MajorPolicyChange(previousPolicy, policyVersion)
		if algorithmChanged || policyChanged {
			reasons := make([]string, 0, 2)
			if algorithmChanged {
				reasons = append(reasons, algorithmReason)
			}
			if policyChanged {
				reasons = append(reasons, policyReason)
			}
			resetReason := strings.Join(reasons, "; ")
			if _, err := tx.Exec(ctx, `UPDATE repository_upgrade_state SET algorithm_version=$3,policy_version=$4,reset_at=$5,calibration_runs_at_reset=$6,audit_runs_at_reset=$7,reason=$8,updated_at=$9
				WHERE tenant_id=$1 AND repository_id=$2`, tenantID, repositoryID, algorithmVersion, policyVersion, now, calibrationRuns, auditRuns, resetReason, now); err != nil {
				return err
			}
			state = domain.UpgradeState{AlgorithmVersion: algorithmVersion, PolicyVersion: policyVersion, ResetAt: now,
				CalibrationRunsAtReset: calibrationRuns, AuditRunsAtReset: auditRuns, Reason: resetReason}
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE repository_upgrade_state SET algorithm_version=$3,policy_version=$4,updated_at=$5 WHERE tenant_id=$1 AND repository_id=$2`,
			tenantID, repositoryID, algorithmVersion, policyVersion, now); err != nil {
			return err
		}
		state = domain.UpgradeState{AlgorithmVersion: algorithmVersion, PolicyVersion: policyVersion}
		if resetAt != nil {
			state.ResetAt = *resetAt
			state.CalibrationRunsAtReset = calibrationAtReset
			state.AuditRunsAtReset = auditAtReset
			state.Reason = reason
		}
		return nil
	})
	return state, err
}
