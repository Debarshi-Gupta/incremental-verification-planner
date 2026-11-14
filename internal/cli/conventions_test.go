package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/analysis"
	"github.com/ivp/ivp/internal/domain"
)

func writeRepoFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// conventionFixture builds a repository that satisfies every convention
// producer at once: a proto schema with an importing consumer, a generated
// stringer with its input, a co-located test/source pair, two build-config
// files, and a CODEOWNERS rule covering Go sources.
func conventionFixture(t *testing.T) (root string, now time.Time, snapshotID uuid.UUID, tracked []string, nodes []domain.GraphNode, rules []analysis.CodeownersRule) {
	t.Helper()
	root = t.TempDir()
	writeRepoFile(t, root, "api/contract.proto", "syntax = \"proto3\";\n")
	writeRepoFile(t, root, "svc/client.go", "package svc\n\nimport \"api/contract.proto\"\n")
	writeRepoFile(t, root, "pkg/stringer.go", "package pkg\n")
	writeRepoFile(t, root, "pkg/stringer_string.go", "package pkg\n")
	writeRepoFile(t, root, "pkg/foo.go", "package pkg\n")
	writeRepoFile(t, root, "pkg/foo_test.go", "package pkg\n")
	writeRepoFile(t, root, "pkg/build.gradle", "apply plugin: 'java'\n")
	writeRepoFile(t, root, "pom.xml", "<project/>\n")
	tracked = []string{
		"api/contract.proto", "svc/client.go", "pkg/stringer.go", "pkg/stringer_string.go",
		"pkg/foo.go", "pkg/foo_test.go", "pkg/build.gradle", "pom.xml",
	}
	now = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	snapshotID = uuid.New()
	for _, rel := range tracked {
		nodes = append(nodes, domain.GraphNode{ID: uuid.New(), SnapshotID: snapshotID, NodeType: domain.NodeSourceFile,
			StableKey: "file:" + rel, Path: rel, EvidenceSource: "fixture", Confidence: 1, ObservedAt: now})
	}
	nodes = append(nodes, domain.GraphNode{ID: uuid.New(), SnapshotID: snapshotID, NodeType: domain.NodeTestCase,
		StableKey: "unit:gradle:compileTestJava", Path: "pkg/foo.go", EvidenceSource: "fixture", Confidence: 0.95, ObservedAt: now})
	rules = analysis.ParseCodeowners("*.go @go-team\n")
	return root, now, snapshotID, tracked, nodes, rules
}

func nodeByStableKey(nodes []domain.GraphNode, key string) (domain.GraphNode, bool) {
	for _, node := range nodes {
		if node.StableKey == key {
			return node, true
		}
	}
	return domain.GraphNode{}, false
}

func TestReadCodeownersUnionsEveryStandardLocation(t *testing.T) {
	root := t.TempDir()
	writeRepoFile(t, root, ".github/CODEOWNERS", "*.go @github-owner\n")
	writeRepoFile(t, root, "CODEOWNERS", "/api/ @root-owner\n")
	writeRepoFile(t, root, "docs/CODEOWNERS", "/docs/ @docs-owner\n")

	rules := readCodeowners(root)
	if len(rules) != 3 {
		t.Fatalf("readCodeowners parsed %d rules, want 3 (one per standard location): %+v", len(rules), rules)
	}
	if owners := analysis.CodeownerRulesFor(rules, "docs/guide.md"); len(owners) != 1 || owners[0] != "@docs-owner" {
		t.Fatalf("owners of docs/guide.md = %v, want [@docs-owner]", owners)
	}
	owners := analysis.CodeownerRulesFor(rules, "api/service.go")
	want := map[string]bool{"@github-owner": true, "@root-owner": true}
	if len(owners) != len(want) {
		t.Fatalf("owners of api/service.go = %v, want %v (union of matching rules)", owners, want)
	}
	for _, owner := range owners {
		if !want[owner] {
			t.Errorf("unexpected owner %q", owner)
		}
	}

	// A repository without any CODEOWNERS file contributes no ownership
	// evidence rather than guessing.
	if rules := readCodeowners(t.TempDir()); rules != nil {
		t.Fatalf("missing CODEOWNERS files must yield no rules, got %+v", rules)
	}
}

func TestUnitOwnerStableKeyUsesFirstOwnedSourcePath(t *testing.T) {
	rules := analysis.ParseCodeowners("*.go @go-team\n/docs/ @docs-team\n")
	cases := []struct {
		name        string
		sourcePaths []string
		want        string
	}{
		{name: "first owned path wins", sourcePaths: []string{"docs/guide.md", "pkg/a.go"}, want: analysis.StableOwnerKey("@docs-team")},
		{name: "skips unowned paths", sourcePaths: []string{"README.md", "pkg/a.go"}, want: analysis.StableOwnerKey("@go-team")},
		{name: "no owned path", sourcePaths: []string{"README.md"}, want: ""},
		{name: "no source paths", sourcePaths: nil, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := unitOwnerStableKey(rules, tc.sourcePaths); got != tc.want {
				t.Fatalf("unitOwnerStableKey() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildConventionEvidenceEmitsEveryConventionEdgeType(t *testing.T) {
	root, now, snapshotID, tracked, nodes, rules := conventionFixture(t)
	newNodes, edges := buildConventionEvidence(root, snapshotID, now, tracked, nodes, rules)

	byType := map[domain.GraphEdgeType][]domain.GraphEdge{}
	for _, edge := range edges {
		byType[edge.EdgeType] = append(byType[edge.EdgeType], edge)
	}
	wantCounts := map[domain.GraphEdgeType]int{
		domain.EdgeOwnedBy:          6, // five Go sources plus the unit node
		domain.EdgeBuiltFrom:        1,
		domain.EdgeConfigures:       1,
		domain.EdgeContractConsumer: 1,
		domain.EdgeColocatedWith:    1,
	}
	if len(edges) != 10 {
		t.Fatalf("buildConventionEvidence emitted %d edges, want 10: %+v", len(edges), edges)
	}
	for kind, want := range wantCounts {
		if len(byType[kind]) != want {
			t.Errorf("%s edges = %d, want %d", kind, len(byType[kind]), want)
		}
	}

	for _, edge := range edges {
		if edge.ID == uuid.Nil || edge.SnapshotID != snapshotID || !edge.ObservedAt.Equal(now) || edge.EvidenceSource == "" {
			t.Errorf("edge is missing identity, snapshot, provenance, or timestamp: %+v", edge)
		}
	}
	assertEdge := func(kind domain.GraphEdgeType, fromKey, toKey string, confidence float64, source, precision string) {
		t.Helper()
		from, okFrom := nodeByStableKey(append(nodes, newNodes...), fromKey)
		to, okTo := nodeByStableKey(append(nodes, newNodes...), toKey)
		if !okFrom || !okTo {
			t.Fatalf("endpoint missing for %s: from=%v to=%v", kind, okFrom, okTo)
		}
		for _, edge := range byType[kind] {
			if edge.FromNodeID != from.ID || edge.ToNodeID != to.ID {
				continue
			}
			if edge.Confidence != confidence || edge.EvidenceSource != source {
				t.Errorf("%s confidence/source = %v/%q, want %v/%q", kind, edge.Confidence, edge.EvidenceSource, confidence, source)
			}
			if got, _ := edge.Attributes["precision"].(string); got != precision {
				t.Errorf("%s precision = %q, want %q", kind, got, precision)
			}
			return
		}
		t.Fatalf("%s edge %s -> %s not found among %+v", kind, fromKey, toKey, byType[kind])
	}
	assertEdge(domain.EdgeBuiltFrom, "file:pkg/stringer_string.go", "file:pkg/stringer.go", 0.9, "conventions:generated-suffix", "GENERATED_SUFFIX")
	assertEdge(domain.EdgeConfigures, "unit:gradle:compileTestJava", analysis.StableConfigKey("pkg/build.gradle"), 0.85, "conventions:nearest-config", "NEAREST_ANCESTOR_CONFIG")
	assertEdge(domain.EdgeContractConsumer, "file:svc/client.go", analysis.StableSchemaKey("api/contract.proto"), 0.7, "conventions:schema-reference", "SCHEMA_REFERENCE")
	assertEdge(domain.EdgeColocatedWith, "file:pkg/foo_test.go", "file:pkg/foo.go", 0.5, "conventions:co-location", "CONVENTION_COLOCATION")
	assertEdge(domain.EdgeOwnedBy, "file:pkg/foo.go", analysis.StableOwnerKey("@go-team"), 0.9, "conventions:codeowners", "CODEOWNERS")
	assertEdge(domain.EdgeOwnedBy, "unit:gradle:compileTestJava", analysis.StableOwnerKey("@go-team"), 0.9, "conventions:codeowners", "CODEOWNERS")

	// Lazily created nodes carry the stable keys the planner matches on.
	wantNewNodes := map[string]domain.GraphNodeType{
		analysis.StableOwnerKey("@go-team"):            domain.NodeOwner,
		analysis.StableConfigKey("pkg/build.gradle"):   domain.NodeConfig,
		analysis.StableSchemaKey("api/contract.proto"): domain.NodeAPISchema,
	}
	if len(newNodes) != len(wantNewNodes) {
		t.Fatalf("created %d convention nodes, want %d: %+v", len(newNodes), len(wantNewNodes), newNodes)
	}
	for _, node := range newNodes {
		wantType, ok := wantNewNodes[node.StableKey]
		if !ok {
			t.Errorf("unexpected convention node %q", node.StableKey)
			continue
		}
		if node.NodeType != wantType || node.SnapshotID != snapshotID || !node.ObservedAt.Equal(now) || node.EvidenceSource == "" {
			t.Errorf("convention node %q is malformed: %+v", node.StableKey, node)
		}
	}

	// Duplicate tracked entries must not duplicate evidence.
	dedupTracked := append([]string(nil), tracked...)
	dedupTracked = append(dedupTracked, tracked...)
	_, dedupEdges := buildConventionEvidence(root, snapshotID, now, dedupTracked, nodes, rules)
	if len(dedupEdges) != len(edges) {
		t.Fatalf("duplicated tracked paths produced %d edges, want %d (deduplicated)", len(dedupEdges), len(edges))
	}
}

func TestBuildConventionEvidenceEmitsNothingWithoutInputs(t *testing.T) {
	newNodes, edges := buildConventionEvidence(t.TempDir(), uuid.New(), time.Now(), nil, nil, nil)
	if len(newNodes) != 0 || len(edges) != 0 {
		t.Fatalf("absent conventions must emit no evidence, got %d nodes and %d edges", len(newNodes), len(edges))
	}
}
