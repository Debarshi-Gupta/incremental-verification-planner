package adapters_test

import (
	"context"
	"testing"

	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/adapters/bazel"
	"github.com/ivp/ivp/internal/adapters/generic"
	"github.com/ivp/ivp/internal/adapters/gradle"
	"github.com/ivp/ivp/internal/adapters/jest"
	"github.com/ivp/ivp/internal/adapters/maven"
	"github.com/ivp/ivp/internal/adapters/nx"
	"github.com/ivp/ivp/internal/adapters/pytest"
)

func TestSupportedAdaptersMaterializeOnlySnapshotKnownTargets(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name    string
		adapter adapters.Adapter
		key     string
		config  adapters.AdapterConfig
		unit    adapters.TestUnit
	}{
		{name: "gradle", adapter: gradle.New(), key: ":test"},
		{name: "maven", adapter: maven.New(), key: "#test"},
		{name: "bazel", adapter: bazel.New(), key: "//:suite"},
		{name: "nx", adapter: nx.New(), key: "app:test", unit: adapters.TestUnit{Key: "app:test", Kind: "TEST_CASE"}},
		{name: "pytest", adapter: pytest.New(), key: "tests/test_sample.py::test_one"},
		{name: "jest", adapter: jest.New(), key: "tests/sample.test.js"},
		{
			name:    "generic",
			adapter: generic.New(),
			key:     "unit",
			config: adapters.AdapterConfig{
				Targets:            []adapters.TargetSpec{{ID: "unit", Argv: []string{"python", "-m", "pytest", "{selected_tests}"}}},
				AllowedExecutables: []string{"python"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targets := []string{tt.key}
			req := adapters.MaterializeRequest{
				Root:          root,
				Config:        tt.config,
				TargetKeys:    targets,
				KnownTargets:  append([]string(nil), targets...),
				KnownUnits:    []adapters.TestUnit{tt.unit},
				SelectedTests: []string{"tests/test_sample.py::test_one"},
			}
			command, err := tt.adapter.Materialize(context.Background(), req)
			if err != nil {
				t.Fatalf("materialize known target: %v", err)
			}
			if command.Executable == "" || command.WorkingDirectory == "" {
				t.Fatalf("materialized command is incomplete: %+v", command)
			}
			if command.Timeout <= 0 || command.MaxOutputBytes <= 0 {
				t.Fatalf("command is missing bounded execution settings: %+v", command)
			}

			req.TargetKeys = []string{"not-in-the-snapshot"}
			if _, err := tt.adapter.Materialize(context.Background(), req); err == nil {
				t.Fatal("adapter materialized a target absent from the immutable snapshot")
			}
		})
	}
}
