package audit

import (
	"testing"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
)

func uuidFrom(s string) uuid.UUID { return uuid.MustParse(s) }

func equivalentFingerprints() (Fingerprint, Fingerprint) {
	f := Fingerprint{SourceTreeHash: "tree", DependencyDigest: "deps", BuildConfigDigest: "build", ToolchainDigest: "tools", TestConfigDigest: "tests", EnvironmentClass: "linux-ci", RequiredServicesDigest: "services"}
	return f, f
}

func auditInput(a, b Fingerprint) Input {
	return Input{
		PlanID:              uuidFrom("00000000-0000-4000-8000-000000000001"),
		RepositoryID:        uuidFrom("00000000-0000-4000-8000-000000000002"),
		SelectedExecutionID: uuidFrom("00000000-0000-4000-8000-000000000003"),
		FullExecutionID:     uuidFrom("00000000-0000-4000-8000-000000000004"),
		SelectedOutcome:     domain.OutcomeTargetedPass,
		FullOutcome:         domain.OutcomeFullVerificationFail,
		SelectedFingerprint: a,
		FullFingerprint:     b,
	}
}

func TestConfirmedOmittedFailureQuarantines(t *testing.T) {
	a, b := equivalentFingerprints()
	in := auditInput(a, b)
	in.SelectedKeys = []string{"selected"}
	in.Failures = []Failure{{TestUnitKey: "omitted", Reproduced: true, RelatedToChange: true}}
	r := Classify(in)
	if r.Disposition != DispositionConfirmed || !r.Quarantine || len(r.MissedTestKeys) != 1 || r.Record.Outcome != domain.AuditConfirmedMiss {
		t.Fatalf("unexpected result: %+v", r)
	}
}

func TestIncomparableAuditCannotClearQuarantine(t *testing.T) {
	a, b := equivalentFingerprints()
	b.ToolchainDigest = "other"
	in := auditInput(a, b)
	in.FullFingerprint = b
	r := Classify(in)
	if r.Disposition != DispositionIncomparable || r.Record.Outcome != domain.AuditIncomparable || r.Quarantine {
		t.Fatalf("unexpected result: %+v", r)
	}
}

func TestPossibleMissQuarantinesUntilHumanResolution(t *testing.T) {
	a, b := equivalentFingerprints()
	in := auditInput(a, b)
	in.Failures = []Failure{{TestUnitKey: "flaky", Flaky: true, RelatedToChange: true}}
	r := Classify(in)
	if r.Disposition != DispositionPossible || !r.Quarantine {
		t.Fatalf("possible miss not quarantined: %+v", r)
	}
}

func TestFailedSelectedRunDoesNotCreateMiss(t *testing.T) {
	a, b := equivalentFingerprints()
	in := auditInput(a, b)
	in.SelectedOutcome = domain.OutcomeTargetedFail
	in.Failures = []Failure{{TestUnitKey: "omitted", Reproduced: true, RelatedToChange: true}}
	r := Classify(in)
	if r.Disposition != DispositionNoMiss || r.Quarantine || r.Record.Outcome != domain.AuditNoMissObserved {
		t.Fatalf("unexpected result: %+v", r)
	}
}

func TestUnreviewedUnrelatedFailureRemainsPossible(t *testing.T) {
	a, b := equivalentFingerprints()
	in := auditInput(a, b)
	in.Failures = []Failure{{TestUnitKey: "failure", Reproduced: true, RelatedToChange: false}}
	r := Classify(in)
	if r.Disposition != DispositionPossible || !r.Quarantine {
		t.Fatalf("unreviewed unrelated failure was dismissed: %+v", r)
	}
}

func TestReviewedUnrelatedFailureIsNotMiss(t *testing.T) {
	a, b := equivalentFingerprints()
	in := auditInput(a, b)
	in.Failures = []Failure{{TestUnitKey: "failure", Reproduced: true, RelatedToChange: false, ReviewedNotAttributable: true}}
	r := Classify(in)
	if r.Disposition != DispositionNotAttributable || r.Quarantine || r.Record.Outcome != domain.AuditNoMissObserved {
		t.Fatalf("unexpected result: %+v", r)
	}
}
