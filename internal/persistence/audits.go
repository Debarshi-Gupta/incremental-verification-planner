package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/audit"
	"github.com/ivp/ivp/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (d *DB) RecordAuditAndQuarantine(ctx context.Context, in audit.Input, result audit.Result) error {
	if in.TenantID == uuid.Nil || in.RepositoryID == uuid.Nil || in.PlanID == uuid.Nil || in.SelectedExecutionID == uuid.Nil || in.FullExecutionID == uuid.Nil {
		return errors.New("audit tenant, repository, plan, and execution identifiers are required")
	}
	if in.QuarantineFor <= 0 || in.QuarantineFor > 365*24*time.Hour {
		return errors.New("quarantine duration is outside the allowed range")
	}
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	}
	missed, err := json.Marshal(result.MissedTestKeys)
	if err != nil {
		return err
	}
	scope := quarantineScope(in.Adapter, in.RuleScope)
	comparability := "COMPARABLE"
	if result.Disposition == audit.DispositionIncomparable {
		comparability = "INCOMPARABLE"
	}
	if result.Disposition == audit.DispositionAuditFailed {
		comparability = "FAILED"
	}
	details, err := json.Marshal(map[string]any{"adapter": in.Adapter, "scope_key": scope, "failure_output_hash": in.FailureOutputHash, "disposition": result.Disposition, "selected_fingerprint": in.SelectedFingerprint, "full_fingerprint": in.FullFingerprint})
	if err != nil {
		return err
	}
	return d.TenantTx(ctx, in.TenantID.String(), func(tx pgx.Tx) error {
		var repositoryID, planID uuid.UUID
		var selectedOutcome, fullOutcome domain.ExecutionOutcome
		var fullSummary []byte
		if err := tx.QueryRow(ctx, `SELECT p.repository_id,e.plan_id,e.outcome,fe.outcome,fe.summary_json
			FROM executions e JOIN verification_plans p ON p.id=e.plan_id AND p.tenant_id=e.tenant_id
			JOIN executions fe ON fe.tenant_id=e.tenant_id AND fe.id=$3 AND fe.plan_id=e.plan_id
			WHERE e.tenant_id=$1 AND e.id=$2`, in.TenantID, in.SelectedExecutionID, in.FullExecutionID).
			Scan(&repositoryID, &planID, &selectedOutcome, &fullOutcome, &fullSummary); err != nil {
			return errors.New("audit executions must belong to this tenant and the same plan")
		}
		if repositoryID != in.RepositoryID || planID != in.PlanID || selectedOutcome != in.SelectedOutcome || fullOutcome != in.FullOutcome {
			return errors.New("audit input does not match persisted repository, plan, or execution outcomes")
		}
		var fullMeta struct {
			FullSuite bool `json:"full_suite"`
		}
		if err := json.Unmarshal(fullSummary, &fullMeta); err != nil || !fullMeta.FullSuite {
			return errors.New("full execution was not registered as a full-suite run")
		}
		if err := verifyAuditUnitEvidence(ctx, tx, in); err != nil {
			return err
		}
		if result.Record.ID == uuid.Nil {
			result.Record.ID = uuid.New()
		}
		if _, err := tx.Exec(ctx, `INSERT INTO audit_records(id,tenant_id,execution_id,full_execution_id,comparability,outcome,missed_test_keys_json,details_json,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, result.Record.ID, in.TenantID, in.SelectedExecutionID, in.FullExecutionID, comparability, result.Record.Outcome, missed, details, result.Record.CreatedAt); err != nil {
			return err
		}
		if !result.Quarantine {
			return nil
		}
		reason := "AUDIT_POSSIBLE_MISS"
		if result.Disposition == audit.DispositionConfirmed {
			reason = "AUDIT_CONFIRMED_MISS"
		}
		expiresAt := in.Now.Add(in.QuarantineFor)
		_, err = tx.Exec(ctx, `INSERT INTO quarantines(tenant_id,repository_id,scope_key,reason_code,source_audit_id,status,created_at,expires_at)
			VALUES($1,$2,$3,$4,$5,'QUARANTINED',$6,$7)
			ON CONFLICT (repository_id,scope_key) WHERE status IN ('QUARANTINED','ACKNOWLEDGED')
			DO UPDATE SET reason_code=EXCLUDED.reason_code,source_audit_id=EXCLUDED.source_audit_id,status='QUARANTINED',created_at=EXCLUDED.created_at,expires_at=EXCLUDED.expires_at,acknowledged_by=NULL,acknowledged_at=NULL,cleared_at=NULL`, in.TenantID, in.RepositoryID, scope, reason, result.Record.ID, in.Now, expiresAt)
		if err != nil {
			return err
		}
		return insertAuditEvent(ctx, tx, in.TenantID, uuid.Nil, "quarantine.create", "repository", in.RepositoryID.String(), map[string]any{"scope_key": scope, "reason": reason, "audit_id": result.Record.ID.String()})
	})
}

func quarantineScope(adapter, rule string) string {
	if adapter == "" {
		adapter = "all"
	}
	if rule == "" {
		rule = "all"
	}
	return adapter + "/" + rule
}

func verifyAuditUnitEvidence(ctx context.Context, tx pgx.Tx, in audit.Input) error {
	planKeys := make([]string, 0)
	rows, err := tx.Query(ctx, `SELECT test_unit_key FROM verification_plan_units WHERE tenant_id=$1 AND plan_id=$2 ORDER BY test_unit_key`, in.TenantID, in.PlanID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		planKeys = append(planKeys, key)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	selected := make(map[string]struct{}, len(in.SelectedKeys))
	for _, key := range in.SelectedKeys {
		if key == "" {
			return errors.New("audit selected unit key is empty")
		}
		if _, duplicate := selected[key]; duplicate {
			return errors.New("audit selected unit keys must be unique")
		}
		selected[key] = struct{}{}
	}
	if len(selected) != len(planKeys) {
		return errors.New("audit selected unit keys do not match the immutable plan")
	}
	for _, key := range planKeys {
		if _, ok := selected[key]; !ok {
			return errors.New("audit selected unit keys do not match the immutable plan")
		}
	}
	omitted := make(map[string]struct{})
	rows, err = tx.Query(ctx, `SELECT test_unit_key FROM omitted_plan_units WHERE tenant_id=$1 AND plan_id=$2`, in.TenantID, in.PlanID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		omitted[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	seen := make(map[string]struct{}, len(in.Failures))
	for _, failure := range in.Failures {
		if failure.TestUnitKey == "" {
			return errors.New("audit failure key is empty")
		}
		if _, duplicate := seen[failure.TestUnitKey]; duplicate {
			return errors.New("audit failure keys must be unique")
		}
		seen[failure.TestUnitKey] = struct{}{}
		if _, exists := omitted[failure.TestUnitKey]; !exists {
			return errors.New("audit failure key is not an omitted plan unit")
		}
		var failed bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_unit_results WHERE tenant_id=$1 AND execution_id=$2 AND test_unit_key=$3 AND status IN ('FAILED','ERROR'))`, in.TenantID, in.FullExecutionID, failure.TestUnitKey).Scan(&failed); err != nil {
			return err
		}
		if !failed {
			return errors.New("audit failure key does not match a persisted full-execution failure")
		}
	}
	return nil
}

func (d *DB) CreateQuarantine(ctx context.Context, tenantID, actorID, repositoryID uuid.UUID, scope, reason string, auditID *uuid.UUID, expiresAt *time.Time) error {
	if scope == "" || reason == "" {
		return errors.New("quarantine scope and reason required")
	}
	return d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		var expiry any
		if expiresAt != nil {
			expiry = *expiresAt
		}
		var id uuid.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO quarantines(tenant_id,repository_id,scope_key,reason_code,source_audit_id,status,expires_at) VALUES($1,$2,$3,$4,$5,'QUARANTINED',$6) RETURNING id`, tenantID, repositoryID, scope, reason, auditID, expiry).Scan(&id); err != nil {
			return err
		}
		return insertAuditEvent(ctx, tx, tenantID, actorID, "quarantine.create", "repository", repositoryID.String(), map[string]any{"scope_key": scope, "reason": reason, "quarantine_id": id.String()})
	})
}

func (d *DB) AcknowledgeQuarantine(ctx context.Context, tenantID, actorID, repositoryID uuid.UUID, scope, reason string) error {
	if reason == "" {
		return errors.New("acknowledgement reason required")
	}
	return d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE quarantines SET status='ACKNOWLEDGED',acknowledged_by=$4,acknowledged_at=now() WHERE tenant_id=$1 AND repository_id=$2 AND scope_key=$3 AND status='QUARANTINED'`, tenantID, repositoryID, scope, actorID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("active quarantine not found")
		}
		return insertAuditEvent(ctx, tx, tenantID, actorID, "quarantine.acknowledge", "repository", repositoryID.String(), map[string]any{"scope_key": scope, "reason": reason})
	})
}

func (d *DB) ClearEligibleQuarantine(ctx context.Context, tenantID, repositoryID uuid.UUID, scope string, now time.Time) error {
	return d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		var actor uuid.UUID
		err := tx.QueryRow(ctx, `SELECT acknowledged_by FROM quarantines WHERE tenant_id=$1 AND repository_id=$2 AND scope_key=$3 AND status='ACKNOWLEDGED' AND acknowledged_at IS NOT NULL AND expires_at IS NOT NULL AND expires_at <= $4 FOR UPDATE`, tenantID, repositoryID, scope, now).Scan(&actor)
		if err != nil {
			return errors.New("quarantine must be acknowledged and expired before clearing")
		}
		var clean bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM audit_records ar JOIN executions e ON e.id=ar.execution_id JOIN verification_plans p ON p.id=e.plan_id WHERE ar.tenant_id=$1 AND p.repository_id=$2 AND ar.outcome=$3 AND ar.comparability='COMPARABLE' AND ar.details_json->>'scope_key'=$4 AND ar.created_at>(SELECT acknowledged_at FROM quarantines WHERE tenant_id=$1 AND repository_id=$2 AND scope_key=$4 AND status='ACKNOWLEDGED'))`, tenantID, repositoryID, domain.AuditNoMissObserved, scope).Scan(&clean); err != nil {
			return err
		}
		if !clean {
			return errors.New("a clean comparable audit after acknowledgement is required")
		}
		if _, err = tx.Exec(ctx, `UPDATE quarantines SET status='CLEARED',cleared_at=$4 WHERE tenant_id=$1 AND repository_id=$2 AND scope_key=$3 AND status='ACKNOWLEDGED'`, tenantID, repositoryID, scope, now); err != nil {
			return err
		}
		return insertAuditEvent(ctx, tx, tenantID, actor, "quarantine.clear", "repository", repositoryID.String(), map[string]any{"scope_key": scope})
	})
}

var _ audit.Recorder = (*DB)(nil)
var _ audit.QuarantineStore = (*DB)(nil)
