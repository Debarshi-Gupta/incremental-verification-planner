package cli

import (
	"testing"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/config"
	"github.com/ivp/ivp/internal/domain"
)

// P0-4b regression: modes.risk_based_ci.audit_sample_rate governs
// RISK_BASED_CI plans and safety.audit.sample_rate every other mode; the
// sampling decision must be able to name the rate source it used.
func TestAuditSampleRateIsScopedToPlanMode(t *testing.T) {
	cfg := config.Defaults()
	cfg.Safety.Audit.SampleRate = 0.25
	cfg.Modes.RiskBasedCI.AuditSampleRate = 0.05

	for _, tc := range []struct {
		mode     domain.PlanMode
		wantRate float64
		wantKey  string
	}{
		{domain.ModeRiskBasedCI, 0.05, "modes.risk_based_ci.audit_sample_rate"},
		{domain.ModeFastFeedback, 0.25, "safety.audit.sample_rate"},
		{domain.ModeObserve, 0.25, "safety.audit.sample_rate"},
		{domain.ModeFull, 0.25, "safety.audit.sample_rate"},
		{domain.PlanMode(""), 0.25, "safety.audit.sample_rate"},
	} {
		rate, key := auditSampleRate(cfg, tc.mode)
		if rate != tc.wantRate || key != tc.wantKey {
			t.Errorf("auditSampleRate(%q) = (%v, %q), want (%v, %q)", tc.mode, rate, key, tc.wantRate, tc.wantKey)
		}
	}
}

// Sampling must be a deterministic function of the execution id so the same
// execution is re-sampled identically, and the bounds must hold.
func TestAuditSampleSelectedIsDeterministicAndBounded(t *testing.T) {
	id := uuid.MustParse("2f9f3b1e-6d0c-4a5f-9d2e-7a1c0b4e5f60")
	if auditSampleSelected(id, 0) {
		t.Fatal("rate 0 must never sample")
	}
	if auditSampleSelected(uuid.Nil, 0.5) {
		t.Fatal("nil execution id must never sample")
	}
	if !auditSampleSelected(id, 1) {
		t.Fatal("rate 1 must always sample")
	}
	first := auditSampleSelected(id, 0.10)
	for i := 0; i < 32; i++ {
		if got := auditSampleSelected(id, 0.10); got != first {
			t.Fatalf("sampling decision for one execution flipped between calls: %v then %v", first, got)
		}
	}
}
