package persistence

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// LLD 12.4: results are append-only. A correction supersedes the recorded
// status without overwriting history: the original status and the audit
// reason stay visible.
func TestApplyResultCorrectionsKeepsHistoryAndSupersedesStatus(t *testing.T) {
	results := []UnitResult{
		{TestUnitKey: "test:a", Attempt: 1, Status: "FAILED"},
		{TestUnitKey: "test:b", Attempt: 1, Status: "PASSED"},
		{TestUnitKey: "test:a", Attempt: 2, Status: "FAILED"},
	}
	corrections := []ResultCorrection{
		{ID: uuid.New(), TestUnitKey: "test:a", Attempt: 1, SupersededStatus: "FAILED", SupersedingStatus: "PASSED",
			Reason: "the report was parsed twice", CreatedAt: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)},
	}
	effective := ApplyResultCorrections(results, corrections)
	if effective[0].Status != "PASSED" || effective[0].SupersededStatus != "FAILED" {
		t.Fatalf("the corrected result must carry the superseding status and keep the original: %+v", effective[0])
	}
	if effective[0].CorrectionReason != "the report was parsed twice" {
		t.Fatalf("the corrected result must carry the audit reason: %+v", effective[0])
	}
	if effective[1].Status != "PASSED" || effective[1].SupersededStatus != "" || effective[1].CorrectionReason != "" {
		t.Fatalf("an uncorrected result must stay untouched: %+v", effective[1])
	}
	if effective[2].Status != "FAILED" || effective[2].SupersededStatus != "" {
		t.Fatalf("another attempt of the same unit must not be corrected: %+v", effective[2])
	}
	if results[0].Status != "FAILED" {
		t.Fatalf("the stored results must not be mutated: %+v", results[0])
	}
}

// The latest correction for a result wins, and the order is deterministic
// even for corrections recorded at the same instant.
func TestLatestCorrectionForAResultWins(t *testing.T) {
	results := []UnitResult{{TestUnitKey: "test:a", Attempt: 1, Status: "FAILED"}}
	first := ResultCorrection{ID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), TestUnitKey: "test:a", Attempt: 1,
		SupersededStatus: "FAILED", SupersedingStatus: "SKIPPED", Reason: "first reading",
		CreatedAt: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
	second := ResultCorrection{ID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), TestUnitKey: "test:a", Attempt: 1,
		SupersededStatus: "FAILED", SupersedingStatus: "PASSED", Reason: "second reading",
		CreatedAt: time.Date(2026, 11, 12, 11, 0, 0, 0, time.UTC)}
	effective := ApplyResultCorrections(results, []ResultCorrection{first, second})
	if effective[0].Status != "PASSED" || effective[0].CorrectionReason != "second reading" {
		t.Fatalf("the latest correction must win: %+v", effective[0])
	}
	sameInstant := ResultCorrection{ID: uuid.MustParse("00000000-0000-0000-0000-000000000003"), TestUnitKey: "test:a", Attempt: 1,
		SupersededStatus: "FAILED", SupersedingStatus: "ERROR", Reason: "third reading", CreatedAt: first.CreatedAt}
	ordered := ApplyResultCorrections(results, []ResultCorrection{first, sameInstant})
	if ordered[0].Status != "ERROR" {
		t.Fatalf("corrections recorded at the same instant must break deterministically: %+v", ordered[0])
	}
}

// A correction without a unit key, attempt, superseding status or audit
// reason is rejected before it reaches the database.
func TestCorrectionRequiresUnitAttemptStatusAndReason(t *testing.T) {
	db := &DB{}
	tenantID, executionID := uuid.New(), uuid.New()
	for name, call := range map[string]func() error{
		"missing unit key": func() error {
			_, err := db.RecordResultCorrection(context.Background(), tenantID, executionID, " ", 1, "PASSED", "reason", uuid.Nil)
			return err
		},
		"missing attempt": func() error {
			_, err := db.RecordResultCorrection(context.Background(), tenantID, executionID, "test:a", 0, "PASSED", "reason", uuid.Nil)
			return err
		},
		"missing superseding status": func() error {
			_, err := db.RecordResultCorrection(context.Background(), tenantID, executionID, "test:a", 1, " ", "reason", uuid.Nil)
			return err
		},
		"missing audit reason": func() error {
			_, err := db.RecordResultCorrection(context.Background(), tenantID, executionID, "test:a", 1, "PASSED", "  ", uuid.Nil)
			return err
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s accepted by RecordResultCorrection", name)
		}
	}
}
