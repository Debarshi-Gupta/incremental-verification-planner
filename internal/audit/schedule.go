package audit

import (
	"errors"
	"time"
)

// DefaultScheduledAuditCadence is the shipped LLD 10.2 cadence: every
// repository runs a full verification at least daily.
const DefaultScheduledAuditCadence = 24 * time.Hour

// DefaultAcknowledgementSLA is the shipped LLD 15.3 response SLA for an
// audit miss: a recorded miss must be acknowledged within one day.
const DefaultAcknowledgementSLA = 24 * time.Hour

// SchedulePolicy is the effective audit scheduling policy. It is derived
// from configuration (safety.audit.scheduled_cadence_hours and
// safety.audit.acknowledgement_sla_hours) and is threaded into the server
// through the same precedence rules as every other safety threshold.
type SchedulePolicy struct {
	// Enabled reports whether scheduled audits run at all.
	Enabled bool
	// Cadence is the maximum age of a repository's last full verification
	// before a scheduled audit is due. It is always positive.
	Cadence time.Duration
	// AcknowledgementSLA is the maximum age of an unacknowledged audit miss
	// before the response SLA is considered breached. It is always positive.
	AcknowledgementSLA time.Duration
}

// Validate rejects a policy whose intervals are not positive.
func (p SchedulePolicy) Validate() error {
	if p.Cadence <= 0 {
		return errors.New("scheduled audit cadence must be a positive interval")
	}
	if p.AcknowledgementSLA <= 0 {
		return errors.New("audit acknowledgement SLA must be a positive interval")
	}
	return nil
}

// ScheduledAuditDue reports whether a repository whose last full
// verification happened at lastFullVerification (zero when it never ran one)
// must run another full verification now.
//
// LLD 10.2 requires a full verification at least once per cadence even when
// no random audit was sampled. A repository that never ran a full
// verification is due immediately: it has no evidence at all, so the first
// scheduled audit cannot be deferred.
func (p SchedulePolicy) ScheduledAuditDue(lastFullVerification, now time.Time) bool {
	if !p.Enabled {
		return false
	}
	if lastFullVerification.IsZero() {
		return true
	}
	return now.Sub(lastFullVerification) >= p.Cadence
}

// AcknowledgementOverdue reports whether a miss recorded at recordedAt is
// still unacknowledged (acknowledgedAt is zero) past the response SLA.
func (p SchedulePolicy) AcknowledgementOverdue(recordedAt time.Time, acknowledgedAt time.Time, now time.Time) bool {
	if recordedAt.IsZero() {
		return false
	}
	if !acknowledgedAt.IsZero() {
		return false
	}
	return now.Sub(recordedAt) >= p.AcknowledgementSLA
}
