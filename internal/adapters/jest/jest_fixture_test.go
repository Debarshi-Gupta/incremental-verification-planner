package jest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ivp/ivp/internal/adapters"
)

// jestFixtureRoot is the §17.3 fixture repository exercised by this test. The
// jest binary is unavailable in the fixture environment, so --version,
// --listTests, and --findRelatedTests are answered through the adapter's
// injectable capture seam with the output of the equivalent real jest run.
const jestFixtureRoot = "../../../examples/sample-jest"

// jestFixtureCapture answers jest's three probes. The listTests outputs are
// absolute paths built from the repository root at call time, which exercises
// Discover's absolute-path normalization branch (ResolveWithin + filepath.Rel)
// exactly as a native jest run would report them.
func jestFixtureCapture(t *testing.T) func(context.Context, string, string, []string, int64) ([]byte, []byte, error) {
	t.Helper()
	return func(_ context.Context, root, executable string, args []string, _ int64) ([]byte, []byte, error) {
		// The fixture carries no node_modules/.bin/jest, so the adapter must
		// fall back to npx with the --no-install prefix.
		if executable != "npx" {
			t.Errorf("jest command executable = %q, want npx", executable)
		}
		if len(args) < 2 || args[0] != "--no-install" || args[1] != "jest" {
			t.Errorf("jest argv must carry the npx --no-install prefix: %v", args)
		}
		rest := args[2:]
		absRoot, err := adapters.ResolveWithin(root, ".")
		if err != nil {
			t.Fatalf("resolve fixture root: %v", err)
		}
		abs := func(rel string) string {
			return filepath.Join(absRoot, filepath.FromSlash(rel))
		}
		has := func(flag string) bool {
			for _, arg := range rest {
				if arg == flag {
					return true
				}
			}
			return false
		}
		switch {
		case len(rest) == 1 && rest[0] == "--version":
			return []byte("29.7.0\n"), nil, nil
		case has("--findRelatedTests"):
			// Only the test covering the changed core module is related.
			out, err := json.Marshal([]string{abs("packages/core/math.test.js")})
			if err != nil {
				t.Fatalf("marshal related output: %v", err)
			}
			return out, nil, nil
		case has("--listTests"):
			out, err := json.Marshal([]string{
				abs("packages/core/math.test.js"),
				abs("packages/ui/label.test.js"),
			})
			if err != nil {
				t.Fatalf("marshal listTests output: %v", err)
			}
			return out, nil, nil
		default:
			t.Fatalf("jest fixture has no canned output for argv %v", args)
			return nil, nil, nil
		}
	}
}

// fixtureUnit returns the discovered unit with the given key or fails the
// test.
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

// TestJestFixtureDetectDiscoverMaterialize implements inventory row
// IVP-17-3-4b5958b676 (§17.3 examples/sample-jest): package.json detection,
// absolute-path test listing through the capture seam, related-test
// candidate narrowing from a changed source file, and test-file
// materialization with the npx prefix and a hashed report path.
func TestJestFixtureDetectDiscoverMaterialize(t *testing.T) {
	ctx := context.Background()
	a := New()
	a.capture = jestFixtureCapture(t)

	detection, err := a.Detect(ctx, jestFixtureRoot)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !detection.Detected {
		t.Fatalf("fixture must be detected as a jest repository: %+v", detection)
	}
	if len(detection.Evidence) == 0 || detection.Evidence[0] != "package.json" {
		t.Fatalf("detection evidence = %v, want package.json first", detection.Evidence)
	}

	version, err := a.Version(ctx, jestFixtureRoot)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version != "29.7.0" {
		t.Fatalf("Version = %q, want %q", version, "29.7.0")
	}

	result, err := a.Discover(ctx, adapters.DiscoverRequest{
		Root:           jestFixtureRoot,
		Config:         adapters.AdapterConfig{Enabled: true, MaxOutputBytes: 1 << 20},
		ChangedFiles:   []string{"packages/core/math.js"},
		MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !result.Complete || len(result.Reasons) != 0 {
		t.Fatalf("fixture discovery must be complete: complete=%v reasons=%v", result.Complete, result.Reasons)
	}
	if result.Version != "29.7.0" {
		t.Fatalf("Discover version = %q, want 29.7.0", result.Version)
	}
	// The absolute --listTests paths must normalize to repository-relative
	// unit keys.
	if len(result.Units) != 2 {
		t.Fatalf("discovered %d units, want 2: %+v", len(result.Units), result.Units)
	}
	math := fixtureUnit(t, result.Units, "packages/core/math.test.js")
	if math.Kind != "TEST_FILE" || math.SelectableGranularity != "JEST_TEST_FILE" || !math.Filterable {
		t.Fatalf("math.test.js unit mismatch: %+v", math)
	}
	if !reflect.DeepEqual(math.Paths, []string{"packages/core/math.test.js"}) {
		t.Fatalf("math.test.js paths = %v, want [packages/core/math.test.js]", math.Paths)
	}
	if !reflect.DeepEqual(math.ReportPaths, []string{".ivp/reports/jest*.json", "build/jest-junit/jest*.xml"}) {
		t.Fatalf("math.test.js report paths = %v, want [.ivp/reports/jest*.json build/jest-junit/jest*.xml]", math.ReportPaths)
	}
	fixtureUnit(t, result.Units, "packages/ui/label.test.js")

	// --findRelatedTests over the changed core module narrows the affected
	// set to the single covering test file.
	wantAffected := []string{"packages/core/math.test.js"}
	if !reflect.DeepEqual(result.AffectedTargets, wantAffected) {
		t.Fatalf("affected targets = %v, want %v", result.AffectedTargets, wantAffected)
	}

	cmd, err := a.Materialize(ctx, adapters.MaterializeRequest{
		Root:         jestFixtureRoot,
		Config:       adapters.AdapterConfig{Enabled: true},
		TargetKeys:   []string{"packages/core/math.test.js"},
		KnownTargets: []string{"packages/core/math.test.js", "packages/ui/label.test.js"},
	})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if cmd.Executable != "npx" {
		t.Fatalf("materialized executable = %q, want npx", cmd.Executable)
	}
	key := "packages/core/math.test.js"
	wantReport := ".ivp/reports/jest-" + shortHash(key) + ".json"
	wantJUnit := "build/jest-junit/jest-" + shortHash(key) + ".xml"
	// The run must carry the JUnit reporter next to the default reporter, and
	// the reporter module must exist in the workspace.
	var reporterArg string
	for _, arg := range cmd.Args {
		if strings.HasPrefix(arg, "--reporters=") && arg != "--reporters=default" {
			reporterArg = arg
		}
	}
	if reporterArg == "" {
		t.Fatalf("materialized args must carry the JUnit reporter: %v", cmd.Args)
	}
	reporter := strings.TrimPrefix(reporterArg, "--reporters=")
	if !filepath.IsAbs(reporter) {
		t.Fatalf("JUnit reporter path = %q, want an absolute workspace path", reporter)
	}
	if _, err := os.Stat(reporter); err != nil {
		t.Fatalf("JUnit reporter module was not written: %v", err)
	}
	wantArgs := []string{"--no-install", "jest", "--runTestsByPath", key, "--reporters=default", reporterArg, "--json", "--outputFile=" + wantReport}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("materialized args = %v, want %v", cmd.Args, wantArgs)
	}
	if !cmd.Filtered || cmd.TargetKey != key {
		t.Fatalf("materialized command must be a filtered test-file selection: %+v", cmd)
	}
	if !reflect.DeepEqual(cmd.ReportPaths, []string{wantReport, wantJUnit}) {
		t.Fatalf("materialized report paths = %v, want [%s %s]", cmd.ReportPaths, wantReport, wantJUnit)
	}
}
