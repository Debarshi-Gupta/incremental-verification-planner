package builtin_test

import (
	"context"
	"testing"

	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/adapters/builtin"
)

func TestAllBuiltinsExposeContractAndCapabilities(t *testing.T) {
	want := map[string]bool{"gradle": true, "maven": true, "bazel": true, "nx": true, "pytest": true, "jest": true, "generic": true}
	seen := map[string]bool{}
	for _, adapter := range builtin.All() {
		if !want[adapter.ID()] || seen[adapter.ID()] {
			t.Fatalf("unexpected or duplicate adapter %q", adapter.ID())
		}
		seen[adapter.ID()] = true
		caps := adapter.Capabilities()
		if caps.SelectableGranularity == "" || caps.DependencyPrecision == "" || caps.CoveragePrecision == "" {
			t.Errorf("%s omitted a capability precision: %+v", adapter.ID(), caps)
		}
		if err := adapter.ValidateConfig(adapters.AdapterConfig{}); err != nil {
			t.Errorf("%s rejected empty default config: %v", adapter.ID(), err)
		}
		if _, err := adapter.ParseResults(context.Background(), adapters.ParseRequest{Root: t.TempDir()}); err == nil {
			t.Errorf("%s treated a missing report as reliable", adapter.ID())
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("registered adapters = %v, want all seven", seen)
	}
}

func TestEveryAdapterRejectsUnknownSnapshotTargets(t *testing.T) {
	root := t.TempDir()
	for _, adapter := range builtin.All() {
		t.Run(adapter.ID(), func(t *testing.T) {
			_, err := adapter.Materialize(context.Background(), adapters.MaterializeRequest{Root: root, TargetKeys: []string{"//untrusted:target"}, KnownTargets: nil})
			if err == nil {
				t.Fatal("unknown target was materialized")
			}
		})
	}
}
