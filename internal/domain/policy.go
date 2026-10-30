package domain

import "time"

// Policy is the effective, versioned selection policy after organization and
// repository precedence has been applied. Security gates are resolved before
// a Policy reaches the planner.
type Policy struct {
	Version                          int64
	LowRiskMaxScore                  int
	MediumRiskMaxScore               int
	LowRiskMinWeightedCoverage       float64
	MediumRiskMinWeightedCoverage    float64
	MinimumSelectionConfidence       float64
	MaxSelectiveRuntimeFraction      float64
	MaxChangedFilesForSelectivePlan  int
	GraphStaleness                   time.Duration
	CoverageStaleness                time.Duration
	DependencyFanoutEscalation       int
	DependencyCap                    int
	MinimumCostFloorMillis           int64
	StartupOverheadMillis            int64
	HardFullSuitePaths               []string
	HardFullSuiteRiskLabels          []string
	AlwaysRunCheckIDs                []string
	CriticalPathPatterns             []string
	DocumentationPathPatterns        []string
	PathMappings                     []PathMapping
	RiskBasedCIEnabled               bool
	RiskBasedCIApproved              bool
	MinimumCalibrationRuns           int
	MinimumAuditRuns                 int
	ObservedCalibrationRuns          int
	ObservedAuditRuns                int
	Quarantined                      bool
	QuarantineScope                  string
	FullSuiteTargetRef               string
	PolicyAvailable                  bool
	HighRiskSelectiveFeedbackAllowed bool
}

type PathMapping struct {
	Pattern  string   `json:"pattern"`
	UnitKeys []string `json:"unit_keys"`
	Reason   string   `json:"reason,omitempty"`
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
