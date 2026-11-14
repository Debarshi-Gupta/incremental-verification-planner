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
	details, err := json.Marshal(map[string]any{"adapter": in.Adapter, "scope_key": scope, "failure_output_hash": in.FailureOutputHash, "disposition": result.Disposition, "selected_fingerprint": in.SelectedFingerprint, "full_fingerprint": in.FullFingerprint, "failures": in.Failures})
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
		if repositoryID != in.RepositoryID || planID != in.PlanID || !auditOutcomeMatches(selectedOutcome, in.SelectedOutcome) || !auditOutcomeMatches(fullOutcome, in.FullOutcome) {
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
		// Reclassify the audited selected execution so its stored outcome
		// reflects what the audit established (ADR 0015). The WHERE guard
		// keeps the update on the outcome this transaction read; a
		// concurrent re-audit that changed it first fails the whole audit
		// so it can be retried against fresh state.
		if newOutcome, ok := AuditOutcomeTransition(selectedOutcome, result.Disposition); ok && newOutcome != selectedOutcome {
			tag, err := tx.Exec(ctx, `UPDATE executions SET outcome=$3 WHERE tenant_id=$1 AND id=$2 AND outcome=$4`, in.TenantID, in.SelectedExecutionID, newOutcome, selectedOutcome)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return errors.New("audit selected execution outcome changed concurrently")
			}
		}
		for _, failure := range in.Failures {
			if failure.RepeatCount == 0 {
				continue
			}
			observations, err := comparableObservationCount(ctx, tx, in.TenantID, in.FullExecutionID, failure.TestUnitKey)
			if err != nil {
				return err
			}
			flakeClass := flakeClassification(failure.Flaky, observations, in.FlakeMinimumComparableObservations)
			if _, err := tx.Exec(ctx, `UPDATE test_history h SET retry_count=GREATEST(h.retry_count,$4),flake_classification=$5
				FROM executions e JOIN verification_plans p ON p.id=e.plan_id AND p.tenant_id=e.tenant_id
				JOIN repository_snapshots s ON s.id=p.snapshot_id AND s.repository_id=p.repository_id
				WHERE e.tenant_id=$1 AND e.id=$2 AND h.tenant_id=$1 AND h.repository_id=p.repository_id
				AND h.stable_test_key=$3 AND h.source_snapshot_hash=s.tree_hash
				AND h.environment_digest=e.environment_digest AND h.started_at=e.started_at`, in.TenantID, in.FullExecutionID, failure.TestUnitKey, failure.RepeatCount, flakeClass); err != nil {
				return err
			}
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

// comparableObservationCount reports how many comparable samples the test
// already carries for the same repository, environment and test configuration
// as the audited full run (the same comparability identity ApplyTestHistory
// aggregates, minus the rolling window: LLD 7.3 asks for a minimum number of
// comparable observations before flakiness may be classified, while the
// rolling window governs the read-side flake rate, which stays gated in
// ApplyTestHistory). Requiring PostgreSQL, its direct assertion is
// integration-only.
func comparableObservationCount(ctx context.Context, tx pgx.Tx, tenantID, fullExecutionID uuid.UUID, testUnitKey string) (int64, error) {
	var observations int64
	err := tx.QueryRow(ctx, `SELECT count(*)
		FROM test_history h
		JOIN executions e ON e.tenant_id=$1 AND e.id=$2
		JOIN verification_plans p ON p.id=e.plan_id AND p.tenant_id=e.tenant_id
		JOIN repository_snapshots s ON s.id=p.snapshot_id AND s.repository_id=p.repository_id
		WHERE h.tenant_id=$1 AND h.repository_id=p.repository_id AND h.stable_test_key=$3
		AND h.environment_digest=e.environment_digest AND h.test_config_digest=s.test_config_digest
		AND h.outcome IN ('PASS','FAIL') AND h.duration_ms>0`, tenantID, fullExecutionID, testUnitKey).Scan(&observations)
	return observations, err
}

// flakeClassification decides the flake_classification an audit repeat writes
// (LLD 7.3). FLAKY requires at least flakeMinimumObservations comparable
// observations: below the minimum — or with a non-positive minimum, which
// config validation forbids and which must never silently disable the gate —
// a flaky failure records no classification instead of claiming flakiness
// from insufficient evidence. Reproduced repeats keep REPEATED, which records
// retry reproducibility, not flakiness, and is not gated; the vocabulary
// (empty, REPEATED, FLAKY) is unchanged. It is pure so the gate is
// unit-testable without a database.
func flakeClassification(flaky bool, observations int64, flakeMinimumObservations int) string {
	if !flaky {
		return "REPEATED"
	}
	if flakeMinimumObservations < 1 || observations < int64(flakeMinimumObservations) {
		return ""
	}
	return "FLAKY"
}

// AuditOutcomeTransition maps an audit disposition onto the execution outcome
// it implies for the audited selected run. Only a passing outcome or an
// outcome already reclassified by an audit may be reclassified; failing,
// infra-failed, and stale executions keep their outcome. The transition is
// pure so its rules are testable without a database (ADR 0015).
func AuditOutcomeTransition(current domain.ExecutionOutcome, disposition audit.Disposition) (domain.ExecutionOutcome, bool) {
	var target domain.ExecutionOutcome
	switch disposition {
	case audit.DispositionConfirmed:
		target = domain.OutcomeAuditMiss
	case audit.DispositionPossible:
		target = domain.OutcomeAuditPossibleMiss
	case audit.DispositionIncomparable:
		target = domain.OutcomeAuditIncomparable
	default:
		return current, false
	}
	if !domain.IsPassLikeOutcome(current) {
		return current, false
	}
	return target, true
}

// auditOutcomeMatches reports whether a persisted execution outcome matches
// the outcome an audit input declares. A re-audit may present the outcome the
// execution held before an earlier audit reclassified it, so an AUDIT_*
// outcome and its originating passing outcome are equivalent (ADR 0015).
func auditOutcomeMatches(persisted, declared domain.ExecutionOutcome) bool {
	if persisted == declared {
		return true
	}
	return domain.IsPassLikeOutcome(persisted) && domain.IsPassLikeOutcome(declared) &&
		(domain.IsAuditOutcome(persisted) || domain.IsAuditOutcome(declared))
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

func (d *DB) ClearEligibleQuarantine(ctx context.Context, tenantID, actorID, repositoryID uuid.UUID, scope, reason string, now time.Time) error {
	if scope == "" || reason == "" {
		return errors.New("scope and clearing reason required")
	}
	return d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		var acknowledgedBy uuid.UUID
		// LLD 10.5: clearing requires acknowledgement plus a clean comparable
		// audit. Expiry alone is not the gate: a never-expiring quarantine
		// (expires_at IS NULL) becomes clearable once acknowledged and followed
		// by a clean audit, while an unexpired one must still wait for its
		// expiry window (or have no expiry at all).
		err := tx.QueryRow(ctx, `SELECT acknowledged_by FROM quarantines WHERE tenant_id=$1 AND repository_id=$2 AND scope_key=$3 AND status='ACKNOWLEDGED' AND acknowledged_by IS NOT NULL AND acknowledged_at IS NOT NULL AND (expires_at IS NULL OR expires_at <= $4) FOR UPDATE`, tenantID, repositoryID, scope, now).Scan(&acknowledgedBy)
		if err != nil {
			return errors.New("quarantine must be acknowledged and either expired or non-expiring before clearing")
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
		return insertAuditEvent(ctx, tx, tenantID, actorID, "quarantine.clear", "repository", repositoryID.String(), map[string]any{"scope_key": scope, "reason": reason, "acknowledged_by": acknowledgedBy.String()})
	})
}

var _ audit.Recorder = (*DB)(nil)
var _ audit.QuarantineStore = (*DB)(nil)
