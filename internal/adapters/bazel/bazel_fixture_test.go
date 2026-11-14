package bazel

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/ivp/ivp/internal/adapters"
)

// bazelFixtureRoot is the §17.3 fixture repository exercised by this test.
// Bazel query execution is unavailable in the fixture environment, so each
// query is answered through the adapter's injectable capture seam with the
// output of the equivalent real `bazel query` invocation.
const bazelFixtureRoot = "../../../examples/sample-bazel"

const (
	bazelTestLabelOutput = `//common:math_test
//api:service_test
//web:app_test
`
	// The configured query resolves the same three test targets: the
	// repository's configuration transitions select every rule of the
	// package graph.
	bazelConfiguredLabelOutput = `//common:math_test
//api:service_test
//web:app_test
`
	bazelGraphOutput = `//web:app_test -> //web:app;
//web:app -> //api:service;
//web:app -> //common:math;
//api:service_test -> //api:service;
//api:service -> //common:math;
//common:math_test -> //common:math;
//common:math -> //common:util;
`
	// The configured graph only carries configured targets: the source file
	// //common:util is absent, and the configured toolchain edge
	// //common:math -> //common:math_tool is present.
	bazelConfiguredGraphOutput = `//web:app_test -> //web:app;
//web:app -> //api:service;
//web:app -> //common:math;
//api:service_test -> //api:service;
//api:service -> //common:math;
//common:math_test -> //common:math;
//common:math -> //common:math_tool;
`
	bazelRdepsOutput = `//web:app_test
//api:service_test
//common:math_test
`
)

// bazelFixtureCapture answers every Bazel command of the fixture
// repository. The configured (cquery) commands are answered separately from
// the unconfigured (query) ones so the two evidence layers stay distinct.
func bazelFixtureCapture(t *testing.T, invoked *[][]string) func(context.Context, string, string, []string, int64) ([]byte, []byte, error) {
	t.Helper()
	return func(_ context.Context, _ string, executable string, args []string, _ int64) ([]byte, []byte, error) {
		// The fixture carries .bazelversion, so the adapter must select bazelisk.
		if executable != "bazelisk" {
			t.Errorf("bazel command executable = %q, want bazelisk", executable)
		}
		if invoked != nil {
			*invoked = append(*invoked, append([]string(nil), args...))
		}
		joined := strings.Join(args, " ")
		switch {
		case args[0] == "--version":
			return []byte("bazel 7.1.0\n"), nil, nil
		case args[0] == "cquery" && strings.Contains(joined, "--output=graph"):
			return []byte(bazelConfiguredGraphOutput), nil, nil
		case args[0] == "cquery":
			return []byte(bazelConfiguredLabelOutput), nil, nil
		case strings.Contains(joined, "--output=graph"):
			return []byte(bazelGraphOutput), nil, nil
		case strings.Contains(joined, "rdeps("):
			return []byte(bazelRdepsOutput), nil, nil
		case strings.Contains(joined, "kind("):
			return []byte(bazelTestLabelOutput), nil, nil
		default:
			t.Fatalf("bazel fixture has no canned output for argv %v", args)
			return nil, nil, nil
		}
	}
}

func bazelFixtureTarget(t *testing.T, targets []adapters.BuildTarget, key string) adapters.BuildTarget {
	t.Helper()
	for _, target := range targets {
		if target.Key == key {
			return target
		}
	}
	t.Fatalf("fixture discovery did not return build target %q; got %+v", key, targets)
	return adapters.BuildTarget{}
}

// TestBazelFixtureDetectDiscoverMaterialize implements inventory row
// IVP-17-3-9a3e1e831c (§17.3 examples/sample-bazel): MODULE.bazel detection,
// bazelisk selection, test-target discovery, the unconfigured target graph
// from `--output=graph` (including the high-fanout //common:math node), the
// configured target graph from `bazel cquery`, affected targets from `rdeps`
// of the changed file, and BEP-report materialization.
func TestBazelFixtureDetectDiscoverMaterialize(t *testing.T) {
	ctx := context.Background()
	a := New()
	var invoked [][]string
	a.capture = bazelFixtureCapture(t, &invoked)

	detection, err := a.Detect(ctx, bazelFixtureRoot)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !detection.Detected {
		t.Fatalf("fixture must be detected as a Bazel repository: %+v", detection)
	}
	if len(detection.Evidence) == 0 || detection.Evidence[0] != "MODULE.bazel" {
		t.Fatalf("detection evidence = %v, want MODULE.bazel first", detection.Evidence)
	}

	version, err := a.Version(ctx, bazelFixtureRoot)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version != "bazel 7.1.0" {
		t.Fatalf("Version = %q, want bazel 7.1.0", version)
	}

	result, err := a.Discover(ctx, adapters.DiscoverRequest{
		Root:           bazelFixtureRoot,
		Config:         adapters.AdapterConfig{Enabled: true, MaxOutputBytes: 1 << 20},
		ChangedFiles:   []string{"common/math.py"},
		MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !result.Complete || len(result.Reasons) != 0 {
		t.Fatalf("fixture discovery must be complete: complete=%v reasons=%v", result.Complete, result.Reasons)
	}
	if result.Version != "bazel 7.1.0" {
		t.Fatalf("Discover version = %q, want bazel 7.1.0", result.Version)
	}
	if len(result.Units) != 3 {
		t.Fatalf("discovered %d test targets, want 3: %+v", len(result.Units), result.Units)
	}
	for _, key := range []string{"//common:math_test", "//api:service_test", "//web:app_test"} {
		found := false
		for _, unit := range result.Units {
			if unit.Key != key {
				continue
			}
			found = true
			if unit.SelectableGranularity != "BAZEL_TEST_TARGET" || !unit.Filterable {
				t.Errorf("unit %q granularity/filtering mismatch: %+v", key, unit)
			}
		}
		if !found {
			t.Errorf("fixture did not discover test target %q: %+v", key, result.Units)
		}
	}

	if len(result.BuildTargets) != 8 {
		t.Fatalf("discovered %d build targets, want 8 (3 tests + 4 unconfigured graph nodes + 1 configured-only node): %+v", len(result.BuildTargets), result.BuildTargets)
	}
	mathTarget := bazelFixtureTarget(t, result.BuildTargets, "//common:math")
	// //common:math is known to both graphs, so the conservative unconfigured
	// evidence stays; its edges are the union of both graphs.
	if mathTarget.EvidenceSource != "bazel:query-graph" || mathTarget.Confidence != 0.80 ||
		!reflect.DeepEqual(mathTarget.Dependencies, []string{"//common:math_tool", "//common:util"}) {
		t.Fatalf("//common:math graph node mismatch: %+v", mathTarget)
	}
	appTarget := bazelFixtureTarget(t, result.BuildTargets, "//web:app")
	if !reflect.DeepEqual(appTarget.Dependencies, []string{"//api:service", "//common:math"}) {
		t.Fatalf("//web:app dependencies = %v, want [//api:service //common:math]", appTarget.Dependencies)
	}
	mathTest := bazelFixtureTarget(t, result.BuildTargets, "//common:math_test")
	// The configured query resolved the test target, which is stronger
	// evidence than package membership alone.
	if mathTest.EvidenceSource != "bazel:cquery-target-set" || mathTest.Confidence != 0.60 {
		t.Fatalf("test target evidence metadata mismatch: %+v", mathTest)
	}
	// //common:math_tool only exists in the configured graph.
	mathTool := bazelFixtureTarget(t, result.BuildTargets, "//common:math_tool")
	if mathTool.EvidenceSource != "bazel:cquery-graph" || mathTool.Confidence != 0.85 {
		t.Fatalf("configured-only graph node evidence mismatch: %+v", mathTool)
	}
	// //common:util is a source file and therefore not a configured target.
	mathUtil := bazelFixtureTarget(t, result.BuildTargets, "//common:util")
	if mathUtil.EvidenceSource != "bazel:query-graph" || mathUtil.Confidence != 0.80 {
		t.Fatalf("unconfigured-only graph node evidence mismatch: %+v", mathUtil)
	}

	wantAffected := []string{"//api:service_test", "//common:math_test", "//web:app_test"}
	if !reflect.DeepEqual(result.AffectedTargets, wantAffected) {
		t.Fatalf("affected targets = %v, want %v (rdeps of //common:math.py)", result.AffectedTargets, wantAffected)
	}

	cmd, err := a.Materialize(ctx, adapters.MaterializeRequest{
		Root:         bazelFixtureRoot,
		Config:       adapters.AdapterConfig{Enabled: true},
		TargetKeys:   []string{"//common:math_test"},
		KnownTargets: []string{"//common:math_test", "//api:service_test", "//web:app_test"},
	})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if cmd.Executable != "bazelisk" {
		t.Fatalf("materialized executable = %q, want bazelisk", cmd.Executable)
	}
	if len(cmd.Args) < 2 || cmd.Args[0] != "test" || cmd.Args[1] != "//common:math_test" {
		t.Fatalf("materialized args = %v, want test //common:math_test ...", cmd.Args)
	}
	if len(cmd.Args) != 3 || !strings.HasPrefix(cmd.Args[2], "--build_event_json_file=") {
		t.Fatalf("materialized command must attach a BEP report path: %v", cmd.Args)
	}
	if len(cmd.ReportPaths) != 1 || !strings.HasPrefix(cmd.ReportPaths[0], ".ivp/reports/bazel-") || !strings.HasSuffix(cmd.ReportPaths[0], ".jsonl") {
		t.Fatalf("materialized report paths = %v", cmd.ReportPaths)
	}
	if cmd.TargetKey != "//common:math_test" {
		t.Fatalf("materialized target key = %q", cmd.TargetKey)
	}

	// The configured queries must be real cquery invocations: the configured
	// test-target set and the configured dependency graph.
	var configuredLabels, configuredGraph []string
	for _, args := range invoked {
		if len(args) == 0 || args[0] != "cquery" {
			continue
		}
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "--output=label"):
			configuredLabels = args
		case strings.Contains(joined, "--output=graph"):
			configuredGraph = args
		}
	}
	if configuredLabels == nil || len(configuredLabels) != 3 ||
		configuredLabels[1] != `kind(".*_test rule", deps(//...))` || configuredLabels[2] != "--output=label" {
		t.Fatalf("configured test-target query = %v, want [cquery kind(\".*_test rule\", deps(//...)) --output=label]", configuredLabels)
	}
	if configuredGraph == nil || len(configuredGraph) != 3 ||
		!strings.HasPrefix(configuredGraph[1], "deps(set(") || configuredGraph[2] != "--output=graph" {
		t.Fatalf("configured dependency graph query = %v, want [cquery deps(set(...)) --output=graph]", configuredGraph)
	}
}
