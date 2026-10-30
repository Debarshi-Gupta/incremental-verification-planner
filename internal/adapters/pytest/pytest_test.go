package pytest

import (
	"path/filepath"
	"testing"
)

func TestPytestCollectionFixtureProducesDeduplicatedNodeIDs(t *testing.T) {
	root := t.TempDir()
	fixture := []byte(`tests/test_api.py::test_reads_health
<Function tests/test_api.py::test_reads_health>
tests/unit/test_plan.py::TestPlanner::test_falls_back
collected 3 items
`)
	got := parseNodeIDs(root, fixture)
	if !got.Complete || len(got.Units) != 2 {
		t.Fatalf("pytest discovery complete=%v units=%+v reasons=%v", got.Complete, got.Units, got.Reasons)
	}
	if got.Units[0].Key != "tests/test_api.py::test_reads_health" || got.Units[1].Module != filepath.ToSlash(filepath.Join("tests", "unit")) {
		t.Fatalf("unexpected pytest units: %+v", got.Units)
	}
	if !got.Units[0].Filterable || got.Units[0].SelectableGranularity != "PYTEST_NODEID" {
		t.Fatalf("pytest node ID did not retain its filtering contract: %+v", got.Units[0])
	}
}

func TestPytestCollectionFixtureEscalatesOnOutsidePath(t *testing.T) {
	got := parseNodeIDs(t.TempDir(), []byte("../outside/test_secret.py::test_leak\n"))
	if got.Complete || len(got.Units) != 0 || len(got.Reasons) == 0 {
		t.Fatalf("pytest path escape was not marked incomplete: %+v", got)
	}
}
