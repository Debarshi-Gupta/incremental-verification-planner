package cli

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/domain"
)

// LLD 8.6: per-test coverage contexts attribute coverage to the test that
// produced it. A context is attributed only when it names a discovered test
// unit and the covered file is part of the indexed source inventory.
func TestPerTestCoverageObservationsAreAttributedToTheirTest(t *testing.T) {
	snapshotID := uuid.New()
	record := indexRecord{
		Snapshot: domain.RepositorySnapshot{ID: snapshotID, BuildConfigDigest: "build-config", TestConfigDigest: "test-config"},
		Units: []domain.TestUnit{
			{ID: uuid.New(), SnapshotID: snapshotID, StableKey: "tests/test_api.py::test_add", Adapter: "pytest", UnitType: domain.UnitTestCase},
			{ID: uuid.New(), SnapshotID: snapshotID, StableKey: "tests/test_api.py::test_sub", Adapter: "pytest", UnitType: domain.UnitTestCase},
		},
		Nodes: []domain.GraphNode{
			{ID: uuid.New(), SnapshotID: snapshotID, NodeType: domain.NodeSourceFile, StableKey: "file:pkg/api.py", Path: "pkg/api.py"},
			{ID: uuid.New(), SnapshotID: snapshotID, NodeType: domain.NodeSourceFile, StableKey: "file:pkg/other.py", Path: "pkg/other.py"},
		},
	}
	sourceNodes := map[string]domain.GraphNode{}
	for _, node := range record.Nodes {
		sourceNodes[node.Path] = node
	}
	local := &localRun{ExecutionID: uuid.New(), EnvironmentDigest: "env"}
	parsed := adapters.CoverageResult{Format: "coverage.py-json", Complete: true, Contexts: map[string]adapters.ContextCoverage{
		"tests/test_api.py::test_add|run":   {Files: map[string][]int{"pkg/api.py": {1, 2}}},
		"tests/test_api.py::test_sub|run":   {Files: map[string][]int{"pkg/api.py": {2}}},
		"tests/test_api.py::test_gone|run":  {Files: map[string][]int{"pkg/api.py": {3}}},
		"tests/test_api.py::test_other|run": {Files: map[string][]int{"pkg/other.py": {1}}},
	}}

	observations, warnings := perTestCoverageObservations(parsed, record, sourceNodes, "pytest", "report-hash", local)

	// The phase suffix is stripped, so both known tests are attributed.
	byUnit := map[string][]domain.CoverageObservation{}
	for _, observation := range observations {
		byUnit[observation.TestUnitKey] = append(byUnit[observation.TestUnitKey], observation)
	}
	add := byUnit["tests/test_api.py::test_add"]
	if len(add) != 2 {
		t.Fatalf("test_add got %d observations, want 2: %+v", len(add), observations)
	}
	for _, observation := range add {
		if observation.Precision != domain.CoverageLine || observation.LineStart != observation.LineEnd {
			t.Fatalf("per-test observation is not line-level: %+v", observation)
		}
		if observation.SuiteLevel || !observation.TestSpecific {
			t.Fatalf("per-test observation must be test-specific: %+v", observation)
		}
		if observation.CoveredStableKey != "file:pkg/api.py" {
			t.Fatalf("per-test observation points at the wrong source: %+v", observation)
		}
	}
	sub := byUnit["tests/test_api.py::test_sub"]
	if len(sub) != 1 || sub[0].LineStart != 2 {
		t.Fatalf("test_sub observations = %+v, want line 2 only", sub)
	}
	// A context that matches no discovered unit is ignored, not attributed.
	if _, ok := byUnit["tests/test_api.py::test_gone|run"]; ok {
		t.Fatalf("an unmatched context was attributed: %+v", observations)
	}
	if !strings.Contains(strings.Join(warnings, "; "), "did not match a discovered test unit") {
		t.Fatalf("the unmatched context must be reported: %+v", warnings)
	}
	// Every observation belongs to a discovered unit.
	for _, observation := range observations {
		switch observation.TestUnitKey {
		case "tests/test_api.py::test_add", "tests/test_api.py::test_sub":
		default:
			t.Fatalf("observation attributed to an unknown unit: %+v", observation)
		}
	}
}

// A report without contexts keeps the suite-level behaviour: no per-test
// observation is produced at all.
func TestPerTestCoverageObservationsAreAbsentWithoutContexts(t *testing.T) {
	record := indexRecord{Units: []domain.TestUnit{{StableKey: "tests/test_api.py::test_add"}}}
	parsed := adapters.CoverageResult{Format: "coverage.py-json", Complete: true}
	observations, warnings := perTestCoverageObservations(parsed, record, nil, "pytest", "report-hash", &localRun{})
	if len(observations) != 0 || len(warnings) != 0 {
		t.Fatalf("a report without contexts produced per-test evidence: %+v %+v", observations, warnings)
	}
}
