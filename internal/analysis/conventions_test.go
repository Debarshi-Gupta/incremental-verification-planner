package analysis

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
)

func TestBuiltFromPairsPairsGeneratedOutputWithTrackedInput(t *testing.T) {
	tracked := []string{
		"api/v1/service.proto",
		"api/v1/service.pb.go",
		"pkg/stringer_string.go",
		"pkg/stringer.go",
		"web/app.min.js",
		"orphan/output.pb.cc", // no orphan/output.proto is tracked
	}
	pairs := BuiltFromPairs(tracked)
	want := map[[2]string]bool{
		{"api/v1/service.pb.go", "api/v1/service.proto"}: true,
		{"pkg/stringer_string.go", "pkg/stringer.go"}:    true,
	}
	if len(pairs) != len(want) {
		t.Fatalf("BuiltFromPairs = %v, want exactly %v", pairs, want)
	}
	for _, pair := range pairs {
		if !want[pair] {
			t.Errorf("unexpected pair %v", pair)
		}
	}
}

func TestCoLocationPairingsPairsTestsWithSourcesOnly(t *testing.T) {
	tracked := []string{
		"pkg/foo.go",
		"pkg/foo_test.go",
		"pkg/foo_test.go.bak", // not a test naming convention
		"pkg/bar_test.go",     // no pkg/bar.go exists
		"pkg/a_test.go",
		"pkg/a_test.go", // duplicate tracked entry must not duplicate a pair
		"svc/auth.py",
		"svc/test_auth.py", // pytest default test_*.py convention
	}
	pairs := CoLocationPairings(tracked)
	want := map[[2]string]bool{
		{"pkg/foo_test.go", "pkg/foo.go"}:   true,
		{"svc/test_auth.py", "svc/auth.py"}: true,
	}
	if len(pairs) != len(want) {
		t.Fatalf("CoLocationPairings = %v, want exactly %v", pairs, want)
	}
	for _, pair := range pairs {
		if !want[pair] {
			t.Errorf("unexpected pair %v", pair)
		}
	}
}

func TestIsTestPathRecognizesShippedConventions(t *testing.T) {
	for _, p := range []string{"pkg/foo_test.go", "tests/test_x.py", "src/app.spec.ts", "spec/models/user_spec.rb", "com/acme/FooTest.java"} {
		if !IsTestPath(p) {
			t.Errorf("IsTestPath(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"pkg/foo.go", "docs/testing.md", "src/app.ts"} {
		if IsTestPath(p) {
			t.Errorf("IsTestPath(%q) = true, want false", p)
		}
	}
}

func TestConfiguredByPairsUsesNearestAncestorThenRoot(t *testing.T) {
	targets := []string{"services/auth/tests/test_login.py", "tools/gen/out.txt"}
	configs := []string{"pyproject.toml", "services/auth/pyproject.toml", "docs/conf.py"}
	pairs := ConfiguredByPairs(targets, configs)
	want := map[[2]string]bool{
		{"services/auth/tests/test_login.py", "services/auth/pyproject.toml"}: true,
		// No config is an ancestor of tools/gen/out.txt except the repo root.
		{"tools/gen/out.txt", "pyproject.toml"}: true,
	}
	if len(pairs) != len(want) {
		t.Fatalf("ConfiguredByPairs = %v, want exactly %v", pairs, want)
	}
	for _, pair := range pairs {
		if !want[pair] {
			t.Errorf("unexpected pair %v", pair)
		}
	}
}

func TestParseCodeownersAndRulesFor(t *testing.T) {
	rules := ParseCodeowners(`# comment
* @platform-team
*.go @go-owners @platform-team
/services/ @service-owners
!*.generated.go @ignored
/docs/ docs-single-owner
no-owners-here
`)
	if len(rules) != 4 {
		t.Fatalf("ParseCodeowners kept %d rules, want 4 (negated and ownerless lines dropped): %+v", len(rules), rules)
	}
	owners := CodeownerRulesFor(rules, "pkg/util.go")
	if len(owners) != 2 || owners[0] != "@platform-team" || owners[1] != "@go-owners" {
		t.Fatalf("owners of pkg/util.go = %v, want [@platform-team @go-owners] in rule order (handles stay verbatim)", owners)
	}
	owners = CodeownerRulesFor(rules, "services/payments/handler.go")
	wantService := map[string]bool{"@platform-team": true, "@go-owners": true, "@service-owners": true}
	if len(owners) != len(wantService) {
		t.Fatalf("owners of services/payments/handler.go = %v, want %v", owners, wantService)
	}
	for _, owner := range owners {
		if !wantService[owner] {
			t.Errorf("unexpected owner %q", owner)
		}
	}
	if owners := CodeownerRulesFor(rules, "README.md"); len(owners) != 1 || owners[0] != "@platform-team" {
		t.Fatalf("owners of README.md = %v, want [@platform-team]", owners)
	}
	// A directory pattern (trailing slash) owns everything beneath it.
	if owners := CodeownerRulesFor(rules, "docs/guide.md"); len(owners) != 2 || owners[0] != "@platform-team" || owners[1] != "docs-single-owner" {
		t.Fatalf("owners of docs/guide.md = %v, want [@platform-team docs-single-owner]", owners)
	}
	if owners := CodeownerRulesFor(nil, "any/path.go"); owners != nil {
		t.Fatalf("no rules must yield no owners, got %v", owners)
	}
}

func TestSchemaConsumersFindsImportsAndReferencesBounded(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("api/contract.proto", "syntax = \"proto3\";\n")
	write("svc/client.go", "package svc\n// see api/contract.proto for messages\n")
	write("svc/importer.go", "package svc\nimport \"api/contract.proto\"\n")
	write("svc/unrelated.go", "package svc\n// nothing here\n")
	write("svc/client_test.go", "package svc\n// api/contract.proto\n")
	tracked := []string{"api/contract.proto", "svc/client.go", "svc/importer.go", "svc/unrelated.go", "svc/client_test.go"}

	consumers := SchemaConsumers(root, "api/contract.proto", tracked, 1<<20, 1<<20)
	want := map[string]bool{"svc/client.go": true, "svc/importer.go": true}
	if len(consumers) != len(want) {
		t.Fatalf("SchemaConsumers = %v, want %v", consumers, want)
	}
	for _, consumer := range consumers {
		if !want[consumer] {
			t.Errorf("unexpected consumer %q", consumer)
		}
	}

	// A zero-byte budget skips every file: missing content only removes
	// evidence, never fabricates it.
	if consumers := SchemaConsumers(root, "api/contract.proto", tracked, 1<<20, 0); len(consumers) != 0 {
		t.Fatalf("zero scan budget returned consumers %v", consumers)
	}
}

func TestMarkConflictingEdgesRetainsAndMarksEveryInvolvedEdge(t *testing.T) {
	snapshot := uuid.New()
	fileA, fileB, owner := uuid.New(), uuid.New(), uuid.New()
	edges := []domain.GraphEdge{
		{ID: uuid.New(), SnapshotID: snapshot, FromNodeID: fileA, ToNodeID: fileB, EdgeType: domain.EdgeTestCovers, EvidenceSource: "adapter-a", Attributes: map[string]any{"precision": "FILE"}},
		{ID: uuid.New(), SnapshotID: snapshot, FromNodeID: fileA, ToNodeID: fileB, EdgeType: domain.EdgeTestCovers, EvidenceSource: "adapter-b", Attributes: map[string]any{"precision": "MODULE"}},
		{ID: uuid.New(), SnapshotID: snapshot, FromNodeID: fileA, ToNodeID: owner, EdgeType: domain.EdgeOwnedBy, EvidenceSource: "codeowners"},
		{ID: uuid.New(), SnapshotID: snapshot, FromNodeID: owner, ToNodeID: fileA, EdgeType: domain.EdgeOwnedBy, EvidenceSource: "codeowners"},
		{ID: uuid.New(), SnapshotID: snapshot, FromNodeID: fileB, ToNodeID: owner, EdgeType: domain.EdgeOwnedBy, EvidenceSource: "codeowners"},
	}
	marked := MarkConflictingEdges(edges)
	if marked != 4 {
		t.Fatalf("MarkConflictingEdges marked %d edges, want 4 (duplicate pair + both opposite-direction edges)", marked)
	}
	if len(edges) != 5 {
		t.Fatalf("MarkConflictingEdges dropped edges: %d remain, want 5", len(edges))
	}
	if !edges[0].Conflict || !edges[1].Conflict {
		t.Errorf("duplicate edges with differing precision must both be marked: %+v", edges[:2])
	}
	if !edges[2].Conflict || !edges[3].Conflict {
		t.Errorf("opposite-direction pair must both be marked: %+v", edges[2:4])
	}
	if edges[4].Conflict {
		t.Errorf("unrelated edge must stay unmarked: %+v", edges[4])
	}
	// Re-running must not double-count already marked edges.
	if again := MarkConflictingEdges(edges); again != 0 {
		t.Fatalf("re-running marked %d additional edges, want 0", again)
	}
}
