package pytest

import (
	"context"
	"reflect"
	"testing"

	"github.com/ivp/ivp/internal/adapters"
)

// pytestFixtureRoot is the §17.3 fixture repository exercised by this test.
// The pytest binary is unavailable in the fixture environment, so the
// collect-only probe is answered through the adapter's injectable capture
// seam with the output of the equivalent real `pytest --collect-only -q`.
const pytestFixtureRoot = "../../../examples/sample-pytest"

// pytestCollectOutput mirrors a real `pytest --collect-only -q` run over the
// fixture: three unique node IDs, one repeated (parametrization/double
// collection) to exercise the parser's deduplication, plus summary lines that
// the node-ID pattern must ignore.
const pytestCollectOutput = `tests/test_calc.py::test_add
tests/test_calc.py::test_total
tests/test_report.py::test_render
tests/test_calc.py::test_add

4 tests collected
`

func pytestFixtureCapture(t *testing.T) func(context.Context, string, string, []string, int64) ([]byte, []byte, error) {
	t.Helper()
	return func(_ context.Context, _ string, executable string, args []string, _ int64) ([]byte, []byte, error) {
		if executable != "pytest" {
			t.Errorf("pytest command executable = %q, want pytest", executable)
		}
		switch {
		case len(args) == 1 && args[0] == "--version":
			return []byte("pytest 8.2.0\n"), nil, nil
		case containsArg(args, "--collect-only"):
			return []byte(pytestCollectOutput), nil, nil
		default:
			t.Fatalf("pytest fixture has no canned output for argv %v", args)
			return nil, nil, nil
		}
	}
}

func fixtureUnit(t *testing.T, units []adapters.TestUnit, key string) adapters.TestUnit {
	t.Helper()
	for _, unit := range units {
		if unit.Key == key {
			return unit
		}
	}
	t.Fatalf("fixture discovery did not return unit %q; got %+v", key, units)
	return adapters.TestUnit{}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

// TestPytestFixtureDetectDiscoverMaterialize implements inventory row
// IVP-17-3-bfa578e511 (§17.3 examples/sample-pytest): pyproject.toml
// detection, node-ID collection through the capture seam (including
// deduplication of repeated node IDs), and node-ID materialization with a
// derived JUnit report path.
func TestPytestFixtureDetectDiscoverMaterialize(t *testing.T) {
	ctx := context.Background()
	a := New()
	a.capture = pytestFixtureCapture(t)

	detection, err := a.Detect(ctx, pytestFixtureRoot)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !detection.Detected {
		t.Fatalf("fixture must be detected as a pytest repository: %+v", detection)
	}
	if len(detection.Evidence) == 0 || detection.Evidence[0] != "pyproject.toml" {
		t.Fatalf("detection evidence = %v, want pyproject.toml first", detection.Evidence)
	}

	version, err := a.Version(ctx, pytestFixtureRoot)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version != "pytest 8.2.0" {
		t.Fatalf("Version = %q, want %q", version, "pytest 8.2.0")
	}

	result, err := a.Discover(ctx, adapters.DiscoverRequest{
		Root:           pytestFixtureRoot,
		Config:         adapters.AdapterConfig{Enabled: true, MaxOutputBytes: 1 << 20},
		MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !result.Complete || len(result.Reasons) != 0 {
		t.Fatalf("fixture discovery must be complete: complete=%v reasons=%v", result.Complete, result.Reasons)
	}
	if result.Version != "pytest 8.2.0" {
		t.Fatalf("Discover version = %q, want pytest 8.2.0", result.Version)
	}
	// Four collected lines, one a duplicate: the parser must return exactly
	// the three unique node IDs.
	if len(result.Units) != 3 {
		t.Fatalf("discovered %d units, want 3 (duplicate node ID deduplicated): %+v", len(result.Units), result.Units)
	}

	add := fixtureUnit(t, result.Units, "tests/test_calc.py::test_add")
	if add.Kind != "TEST_CASE" || add.Module != "tests" {
		t.Fatalf("test_add unit mismatch: %+v", add)
	}
	if !reflect.DeepEqual(add.Paths, []string{"tests/test_calc.py"}) {
		t.Fatalf("test_add paths = %v, want [tests/test_calc.py]", add.Paths)
	}
	if !reflect.DeepEqual(add.ReportPaths, []string{".ivp/reports/pytest*.xml"}) {
		t.Fatalf("test_add report paths = %v, want [.ivp/reports/pytest*.xml]", add.ReportPaths)
	}
	if add.SelectableGranularity != "PYTEST_NODEID" || !add.Filterable {
		t.Fatalf("test_add granularity/filtering mismatch: %+v", add)
	}
	fixtureUnit(t, result.Units, "tests/test_calc.py::test_total")
	fixtureUnit(t, result.Units, "tests/test_report.py::test_render")

	cmd, err := a.Materialize(ctx, adapters.MaterializeRequest{
		Root:         pytestFixtureRoot,
		Config:       adapters.AdapterConfig{Enabled: true, Options: []string{"--maxfail=1"}},
		TargetKeys:   []string{"tests/test_calc.py::test_add", "tests/test_report.py::test_render"},
		KnownTargets: []string{"tests/test_calc.py::test_add", "tests/test_calc.py::test_total", "tests/test_report.py::test_render"},
	})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if cmd.Executable != "pytest" {
		t.Fatalf("materialized executable = %q, want pytest", cmd.Executable)
	}
	key := "tests/test_calc.py::test_add,tests/test_report.py::test_render"
	wantReport := ".ivp/reports/pytest-" + shortHash(key) + ".xml"
	wantArgs := []string{"--maxfail=1", "-q", "--junitxml=" + wantReport,
		"tests/test_calc.py::test_add", "tests/test_report.py::test_render"}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("materialized args = %v, want %v", cmd.Args, wantArgs)
	}
	if !cmd.Filtered || cmd.TargetKey != key {
		t.Fatalf("materialized command must be a filtered node-ID selection: %+v", cmd)
	}
	if !reflect.DeepEqual(cmd.ReportPaths, []string{wantReport}) {
		t.Fatalf("materialized report paths = %v, want [%s]", cmd.ReportPaths, wantReport)
	}
	if got := len(cmd.WorkingDirectory); got == 0 {
		t.Fatalf("materialized working directory must not be empty: %+v", cmd)
	}
}
