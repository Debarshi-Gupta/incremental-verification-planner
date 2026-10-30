package domain

import (
	"time"

	"github.com/google/uuid"
)

// UUID is the identifier type used by persisted IVP entities.
type UUID = uuid.UUID

type Repository struct {
	ID            UUID      `json:"id"`
	TenantID      UUID      `json:"tenant_id"`
	Slug          string    `json:"slug"`
	CanonicalURL  string    `json:"canonical_url"`
	DefaultBranch string    `json:"default_branch"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// RepositorySnapshot is immutable evidence for one repository tree/worktree.
type RepositorySnapshot struct {
	ID                  UUID      `json:"id"`
	RepositoryID        UUID      `json:"repository_id"`
	CommitSHA           string    `json:"commit_sha"`
	BaseSHA             string    `json:"base_sha"`
	TreeHash            string    `json:"tree_hash"`
	WorktreeHash        string    `json:"worktree_hash"`
	SubmoduleDigest     string    `json:"submodule_digest"`
	ToolchainDigest     string    `json:"toolchain_digest"`
	BuildConfigDigest   string    `json:"build_config_digest"`
	AdapterSetDigest    string    `json:"adapter_set_digest"`
	GraphVersion        int64     `json:"graph_version"`
	EvidenceGeneratedAt time.Time `json:"evidence_generated_at"`
	IsComplete          bool      `json:"is_complete"`
	CompletenessReasons []string  `json:"completeness_reasons,omitempty"`
}

type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type ChangedFile struct {
	Path               string       `json:"path"`
	Status             ChangeStatus `json:"status"`
	OldPath            string       `json:"old_path,omitempty"`
	OldHash            string       `json:"old_hash,omitempty"`
	NewHash            string       `json:"new_hash,omitempty"`
	DiffHunks          []LineRange  `json:"diff_hunks,omitempty"`
	IsBinary           bool         `json:"is_binary"`
	IsGenerated        bool         `json:"is_generated"`
	DetectedRiskLabels []string     `json:"detected_risk_labels,omitempty"`
}

type GraphNode struct {
	ID             UUID           `json:"id"`
	SnapshotID     UUID           `json:"snapshot_id"`
	NodeType       GraphNodeType  `json:"node_type"`
	StableKey      string         `json:"stable_key"`
	Path           string         `json:"path,omitempty"`
	DisplayName    string         `json:"display_name,omitempty"`
	Labels         []string       `json:"labels,omitempty"`
	EvidenceSource string         `json:"evidence_source"`
	Confidence     float64        `json:"confidence"`
	ObservedAt     time.Time      `json:"observed_at"`
	ValidUntil     *time.Time     `json:"valid_until,omitempty"`
	Attributes     map[string]any `json:"attributes,omitempty"`
}

type GraphEdge struct {
	ID             UUID           `json:"id"`
	SnapshotID     UUID           `json:"snapshot_id"`
	FromNodeID     UUID           `json:"from_node_id"`
	ToNodeID       UUID           `json:"to_node_id"`
	EdgeType       GraphEdgeType  `json:"edge_type"`
	Confidence     float64        `json:"confidence"`
	EvidenceSource string         `json:"evidence_source"`
	ObservedAt     time.Time      `json:"observed_at"`
	ValidUntil     *time.Time     `json:"valid_until,omitempty"`
	Attributes     map[string]any `json:"attributes,omitempty"`
	Conflict       bool           `json:"conflict,omitempty"`
}

type TestUnit struct {
	ID                   UUID           `json:"id"`
	SnapshotID           UUID           `json:"snapshot_id"`
	StableKey            string         `json:"stable_key"`
	Adapter              string         `json:"adapter"`
	UnitType             TestUnitType   `json:"unit_type"`
	ModuleKey            string         `json:"module_key,omitempty"`
	RiskLabels           []string       `json:"risk_labels,omitempty"`
	Mandatory            bool           `json:"mandatory"`
	EstimatedMillis      int64          `json:"estimated_millis"`
	DurationP50Millis    int64          `json:"duration_p50_millis"`
	DurationP95Millis    int64          `json:"duration_p95_millis"`
	DurationSampleCount  int            `json:"duration_sample_count"`
	FlakeRate            float64        `json:"flake_rate"`
	LastSeenAt           time.Time      `json:"last_seen_at,omitempty"`
	Selectable           bool           `json:"selectable"`
	SelectionGranularity string         `json:"selection_granularity"`
	TargetRef            string         `json:"target_ref"`
	ExecutionGroup       string         `json:"execution_group,omitempty"`
	SourcePaths          []string       `json:"source_paths,omitempty"`
	HistoricalFailures   int            `json:"historical_failures,omitempty"`
	Metadata             map[string]any `json:"metadata,omitempty"`
}

type CoverageObservation struct {
	TestUnitKey            string            `json:"test_unit_key"`
	CoveredStableKey       string            `json:"covered_stable_key"`
	CoveredPath            string            `json:"covered_path,omitempty"`
	LineStart              int               `json:"line_start,omitempty"`
	LineEnd                int               `json:"line_end,omitempty"`
	SourceType             string            `json:"source_type"`
	ReportHash             string            `json:"report_hash"`
	RunID                  UUID              `json:"run_id"`
	ObservedAt             time.Time         `json:"observed_at"`
	EnvironmentFingerprint string            `json:"environment_fingerprint"`
	BuildConfigDigest      string            `json:"build_config_digest"`
	TestConfigDigest       string            `json:"test_config_digest"`
	Precision              CoveragePrecision `json:"precision"`
	SuiteLevel             bool              `json:"suite_level"`
	TestSpecific           bool              `json:"test_specific"`
	EvidenceConfidence     float64           `json:"evidence_confidence"`
}

type EvidenceReference struct {
	Kind       string    `json:"kind"`
	StableKey  string    `json:"stable_key"`
	Digest     string    `json:"digest,omitempty"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
}

type PlanUnit struct {
	UnitID                  UUID                 `json:"unit_id"`
	StableKey               string               `json:"stable_key"`
	UnitType                TestUnitType         `json:"unit_type"`
	ReasonCodes             []SelectedReasonCode `json:"reason_codes"`
	CoverageContribution    float64              `json:"coverage_contribution"`
	RiskContribution        int                  `json:"risk_contribution"`
	EstimatedDurationMillis int64                `json:"estimated_duration_ms"`
	ExecutionGroup          string               `json:"execution_group,omitempty"`
	Mandatory               bool                 `json:"mandatory"`
	TargetRef               string               `json:"target_ref"`
	SelectionGranularity    string               `json:"selection_granularity"`
	FlakeRate               float64              `json:"flake_rate,omitempty"`
	Explanation             string               `json:"explanation"`
}

type OmittedUnit struct {
	StableKey   string               `json:"stable_key"`
	ReasonCodes []OmissionReasonCode `json:"reason_codes"`
	Explanation string               `json:"explanation"`
}

type EscalationReason struct {
	Code    EscalationReasonCode `json:"code"`
	Detail  string               `json:"detail"`
	Checked bool                 `json:"checked"`
}

type HardRuleCheck struct {
	Code      EscalationReasonCode `json:"code"`
	Triggered bool                 `json:"triggered"`
	Detail    string               `json:"detail"`
}

type RiskContribution struct {
	Feature string  `json:"feature"`
	Value   float64 `json:"value"`
	Points  int     `json:"points"`
	Reason  string  `json:"reason"`
}

type VerificationPlan struct {
	ID                      UUID                `json:"id"`
	RepositoryID            UUID                `json:"repository_id"`
	SnapshotID              UUID                `json:"snapshot_id"`
	BaseSHA                 string              `json:"base_sha"`
	HeadSHA                 string              `json:"head_sha"`
	WorktreeHash            string              `json:"worktree_hash"`
	Mode                    PlanMode            `json:"mode"`
	Status                  PlanStatus          `json:"status"`
	RiskScore               int                 `json:"risk_score"`
	RiskLevel               RiskLevel           `json:"risk_level"`
	RiskContributions       []RiskContribution  `json:"risk_contributions"`
	SelectionConfidence     float64             `json:"selection_confidence"`
	WeightedCoverage        float64             `json:"weighted_coverage"`
	EstimatedDurationMillis int64               `json:"estimated_duration_ms"`
	EstimatedComputeMillis  int64               `json:"estimated_compute_ms"`
	SelectedUnits           []PlanUnit          `json:"selected_units"`
	MandatoryUnits          []PlanUnit          `json:"mandatory_units"`
	OmittedUnits            []OmittedUnit       `json:"omitted_units"`
	EscalationReasons       []EscalationReason  `json:"escalation_reasons"`
	HardRuleChecks          []HardRuleCheck     `json:"hard_rule_checks"`
	EvidenceReferences      []EvidenceReference `json:"evidence_references"`
	FullSuiteFallbackRef    string              `json:"full_suite_fallback_ref,omitempty"`
	Explanation             string              `json:"explanation"`
	PolicyVersion           int64               `json:"policy_version"`
	AlgorithmVersion        string              `json:"algorithm_version"`
	AdapterCapabilities     map[string]string   `json:"adapter_capabilities,omitempty"`
	CreatedAt               time.Time           `json:"created_at"`
	ExpiresAt               time.Time           `json:"expires_at"`
}

type ExecutionOutcome string

type Execution struct {
	ID                     UUID             `json:"id"`
	PlanID                 UUID             `json:"plan_id"`
	Outcome                ExecutionOutcome `json:"outcome"`
	StartedAt              time.Time        `json:"started_at"`
	CompletedAt            time.Time        `json:"completed_at,omitempty"`
	ExitStatus             *int             `json:"exit_status,omitempty"`
	CancellationReason     string           `json:"cancellation_reason,omitempty"`
	AdapterVersion         string           `json:"adapter_version,omitempty"`
	ArgvTemplateIDs        []string         `json:"argv_template_ids,omitempty"`
	EnvironmentFingerprint string           `json:"environment_fingerprint"`
	EscalationPerformed    bool             `json:"escalation_performed"`
	OutputHashes           []string         `json:"output_hashes,omitempty"`
	SanitizedExcerptRefs   []string         `json:"sanitized_excerpt_refs,omitempty"`
}

type AuditOutcome string

type AuditRecord struct {
	ID                    UUID         `json:"id"`
	RepositoryID          UUID         `json:"repository_id"`
	PlanID                UUID         `json:"plan_id"`
	Outcome               AuditOutcome `json:"outcome"`
	SameSnapshot          bool         `json:"same_snapshot"`
	EquivalentEnvironment bool         `json:"equivalent_environment"`
	MissedUnitKeys        []string     `json:"missed_unit_keys,omitempty"`
	CreatedAt             time.Time    `json:"created_at"`
	AdjudicatedAt         *time.Time   `json:"adjudicated_at,omitempty"`
}
