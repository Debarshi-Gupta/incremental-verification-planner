package analysis

import (
	"time"

	"github.com/ivp/ivp/internal/domain"
)

// EvidenceSnapshot contains a single immutable graph/evidence version and the
// change set to analyze. Flags are explicit because absence of evidence must
// never be inferred as evidence of absence.
type EvidenceSnapshot struct {
	Snapshot                  domain.RepositorySnapshot
	ChangedFiles              []domain.ChangedFile
	Nodes                     []domain.GraphNode
	Edges                     []domain.GraphEdge
	Coverage                  []domain.CoverageObservation
	Units                     []domain.TestUnit
	History                   []domain.HistoryObservation
	GraphComplete             bool
	GraphMissingEdges         bool
	GraphHasConflicts         bool
	GraphHasCycles            bool
	DiscoveryReliable         bool
	CoverageExpected          bool
	DocumentationOnly         bool
	BroadRefactor             bool
	PolicyTrusted             bool
	ToolchainComparable       bool
	CapturedAt                time.Time
	AdapterCapabilityVersions map[string]string
}

type ImpactEntity struct {
	StableKey     string
	Path          string
	NodeType      domain.GraphNodeType
	Weight        float64
	Criticality   string
	Critical      bool
	Labels        []string
	ReverseFanout int
}

type ImpactResult struct {
	Entities        []ImpactEntity
	ImpactedNodeIDs map[domain.UUID]bool
	ReverseFanout   int
	GraphConfidence float64
	HasConflict     bool
	HasCycle        bool
	MissingEdges    bool
}
