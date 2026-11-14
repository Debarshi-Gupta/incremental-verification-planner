package gradle

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ivp/ivp/internal/adapters"
)

// gradleFixtureRoot is the §17.3 fixture repository exercised by this test.
// Native Gradle execution is unavailable in the fixture environment, so every
// command goes through the adapter's injectable capture seam and is answered
// with the checked-in output of the equivalent real Gradle invocation.
const gradleFixtureRoot = "../../../examples/sample-gradle"

func gradleFixtureOutput(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(gradleFixtureRoot, ".fixture-output", name))
	if err != nil {
		t.Fatalf("read gradle fixture output %q: %v", name, err)
	}
	return data
}

func gradleFixtureCapture(t *testing.T) func(context.Context, string, string, []string, int64) ([]byte, []byte, error) {
	t.Helper()
	return func(_ context.Context, _ string, executable string, args []string, _ int64) ([]byte, []byte, error) {
		if got := filepath.Base(executable); got != "gradlew" {
			t.Errorf("gradle command executable = %q, want the gradlew wrapper", executable)
		}
		if len(args) == 0 {
			t.Fatalf("gradle adapter invoked a command without arguments")
		}
		var file string
		switch args[0] {
		case "--version":
			file = "version.txt"
		case "projects":
			file = "projects.txt"
		case "tasks":
			file = "tasks-root.txt"
		case ":shared:tasks":
			file = "tasks-shared.txt"
		case ":app:tasks":
			file = "tasks-app.txt"
		case "dependencies":
			file = "deps-root.txt"
		case ":shared:dependencies":
			file = "deps-shared.txt"
		case ":app:dependencies":
			file = "deps-app.txt"
		default:
			t.Fatalf("gradle fixture has no canned output for argv %v", args)
		}
		return gradleFixtureOutput(t, file), nil, nil
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

// TestGradleFixtureDetectDiscoverMaterialize implements inventory row
// IVP-17-3-a64e4d97a5 (§17.3 examples/sample-gradle): detection evidence, the
// wrapper tool, task/dependency discovery through the capture seam, the
// documented incomplete root-dependency verdict, and task materialization
// with a changed-test filter.
func TestGradleFixtureDetectDiscoverMaterialize(t *testing.T) {
	ctx := context.Background()
	a := New()
	a.capture = gradleFixtureCapture(t)

	detection, err := a.Detect(ctx, gradleFixtureRoot)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !detection.Detected {
		t.Fatalf("fixture must be detected as a Gradle repository: %+v", detection)
	}
	if len(detection.Evidence) == 0 || detection.Evidence[0] != "settings.gradle" {
		t.Fatalf("detection evidence = %v, want settings.gradle first", detection.Evidence)
	}
	if got := detection.Metadata["wrapper"]; got != "gradlew" {
		t.Fatalf("detection wrapper metadata = %q, want gradlew", got)
	}

	version, err := a.Version(ctx, gradleFixtureRoot)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version != "Gradle 8.5" {
		t.Fatalf("Version = %q, want %q", version, "Gradle 8.5")
	}

	result, err := a.Discover(ctx, adapters.DiscoverRequest{
		Root:           gradleFixtureRoot,
		Config:         adapters.AdapterConfig{Enabled: true, MaxOutputBytes: 1 << 20},
		MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if result.Version != "Gradle 8.5" {
		t.Fatalf("Discover version = %q, want Gradle 8.5", result.Version)
	}
	if len(result.Units) != 3 {
		t.Fatalf("discovered %d units, want 3: %+v", len(result.Units), result.Units)
	}

	sharedTest := fixtureUnit(t, result.Units, ":shared:test")
	// ReportPaths are built with filepath.Join; on Windows Clean() prefixes a
	// colon-leading relative path with ".\", so expectations use the same
	// construction as production instead of a hardcoded string.
	wantSharedReport := filepath.ToSlash(filepath.Join(":shared/build/test-results/test", "TEST-*.xml"))
	if !reflect.DeepEqual(sharedTest.Paths, []string{"shared"}) ||
		!reflect.DeepEqual(sharedTest.BuildTargets, []string{":shared"}) ||
		!reflect.DeepEqual(sharedTest.ReportPaths, []string{wantSharedReport}) {
		t.Fatalf("shared test unit mismatch: %+v", sharedTest)
	}
	if sharedTest.SelectableGranularity != "GRADLE_TASK" || !sharedTest.Filterable || sharedTest.Module != ":shared" {
		t.Fatalf("shared test unit granularity/filtering mismatch: %+v", sharedTest)
	}

	appTest := fixtureUnit(t, result.Units, ":app:test")
	wantAppReport := filepath.ToSlash(filepath.Join(":app/build/test-results/test", "TEST-*.xml"))
	if !reflect.DeepEqual(appTest.ReportPaths, []string{wantAppReport}) ||
		!reflect.DeepEqual(appTest.Paths, []string{"app"}) {
		t.Fatalf("app test unit mismatch: %+v", appTest)
	}
	appIT := fixtureUnit(t, result.Units, ":app:integrationTest")
	wantITReport := filepath.ToSlash(filepath.Join(":app/build/test-results/integrationTest", "TEST-*.xml"))
	if !reflect.DeepEqual(appIT.ReportPaths, []string{wantITReport}) {
		t.Fatalf("app integrationTest unit mismatch: %+v", appIT)
	}

	if len(result.BuildTargets) != 3 {
		t.Fatalf("discovered %d build targets, want 3: %+v", len(result.BuildTargets), result.BuildTargets)
	}
	targets := map[string]adapters.BuildTarget{}
	for _, target := range result.BuildTargets {
		targets[target.Key] = target
	}
	if !reflect.DeepEqual(targets[":app"].Dependencies, []string{":shared"}) {
		t.Fatalf(":app build target dependencies = %v, want [:shared]", targets[":app"].Dependencies)
	}
	if len(targets[":"].Dependencies) != 0 || len(targets[":shared"].Dependencies) != 0 {
		t.Fatalf("root/shared targets must not claim project dependencies: root=%v shared=%v",
			targets[":"].Dependencies, targets[":shared"].Dependencies)
	}
	if targets[":app"].SourcePattern != "app/**" || targets[":"].SourcePattern != "**" {
		t.Fatalf("build target source patterns mismatch: %+v", targets)
	}

	// Documented current behavior: the root project's `dependencies` banner
	// ("Root project 'sample'") has no ':path' dependency form, so the parser
	// reports discovery as incomplete even though the subproject output parsed
	// cleanly. The planner sees this as a reason to stay conservative.
	if result.Complete {
		t.Fatalf("root dependency output without ':path' entries must mark discovery incomplete: %+v", result)
	}
	if len(result.Reasons) != 1 || !strings.Contains(result.Reasons[0], "Gradle project dependency output was incomplete for") {
		t.Fatalf("discovery reasons = %v, want exactly the incomplete root-dependency reason", result.Reasons)
	}

	cmd, err := a.Materialize(ctx, adapters.MaterializeRequest{
		Root:         gradleFixtureRoot,
		Config:       adapters.AdapterConfig{Enabled: true},
		TargetKeys:   []string{":app:test"},
		KnownTargets: []string{":shared:test", ":app:test", ":app:integrationTest"},
		ChangedFiles: []string{"app/src/test/java/com/example/app/AppTest.java"},
	})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if filepath.Base(cmd.Executable) != "gradlew" {
		t.Fatalf("materialized executable = %q, want gradlew", cmd.Executable)
	}
	wantArgs := []string{":app:test", "--console=plain", "--tests", "com.example.app.AppTest"}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("materialized args = %v, want %v", cmd.Args, wantArgs)
	}
	if !cmd.Filtered || cmd.TargetKey != ":app:test" {
		t.Fatalf("materialized command must be a filtered :app:test selection: %+v", cmd)
	}
	if len(cmd.ReportPaths) != 1 || cmd.ReportPaths[0] != filepath.ToSlash(filepath.Join("app", "build", "test-results", "test", "TEST-*.xml")) {
		t.Fatalf("materialized report paths = %v", cmd.ReportPaths)
	}
}
