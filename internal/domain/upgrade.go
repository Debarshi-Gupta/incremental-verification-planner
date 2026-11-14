package domain

import (
	"strconv"
	"strings"
	"time"
)

// UpgradeState records the algorithm and policy versions a repository was last
// calibrated against. LLD 18.4: "On major algorithm/policy change, reset CI
// selective eligibility to OBSERVE until re-calibrated."
type UpgradeState struct {
	// AlgorithmVersion is the selection-algorithm version observed last.
	AlgorithmVersion string
	// PolicyVersion is the effective policy version observed last.
	PolicyVersion int64
	// ResetAt is the time the eligibility was reset. It is zero when the
	// repository was never reset.
	ResetAt time.Time
	// CalibrationRunsAtReset is the number of calibration runs that had been
	// observed when the reset happened.
	CalibrationRunsAtReset int
	// AuditRunsAtReset is the number of comparable audits that had been
	// observed when the reset happened.
	AuditRunsAtReset int
	// Reason explains the reset.
	Reason string
}

// Reset reports whether the repository was ever reset to OBSERVE.
func (s UpgradeState) Reset() bool { return !s.ResetAt.IsZero() }

// Recalibrated reports whether the repository produced enough new calibration
// runs and comparable audits since the reset to regain CI selective
// eligibility. Evidence observed before the reset does not count: a changed
// algorithm or policy invalidates the calibration that justified it.
func (s UpgradeState) Recalibrated(calibrationRuns, auditRuns int, policy Policy) bool {
	if !s.Reset() {
		return true
	}
	newCalibration := calibrationRuns - s.CalibrationRunsAtReset
	newAudits := auditRuns - s.AuditRunsAtReset
	if newCalibration < 0 {
		newCalibration = 0
	}
	if newAudits < 0 {
		newAudits = 0
	}
	minimumCalibration := policy.MinimumCalibrationRuns
	minimumAudits := policy.MinimumAuditRuns
	if minimumCalibration <= 0 {
		minimumCalibration = 1
	}
	if minimumAudits <= 0 {
		minimumAudits = 1
	}
	return newCalibration >= minimumCalibration && newAudits >= minimumAudits
}

// ResetReason explains why CI selective eligibility is currently unavailable.
func (s UpgradeState) ResetReason(calibrationRuns, auditRuns int, policy Policy) string {
	if !s.Reset() {
		return ""
	}
	if s.Recalibrated(calibrationRuns, auditRuns, policy) {
		return ""
	}
	newCalibration := calibrationRuns - s.CalibrationRunsAtReset
	newAudits := auditRuns - s.AuditRunsAtReset
	if newCalibration < 0 {
		newCalibration = 0
	}
	if newAudits < 0 {
		newAudits = 0
	}
	minimumCalibration := policy.MinimumCalibrationRuns
	minimumAudits := policy.MinimumAuditRuns
	if minimumCalibration <= 0 {
		minimumCalibration = 1
	}
	if minimumAudits <= 0 {
		minimumAudits = 1
	}
	reason := s.Reason
	if reason == "" {
		reason = "a major algorithm or policy change was detected"
	}
	return reason + ": " + strconv.Itoa(newCalibration) + "/" + strconv.Itoa(minimumCalibration) +
		" calibration runs and " + strconv.Itoa(newAudits) + "/" + strconv.Itoa(minimumAudits) +
		" comparable audits observed since the reset"
}

// MajorAlgorithmChange reports whether the selection-algorithm version changed
// in a way that invalidates prior calibration. The major component of the
// version decides: a major bump resets the eligibility, a minor or patch
// release does not. An unparsable version that differs from the recorded one
// is treated as a major change because it cannot be shown to be compatible.
func MajorAlgorithmChange(previous, current string) (bool, string) {
	previous = strings.TrimSpace(previous)
	current = strings.TrimSpace(current)
	if previous == "" || current == "" {
		return false, "the algorithm version is unknown"
	}
	if previous == current {
		return false, "the algorithm version is unchanged"
	}
	previousMajor, previousOK := parseAlgorithmMajor(previous)
	currentMajor, currentOK := parseAlgorithmMajor(current)
	if !previousOK || !currentOK {
		return true, "the algorithm version changed and could not be compared"
	}
	if previousMajor != currentMajor {
		return true, "the selection algorithm major version changed"
	}
	return false, "the selection algorithm version changed without a major bump"
}

// parseAlgorithmMajor extracts the major component of an algorithm version.
// Algorithm versions are prefixed names ("ivp-planner-v1.0.0"), so the
// version part is the last dash-separated component.
func parseAlgorithmMajor(raw string) (int, bool) {
	candidate := raw
	if index := strings.LastIndex(raw, "-"); index >= 0 && index+1 < len(raw) {
		candidate = raw[index+1:]
	}
	candidate = strings.TrimPrefix(strings.TrimPrefix(candidate, "v"), "V")
	parts := strings.Split(candidate, ".")
	if len(parts) == 0 {
		return 0, false
	}
	major, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || major < 0 {
		return 0, false
	}
	return major, true
}

// MajorPolicyChange reports whether the effective policy changed in a way that
// invalidates prior calibration. Policy versions are opaque monotonic
// integers, so any change is treated as a major policy change: IVP cannot
// prove that a new policy keeps the calibration of the previous one.
func MajorPolicyChange(previous, current int64) (bool, string) {
	if previous == current {
		return false, "the policy version is unchanged"
	}
	if previous <= 0 {
		return false, "no previous policy version was recorded"
	}
	return true, "the effective policy version changed"
}
