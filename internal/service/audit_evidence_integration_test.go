//go:build integration

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ivp/ivp/internal/audit"
	"github.com/ivp/ivp/internal/config"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/security"
	"github.com/jackc/pgx/v5"
)

// LLD 10.5 and 10.6: the audit record stores the missed tests, the selected
// set, the evidence, the policy and the failure output hash; the quarantine is
// applied in the same transaction; a recorded miss survives a policy change;
// and the quality dashboards read the retained data back.
func TestAuditRecordContentRetentionAndDashboards(t *testing.T) {
	ctx := context.Background()
	db := openServiceDB(t)
	backend := NewBackend(db, nil)
	tenant, user, repo, planID, selectedExecution, fullExecution, selectedKey, omittedKey := seedAuditScenario(t, db)

	fingerprints := comparableFingerprints()
	confirmed, err := backend.Audit.Process(ctx, audit.Input{
		TenantID: tenant, RepositoryID: repo, PlanID: planID,
		SelectedExecutionID: selectedExecution, FullExecutionID: fullExecution,
		Adapter: "pytest", SelectedOutcome: domain.OutcomeTargetedPass, FullOutcome: domain.OutcomeFullVerificationFail,
		SelectedKeys:        []string{selectedKey},
		Failures:            []audit.Failure{{TestUnitKey: omittedKey, Reproduced: true, RelatedToChange: true}},
		SelectedFingerprint: fingerprints, FullFingerprint: fingerprints,
		FailureOutputHash: "sha256:report",
	})
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Disposition != audit.DispositionConfirmed || !confirmed.Quarantine {
		t.Fatalf("the audit was not classified as a confirmed miss: %+v", confirmed)
	}

	// (1) The stored record carries the missed tests and the evidence.
	var missed []byte
	var details []byte
	var comparability, outcome string
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT missed_test_keys_json,details_json,comparability,outcome FROM audit_records WHERE tenant_id=$1 AND id=$2`,
			tenant, confirmed.Record.ID).Scan(&missed, &details, &comparability, &outcome)
	}); err != nil {
		t.Fatal(err)
	}
	var missedKeys []string
	if err := json.Unmarshal(missed, &missedKeys); err != nil {
		t.Fatal(err)
	}
	if len(missedKeys) != 1 || missedKeys[0] != omittedKey {
		t.Fatalf("the audit record must store the missed test keys, got %v", missedKeys)
	}
	if comparability != "COMPARABLE" || outcome != string(domain.AuditConfirmedMiss) {
		t.Fatalf("the audit record comparability/outcome = %q/%q", comparability, outcome)
	}
	var recorded map[string]any
	if err := json.Unmarshal(details, &recorded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"adapter", "scope_key", "failure_output_hash", "disposition", "selected_fingerprint", "full_fingerprint", "failures"} {
		if _, ok := recorded[key]; !ok {
			t.Fatalf("the audit details must record %q, got %v", key, recorded)
		}
	}
	if recorded["adapter"] != "pytest" || recorded["scope_key"] != "pytest/all" {
		t.Fatalf("the audit details must name the adapter and the scope, got %v", recorded)
	}

	// (2) The quarantine and its audit event are written with the record.
	var quarantines, events int
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM quarantines WHERE tenant_id=$1 AND source_audit_id=$2`, tenant, confirmed.Record.ID).Scan(&quarantines); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='quarantine.create' AND metadata_json->>'audit_id'=$2`,
			tenant, confirmed.Record.ID.String()).Scan(&events)
	}); err != nil {
		t.Fatal(err)
	}
	if quarantines != 1 {
		t.Fatalf("the quarantine rows referencing the audit = %d, want 1", quarantines)
	}
	if events != 1 {
		t.Fatalf("the audit events referencing the audit = %d, want 1", events)
	}

	// (3) A policy change never erases a recorded miss.
	policy := config.ToDomainPolicy(config.Defaults(), 2, nil)
	encoded, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := security.CanonicalJSONHash(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SavePolicy(ctx, tenant, user, repo, policy, hash); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_records WHERE tenant_id=$1 AND id=$2 AND audit_records.outcome=$3`,
			tenant, confirmed.Record.ID, domain.AuditConfirmedMiss).Scan(&remaining)
	}); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("the recorded miss must survive a policy change, found %d rows", remaining)
	}

	// (4) The quality dashboards read the retained data back.
	metrics, err := db.GetRepositoryMetrics(ctx, tenant, repo, 30, "")
	if err != nil {
		t.Fatal(err)
	}
	if metrics.ConfirmedMisses < 1 || metrics.ComparableAudits < 1 {
		t.Fatalf("the retained miss data is missing: %+v", metrics)
	}
	if metrics.ObservedMissRate <= 0 || metrics.MissRateCI95Lower > metrics.ObservedMissRate || metrics.MissRateCI95Upper < metrics.ObservedMissRate {
		t.Fatalf("the miss rate and its Wilson bounds are inconsistent: %+v", metrics)
	}
	if metrics.AuditedRuns != metrics.ComparableAudits+metrics.IncomparableAudits {
		t.Fatalf("the audited-run denominator = %d, want %d", metrics.AuditedRuns, metrics.ComparableAudits+metrics.IncomparableAudits)
	}
	if metrics.RunsLackingComparableEvidence < 0 || metrics.RunsLackingComparableEvidence > 1 {
		t.Fatalf("the share of runs lacking comparable evidence = %v, want a share in [0,1]", metrics.RunsLackingComparableEvidence)
	}
	_ = time.Now
}

// LLD 4.5 and 23: the retry history of a flaky failure is preserved on the
// test history, and the flaky classification is recorded with it.
func TestFlakyRetryHistoryIsPreserved(t *testing.T) {
	ctx := context.Background()
	db := openServiceDB(t)
	backend := NewBackend(db, nil)
	tenant, _, repo, planID, selectedExecution, fullExecution, selectedKey, omittedKey := seedAuditScenario(t, db)

	// The test history row the audit updates: same repository, snapshot hash,
	// environment and start time as the audited full run, and the same test
	// configuration digest as the snapshot.
	var startedAt time.Time
	var testConfigDigest string
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT started_at FROM executions WHERE tenant_id=$1 AND id=$2`, tenant, fullExecution).Scan(&startedAt); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT s.test_config_digest FROM executions e JOIN verification_plans p ON p.id=e.plan_id AND p.tenant_id=e.tenant_id
			JOIN repository_snapshots s ON s.id=p.snapshot_id AND s.repository_id=p.repository_id
			WHERE e.tenant_id=$1 AND e.id=$2`, tenant, fullExecution).Scan(&testConfigDigest)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO test_history(tenant_id,repository_id,stable_test_key,adapter,source_snapshot_hash,environment_digest,build_config_digest,test_config_digest,started_at,duration_ms,cpu_ms,outcome,failure_fingerprint)
			VALUES($1,$2,$3,'pytest','tree-audit','environment','build',$4,$5,100,10,'FAIL','fp')`, tenant, repo, omittedKey, testConfigDigest, startedAt)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	fingerprints := comparableFingerprints()
	result, err := backend.Audit.Process(ctx, audit.Input{
		TenantID: tenant, RepositoryID: repo, PlanID: planID,
		SelectedExecutionID: selectedExecution, FullExecutionID: fullExecution,
		Adapter: "pytest", SelectedOutcome: domain.OutcomeTargetedPass, FullOutcome: domain.OutcomeFullVerificationFail,
		SelectedKeys:        []string{selectedKey},
		Failures:            []audit.Failure{{TestUnitKey: omittedKey, Reproduced: true, RelatedToChange: true, Flaky: true, RepeatCount: 2}},
		SelectedFingerprint: fingerprints, FullFingerprint: fingerprints,
		// The LLD 7.3 gate: flakiness is classified only after at least one
		// comparable observation.
		FlakeMinimumComparableObservations: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != audit.DispositionPossible {
		t.Fatalf("a flaky failure must not be a confirmed miss: %+v", result)
	}
	var retryCount int
	var classification string
	if err := db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT retry_count,flake_classification FROM test_history WHERE tenant_id=$1 AND stable_test_key=$2`,
			tenant, omittedKey).Scan(&retryCount, &classification)
	}); err != nil {
		t.Fatal(err)
	}
	if retryCount != 2 {
		t.Fatalf("the retry history = %d, want the 2 recorded repeats", retryCount)
	}
	if classification != "FLAKY" {
		t.Fatalf("the flake classification = %q, want FLAKY", classification)
	}
}
