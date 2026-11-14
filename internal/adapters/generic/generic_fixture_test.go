package generic

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/config"
)

// genericFixtureRoot is the §17.3 fixture repository exercised by this test.
// The generic adapter is fully declarative: no external binary is needed, so
// the test drives it from the checked-in strict overlay .ivp/config.yaml.
const genericFixtureRoot = "../../../examples/sample-generic"

func loadFixtureConfig(t *testing.T) (config.Config, adapters.AdapterConfig) {
	t.Helper()
	cfg, err := config.LoadFile(filepath.Join(genericFixtureRoot, ".ivp", "config.yaml"))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	// Bare executables must come from trusted policy, not repository YAML.
	return cfg, cfg.ToAdapterConfig("generic", []string{"python3"})
}

// TestGenericFixtureDetectDiscoverMaterialize implements inventory row
// IVP-17-3-231c121373 (§17.3 examples/sample-generic): detection of the
// declarative .ivp/config.yaml, unit/check discovery with declared
// dependencies, resource groups and source mappings, and single-unit
// materialization (including {changed_files} expansion) driven entirely by
// the checked-in overlay.
func TestGenericFixtureDetectDiscoverMaterialize(t *testing.T) {
	ctx := context.Background()
	_, adapterCfg := loadFixtureConfig(t)
	a := New()

	detection, err := a.Detect(ctx, genericFixtureRoot)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !detection.Detected {
		t.Fatalf("fixture must be detected as a generic repository: %+v", detection)
	}
	if !reflect.DeepEqual(detection.Evidence, []string{".ivp/config.yaml"}) {
		t.Fatalf("detection evidence = %v, want [.ivp/config.yaml]", detection.Evidence)
	}

	version, err := a.Version(ctx, genericFixtureRoot)
	if err != nil || version != "generic-config-v1" {
		t.Fatalf("Version = %q, %v; want generic-config-v1", version, err)
	}

	result, err := a.Discover(ctx, adapters.DiscoverRequest{
		Root:           genericFixtureRoot,
		Config:         adapterCfg,
		MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !result.Complete || len(result.Reasons) != 0 {
		t.Fatalf("fixture discovery must be complete: complete=%v reasons=%v", result.Complete, result.Reasons)
	}
	// Units are sorted by key: the two checks first, then the test units.
	if len(result.Units) != 4 {
		t.Fatalf("discovered %d units, want 4: %+v", len(result.Units), result.Units)
	}
	wantKeys := []string{"check-format", "check-secret-scan", "unit-api", "unit-core"}
	for i, unit := range result.Units {
		if unit.Key != wantKeys[i] {
			t.Fatalf("unit[%d] = %q, want %q (full order %v)", i, unit.Key, wantKeys[i], wantKeys)
		}
		if !unit.TrustedConfig || unit.SelectableGranularity != "DECLARED_UNIT" || !unit.Filterable {
			t.Fatalf("unit %q snapshot metadata mismatch: %+v", unit.Key, unit)
		}
	}

	checkFormat := result.Units[0]
	if checkFormat.Kind != "CHECK" || checkFormat.Dependencies == nil ||
		!reflect.DeepEqual(checkFormat.Dependencies, []string{"check-secret-scan"}) {
		t.Fatalf("check-format unit mismatch: %+v", checkFormat)
	}
	secretScan := result.Units[1]
	if secretScan.Kind != "CHECK" || !secretScan.Mandatory || secretScan.ResourceGroup != "io" {
		t.Fatalf("check-secret-scan unit mismatch: %+v", secretScan)
	}
	if secretScan.Timeout != 60*time.Second {
		t.Fatalf("check-secret-scan timeout = %v, want 60s", secretScan.Timeout)
	}
	unitAPI := result.Units[2]
	if unitAPI.Kind != "TEST" || unitAPI.ResourceGroup != "cpu-large" ||
		!reflect.DeepEqual(unitAPI.Dependencies, []string{"unit-core"}) {
		t.Fatalf("unit-api unit mismatch: %+v", unitAPI)
	}
	if !reflect.DeepEqual(unitAPI.Paths, []string{"app/api.py"}) {
		t.Fatalf("unit-api paths = %v, want [app/api.py]", unitAPI.Paths)
	}
	if !reflect.DeepEqual(unitAPI.ReportPaths, []string{"reports/unit-api.xml"}) {
		t.Fatalf("unit-api report paths = %v, want [reports/unit-api.xml]", unitAPI.ReportPaths)
	}
	if unitAPI.Timeout != 180*time.Second || unitAPI.EstimatedDuration != 180*time.Second {
		t.Fatalf("unit-api timeout/estimate = %v/%v, want 180s/180s", unitAPI.Timeout, unitAPI.EstimatedDuration)
	}
	unitCore := result.Units[3]
	if unitCore.ResourceGroup != "cpu-small" || unitCore.Mandatory {
		t.Fatalf("unit-core unit mismatch: %+v", unitCore)
	}
	if !reflect.DeepEqual(unitCore.CoveragePaths, []string{"coverage/unit-core.xml"}) {
		t.Fatalf("unit-core coverage paths = %v, want [coverage/unit-core.xml]", unitCore.CoveragePaths)
	}

	// One FILE-precision source mapping per declared maps_paths entry.
	if len(result.SourceMappings) != 3 {
		t.Fatalf("discovered %d source mappings, want 3: %+v", len(result.SourceMappings), result.SourceMappings)
	}
	wantMappings := map[string]string{
		"unit-core":         "app/core.py",
		"unit-api":          "app/api.py",
		"check-secret-scan": "**/*.py",
	}
	for _, mapping := range result.SourceMappings {
		want, ok := wantMappings[mapping.TargetKey]
		if !ok || mapping.PathPattern != want {
			t.Fatalf("unexpected source mapping: %+v", mapping)
		}
		if mapping.Precision != "FILE" || mapping.EvidenceSource != "generic:repository-config" || mapping.Confidence != 0.75 {
			t.Fatalf("source mapping evidence mismatch: %+v", mapping)
		}
	}

	// Materialize a test unit: declared argv, resource group, report paths.
	cmd, err := a.Materialize(ctx, adapters.MaterializeRequest{
		Root:         genericFixtureRoot,
		Config:       adapterCfg,
		TargetKeys:   []string{"unit-api"},
		KnownTargets: []string{"check-format", "check-secret-scan", "unit-api", "unit-core"},
	})
	if err != nil {
		t.Fatalf("Materialize unit-api: %v", err)
	}
	wantArgs := []string{"-m", "pytest", "-q", "tests/test_api.py"}
	if cmd.Executable != "python3" || !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("materialized command = %q %v, want python3 %v", cmd.Executable, cmd.Args, wantArgs)
	}
	if cmd.ResourceGroup != "cpu-large" || cmd.TargetKey != "unit-api" || cmd.CheckOnly {
		t.Fatalf("unit-api materialized metadata mismatch: %+v", cmd)
	}
	if !cmd.Filtered || cmd.FullSuite || cmd.Timeout != 180*time.Second {
		t.Fatalf("unit-api materialized flags mismatch: %+v", cmd)
	}
	if !reflect.DeepEqual(cmd.ReportPaths, []string{"reports/unit-api.xml"}) || len(cmd.CoveragePaths) != 0 {
		t.Fatalf("unit-api materialized report/coverage paths = %v/%v, want [reports/unit-api.xml]/none", cmd.ReportPaths, cmd.CoveragePaths)
	}

	// Materialize a check: check-only, and {changed_files} expands to the
	// canonical repository-relative changed path.
	checkCmd, err := a.Materialize(ctx, adapters.MaterializeRequest{
		Root:         genericFixtureRoot,
		Config:       adapterCfg,
		TargetKeys:   []string{"check-format"},
		KnownTargets: []string{"check-format", "check-secret-scan", "unit-api", "unit-core"},
		ChangedFiles: []string{"app/core.py"},
	})
	if err != nil {
		t.Fatalf("Materialize check-format: %v", err)
	}
	wantCheckArgs := []string{"tools/format_check.py", "app/core.py"}
	if checkCmd.Executable != "python3" || !reflect.DeepEqual(checkCmd.Args, wantCheckArgs) {
		t.Fatalf("check-format materialized command = %q %v, want python3 %v", checkCmd.Executable, checkCmd.Args, wantCheckArgs)
	}
	if !checkCmd.CheckOnly || !checkCmd.Filtered || checkCmd.ResourceGroup != "" {
		t.Fatalf("check-format materialized flags mismatch: %+v", checkCmd)
	}

	// The overlay's full-suite argv materializes as the authoritative suite.
	fullCmd, err := a.Materialize(ctx, adapters.MaterializeRequest{
		Root:      genericFixtureRoot,
		Config:    adapterCfg,
		FullSuite: true,
	})
	if err != nil {
		t.Fatalf("Materialize full suite: %v", err)
	}
	if !fullCmd.FullSuite || fullCmd.Executable != "python3" ||
		!reflect.DeepEqual(fullCmd.Args, []string{"-m", "pytest", "-q"}) {
		t.Fatalf("full-suite materialized command mismatch: %+v", fullCmd)
	}
}

// TestGenericFixtureConfigRejections implements the rejection half of
// inventory row IVP-17-3-231c121373: shell executables, escaping CWD and
// maps_paths, escaping changed files, dependency cycles, and unknown
// dependencies are all refused.
func TestGenericFixtureConfigRejections(t *testing.T) {
	ctx := context.Background()
	_, adapterCfg := loadFixtureConfig(t)
	a := New()

	// Shell argv is refused by the static executable check.
	err := a.ValidateConfig(adapters.AdapterConfig{
		AllowedExecutables: []string{"python3"},
		Targets:            []adapters.TargetSpec{{ID: "evil", Argv: []string{"sh", "-c", "true"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "shell executable") {
		t.Fatalf("shell argv error = %v, want a shell-executable rejection", err)
	}

	// A maps_paths glob that escapes the repository is refused.
	err = a.ValidateConfig(adapters.AdapterConfig{
		AllowedExecutables: []string{"python3"},
		Targets:            []adapters.TargetSpec{{ID: "escape", Argv: []string{"python3"}, MapsPaths: []string{"../secrets/**"}}},
	})
	if !errors.Is(err, adapters.ErrInvalidPath) {
		t.Fatalf("escaping maps_paths error = %v, want ErrInvalidPath", err)
	}

	// A CWD outside the repository is refused at materialization time (the
	// generic adapter resolves it through ResolveWithin).
	escapeCfg := adapterCfg
	escapeCfg.Targets = append(append([]adapters.TargetSpec(nil), adapterCfg.Targets...), adapters.TargetSpec{
		ID: "escape-cwd", Argv: []string{"python3"}, CWD: "../../outside",
	})
	_, err = a.Materialize(ctx, adapters.MaterializeRequest{
		Root:       genericFixtureRoot,
		Config:     escapeCfg,
		TargetKeys: []string{"escape-cwd"},
	})
	if !errors.Is(err, adapters.ErrInvalidPath) {
		t.Fatalf("escaping CWD error = %v, want ErrInvalidPath", err)
	}

	// Changed files outside the repository are refused before execution.
	_, err = a.Materialize(ctx, adapters.MaterializeRequest{
		Root:         genericFixtureRoot,
		Config:       adapterCfg,
		TargetKeys:   []string{"unit-core"},
		KnownTargets: []string{"check-format", "check-secret-scan", "unit-api", "unit-core"},
		ChangedFiles: []string{"../outside.txt"},
	})
	if !errors.Is(err, adapters.ErrInvalidPath) {
		t.Fatalf("escaping changed file error = %v, want ErrInvalidPath", err)
	}

	// A dependency cycle is refused.
	err = a.ValidateConfig(adapters.AdapterConfig{
		AllowedExecutables: []string{"python3"},
		Targets: []adapters.TargetSpec{
			{ID: "a", Argv: []string{"python3"}, DependsOn: []string{"b"}},
			{ID: "b", Argv: []string{"python3"}, DependsOn: []string{"a"}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "generic target dependency cycle") {
		t.Fatalf("cycle error = %v, want a dependency-cycle rejection", err)
	}

	// An unknown dependency is refused.
	err = a.ValidateConfig(adapters.AdapterConfig{
		AllowedExecutables: []string{"python3"},
		Targets: []adapters.TargetSpec{
			{ID: "solo", Argv: []string{"python3"}, DependsOn: []string{"missing"}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "depends on unknown target") {
		t.Fatalf("unknown-dependency error = %v, want an unknown-target rejection", err)
	}
}
