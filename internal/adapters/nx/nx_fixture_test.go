package nx

import (
	"context"
	"reflect"
	"testing"

	"github.com/ivp/ivp/internal/adapters"
)

// nxFixtureRoot is the §17.3 fixture repository exercised by this test. Nx
// CLI execution is unavailable in the fixture environment, so every command
// goes through the adapter's injectable capture seam and is answered with the
// output of the equivalent real `nx` invocation.
const nxFixtureRoot = "../../../examples/sample-nx"

const nxWebProjectJSON = `{
  "name": "web",
  "root": "apps/web",
  "sourceRoot": "apps/web/src",
  "implicitDependencies": ["api"],
  "targets": {
    "lint": {
      "executor": "@nx/linter:eslint",
      "options": {}
    },
    "test": {
      "executor": "@nx/jest:jest",
      "options": {"jestConfig": "apps/web/jest.config.js"},
      "configurations": {"ci": {"codeCoverage": true}}
    }
  }
}`

const nxApiProjectJSON = `{
  "name": "api",
  "root": "apps/api",
  "sourceRoot": "apps/api/src",
  "targets": {
    "test": {
      "executor": "@nx/jest:jest",
      "options": {"jestConfig": "apps/api/jest.config.js"}
    }
  }
}`

// nxFixtureCapture answers the four Nx invocations Discover performs: the
// version probe, the project list, per-project metadata, and the affected
// query (matched before the plain list because it carries --affected).
func nxFixtureCapture(t *testing.T) func(context.Context, string, string, []string, int64) ([]byte, []byte, error) {
	t.Helper()
	return func(_ context.Context, root, executable string, args []string, _ int64) ([]byte, []byte, error) {
		// The fixture has no node_modules/.bin/nx, so the adapter must fall
		// back to npx with the --no-install prefix.
		if executable != "npx" {
			t.Errorf("nx command executable = %q, want npx", executable)
		}
		if len(args) < 2 || args[0] != "--no-install" || args[1] != "nx" {
			t.Errorf("nx argv must carry the npx --no-install prefix: %v", args)
		}
		rest := args[2:]
		switch {
		case len(rest) == 1 && rest[0] == "--version":
			return []byte("17.3.0\n"), nil, nil
		case rest[0] == "show" && len(rest) >= 3 && rest[1] == "projects" && rest[2] == "--affected":
			return []byte(`["web"]`), nil, nil
		case rest[0] == "show" && len(rest) >= 3 && rest[1] == "projects" && rest[2] == "--json":
			// Deliberately unsorted; parseProjectNames must sort to api first.
			return []byte(`["web", "api"]`), nil, nil
		case rest[0] == "show" && len(rest) >= 3 && rest[1] == "project":
			switch rest[2] {
			case "web":
				return []byte(nxWebProjectJSON), nil, nil
			case "api":
				return []byte(nxApiProjectJSON), nil, nil
			}
			t.Fatalf("nx fixture has no metadata for project %q", rest[2])
		default:
			t.Fatalf("nx fixture has no canned output for argv %v", args)
		}
		return nil, nil, nil
	}
}

func nxFixtureUnit(t *testing.T, units []adapters.TestUnit, key string) adapters.TestUnit {
	t.Helper()
	for _, unit := range units {
		if unit.Key == key {
			return unit
		}
	}
	t.Fatalf("fixture discovery did not return unit %q; got %+v", key, units)
	return adapters.TestUnit{}
}

func nxFixtureTarget(t *testing.T, targets []adapters.BuildTarget, key string) adapters.BuildTarget {
	t.Helper()
	for _, target := range targets {
		if target.Key == key {
			return target
		}
	}
	t.Fatalf("fixture discovery did not return build target %q; got %+v", key, targets)
	return adapters.BuildTarget{}
}

// TestNxFixtureDetectDiscoverMaterialize implements inventory row
// IVP-17-3-505d587c63 (§17.3 examples/sample-nx): nx.json detection, project
// and target discovery through the capture seam (including jest report
// derivation from the target key), affected-project expansion into affected
// targets, and single-target materialization with the npx prefix.
func TestNxFixtureDetectDiscoverMaterialize(t *testing.T) {
	ctx := context.Background()
	a := New()
	a.capture = nxFixtureCapture(t)

	detection, err := a.Detect(ctx, nxFixtureRoot)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !detection.Detected {
		t.Fatalf("fixture must be detected as an Nx repository: %+v", detection)
	}
	if len(detection.Evidence) == 0 || detection.Evidence[0] != "nx.json" {
		t.Fatalf("detection evidence = %v, want nx.json first", detection.Evidence)
	}

	version, err := a.Version(ctx, nxFixtureRoot)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version != "17.3.0" {
		t.Fatalf("Version = %q, want %q", version, "17.3.0")
	}

	result, err := a.Discover(ctx, adapters.DiscoverRequest{
		Root:           nxFixtureRoot,
		Config:         adapters.AdapterConfig{Enabled: true, MaxOutputBytes: 1 << 20},
		BaseSHA:        "base-sha",
		HeadSHA:        "head-sha",
		MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !result.Complete || len(result.Reasons) != 0 {
		t.Fatalf("fixture discovery must be complete: complete=%v reasons=%v", result.Complete, result.Reasons)
	}
	if result.Version != "17.3.0" {
		t.Fatalf("Discover version = %q, want 17.3.0", result.Version)
	}
	if len(result.Units) != 4 {
		t.Fatalf("discovered %d units, want 4 (lint+test+test:ci+api test): %+v", len(result.Units), result.Units)
	}

	lint := nxFixtureUnit(t, result.Units, "web:lint")
	if lint.Kind != "CHECK" || lint.Runner != "@nx/linter:eslint" || lint.Module != "web" {
		t.Fatalf("web:lint unit mismatch: %+v", lint)
	}
	if !reflect.DeepEqual(lint.Paths, []string{"apps/web/src"}) {
		t.Fatalf("web:lint paths = %v, want [apps/web/src]", lint.Paths)
	}
	if len(lint.ReportPaths) != 0 {
		t.Fatalf("web:lint must not declare a report file: %v", lint.ReportPaths)
	}
	if lint.SelectableGranularity != "NX_PROJECT_TARGET" || !lint.Filterable {
		t.Fatalf("web:lint granularity/filtering mismatch: %+v", lint)
	}

	webTest := nxFixtureUnit(t, result.Units, "web:test")
	wantWebReport := ".ivp/reports/nx-" + shortHash("web:test") + ".json"
	if webTest.Kind != "BUILD_TARGET" || webTest.Runner != "@nx/jest:jest" {
		t.Fatalf("web:test unit mismatch: %+v", webTest)
	}
	if !reflect.DeepEqual(webTest.ReportPaths, []string{wantWebReport}) {
		t.Fatalf("web:test report paths = %v, want [%s]", webTest.ReportPaths, wantWebReport)
	}
	webTestCI := nxFixtureUnit(t, result.Units, "web:test:ci")
	wantCIReport := ".ivp/reports/nx-" + shortHash("web:test:ci") + ".json"
	if !reflect.DeepEqual(webTestCI.ReportPaths, []string{wantCIReport}) {
		t.Fatalf("web:test:ci report paths = %v, want [%s]", webTestCI.ReportPaths, wantCIReport)
	}
	apiTest := nxFixtureUnit(t, result.Units, "api:test")
	if !reflect.DeepEqual(apiTest.Paths, []string{"apps/api/src"}) {
		t.Fatalf("api:test paths = %v, want [apps/api/src]", apiTest.Paths)
	}

	if len(result.BuildTargets) != 2 {
		t.Fatalf("discovered %d build targets, want 2: %+v", len(result.BuildTargets), result.BuildTargets)
	}
	webTarget := nxFixtureTarget(t, result.BuildTargets, "web")
	if webTarget.SourcePattern != "apps/web/**" ||
		webTarget.EvidenceSource != "nx:project-metadata" || webTarget.Confidence != 0.70 {
		t.Fatalf("web build target mismatch: %+v", webTarget)
	}
	if !reflect.DeepEqual(webTarget.Dependencies, []string{"api"}) {
		t.Fatalf("web dependencies = %v, want [api] from implicitDependencies", webTarget.Dependencies)
	}
	apiTarget := nxFixtureTarget(t, result.BuildTargets, "api")
	if apiTarget.SourcePattern != "apps/api/**" || len(apiTarget.Dependencies) != 0 {
		t.Fatalf("api build target mismatch: %+v", apiTarget)
	}

	wantAffected := []string{"web:lint", "web:test", "web:test:ci"}
	if !reflect.DeepEqual(result.AffectedTargets, wantAffected) {
		t.Fatalf("affected targets = %v, want %v (all web targets)", result.AffectedTargets, wantAffected)
	}

	// Materializing a CHECK target: nx run web:lint through npx, no report.
	cmd, err := a.Materialize(ctx, adapters.MaterializeRequest{
		Root:         nxFixtureRoot,
		Config:       adapters.AdapterConfig{Enabled: true},
		TargetKeys:   []string{"web:lint"},
		KnownTargets: []string{"web:lint", "web:test", "web:test:ci", "api:test"},
		KnownUnits:   result.Units,
	})
	if err != nil {
		t.Fatalf("Materialize web:lint: %v", err)
	}
	wantLintArgs := []string{"--no-install", "nx", "run", "web:lint"}
	if cmd.Executable != "npx" || !reflect.DeepEqual(cmd.Args, wantLintArgs) {
		t.Fatalf("materialized command = %q %v, want npx %v", cmd.Executable, cmd.Args, wantLintArgs)
	}
	if !cmd.CheckOnly || cmd.TargetKey != "web:lint" {
		t.Fatalf("web:lint must materialize as a check-only selection: %+v", cmd)
	}
	if len(cmd.ReportPaths) != 0 {
		t.Fatalf("web:lint materialized report paths = %v, want none", cmd.ReportPaths)
	}

	// Materializing a jest target with a configuration: the derived report
	// file is attached as an --outputFile argument and the report path.
	ciCmd, err := a.Materialize(ctx, adapters.MaterializeRequest{
		Root:         nxFixtureRoot,
		Config:       adapters.AdapterConfig{Enabled: true},
		TargetKeys:   []string{"web:test:ci"},
		KnownTargets: []string{"web:lint", "web:test", "web:test:ci", "api:test"},
		KnownUnits:   result.Units,
	})
	if err != nil {
		t.Fatalf("Materialize web:test:ci: %v", err)
	}
	wantCIArgs := []string{
		"--no-install", "nx", "run", "web:test", "--configuration=ci",
		"--json", "--outputFile=" + wantCIReport,
	}
	if !reflect.DeepEqual(ciCmd.Args, wantCIArgs) {
		t.Fatalf("materialized args = %v, want %v", ciCmd.Args, wantCIArgs)
	}
	if ciCmd.CheckOnly {
		t.Fatalf("jest target must not be check-only: %+v", ciCmd)
	}
	if !reflect.DeepEqual(ciCmd.ReportPaths, []string{wantCIReport}) {
		t.Fatalf("materialized report paths = %v, want [%s]", ciCmd.ReportPaths, wantCIReport)
	}

	// Without trusted snapshot metadata the adapter refuses to materialize.
	if _, err := a.Materialize(ctx, adapters.MaterializeRequest{
		Root:       nxFixtureRoot,
		Config:     adapters.AdapterConfig{Enabled: true},
		TargetKeys: []string{"web:lint"},
	}); err == nil {
		t.Fatalf("Materialize without KnownUnits must fail with a snapshot-metadata error")
	}
}
