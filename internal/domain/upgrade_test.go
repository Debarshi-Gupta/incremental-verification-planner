package domain

import (
	"testing"
	"time"
)

func TestMajorAlgorithmChangeDetectsMajorBumpsOnly(t *testing.T) {
	if changed, _ := MajorAlgorithmChange("ivp-planner-v1.0.0", "ivp-planner-v1.0.1"); changed {
		t.Fatal("a patch release must not reset the calibration")
	}
	if changed, _ := MajorAlgorithmChange("ivp-planner-v1.0.0", "ivp-planner-v2.0.0"); !changed {
		t.Fatal("a major algorithm change must reset the calibration")
	}
	if changed, _ := MajorAlgorithmChange("ivp-planner-v1.0.0", "ivp-planner-v1.0.0"); changed {
		t.Fatal("an unchanged algorithm version must not reset the calibration")
	}
	if changed, _ := MajorAlgorithmChange("", "ivp-planner-v2.0.0"); changed {
		t.Fatal("an unknown previous algorithm version must not be reported as a change")
	}
}

func TestMajorPolicyChangeTreatsEveryVersionChangeAsMajor(t *testing.T) {
	if changed, _ := MajorPolicyChange(3, 3); changed {
		t.Fatal("an unchanged policy version must not reset the calibration")
	}
	if changed, _ := MajorPolicyChange(3, 4); !changed {
		t.Fatal("a new policy version must reset the calibration")
	}
	if changed, _ := MajorPolicyChange(0, 4); changed {
		t.Fatal("a repository without a recorded policy version must not be reported as changed")
	}
}

func TestUpgradeStateRecalibrationRequiresEvidenceObservedAfterTheReset(t *testing.T) {
	policy := Policy{MinimumCalibrationRuns: 2, MinimumAuditRuns: 3}
	reset := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	state := UpgradeState{AlgorithmVersion: "ivp-planner-v2.0.0", PolicyVersion: 4, ResetAt: reset,
		CalibrationRunsAtReset: 10, AuditRunsAtReset: 5, Reason: "the selection algorithm major version changed"}
	if !state.Reset() {
		t.Fatal("a recorded reset must be reported as a reset")
	}
	if state.Recalibrated(10, 5, policy) {
		t.Fatal("evidence observed before the reset must not re-enable selective gating")
	}
	if state.Recalibrated(11, 7, policy) {
		t.Fatal("insufficient post-reset calibration must not re-enable selective gating")
	}
	if !state.Recalibrated(12, 8, policy) {
		t.Fatal("sufficient post-reset calibration must re-enable selective gating")
	}
	reason := state.ResetReason(11, 7, policy)
	if reason == "" {
		t.Fatal("a repository that is not re-calibrated must explain why CI selective gating is unavailable")
	}
	fresh := UpgradeState{AlgorithmVersion: "ivp-planner-v2.0.0", PolicyVersion: 4}
	if fresh.Reset() || fresh.Recalibrated(0, 0, policy) != true {
		t.Fatal("a repository without a reset stays eligible")
	}
}
