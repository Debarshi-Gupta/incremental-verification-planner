//go:build performance

package performance

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/analysis"
	"github.com/ivp/ivp/internal/domain"
)

func BenchmarkAnalyzeImpact10kTests(b *testing.B) {
	const testCount = 10_000
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	changedID := uuid.New()
	nodes := make([]domain.GraphNode, 0, testCount+1)
	edges := make([]domain.GraphEdge, 0, testCount)
	nodes = append(nodes, domain.GraphNode{
		ID: changedID, NodeType: domain.NodeSourceFile, StableKey: "file:src/changed.go",
		Path: "src/changed.go", EvidenceSource: "synthetic-benchmark", Confidence: 1, ObservedAt: now,
	})
	for i := range testCount {
		testID := uuid.New()
		nodes = append(nodes, domain.GraphNode{
			ID: testID, NodeType: domain.NodeTestTarget, StableKey: fmt.Sprintf("test:%05d", i),
			EvidenceSource: "synthetic-benchmark", Confidence: 1, ObservedAt: now,
		})
		edges = append(edges, domain.GraphEdge{
			ID: uuid.New(), FromNodeID: testID, ToNodeID: changedID, EdgeType: domain.EdgeTestCovers,
			EvidenceSource: "synthetic-benchmark", Confidence: 1, ObservedAt: now,
		})
	}
	changes := []domain.ChangedFile{{Path: "src/changed.go", Status: domain.ChangeModified}}
	policy := domain.Policy{DependencyCap: 100}

	b.ResetTimer()
	for range b.N {
		result := analysis.AnalyzeImpact(changes, nodes, edges, policy, now, true, false, false, false)
		if len(result.ImpactedNodeIDs) != testCount+1 {
			b.Fatalf("impact traversal returned %d nodes, want %d", len(result.ImpactedNodeIDs), testCount+1)
		}
	}
}
