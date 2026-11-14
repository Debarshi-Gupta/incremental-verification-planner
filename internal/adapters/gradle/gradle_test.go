package gradle

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/ivp/ivp/internal/adapters"
)

func TestGradleDiscoveryParsersUseProjectAndTaskFixtures(t *testing.T) {
	projectFixture := []byte(`Root project 'sample'
+--- Project ':api'
+--- Project ':service:worker'
+--- Project ':api'
`)
	if got, want := parseProjectNames(projectFixture), []string{":api", ":service:worker"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Gradle projects = %v, want %v", got, want)
	}

	taskFixture := []byte(`Verification tasks
-------------------
 test - Runs the test suite
 integrationTest - Runs integration tests
 check - Runs verification tasks
 compileTestJava - Compiles test sources
 test - Duplicate task output
`)
	if got, want := parseTestTaskNames(taskFixture), []string{"test", "integrationTest", "compileTestJava"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Gradle test tasks = %v, want %v", got, want)
	}
}

// TestSupportsGradleTestFilterAcceptsOnlyTestTasks pins the ADR 0008 gate:
// --tests is emitted only for a task whose name denotes a Gradle Test task
// ("test", or a camel-case "<name>Test" task). Compile tasks, lifecycle tasks
// and names that merely contain "test" as a substring are not Test tasks by
// convention and must be executed whole.
func TestSupportsGradleTestFilterAcceptsOnlyTestTasks(t *testing.T) {
	tests := []struct {
		name string
		task string
		want bool
	}{
		{name: "default test task", task: "test", want: true},
		{name: "configured integration test task", task: "integrationTest", want: true},
		{name: "custom unit test task", task: "unitTest", want: true},
		{name: "java test compilation task", task: "compileTestJava", want: false},
		{name: "kotlin test compilation task", task: "compileTestKotlin", want: false},
		{name: "test classes lifecycle task", task: "testClasses", want: false},
		{name: "empty task name", task: "", want: false},
		{name: "substring coincidence", task: "latest", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := supportsGradleTestFilter(tt.task); got != tt.want {
				t.Fatalf("supportsGradleTestFilter(%q) = %v, want %v", tt.task, got, tt.want)
			}
		})
	}
}

// TestGradleMaterializeAppliesTestFilterOnlyToSupportedTasks pins the
// materialization half of the contract: a task the gate accepts is narrowed
// with a class-level --tests filter and reports Filtered=true, while a task
// the gate rejects is executed whole with no --tests argument and
// Filtered=false.
func TestGradleMaterializeAppliesTestFilterOnlyToSupportedTasks(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		wantArgs     []string
		wantFiltered bool
	}{
		{
			name:         "test task is narrowed by class",
			key:          ":app:test",
			wantArgs:     []string{":app:test", "--console=plain", "--tests", "com.example.app.AppTest"},
			wantFiltered: true,
		},
		{
			name:         "integration test task is narrowed by class",
			key:          ":app:integrationTest",
			wantArgs:     []string{":app:integrationTest", "--console=plain", "--tests", "com.example.app.AppIT"},
			wantFiltered: true,
		},
		{
			name:         "compile task runs the whole task",
			key:          ":app:compileTestJava",
			wantArgs:     []string{":app:compileTestJava", "--console=plain"},
			wantFiltered: false,
		},
	}
	ctx := context.Background()
	a := New()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := a.Materialize(ctx, adapters.MaterializeRequest{
				Root:         gradleFixtureRoot,
				Config:       adapters.AdapterConfig{Enabled: true},
				TargetKeys:   []string{tt.key},
				KnownTargets: []string{":shared:test", ":app:test", ":app:integrationTest", ":app:compileTestJava"},
				ChangedFiles: []string{
					"app/src/test/java/com/example/app/AppTest.java",
					"app/src/integrationTest/java/com/example/app/AppIT.java",
				},
			})
			if err != nil {
				t.Fatalf("Materialize: %v", err)
			}
			if !reflect.DeepEqual(cmd.Args, tt.wantArgs) {
				t.Fatalf("materialized args = %v, want %v", cmd.Args, tt.wantArgs)
			}
			if cmd.Filtered != tt.wantFiltered {
				t.Fatalf("materialized Filtered = %v, want %v", cmd.Filtered, tt.wantFiltered)
			}
			if cmd.TargetKey != tt.key {
				t.Fatalf("materialized TargetKey = %q, want %q", cmd.TargetKey, tt.key)
			}
			if !tt.wantFiltered {
				for _, arg := range cmd.Args {
					if arg == "--tests" || strings.HasPrefix(arg, "--tests=") {
						t.Fatalf("gate-rejected task must not be filtered: args = %v", cmd.Args)
					}
				}
			}
			for i, arg := range cmd.Args {
				if arg != "--tests" || i+1 >= len(cmd.Args) {
					continue
				}
				value := cmd.Args[i+1]
				if strings.ContainsAny(value, "#(") {
					t.Fatalf("Gradle filter %q must stay class-level (no method selector)", value)
				}
			}
		})
	}
}

// TestGradleMaterializeRunsWholeTaskWhenTaskTypeIsUnknown pins LLD §8.2 line
// 590: when the adapter cannot tell that a task supports --tests, the whole
// task runs instead of a silently narrowed selection. The changed test file
// is supplied so that a regressed gate would emit a --tests argument.
func TestGradleMaterializeRunsWholeTaskWhenTaskTypeIsUnknown(t *testing.T) {
	keys := []string{":app:compileTestJava", ":app:compileTestKotlin", ":app:testClasses", ":app:latest"}
	ctx := context.Background()
	a := New()
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			cmd, err := a.Materialize(ctx, adapters.MaterializeRequest{
				Root:         gradleFixtureRoot,
				Config:       adapters.AdapterConfig{Enabled: true},
				TargetKeys:   []string{key},
				KnownTargets: []string{":app:test", key},
				ChangedFiles: []string{"app/src/test/java/com/example/app/AppTest.java"},
			})
			if err != nil {
				t.Fatalf("Materialize: %v", err)
			}
			wantArgs := []string{key, "--console=plain"}
			if !reflect.DeepEqual(cmd.Args, wantArgs) {
				t.Fatalf("unknown-type task must run whole: args = %v, want %v", cmd.Args, wantArgs)
			}
			if cmd.Filtered {
				t.Fatalf("unknown-type task must not be marked filtered: %+v", cmd)
			}
			for _, arg := range cmd.Args {
				if arg == "--tests" || strings.HasPrefix(arg, "--tests=") {
					t.Fatalf("unknown-type task must not receive --tests: args = %v", cmd.Args)
				}
			}
			if cmd.TargetKey != key {
				t.Fatalf("materialized TargetKey = %q, want %q", cmd.TargetKey, key)
			}
		})
	}
}

// gradleCandidateCapture answers discovery with a project whose task list
// holds a real test task next to the known name-heuristic false positive
// (compileTestJava).
func gradleCandidateCapture(t *testing.T) func(context.Context, string, string, []string, int64) ([]byte, []byte, error) {
	t.Helper()
	return func(_ context.Context, _ string, _ string, args []string, _ int64) ([]byte, []byte, error) {
		switch args[0] {
		case "--version":
			return []byte("Gradle 8.5\n"), nil, nil
		case "projects":
			return []byte("Root project 'sample'\n"), nil, nil
		case "tasks":
			return []byte("Verification tasks\n-------------------\n test - Runs the test suite\n compileTestJava - Compiles test sources\n"), nil, nil
		case "dependencies":
			return []byte(""), nil, nil
		}
		t.Fatalf("unexpected gradle argv %v", args)
		return nil, nil, nil
	}
}

// TestGradleDiscoveryDeclaresClassLevelOnlyCapability pins the honest
// capability contract of ADR 0008: the name heuristic keeps compile-only
// tasks as candidates (they fail at execution time as ZERO_TESTS, never as a
// pass), every candidate is declared at GRADLE_TASK granularity with
// Filterable=true, and nothing in the adapter capabilities claims method-level
// selection.
func TestGradleDiscoveryDeclaresClassLevelOnlyCapability(t *testing.T) {
	ctx := context.Background()
	a := New()
	a.capture = gradleCandidateCapture(t)

	result, err := a.Discover(ctx, adapters.DiscoverRequest{
		Root:           t.TempDir(),
		Config:         adapters.AdapterConfig{Enabled: true, MaxOutputBytes: 1 << 20},
		MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	candidates := map[string]adapters.TestUnit{}
	for _, unit := range result.Units {
		candidates[unit.Key] = unit
	}
	for _, key := range []string{":test", ":compileTestJava"} {
		unit, ok := candidates[key]
		if !ok {
			t.Fatalf("discovery must keep candidate %q; got %+v", key, result.Units)
		}
		if unit.SelectableGranularity != "GRADLE_TASK" {
			t.Fatalf("candidate %q granularity = %q, want GRADLE_TASK", key, unit.SelectableGranularity)
		}
		if !unit.Filterable {
			t.Fatalf("candidate %q must stay filterable (the gate decides per task): %+v", key, unit)
		}
		if strings.Contains(strings.ToUpper(unit.SelectableGranularity), "METHOD") {
			t.Fatalf("candidate %q must not claim method-level selection: %+v", key, unit)
		}
	}

	caps := New().Capabilities()
	if caps.SelectableGranularity != "GRADLE_TASK" {
		t.Fatalf("capability granularity = %q, want GRADLE_TASK", caps.SelectableGranularity)
	}
	if !caps.SupportsFiltering {
		t.Fatalf("capabilities must declare supported filtering: %+v", caps)
	}
	if caps.CoveragePrecision != "CLASS_OR_FILE" {
		t.Fatalf("capability coverage precision = %q, want CLASS_OR_FILE", caps.CoveragePrecision)
	}
	if strings.Contains(strings.ToUpper(caps.SelectableGranularity), "METHOD") {
		t.Fatalf("capabilities must not claim method-level selection: %+v", caps)
	}
}
