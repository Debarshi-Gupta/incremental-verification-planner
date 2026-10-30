package audit

import (
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
)

type Fingerprint struct {
	SourceTreeHash         string `json:"source_tree_hash"`
	DependencyDigest       string `json:"dependency_digest"`
	BuildConfigDigest      string `json:"build_config_digest"`
	ToolchainDigest        string `json:"toolchain_digest"`
	TestConfigDigest       string `json:"test_config_digest"`
	EnvironmentClass       string `json:"environment_class"`
	RequiredServicesDigest string `json:"required_services_digest"`
}

func (f Fingerprint) Equivalent(other Fingerprint) bool {
	return f.SourceTreeHash != "" && f.SourceTreeHash == other.SourceTreeHash &&
		f.DependencyDigest != "" && f.DependencyDigest == other.DependencyDigest &&
		f.BuildConfigDigest != "" && f.BuildConfigDigest == other.BuildConfigDigest &&
		f.ToolchainDigest != "" && f.ToolchainDigest == other.ToolchainDigest &&
		f.TestConfigDigest != "" && f.TestConfigDigest == other.TestConfigDigest &&
		f.EnvironmentClass != "" && f.EnvironmentClass == other.EnvironmentClass &&
		f.RequiredServicesDigest != "" && f.RequiredServicesDigest == other.RequiredServicesDigest
}

type Failure struct {
	TestUnitKey             string `json:"test_unit_key"`
	Reproduced              bool   `json:"reproduced"`
	RelatedToChange         bool   `json:"related_to_change"`
	ReviewedNotAttributable bool   `json:"reviewed_not_attributable"`
	Flaky                   bool   `json:"flaky"`
	RepeatCount             int    `json:"repeat_count,omitempty"`
}
type Disposition string

const (
	DispositionNoMiss          Disposition = "NO_MISS_OBSERVED"
	DispositionPossible        Disposition = "POSSIBLE_MISS"
	DispositionConfirmed       Disposition = "CONFIRMED_MISS"
	DispositionNotAttributable Disposition = "NOT_ATTRIBUTABLE"
	DispositionIncomparable    Disposition = "INCOMPARABLE"
	DispositionAuditFailed     Disposition = "AUDIT_FAILED"
)

type Input struct {
	TenantID            uuid.UUID
	RepositoryID        uuid.UUID
	PlanID              uuid.UUID
	SelectedExecutionID uuid.UUID
	FullExecutionID     uuid.UUID
	Adapter             string
	RuleScope           string
	SelectedOutcome     domain.ExecutionOutcome
	FullOutcome         domain.ExecutionOutcome
	SelectedKeys        []string
	Failures            []Failure
	SelectedFingerprint Fingerprint
	FullFingerprint     Fingerprint
	FailureOutputHash   string
	QuarantineFor       time.Duration
	Now                 time.Time
}

type Result struct {
	Disposition    Disposition
	Record         domain.AuditRecord
	Quarantine     bool
	MissedTestKeys []string
}

func Classify(in Input) Result {
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	}
	result := Result{Disposition: DispositionAuditFailed}
	if in.SelectedExecutionID == uuid.Nil || in.FullExecutionID == uuid.Nil || in.PlanID == uuid.Nil || in.RepositoryID == uuid.Nil {
		return result
	}
	if !in.SelectedFingerprint.Equivalent(in.FullFingerprint) {
		result.Disposition = DispositionIncomparable
		result.Record = makeRecord(in, domain.AuditIncomparable, nil)
		return result
	}
	if in.FullOutcome != domain.OutcomeFullVerificationFail {
		if in.FullOutcome == domain.OutcomeFullVerificationPass {
			result.Disposition = DispositionNoMiss
			result.Record = makeRecord(in, domain.AuditNoMissObserved, nil)
		} else {
			result.Record = makeRecord(in, domain.AuditFailed, nil)
		}
		return result
	}
	// A failing selected run followed by a failing full run does not establish
	// a missed test. A miss is only possible when the selected verification
	// passed but the equivalent full run found a failure.
	if in.SelectedOutcome != domain.OutcomeTargetedPass && in.SelectedOutcome != domain.OutcomeFullVerificationPass {
		if in.SelectedOutcome == domain.OutcomeTargetedFail || in.SelectedOutcome == domain.OutcomeFullVerificationFail {
			result.Disposition = DispositionNoMiss
			result.Record = makeRecord(in, domain.AuditNoMissObserved, nil)
		} else {
			result.Record = makeRecord(in, domain.AuditFailed, nil)
		}
		return result
	}
	if len(in.Failures) == 0 {
		// The authoritative suite failed after the selected run passed, but the
		// reports could not attribute a failing unit. This is unresolved evidence,
		// not evidence that the selected plan was safe.
		result.Disposition = DispositionPossible
		result.Quarantine = true
		result.Record = makeRecord(in, domain.AuditPossibleMiss, nil)
		return result
	}
	selected := make(map[string]struct{}, len(in.SelectedKeys))
	for _, key := range in.SelectedKeys {
		selected[key] = struct{}{}
	}
	missed := make([]string, 0)
	seen := make(map[string]struct{})
	allConfirmed := true
	anyAttributable := false
	allReviewedNotAttributable := true
	for _, f := range in.Failures {
		if _, wasSelected := selected[f.TestUnitKey]; wasSelected {
			continue
		}
		if f.TestUnitKey == "" {
			continue
		}
		if _, duplicate := seen[f.TestUnitKey]; duplicate {
			continue
		}
		seen[f.TestUnitKey] = struct{}{}
		missed = append(missed, f.TestUnitKey)
		if f.RelatedToChange {
			anyAttributable = true
		}
		if !f.ReviewedNotAttributable {
			allReviewedNotAttributable = false
		}
		if !f.Reproduced || f.Flaky || !f.RelatedToChange || f.ReviewedNotAttributable {
			allConfirmed = false
		}
	}
	sort.Strings(missed)
	result.MissedTestKeys = missed
	if len(missed) == 0 {
		result.Disposition = DispositionNoMiss
		result.Record = makeRecord(in, domain.AuditNoMissObserved, nil)
		return result
	}
	if !anyAttributable && allReviewedNotAttributable {
		result.Disposition = DispositionNotAttributable
		result.Record = makeRecord(in, domain.AuditNoMissObserved, nil)
		return result
	}
	if allConfirmed {
		result.Disposition = DispositionConfirmed
		result.Quarantine = true
		result.Record = makeRecord(in, domain.AuditConfirmedMiss, missed)
	} else {
		result.Disposition = DispositionPossible
		result.Quarantine = true
		result.Record = makeRecord(in, domain.AuditPossibleMiss, missed)
	}
	return result
}

func makeRecord(in Input, outcome domain.AuditOutcome, missed []string) domain.AuditRecord {
	sort.Strings(missed)
	return domain.AuditRecord{ID: uuid.New(), RepositoryID: in.RepositoryID, PlanID: in.PlanID, Outcome: outcome, SameSnapshot: in.SelectedFingerprint.SourceTreeHash != "" && in.SelectedFingerprint.SourceTreeHash == in.FullFingerprint.SourceTreeHash, EquivalentEnvironment: in.SelectedFingerprint.Equivalent(in.FullFingerprint), MissedUnitKeys: missed, CreatedAt: in.Now}
}
