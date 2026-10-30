package persistence

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type AcceptedChangeOutcome struct {
	ID                     uuid.UUID  `json:"id"`
	RepositoryID           uuid.UUID  `json:"repository_id"`
	ChangeKey              string     `json:"change_key"`
	StartedAt              time.Time  `json:"started_at"`
	AcceptedAt             time.Time  `json:"accepted_at"`
	AcceptedCorrect        bool       `json:"accepted_correct"`
	ComputeMillis          int64      `json:"compute_ms"`
	CostMicrousd           int64      `json:"cost_microusd"`
	HumanCorrectionMinutes int64      `json:"human_correction_minutes"`
	ExecutionID            *uuid.UUID `json:"execution_id,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
}

// RecordAcceptedChange appends the acceptance and correction-cost outcome used
// by repository-level time and cost metrics. It rejects an execution link from
// a different repository or tenant.
func (d *DB) RecordAcceptedChange(ctx context.Context, tenantID, actorID, repositoryID uuid.UUID, outcome AcceptedChangeOutcome) (AcceptedChangeOutcome, error) {
	if outcome.ChangeKey == "" || outcome.StartedAt.IsZero() || outcome.AcceptedAt.IsZero() || outcome.AcceptedAt.Before(outcome.StartedAt) || outcome.ComputeMillis < 0 || outcome.CostMicrousd < 0 || outcome.HumanCorrectionMinutes < 0 {
		return AcceptedChangeOutcome{}, errors.New("accepted-change outcome is invalid")
	}
	outcome.ID = uuid.New()
	outcome.RepositoryID = repositoryID
	outcome.CreatedAt = time.Now().UTC()
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		if outcome.ExecutionID != nil {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(
				SELECT 1 FROM executions e JOIN verification_plans p ON p.id=e.plan_id AND p.tenant_id=e.tenant_id
				WHERE e.tenant_id=$1 AND e.id=$2 AND p.repository_id=$3 AND e.completed_at IS NOT NULL)`, tenantID, *outcome.ExecutionID, repositoryID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return errors.New("linked execution is incomplete or belongs to another repository")
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO accepted_change_outcomes
			(id,tenant_id,repository_id,change_key,started_at,accepted_at,accepted_correct,compute_ms,cost_microusd,human_correction_minutes,execution_id,created_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, outcome.ID, tenantID, repositoryID, outcome.ChangeKey, outcome.StartedAt, outcome.AcceptedAt, outcome.AcceptedCorrect, outcome.ComputeMillis, outcome.CostMicrousd, outcome.HumanCorrectionMinutes, outcome.ExecutionID, outcome.CreatedAt); err != nil {
			return err
		}
		return insertAuditEvent(ctx, tx, tenantID, actorID, "accepted_change.recorded", "repository", repositoryID.String(), map[string]any{
			"outcome_id": outcome.ID, "change_key": outcome.ChangeKey, "accepted_correct": outcome.AcceptedCorrect,
			"compute_ms": outcome.ComputeMillis, "cost_microusd": outcome.CostMicrousd,
			"human_correction_minutes": outcome.HumanCorrectionMinutes, "execution_id": outcome.ExecutionID,
		})
	})
	return outcome, err
}
