package generic

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ivp/ivp/internal/adapters"
)

func TestMaterializeKeepsArgvAndExpandsWholeArgumentPlaceholders(t *testing.T) {
	root := t.TempDir()
	cfg := adapters.AdapterConfig{Enabled: true, AllowedExecutables: []string{"python"}, Targets: []adapters.TargetSpec{{ID: "unit", Kind: "TEST", Argv: []string{"python", "-c", "print('literal value')", "{changed_files}", "{selected_tests}"}, MapsPaths: []string{"src/**"}}}}
	a := New()
	if err := a.ValidateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := a.Materialize(context.Background(), adapters.MaterializeRequest{Root: root, Config: cfg, TargetKeys: []string{"unit"}, ChangedFiles: []string{"src/a.py"}, SelectedTests: []string{"tests/a.py::test_one"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-c", "print('literal value')", "src/a.py", "tests/a.py::test_one"}
	if !reflect.DeepEqual(got.Args, want) {
		t.Fatalf("argv = %#v, want %#v", got.Args, want)
	}
	if got.Executable != "python" || got.Filtered == false {
		t.Fatalf("unexpected command: %+v", got)
	}
}

func TestGenericRejectsShellInjectionAndTraversal(t *testing.T) {
	root := t.TempDir()
	a := New()
	cases := []struct {
		name    string
		config  adapters.AdapterConfig
		changed []string
	}{
		{"shell substitution in argument", adapters.AdapterConfig{Enabled: true, AllowedExecutables: []string{"python"}, Targets: []adapters.TargetSpec{{ID: "x", Argv: []string{"python", "$(touch /tmp/pwned)"}}}}, nil},
		{"path traversal placeholder", adapters.AdapterConfig{Enabled: true, AllowedExecutables: []string{"python"}, Targets: []adapters.TargetSpec{{ID: "x", Argv: []string{"python", "{changed_files}"}}}}, []string{"../outside.py"}},
		{"unallowlisted binary", adapters.AdapterConfig{Enabled: true, Targets: []adapters.TargetSpec{{ID: "x", Argv: []string{"sh", "-c", "echo unsafe"}}}}, nil},
		{"allowlisted shell", adapters.AdapterConfig{Enabled: true, AllowedExecutables: []string{"sh"}, Targets: []adapters.TargetSpec{{ID: "x", Argv: []string{"sh", "-c", "echo unsafe"}}}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := a.ValidateConfig(tc.config); err != nil {
				if tc.changed == nil {
					return
				}
				t.Fatalf("unexpected config error: %v", err)
			}
			_, err := a.Materialize(context.Background(), adapters.MaterializeRequest{Root: root, Config: tc.config, TargetKeys: []string{"x"}, ChangedFiles: tc.changed})
			if err == nil {
				t.Fatal("unsafe command/path accepted")
			}
		})
	}
}

func TestGenericRejectsUnknownTargetsAndDependencyCycles(t *testing.T) {
	a := New()
	root := t.TempDir()
	cfg := adapters.AdapterConfig{Enabled: true, AllowedExecutables: []string{"python"}, Targets: []adapters.TargetSpec{{ID: "known", Argv: []string{"python"}}}}
	_, err := a.Materialize(context.Background(), adapters.MaterializeRequest{Root: root, Config: cfg, TargetKeys: []string{"not-in-snapshot"}})
	if !errors.Is(err, adapters.ErrUnknownTarget) {
		t.Fatalf("expected unknown target rejection, got %v", err)
	}
	cfg.Targets = []adapters.TargetSpec{{ID: "a", Argv: []string{"python"}, DependsOn: []string{"b"}}, {ID: "b", Argv: []string{"python"}, DependsOn: []string{"a"}}}
	if err := a.ValidateConfig(cfg); err == nil {
		t.Fatal("dependency cycle accepted")
	}
}

func TestGenericDiscoversCheckKind(t *testing.T) {
	a := New()
	result, err := a.Discover(context.Background(), adapters.DiscoverRequest{Root: t.TempDir(), Config: adapters.AdapterConfig{Enabled: true, AllowedExecutables: []string{"make"}, Targets: []adapters.TargetSpec{{ID: "format", Kind: "CHECK", Argv: []string{"make", "format"}, Mandatory: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || len(result.Units) != 1 || result.Units[0].Kind != "CHECK" || !result.Units[0].Mandatory {
		t.Fatalf("unexpected discovery: %+v", result)
	}
}
