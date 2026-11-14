package domain

import "time"

// Policy is the effective, versioned selection policy after organization and
// repository precedence has been applied. Security gates are resolved before
// a Policy reaches the planner.
type Policy struct {
	Version                         int64
	LowRiskMaxScore                 int
	MediumRiskMaxScore              int
	LowRiskMinWeightedCoverage      float64
	MediumRiskMinWeightedCoverage   float64
	MinimumSelectionConfidence      float64
	MaxSelectiveRuntimeFraction     float64
	MaxChangedFilesForSelectivePlan int
	GraphStaleness                  time.Duration
	CoverageStaleness               time.Duration
	HistoryWindow                   time.Duration
	// FlakeMinimumComparableObservations is the analysis
	// flake_minimum_comparable_observations gate: flake classification and
	// flake-rate claims require at least this many comparable observations
	// (LLD 7.3), mirroring how HistoryWindow carries analysis.history_window_days.
	FlakeMinimumComparableObservations int
	DependencyFanoutEscalation         int
	DependencyCap                      int
	MinimumCostFloorMillis             int64
	StartupOverheadMillis              int64
	HardFullSuitePaths                 []string
	HardFullSuiteRiskLabels            []string
	AlwaysRunCheckIDs                  []string
	CriticalPathPatterns               []string
	DocumentationPathPatterns          []string
	PathMappings                       []PathMapping
	ReviewedExclusions                 []ReviewedExclusion
	HardPathExceptions                 []HardPathException
	RiskBasedCIEnabled                 bool
	RiskBasedCIApproved                bool
	MinimumCalibrationRuns             int
	MinimumAuditRuns                   int
	ObservedCalibrationRuns            int
	ObservedAuditRuns                  int
	Quarantined                        bool
	QuarantineScope                    string
	FullSuiteTargetRef                 string
	PolicyAvailable                    bool
	HighRiskSelectiveFeedbackAllowed   bool
	// CISelectiveEligible reports whether CI selective gating (RISK_BASED_CI)
	// may currently be used for the repository. A major algorithm or policy
	// change resets the eligibility to OBSERVE until the repository is
	// re-calibrated (LLD 18.4).
	CISelectiveEligible bool
	// CISelectiveResetReason explains why CI selective eligibility is not
	// available. It is empty while the repository stays eligible.
	CISelectiveResetReason string
	// TraversalLimits bound the dependency traversal performed for a plan
	// (LLD 13.3). A zero bound means "unbounded"; when a bound is reached the
	// graph is marked incomplete and the plan escalates instead of silently
	// truncating the traversal.
	TraversalLimits TraversalLimits
	// AuditSchedule carries the scheduled-audit cadence and the audit
	// acknowledgement SLA (LLD 10.2, 15.3).
	AuditSchedule AuditSchedule
}

// AuditSchedule carries the intervals the scheduled-audit orchestration uses:
// the maximum age of a repository's last full verification before a scheduled
// audit is due, and the maximum age of an unacknowledged audit miss before
// the response SLA is considered breached.
type AuditSchedule struct {
	Cadence            time.Duration
	AcknowledgementSLA time.Duration
}

// TraversalLimits are the policy-driven bounds for graph traversal: the
// maximum number of nodes one traversal may visit and the maximum dependency
// depth it may follow (LLD 13.3).
type TraversalLimits struct {
	MaxNodes int
	MaxDepth int
}

type PathMapping struct {
	Pattern  string   `json:"pattern"`
	UnitKeys []string `json:"unit_keys"`
	Reason   string   `json:"reason,omitempty"`
}

// ReviewedExclusion is a reviewed, org/repository-approved decision to keep a
// unit out of selective selection. Every field is mandatory in configuration;
// the planner still treats a missing unit key as "no exclusion" (ADR 0013).
type ReviewedExclusion struct {
	UnitKey    string `json:"unit_key"`
	Reviewer   string `json:"reviewer"`
	ReviewedAt string `json:"reviewed_at"`
	Rationale  string `json:"rationale"`
}

// HardPathException is a reviewed, organization-approved narrow exception to
// one hard full-suite rule (LLD 6.3 Stage 3 rule 3's "narrower, safe
// exception"; ADR 0019). The exception names a changed-file glob and the rule
// it covers; the planner skips only that matched escalation and records an
// audit check. Labels are never exceptable. Every field is mandatory in
// configuration; the planner still treats a missing path or rule as "no
// exception" (ADR 0019 §5 fails closed).
type HardPathException struct {
	Path     string `json:"path"`
	Rule     string `json:"rule"`
	Reviewer string `json:"reviewer"`
	// ReviewedAt is a calendar date in UTC (YYYY-MM-DD), validated at
	// configuration time; the planner only formats it into the audit detail.
	ReviewedAt string `json:"reviewed_at"`
	Rationale  string `json:"rationale"`
}

type HistoryObservation struct {
	TestUnitKey      string
	RelatedStableKey string
	Comparable       bool
	Outcome          string
	FailureCount     int
	SampleCount      int
	ObservedAt       time.Time
	Environment      string
}

type PlanRequest struct {
	RepositoryID            UUID
	SnapshotID              UUID
	BaseSHA                 string
	HeadSHA                 string
	CurrentWorktreeHash     string
	Mode                    PlanMode
	Now                     time.Time
	FullSuiteEstimateMillis int64
	AdapterID               string
	AdapterSupported        bool
	AdapterDiscoveryOK      bool
	AdapterCapabilities     map[string]string
	EnvironmentFingerprint  string
	PolicyVersion           int64
	PlanTTL                 time.Duration
	PolicySnapshotTrusted   bool
}
