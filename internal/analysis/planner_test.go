package analysis

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
)

func TestCreatePlanBuildsExplainableTargetedPass(t *testing.T) {
	req, evidence, policy := readyFixture(t)
	plan, err := CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanReady {
		t.Fatalf("expected READY, got %s with reasons %+v", plan.Status, plan.EscalationReasons)
	}
	if len(plan.SelectedUnits) != 1 || plan.SelectedUnits[0].StableKey != "test:unit" {
		t.Fatalf("unexpected selected units: %+v", plan.SelectedUnits)
	}
	if plan.WeightedCoverage < 0.95 || plan.SelectionConfidence < 0.98 {
		t.Fatalf("plan missed policy gates: coverage=%v confidence=%v", plan.WeightedCoverage, plan.SelectionConfidence)
	}
	if !strings.Contains(plan.Explanation, "TARGETED_PASS") || !strings.Contains(plan.Explanation, "nothing about unselected tests") {
		t.Fatalf("explanation does not distinguish selective from full-suite outcomes: %q", plan.Explanation)
	}
	if plan.AlgorithmVersion != AlgorithmVersion || len(plan.EvidenceReferences) < 2 {
		t.Fatalf("missing planner version or evidence references: %+v", plan)
	}
	for _, check := range plan.HardRuleChecks {
		if check.Triggered {
			t.Errorf("READY plan contains triggered hard rule %s", check.Code)
		}
	}
}

func TestCreatePlanEscalatesForStaleFingerprintAndUnavailableFallback(t *testing.T) {
	req, evidence, policy := readyFixture(t)
	req.CurrentWorktreeHash = "changed-after-snapshot"
	policy.FullSuiteTargetRef = ""
	plan, err := CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanFullRequired {
		t.Fatalf("unsafe evidence must require full verification, got %s", plan.Status)
	}
	if !hasReason(plan, domain.EscPlanStale) || !hasReason(plan, domain.EscPolicyUnavailable) {
		t.Fatalf("expected stale and unavailable-policy escalation, got %+v", plan.EscalationReasons)
	}
	if len(plan.SelectedUnits) == 0 || plan.SelectedUnits[0].StableKey != "FULL_SUITE" {
		t.Fatalf("full-suite fallback target missing: %+v", plan.SelectedUnits)
	}
	if plan.SelectedUnits[0].TargetRef != "" {
		t.Fatalf("planner invented a missing local command target: %+v", plan.SelectedUnits[0])
	}
}

func TestCreatePlanEscalatesWhenHardPathChangesOrGraphIsDangling(t *testing.T) {
	t.Run("hard path", func(t *testing.T) {
		req, evidence, policy := readyFixture(t)
		evidence.ChangedFiles = []domain.ChangedFile{{Path: "pom.xml", Status: domain.ChangeModified}}
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanFullRequired || !hasReason(plan, domain.EscConfigOrToolchainChange) {
			t.Fatalf("hard path did not force full verification: status=%s reasons=%+v", plan.Status, plan.EscalationReasons)
		}
	})

	t.Run("dangling edge", func(t *testing.T) {
		req, evidence, policy := readyFixture(t)
		evidence.Edges = append(evidence.Edges, domain.GraphEdge{ID: uuid.New(), SnapshotID: evidence.Snapshot.ID,
			FromNodeID: uuid.New(), ToNodeID: evidence.Nodes[0].ID, EdgeType: domain.EdgeDependsOn, Confidence: 1, EvidenceSource: "fixture"})
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanFullRequired || !hasReason(plan, domain.EscGraphIncomplete) {
			t.Fatalf("dangling graph edge did not force full verification: status=%s reasons=%+v", plan.Status, plan.EscalationReasons)
		}
	})
}

func TestCreatePlanOrderingAndReasonsAreDeterministic(t *testing.T) {
	req, evidence, policy := readyFixture(t)
	first, err := CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != second.Status || first.RiskScore != second.RiskScore || first.RiskContributions[0] != second.RiskContributions[0] ||
		!reflect.DeepEqual(first.SelectedUnits, second.SelectedUnits) || !reflect.DeepEqual(first.HardRuleChecks, second.HardRuleChecks) ||
		!reflect.DeepEqual(first.EscalationReasons, second.EscalationReasons) {
		t.Fatalf("same normalized evidence produced different plans:\nfirst=%+v\nsecond=%+v", first, second)
	}
}

func TestGreedySetCoverUsesStableTieBreak(t *testing.T) {
	policy := domain.Policy{MinimumCostFloorMillis: 1}
	a := &candidate{unit: domain.TestUnit{StableKey: "a"}, duration: 10, coverage: map[string]float64{"file:x": 1}}
	b := &candidate{unit: domain.TestUnit{StableKey: "b"}, duration: 10, coverage: map[string]float64{"file:x": 1}}
	entities := []ImpactEntity{{StableKey: "file:x", Weight: 1}}
	selected, coverage, _, impossible := greedySetCover([]*candidate{b, a}, entities, 0.95, policy)
	if impossible || coverage != 1 || len(selected) != 1 || selected[0].unit.StableKey != "a" {
		t.Fatalf("expected stable-key tie break to select a, got %+v coverage=%v impossible=%v", selected, coverage, impossible)
	}
}

func readyFixture(t *testing.T) (domain.PlanRequest, EvidenceSnapshot, domain.Policy) {
	t.Helper()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	repositoryID, snapshotID, unitID := uuid.New(), uuid.New(), uuid.New()
	sourceID, testID := uuid.New(), uuid.New()
	unit := domain.TestUnit{ID: unitID, SnapshotID: snapshotID, StableKey: "test:unit", Adapter: "generic", UnitType: domain.UnitTestCase,
		EstimatedMillis: 1000, DurationP95Millis: 1000, DurationSampleCount: 5, Selectable: true,
		SelectionGranularity: "DECLARED_UNIT", TargetRef: "test:unit", SourcePaths: []string{"src/main.go"},
		Metadata: map[string]any{"test_config_digest": "test-config"}}
	source := domain.GraphNode{ID: sourceID, SnapshotID: snapshotID, NodeType: domain.NodeSourceFile, StableKey: "file:src/main.go", Path: "src/main.go",
		EvidenceSource: "fixture", Confidence: 1, ObservedAt: now}
	testNode := domain.GraphNode{ID: testID, SnapshotID: snapshotID, NodeType: domain.NodeTestTarget, StableKey: "test:unit", EvidenceSource: "fixture", Confidence: 1, ObservedAt: now}
	snapshot := domain.RepositorySnapshot{ID: snapshotID, RepositoryID: repositoryID, CommitSHA: "head", BaseSHA: "base", TreeHash: "tree",
		WorktreeHash: "worktree", BuildConfigDigest: "build-config", AdapterSetDigest: "adapter-set", GraphVersion: 1,
		EvidenceGeneratedAt: now, IsComplete: true}
	observation := domain.CoverageObservation{TestUnitKey: unit.StableKey, CoveredStableKey: source.StableKey, CoveredPath: source.Path,
		SourceType: "fixture", ReportHash: "report-digest", RunID: uuid.New(), ObservedAt: now, EnvironmentFingerprint: "env",
		BuildConfigDigest: "build-config", TestConfigDigest: "test-config", Precision: domain.CoverageLine, LineStart: 1, LineEnd: 1, TestSpecific: true,
		EvidenceConfidence: 1}
	evidence := EvidenceSnapshot{Snapshot: snapshot, ChangedFiles: []domain.ChangedFile{{Path: "src/main.go", Status: domain.ChangeModified}},
		Nodes: []domain.GraphNode{source, testNode}, Edges: []domain.GraphEdge{{ID: uuid.New(), SnapshotID: snapshotID, FromNodeID: testID, ToNodeID: sourceID,
			EdgeType: domain.EdgeTestCovers, Confidence: 1, EvidenceSource: "fixture", ObservedAt: now}},
		Coverage: []domain.CoverageObservation{observation}, Units: []domain.TestUnit{unit},
		History:       []domain.HistoryObservation{{TestUnitKey: unit.StableKey, RelatedStableKey: "src/main.go", Comparable: true, Outcome: "PASS", SampleCount: 5, ObservedAt: now}},
		GraphComplete: true, DiscoveryReliable: true, PolicyTrusted: true, ToolchainComparable: true, AdapterCapabilityVersions: map[string]string{"generic": "v1"}}
	policy := domain.Policy{Version: 7, LowRiskMaxScore: 24, MediumRiskMaxScore: 59, LowRiskMinWeightedCoverage: 0.95,
		MediumRiskMinWeightedCoverage: 0.99, MinimumSelectionConfidence: 0.98, MaxSelectiveRuntimeFraction: 0.70,
		MaxChangedFilesForSelectivePlan: 500, GraphStaleness: 24 * time.Hour, CoverageStaleness: 30 * 24 * time.Hour,
		DependencyFanoutEscalation: 250, DependencyCap: 100, MinimumCostFloorMillis: 1000, StartupOverheadMillis: 0,
		HardFullSuitePaths: []string{"**/pom.xml"}, HardFullSuiteRiskLabels: []string{"SECURITY_CRITICAL", "PUBLIC_CONTRACT"},
		FullSuiteTargetRef: "full-suite", PolicyAvailable: true, HighRiskSelectiveFeedbackAllowed: true}
	req := domain.PlanRequest{RepositoryID: repositoryID, SnapshotID: snapshotID, BaseSHA: "base", HeadSHA: "head", CurrentWorktreeHash: "worktree",
		Mode: domain.ModeFastFeedback, Now: now, FullSuiteEstimateMillis: 100_000, AdapterID: "generic", AdapterSupported: true,
		AdapterDiscoveryOK: true, EnvironmentFingerprint: "env", PolicyVersion: policy.Version, PlanTTL: 10 * time.Minute, PolicySnapshotTrusted: true}
	return req, evidence, policy
}

func hasReason(plan domain.VerificationPlan, code domain.EscalationReasonCode) bool {
	for _, reason := range plan.EscalationReasons {
		if reason.Code == code {
			return true
		}
	}
	return false
}
