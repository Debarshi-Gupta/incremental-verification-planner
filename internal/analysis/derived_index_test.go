package analysis

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
)

// LLD 13.3: the derived graph index (adjacency lists by edge type, connected
// target/module mappings, affected-target index) is derived once per snapshot
// and cached by snapshot hash and algorithm version.
func TestDerivedIndexCarriesAdjacencyAndAffectedTargets(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	sourceID, targetID, testID, contractID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	nodes := []domain.GraphNode{
		{ID: sourceID, NodeType: domain.NodeSourceFile, StableKey: "file:src/app.go", Path: "src/app.go", ObservedAt: now},
		{ID: targetID, NodeType: domain.NodeBuildTarget, StableKey: "build:app", Path: "app", ObservedAt: now},
		{ID: testID, NodeType: domain.NodeTestTarget, StableKey: "test:app", Path: "app/test", ObservedAt: now},
		{ID: contractID, NodeType: domain.NodeAPISchema, StableKey: "schema:api", Path: "api/openapi.yaml", ObservedAt: now},
	}
	edges := []domain.GraphEdge{
		{FromNodeID: targetID, ToNodeID: sourceID, EdgeType: domain.EdgeDependsOn, Confidence: 1},
		{FromNodeID: testID, ToNodeID: targetID, EdgeType: domain.EdgeDependsOn, Confidence: 1},
		{FromNodeID: testID, ToNodeID: sourceID, EdgeType: domain.EdgeTestCovers, Confidence: 1},
		{FromNodeID: targetID, ToNodeID: contractID, EdgeType: domain.EdgeContractConsumer, Confidence: 1},
	}
	index := BuildDerivedIndex("snapshot-a", nodes, edges)

	if got := index.ReverseDeps[sourceID]; len(got) != 1 || got[0] != targetID {
		t.Fatalf("reverse dependency adjacency = %+v, want [%s]", got, targetID)
	}
	if got := index.ReverseDeps[targetID]; len(got) != 1 || got[0] != testID {
		t.Fatalf("reverse dependency adjacency for the target = %+v, want [%s]", got, testID)
	}
	if got := index.DependencyAdj[testID]; len(got) != 1 || got[0] != targetID {
		t.Fatalf("dependency adjacency = %+v, want [%s]", got, targetID)
	}
	if got := index.ReverseCoverage[sourceID]; len(got) != 1 || got[0] != testID {
		t.Fatalf("coverage adjacency = %+v, want [%s]", got, testID)
	}
	if got := index.ContractConsumers[contractID]; len(got) != 1 || got[0] != targetID {
		t.Fatalf("contract consumers = %+v, want [%s]", got, contractID)
	}
	// Affected-target index: the source path maps to the targets that depend
	// on it.
	if got := index.AffectedTargets["src/app.go"]; len(got) != 2 || got[0] != "build:app" || got[1] != "test:app" {
		t.Fatalf("affected-target index for src/app.go = %+v, want [build:app test:app]", got)
	}
	// Connected target/module mappings connect both directions.
	if got := index.ConnectedTargets["build:app"]; len(got) != 2 {
		t.Fatalf("connected targets for build:app = %+v, want two neighbours", got)
	}
	if got := index.ConnectedTargets["test:app"]; len(got) != 1 || got[0] != "build:app" {
		t.Fatalf("connected targets for test:app = %+v, want [build:app]", got)
	}
}

// LLD 13.3: the derived index is cached by snapshot hash and algorithm
// version, and a reused index is only reused when it still describes the
// graph it is asked about.
func TestDerivedIndexIsCachedBySnapshotHashAndAlgorithmVersion(t *testing.T) {
	nodes := []domain.GraphNode{{ID: uuid.New(), NodeType: domain.NodeSourceFile, StableKey: "file:a.go", Path: "a.go"}}
	edges := []domain.GraphEdge{}
	before := DerivedIndexCacheEntries()

	first := DerivedIndexFor("snapshot-cache", nodes, edges)
	second := DerivedIndexFor("snapshot-cache", nodes, edges)
	if first != second {
		t.Fatal("the derived index for the same snapshot hash was not reused")
	}
	if after := DerivedIndexCacheEntries(); after != before+1 {
		t.Fatalf("cache entries = %d, want %d (one new snapshot)", after, before+1)
	}
	other := DerivedIndexFor("snapshot-other", nodes, edges)
	if other == first {
		t.Fatal("a different snapshot hash must not reuse the cached index")
	}
	if got := DerivedIndexKey("snapshot-cache"); got == DerivedIndexKey("snapshot-other") {
		t.Fatal("snapshot hashes must produce different cache keys")
	}
	// A graph that does not match the cached entry is never served from it,
	// even when the node and edge counts are identical.
	otherNodes := append([]domain.GraphNode(nil), nodes...)
	otherNodes[0] = domain.GraphNode{ID: uuid.New(), NodeType: domain.NodeSourceFile, StableKey: "file:b.go", Path: "b.go"}
	mismatched := DerivedIndexFor("snapshot-cache", otherNodes, edges)
	if mismatched == first {
		t.Fatal("a cached index must not be served for a different graph")
	}
}

// LLD 13.3: when a traversal bound is reached the graph is marked incomplete
// and the plan escalates; the traversal is never silently truncated.
func TestTraversalBoundMarksGraphIncompleteAndEscalates(t *testing.T) {
	// dependencyChain adds a build target between the changed source file and
	// the test target, so the traversal has a chain to bound.
	dependencyChain := func(t *testing.T) (domain.PlanRequest, EvidenceSnapshot, domain.Policy) {
		t.Helper()
		req, evidence, policy := readyFixture(t)
		var sourceID, testID uuid.UUID
		for _, node := range evidence.Nodes {
			switch node.StableKey {
			case "file:src/main.go":
				sourceID = node.ID
			case "test:unit":
				testID = node.ID
			}
		}
		if sourceID == uuid.Nil || testID == uuid.Nil {
			t.Fatalf("fixture nodes are missing: %+v", evidence.Nodes)
		}
		buildID := uuid.New()
		evidence.Nodes = append(evidence.Nodes, domain.GraphNode{ID: buildID, SnapshotID: evidence.Snapshot.ID,
			NodeType: domain.NodeBuildTarget, StableKey: "build:app", EvidenceSource: "fixture", Confidence: 1,
			ObservedAt: evidence.Snapshot.EvidenceGeneratedAt})
		evidence.Edges = append(evidence.Edges,
			domain.GraphEdge{ID: uuid.New(), SnapshotID: evidence.Snapshot.ID, FromNodeID: buildID, ToNodeID: sourceID,
				EdgeType: domain.EdgeDependsOn, Confidence: 1, EvidenceSource: "fixture", ObservedAt: evidence.Snapshot.EvidenceGeneratedAt},
			domain.GraphEdge{ID: uuid.New(), SnapshotID: evidence.Snapshot.ID, FromNodeID: testID, ToNodeID: buildID,
				EdgeType: domain.EdgeDependsOn, Confidence: 1, EvidenceSource: "fixture", ObservedAt: evidence.Snapshot.EvidenceGeneratedAt})
		return req, evidence, policy
	}

	t.Run("depth budget reached escalates", func(t *testing.T) {
		req, evidence, policy := dependencyChain(t)
		policy.TraversalLimits = domain.TraversalLimits{MaxDepth: 1}
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanFullRequired || !hasReason(plan, domain.EscGraphIncomplete) {
			t.Fatalf("a reached traversal bound must escalate, got %s with %+v", plan.Status, plan.EscalationReasons)
		}
		if !detailContains(plan, domain.EscGraphIncomplete, "depth budget 1") {
			t.Fatalf("the escalation must name the bound: %+v", plan.EscalationReasons)
		}
	})

	t.Run("node budget reached escalates", func(t *testing.T) {
		req, evidence, policy := dependencyChain(t)
		policy.TraversalLimits = domain.TraversalLimits{MaxNodes: 1}
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanFullRequired || !hasReason(plan, domain.EscGraphIncomplete) {
			t.Fatalf("a reached traversal bound must escalate, got %s with %+v", plan.Status, plan.EscalationReasons)
		}
		if !detailContains(plan, domain.EscGraphIncomplete, "node budget 1") {
			t.Fatalf("the escalation must name the bound: %+v", plan.EscalationReasons)
		}
	})

	t.Run("unset bounds keep the plan unchanged", func(t *testing.T) {
		req, evidence, policy := dependencyChain(t)
		policy.TraversalLimits = domain.TraversalLimits{}
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanReady || hasReason(plan, domain.EscGraphIncomplete) {
			t.Fatalf("unbounded traversal must not escalate, got %s with %+v", plan.Status, plan.EscalationReasons)
		}
	})
}
