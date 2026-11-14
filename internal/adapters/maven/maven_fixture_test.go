package maven

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ivp/ivp/internal/adapters"
)

// mavenFixtureRoot is the §17.3 fixture repository exercised by this test.
// Maven discovery parses the reactor POM model from disk, so no mvn binary is
// required; the wrapper only has to be discoverable.
const mavenFixtureRoot = "../../../examples/sample-maven"

func mavenFixtureUnit(t *testing.T, units []adapters.TestUnit, key string) adapters.TestUnit {
	t.Helper()
	for _, unit := range units {
		if unit.Key == key {
			return unit
		}
	}
	t.Fatalf("fixture discovery did not return unit %q; got %+v", key, units)
	return adapters.TestUnit{}
}

// TestMavenFixtureDetectDiscoverMaterialize implements inventory row
// IVP-17-3-c0a7e16b83 (§17.3 examples/sample-maven): pom.xml detection with
// wrapper metadata, reactor discovery over parent/core/service POMs, the
// failsafe-only integration unit in the service module, reactor dependency
// edges (service -> core), and verify-goal materialization with an IT filter.
func TestMavenFixtureDetectDiscoverMaterialize(t *testing.T) {
	ctx := context.Background()
	a := New()

	detection, err := a.Detect(ctx, mavenFixtureRoot)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !detection.Detected {
		t.Fatalf("fixture must be detected as a Maven repository: %+v", detection)
	}
	if len(detection.Evidence) == 0 || detection.Evidence[0] != "pom.xml" {
		t.Fatalf("detection evidence = %v, want pom.xml first", detection.Evidence)
	}
	if got := detection.Metadata["wrapper"]; got != "mvnw" {
		t.Fatalf("detection wrapper metadata = %q, want mvnw", got)
	}

	result, err := a.Discover(ctx, adapters.DiscoverRequest{
		Root:   mavenFixtureRoot,
		Config: adapters.AdapterConfig{Enabled: true},
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !result.Complete || len(result.Reasons) != 0 {
		t.Fatalf("static reactor discovery must be complete: complete=%v reasons=%v", result.Complete, result.Reasons)
	}
	if len(result.Units) != 4 {
		t.Fatalf("discovered %d units, want 4: %+v", len(result.Units), result.Units)
	}

	rootTest := mavenFixtureUnit(t, result.Units, "#test")
	if rootTest.Module != "" || !reflect.DeepEqual(rootTest.Paths, []string{"."}) ||
		!reflect.DeepEqual(rootTest.ReportPaths, []string{filepath.ToSlash(filepath.Join("target", "surefire-reports", "TEST-*.xml"))}) {
		t.Fatalf("root unit mismatch: %+v", rootTest)
	}

	coreTest := mavenFixtureUnit(t, result.Units, "core#test")
	if coreTest.Module != "core" || !reflect.DeepEqual(coreTest.BuildTargets, []string{"core"}) ||
		!reflect.DeepEqual(coreTest.ReportPaths, []string{filepath.ToSlash(filepath.Join("core", "target", "surefire-reports", "TEST-*.xml"))}) {
		t.Fatalf("core unit mismatch: %+v", coreTest)
	}

	// Only the service module declares the failsafe plugin, so only it gains a
	// #verify integration unit; the root and core stay surefire-only.
	mavenFixtureUnit(t, result.Units, "service#test")
	serviceVerify := mavenFixtureUnit(t, result.Units, "service#verify")
	if serviceVerify.Kind != "PROJECT_SUITE" || !serviceVerify.Filterable ||
		serviceVerify.SelectableGranularity != "MAVEN_MODULE_GOAL" ||
		!reflect.DeepEqual(serviceVerify.ReportPaths, []string{filepath.ToSlash(filepath.Join("service", "target", "failsafe-reports", "TEST-*.xml"))}) {
		t.Fatalf("service verify unit mismatch: %+v", serviceVerify)
	}
	if serviceVerify.Timeout != 45*60*1000000000 {
		t.Fatalf("integration unit timeout = %v, want 45m", serviceVerify.Timeout)
	}

	if len(result.BuildTargets) != 3 {
		t.Fatalf("discovered %d build targets, want 3: %+v", len(result.BuildTargets), result.BuildTargets)
	}
	targets := map[string]adapters.BuildTarget{}
	for _, target := range result.BuildTargets {
		targets[target.Key] = target
	}
	if !reflect.DeepEqual(targets["service"].Dependencies, []string{"core"}) {
		t.Fatalf("service reactor dependencies = %v, want [core]", targets["service"].Dependencies)
	}
	if len(targets["core"].Dependencies) != 0 || len(targets["."].Dependencies) != 0 {
		t.Fatalf("core/root targets must not claim reactor dependencies: %+v", targets)
	}
	if targets["service"].SourcePattern != "service/**" || targets["."].SourcePattern != "**" ||
		targets["core"].EvidenceSource != "maven:reactor-module-model" {
		t.Fatalf("build target metadata mismatch: %+v", targets)
	}

	cmd, err := a.Materialize(ctx, adapters.MaterializeRequest{
		Root:         mavenFixtureRoot,
		Config:       adapters.AdapterConfig{Enabled: true},
		TargetKeys:   []string{"service#verify"},
		KnownTargets: []string{"#test", "core#test", "service#test", "service#verify"},
		ChangedFiles: []string{"service/src/it/java/com/example/service/ReceiptIT.java"},
	})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if filepath.Base(cmd.Executable) != "mvnw" {
		t.Fatalf("materialized executable = %q, want mvnw", cmd.Executable)
	}
	wantArgs := []string{"-pl", "service", "-am", "verify", "-Dit.test=com.example.service.ReceiptIT"}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("materialized args = %v, want %v", cmd.Args, wantArgs)
	}
	if !cmd.Filtered || cmd.TargetKey != "service#verify" {
		t.Fatalf("materialized command must be a filtered service#verify selection: %+v", cmd)
	}
	if len(cmd.ReportPaths) != 1 || cmd.ReportPaths[0] != filepath.ToSlash(filepath.Join("service", "target", "failsafe-reports", "TEST-*.xml")) {
		t.Fatalf("materialized report paths = %v", cmd.ReportPaths)
	}
}
