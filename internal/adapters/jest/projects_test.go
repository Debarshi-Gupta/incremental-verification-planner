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

// writeProjectFixtureFile writes a file of a fixture repository.
func writeProjectFixtureFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// multiProjectRoot writes a repository whose root Jest configuration declares
// two projects: packages/core and packages/ui. tools/build.js belongs to no
// project.
func multiProjectRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeProjectFixtureFile(t, root, "jest.config.js", "module.exports = {\n"+
		"  // the core package owns its own tests\n"+
		"  projects: [\n"+
		"    '<rootDir>/packages/core',\n"+
		"    { displayName: 'ui', rootDir: '<rootDir>/packages/ui' },\n"+
		"  ],\n"+
		"};\n")
	writeProjectFixtureFile(t, root, "packages/core/math.js", "export const math = 1;\n")
	writeProjectFixtureFile(t, root, "packages/core/math.test.js", "test('math', () => {});\n")
	writeProjectFixtureFile(t, root, "packages/ui/label.js", "export const label = 'x';\n")
	writeProjectFixtureFile(t, root, "packages/ui/label.test.js", "test('label', () => {});\n")
	writeProjectFixtureFile(t, root, "tools/build.js", "module.exports = {};\n")
	return root
}

// multiProjectCapture answers the jest probes of the multi-project fixture
// with the output of the equivalent real jest run.
func multiProjectCapture(t *testing.T, related []string) func(context.Context, string, string, []string, int64) ([]byte, []byte, error) {
	t.Helper()
	return func(_ context.Context, root, executable string, args []string, _ int64) ([]byte, []byte, error) {
		rest := args
		if len(rest) >= 2 && rest[0] == "--no-install" && rest[1] == "jest" {
			rest = rest[2:]
		}
		absRoot, err := adapters.ResolveWithin(root, ".")
		if err != nil {
			t.Fatalf("resolve fixture root: %v", err)
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
			paths := make([]string, 0, len(related))
			for _, rel := range related {
				paths = append(paths, filepath.Join(absRoot, filepath.FromSlash(rel)))
			}
			out, err := json.Marshal(paths)
			if err != nil {
				t.Fatalf("marshal related output: %v", err)
			}
			return out, nil, nil
		case has("--listTests"):
			out, err := json.Marshal([]string{
				filepath.Join(absRoot, "packages", "core", "math.test.js"),
				filepath.Join(absRoot, "packages", "ui", "label.test.js"),
			})
			if err != nil {
				t.Fatalf("marshal listTests output: %v", err)
			}
			return out, nil, nil
		}
		t.Fatalf("jest fixture has no canned output for argv %v", args)
		return nil, nil, nil
	}
}

// containsReason reports whether the discovery reasons contain the text.
func containsReason(reasons []string, text string) bool {
	for _, reason := range reasons {
		if strings.Contains(reason, text) {
			return true
		}
	}
	return false
}

// TestReadJestProjectsResolvesProjectScopes proves that the projects array of
// the root configuration is read statically and mapped to repository relative
// scopes.
func TestReadJestProjectsResolvesProjectScopes(t *testing.T) {
	root := multiProjectRoot(t)
	projects, err := readJestProjects(root)
	if err != nil {
		t.Fatal(err)
	}
	if !projects.declared || projects.failed {
		t.Fatalf("projects = %+v, want a resolved projects declaration", projects)
	}
	want := []jestProject{{Scope: "packages/core"}, {Name: "ui", Scope: "packages/ui"}}
	if !reflect.DeepEqual(projects.projects, want) {
		t.Fatalf("projects = %+v, want %+v", projects.projects, want)
	}
}

// TestReadJestProjectsFromPackageJSON proves that the projects array of the
// package.json jest section is read as well.
func TestReadJestProjectsFromPackageJSON(t *testing.T) {
	root := t.TempDir()
	writeProjectFixtureFile(t, root, "package.json", `{"name":"fixture","jest":{"projects":["<rootDir>/packages/core",{"rootDir":"<rootDir>/packages/ui"}]}}`)
	projects, err := readJestProjects(root)
	if err != nil {
		t.Fatal(err)
	}
	if !projects.declared || projects.failed {
		t.Fatalf("projects = %+v, want a resolved projects declaration", projects)
	}
	want := []jestProject{{Scope: "packages/core"}, {Scope: "packages/ui"}}
	if !reflect.DeepEqual(projects.projects, want) {
		t.Fatalf("projects = %+v, want %+v", projects.projects, want)
	}
}

// TestReadJestProjectsWithoutProjectsArray proves that a configuration without
// a projects array stays a single project configuration.
func TestReadJestProjectsWithoutProjectsArray(t *testing.T) {
	root := t.TempDir()
	writeProjectFixtureFile(t, root, "jest.config.js", "module.exports = { testEnvironment: 'node' };\n")
	projects, err := readJestProjects(root)
	if err != nil {
		t.Fatal(err)
	}
	if projects.declared || projects.failed || len(projects.projects) != 0 {
		t.Fatalf("projects = %+v, want a single project configuration", projects)
	}
}

// TestReadJestProjectsRejectsUnresolvedDeclaration proves that a projects
// array that cannot be read statically is reported instead of being guessed.
func TestReadJestProjectsRejectsUnresolvedDeclaration(t *testing.T) {
	root := t.TempDir()
	writeProjectFixtureFile(t, root, "jest.config.js", "module.exports = { projects: [...sharedProjects] };\n")
	projects, err := readJestProjects(root)
	if err == nil {
		t.Fatal("an unreadable projects array must be reported")
	}
	if !projects.failed {
		t.Fatalf("projects = %+v, want a failed resolution", projects)
	}
}

// TestJestProjectResolutionPicksOwningProject proves that a path is resolved
// to the project whose scope contains it and that a path outside every
// project resolves to no project.
func TestJestProjectResolutionPicksOwningProject(t *testing.T) {
	root := multiProjectRoot(t)
	projects, err := readJestProjects(root)
	if err != nil {
		t.Fatal(err)
	}
	owners := projects.owners("packages/core/math.js")
	if len(owners) != 1 || owners[0].Scope != "packages/core" {
		t.Fatalf("owners of packages/core/math.js = %+v, want the core project", owners)
	}
	owners = projects.owners("packages/ui/label.js")
	if len(owners) != 1 || owners[0].Scope != "packages/ui" {
		t.Fatalf("owners of packages/ui/label.js = %+v, want the ui project", owners)
	}
	if owners = projects.owners("tools/build.js"); len(owners) != 0 {
		t.Fatalf("owners of tools/build.js = %+v, want no project", owners)
	}
}

// TestJestMultiProjectDiscoveryAttributesTargetsToProjects proves that the
// discovery of a multi-project configuration yields one target per project
// with the right scope and that the affected target is resolved to the
// project that owns the changed path.
func TestJestMultiProjectDiscoveryAttributesTargetsToProjects(t *testing.T) {
	root := multiProjectRoot(t)
	a := New()
	a.capture = multiProjectCapture(t, []string{"packages/core/math.test.js"})
	result, err := a.Discover(context.Background(), adapters.DiscoverRequest{
		Root:           root,
		Config:         adapters.AdapterConfig{Enabled: true},
		ChangedFiles:   []string{"packages/core/math.js"},
		MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !result.Complete || len(result.Reasons) != 0 {
		t.Fatalf("multi-project discovery must be complete: complete=%v reasons=%v", result.Complete, result.Reasons)
	}
	if len(result.Units) != 2 {
		t.Fatalf("discovered %d units, want one per project: %+v", len(result.Units), result.Units)
	}
	core := fixtureUnit(t, result.Units, "packages/core/math.test.js")
	if core.Module != "packages/core" {
		t.Fatalf("core unit module = %q, want packages/core", core.Module)
	}
	ui := fixtureUnit(t, result.Units, "packages/ui/label.test.js")
	if ui.Module != "packages/ui" {
		t.Fatalf("ui unit module = %q, want packages/ui", ui.Module)
	}
	if !reflect.DeepEqual(result.AffectedTargets, []string{"packages/core/math.test.js"}) {
		t.Fatalf("affected targets = %v, want the test of the project that owns the changed path", result.AffectedTargets)
	}
}

// TestJestMultiProjectDiscoveryReportsPathOutsideEveryProject proves that a
// changed path that belongs to no configured project is reported instead of
// selecting a unit of an unrelated project.
func TestJestMultiProjectDiscoveryReportsPathOutsideEveryProject(t *testing.T) {
	root := multiProjectRoot(t)
	a := New()
	a.capture = multiProjectCapture(t, []string{"packages/core/math.test.js"})
	result, err := a.Discover(context.Background(), adapters.DiscoverRequest{
		Root:           root,
		Config:         adapters.AdapterConfig{Enabled: true},
		ChangedFiles:   []string{"tools/build.js"},
		MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if result.Complete {
		t.Fatalf("discovery must be incomplete for a path outside every project: %+v", result)
	}
	if !containsReason(result.Reasons, "Jest changed path is outside every configured Jest project") {
		t.Fatalf("reasons = %v, want the unmatched project reason", result.Reasons)
	}
	if len(result.AffectedTargets) != 0 {
		t.Fatalf("affected targets = %v, want no selection for an unmatched path", result.AffectedTargets)
	}
}

// TestJestMultiProjectDiscoveryReportsMisattributedCandidate proves that a
// related-test candidate of a project that owns none of the changed paths is
// reported instead of being selected.
func TestJestMultiProjectDiscoveryReportsMisattributedCandidate(t *testing.T) {
	root := multiProjectRoot(t)
	a := New()
	a.capture = multiProjectCapture(t, []string{"packages/ui/label.test.js"})
	result, err := a.Discover(context.Background(), adapters.DiscoverRequest{
		Root:           root,
		Config:         adapters.AdapterConfig{Enabled: true},
		ChangedFiles:   []string{"packages/core/math.js"},
		MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if result.Complete {
		t.Fatalf("discovery must be incomplete for a mis-attributed candidate: %+v", result)
	}
	if !containsReason(result.Reasons, "Jest related-test candidate belongs to a project that owns none of the changed paths") {
		t.Fatalf("reasons = %v, want the mis-attribution reason", result.Reasons)
	}
	if len(result.AffectedTargets) != 0 {
		t.Fatalf("affected targets = %v, want no mis-attributed selection", result.AffectedTargets)
	}
}

// TestJestMultiProjectDiscoveryReportsUnresolvedProjects proves that a
// projects array that cannot be read statically makes the discovery
// incomplete instead of guessing a project layout.
func TestJestMultiProjectDiscoveryReportsUnresolvedProjects(t *testing.T) {
	root := t.TempDir()
	writeProjectFixtureFile(t, root, "jest.config.js", "module.exports = { projects: [...sharedProjects] };\n")
	writeProjectFixtureFile(t, root, "packages/core/math.test.js", "test('math', () => {});\n")
	a := New()
	a.capture = multiProjectCapture(t, nil)
	result, err := a.Discover(context.Background(), adapters.DiscoverRequest{
		Root:           root,
		Config:         adapters.AdapterConfig{Enabled: true},
		ChangedFiles:   []string{"packages/core/math.js"},
		MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if result.Complete {
		t.Fatalf("discovery must be incomplete for an unresolved project layout: %+v", result)
	}
	if !containsReason(result.Reasons, "Jest project configuration could not be resolved") {
		t.Fatalf("reasons = %v, want the unresolved project reason", result.Reasons)
	}
}
