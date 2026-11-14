//go:build native

// Package native_test runs the real build adapters against the checked-in
// sample projects with the native toolchains installed.
//
// The suite is the LLD 17.9 nightly job: it is not part of the pull-request
// gate because it needs the native tools (Gradle, Maven, Bazel, Node, Python)
// on the machine. Every case skips when its tool is missing, so the suite is
// still safe to run anywhere.
package native_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/adapters/builtin"
	"github.com/ivp/ivp/internal/config"
)

// sampleCases are the checked-in sample projects and the adapter that must
// discover them (WP-A fixture corpus).
var sampleCases = []struct {
	adapter string
	project string
	tool    string
}{
	{adapter: "gradle", project: "sample-gradle", tool: "gradle"},
	{adapter: "maven", project: "sample-maven", tool: "mvn"},
	{adapter: "bazel", project: "sample-bazel", tool: "bazel"},
	{adapter: "nx", project: "sample-nx", tool: "nx"},
	{adapter: "pytest", project: "sample-pytest", tool: "pytest"},
	{adapter: "jest", project: "sample-jest", tool: "jest"},
	{adapter: "generic", project: "sample-generic", tool: ""},
}

func sampleRoot(t *testing.T, project string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "examples", project))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("sample project %s is missing: %v", project, err)
	}
	return root
}

// adapterConfig builds the adapter view from the sample project's own
// repository configuration, exactly as the CLI does, so a sample that declares
// its units (the generic sample) is discovered the same way here.
func adapterConfig(t *testing.T, root, adapterID string) adapters.AdapterConfig {
	t.Helper()
	path := filepath.Join(root, ".ivp", "config.yaml")
	if _, err := os.Stat(path); err != nil {
		return adapters.AdapterConfig{Enabled: true}
	}
	loaded, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("sample configuration %s is invalid: %v", path, err)
	}
	return loaded.ToAdapterConfig(adapterID, nativeAllowedExecutables)
}

// nativeAllowedExecutables is the executable allowlist the nightly job
// grants to the sample projects; in production it comes from the signed
// organization policy.
var nativeAllowedExecutables = []string{
	"python", "python3", "pytest", "node", "npm", "npx", "bash", "sh",
	"mvn", "mvnw", "gradle", "gradlew", "bazel", "bazelisk", "nx",
}

// TestNativeAdaptersDiscoverTheSampleProjects runs every adapter against its
// sample project with the native toolchain and asserts that real units are
// discovered and can be materialized into a runnable command.
func TestNativeAdaptersDiscoverTheSampleProjects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for _, tc := range sampleCases {
		t.Run(tc.adapter, func(t *testing.T) {
			if tc.tool != "" {
				if _, err := exec.LookPath(tc.tool); err != nil {
					t.Skipf("%s is not installed on this machine: %v", tc.tool, err)
				}
			}
			adapter, err := builtin.ByID(tc.adapter)
			if err != nil {
				t.Fatalf("adapter %s is unavailable: %v", tc.adapter, err)
			}
			root := sampleRoot(t, tc.project)
			detection, err := adapter.Detect(ctx, root)
			if err != nil {
				t.Fatalf("%s detection failed: %v", tc.adapter, err)
			}
			if !detection.Detected {
				t.Skipf("%s did not detect %s", tc.adapter, tc.project)
			}
			result, err := adapter.Discover(ctx, adapters.DiscoverRequest{Root: root, Config: adapterConfig(t, root, tc.adapter), MaxOutputBytes: 8 << 20})
			if err != nil {
				t.Fatalf("%s discovery failed: %v", tc.adapter, err)
			}
			if len(result.Units) == 0 {
				t.Fatalf("%s discovered no unit in %s (reasons: %v)", tc.adapter, tc.project, result.Reasons)
			}
			known := make([]string, 0, len(result.Units))
			for _, unit := range result.Units {
				known = append(known, unit.Key)
			}
			command, err := adapter.Materialize(ctx, adapters.MaterializeRequest{
				Root: root, Config: adapterConfig(t, root, tc.adapter), TargetKeys: []string{result.Units[0].Key},
				KnownTargets: known, KnownUnits: result.Units, MaxOutputBytes: 8 << 20,
			})
			if err != nil {
				t.Fatalf("%s materialization failed: %v", tc.adapter, err)
			}
			if command.Executable == "" || len(command.Args) == 0 {
				t.Fatalf("%s materialized an empty command: %+v", tc.adapter, command)
			}
			t.Logf("%s discovered %d unit(s) in %s; first command: %s %v", tc.adapter, len(result.Units), tc.project, command.Executable, command.Args)
		})
	}
}
