package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/audit"
	"github.com/ivp/ivp/internal/domain"
)

// fakeAuditRecorder records the audit outcome without a database.
type fakeAuditRecorder struct {
	recorded int
	result   audit.Result
}

func (r *fakeAuditRecorder) RecordAuditAndQuarantine(_ context.Context, _ audit.Input, result audit.Result) error {
	r.recorded++
	r.result = result
	return nil
}

// TestNewBackendWithoutTelemetryKeepsAuditInterfacesHonest is the regression
// test for the typed-nil defect: NewBackend(db, nil) used to store a typed
// nil *telemetry.Metrics into the audit metrics interface, so the nil guard
// inside the audit service passed and the first metric call panicked.
func TestNewBackendWithoutTelemetryKeepsAuditInterfacesHonest(t *testing.T) {
	backend := NewBackend(nil, nil)
	if backend.Audit.Metrics != nil {
		t.Fatal("a typed nil metrics pointer must never be stored in the audit metrics interface")
	}
	if backend.Audit.Store != nil {
		t.Fatal("a typed nil database pointer must never be stored in the audit recorder interface")
	}
	recorder := &fakeAuditRecorder{}
	backend.Audit.Store = recorder
	fingerprint := audit.Fingerprint{SourceTreeHash: strings.Repeat("a", 40), DependencyDigest: strings.Repeat("b", 64),
		BuildConfigDigest: strings.Repeat("c", 64), ToolchainDigest: strings.Repeat("d", 64),
		TestConfigDigest: strings.Repeat("e", 64), EnvironmentClass: "linux-ci", RequiredServicesDigest: strings.Repeat("f", 64)}
	input := audit.Input{TenantID: uuid.New(), RepositoryID: uuid.New(), PlanID: uuid.New(), SelectedExecutionID: uuid.New(),
		FullExecutionID: uuid.New(), Adapter: "pytest", RuleScope: "unit", SelectedOutcome: domain.OutcomeTargetedPass,
		FullOutcome: domain.OutcomeFullVerificationFail, SelectedKeys: []string{"selected::unit"},
		Failures:            []audit.Failure{{TestUnitKey: "omitted::unit", Reproduced: true, RelatedToChange: true}},
		SelectedFingerprint: fingerprint, FullFingerprint: fingerprint, Now: time.Now().UTC()}
	// The audit path must complete without telemetry and without a panic.
	result, err := backend.Audit.Process(context.Background(), input)
	if err != nil {
		t.Fatalf("the audit path must work without telemetry: %v", err)
	}
	if result.Disposition != audit.DispositionConfirmed {
		t.Fatalf("unexpected audit disposition: %s", result.Disposition)
	}
	if !result.Quarantine {
		t.Fatal("a confirmed miss must quarantine the selection scope")
	}
	if recorder.recorded != 1 {
		t.Fatalf("the audit record must be written exactly once: %d", recorder.recorded)
	}
}
