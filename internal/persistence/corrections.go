package persistence

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ResultCorrection is the superseding record for an incorrectly parsed
// outcome (LLD 12.4). The stored result row is never changed or deleted: the
// correction carries the status that supersedes it, the audit reason and who
// made the correction.
type ResultCorrection struct {
	ID                uuid.UUID `json:"id"`
	TenantID          uuid.UUID `json:"tenant_id"`
	ExecutionID       uuid.UUID `json:"execution_id"`
	TestUnitKey       string    `json:"test_unit_key"`
	Attempt           int       `json:"attempt"`
	SupersededStatus  string    `json:"superseded_status"`
	SupersedingStatus string    `json:"superseding_status"`
	Reason            string    `json:"reason"`
	CorrectedBy       uuid.UUID `json:"corrected_by,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

// RecordResultCorrection appends a superseding record for one parsed
// outcome. The stored result row is read to capture the status it supersedes
// and is never modified or deleted: results stay append-only (LLD 12.4).
func (d *DB) RecordResultCorrection(ctx context.Context, tenantID, executionID uuid.UUID, unitKey string, attempt int, supersedingStatus, reason string, correctedBy uuid.UUID) (ResultCorrection, error) {
	correction := ResultCorrection{
		TenantID: tenantID, ExecutionID: executionID, TestUnitKey: unitKey, Attempt: attempt,
		SupersedingStatus: supersedingStatus, Reason: reason, CorrectedBy: correctedBy,
	}
	if strings.TrimSpace(unitKey) == "" {
		return correction, errors.New("a correction requires a test unit key")
	}
	if attempt <= 0 {
		return correction, errors.New("a correction requires a positive attempt number")
	}
	if strings.TrimSpace(supersedingStatus) == "" {
		return correction, errors.New("a correction requires the superseding status")
	}
	if strings.TrimSpace(reason) == "" {
		return correction, errors.New("a correction requires an audit reason")
	}
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		var superseded string
		if err := tx.QueryRow(ctx, `SELECT status FROM execution_unit_results WHERE tenant_id=$1 AND execution_id=$2 AND test_unit_key=$3 AND attempt=$4`,
			tenantID, executionID, unitKey, attempt).Scan(&superseded); err != nil {
			return fmt.Errorf("correction refers to an unknown result: %w", err)
		}
		if superseded == supersedingStatus {
			return errors.New("a correction must change the recorded status")
		}
		correction.ID = uuid.New()
		correction.SupersededStatus = superseded
		correction.CreatedAt = time.Now().UTC()
		_, err := tx.Exec(ctx, `INSERT INTO result_corrections(id,tenant_id,execution_id,test_unit_key,attempt,superseded_status,superseding_status,reason,corrected_by,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			correction.ID, tenantID, executionID, unitKey, attempt, superseded, supersedingStatus, reason, nullIfEmptyUUID(correctedBy), correction.CreatedAt)
		return err
	})
	return correction, err
}

// ApplyResultCorrections returns the effective results: for every corrected
// (test unit, attempt) the latest correction supersedes the recorded status.
// The stored rows stay untouched, and the effective result keeps the
// superseded status and the audit reason so the correction is visible instead
// of silent.
func ApplyResultCorrections(results []UnitResult, corrections []ResultCorrection) []UnitResult {
	if len(corrections) == 0 {
		return results
	}
	latest := make(map[string]ResultCorrection, len(corrections))
	for _, correction := range corrections {
		key := correctionKey(correction.TestUnitKey, correction.Attempt)
		if existing, ok := latest[key]; ok && !correctionIsNewer(correction, existing) {
			continue
		}
		latest[key] = correction
	}
	out := make([]UnitResult, len(results))
	copy(out, results)
	for i := range out {
		correction, ok := latest[correctionKey(out[i].TestUnitKey, out[i].Attempt)]
		if !ok {
			continue
		}
		out[i].Status = correction.SupersedingStatus
		out[i].SupersededStatus = correction.SupersededStatus
		out[i].CorrectionReason = correction.Reason
	}
	return out
}

func correctionKey(unitKey string, attempt int) string {
	return unitKey + "\x00" + fmt.Sprint(attempt)
}

// correctionIsNewer reports whether a correction supersedes another one for
// the same result: the later record wins, and an equal timestamp is broken by
// the identifier so the order is deterministic.
func correctionIsNewer(candidate, existing ResultCorrection) bool {
	if candidate.CreatedAt.Equal(existing.CreatedAt) {
		return candidate.ID.String() > existing.ID.String()
	}
	return candidate.CreatedAt.After(existing.CreatedAt)
}

func nullIfEmptyUUID(value uuid.UUID) any {
	if value == uuid.Nil {
		return nil
	}
	return value
}
