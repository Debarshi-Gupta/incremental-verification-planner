package bazel

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ivp/ivp/internal/adapters"
)

// bazelCQueryCapture answers the unconfigured Bazel queries of the fixture
// repository and records every invocation. When configuredLabels is empty the
// configured query answers with the unconfigured label set.
func bazelCQueryCapture(t *testing.T, configuredLabels string, invoked *[][]string) func(context.Context, string, string, []string, int64) ([]byte, []byte, error) {
	t.Helper()
	return func(_ context.Context, _ string, _ string, args []string, _ int64) ([]byte, []byte, error) {
		if invoked != nil {
			*invoked = append(*invoked, append([]string(nil), args...))
		}
		joined := strings.Join(args, " ")
		switch {
		case len(args) > 0 && args[0] == "--version":
			return []byte("bazel 7.1.0\n"), nil, nil
		case len(args) > 0 && args[0] == "cquery" && strings.Contains(joined, "--output=graph"):
			return []byte(bazelConfiguredGraphOutput), nil, nil
		case len(args) > 0 && args[0] == "cquery":
			if configuredLabels == "" {
				return []byte(bazelConfiguredLabelOutput), nil, nil
			}
			return []byte(configuredLabels), nil, nil
		case strings.Contains(joined, "--output=graph"):
			return []byte(bazelGraphOutput), nil, nil
		case strings.Contains(joined, "rdeps("):
			return []byte(bazelRdepsOutput), nil, nil
		case strings.Contains(joined, "kind("):
			return []byte(bazelTestLabelOutput), nil, nil
		default:
			t.Fatalf("bazel fixture has no canned output for argv %v", args)
			return nil, nil, nil
		}
	}
}

// TestBazelCQueryFailureKeepsQueryDerivedEvidence proves the honest
// degradation of the configured query: when cquery cannot run, the
// query-derived target set survives, the discovery records why the
// configured evidence is missing, and no configured evidence is claimed.
func TestBazelCQueryFailureKeepsQueryDerivedEvidence(t *testing.T) {
	ctx := context.Background()
	a := New()
	a.capture = func(_ context.Context, _ string, _ string, args []string, _ int64) ([]byte, []byte, error) {
		if len(args) > 0 && args[0] == "cquery" {
			return nil, []byte("ERROR: no such command 'cquery'\n"), errors.New("exit status 2")
		}
		joined := strings.Join(args, " ")
		switch {
		case len(args) > 0 && args[0] == "--version":
			return []byte("bazel 7.1.0\n"), nil, nil
		case strings.Contains(joined, "--output=graph"):
			return []byte(bazelGraphOutput), nil, nil
		case strings.Contains(joined, "rdeps("):
			return []byte(bazelRdepsOutput), nil, nil
		case strings.Contains(joined, "kind("):
			return []byte(bazelTestLabelOutput), nil, nil
		default:
			t.Fatalf("bazel fixture has no canned output for argv %v", args)
			return nil, nil, nil
		}
	}

	result, err := a.Discover(ctx, adapters.DiscoverRequest{
		Root:           bazelFixtureRoot,
		Config:         adapters.AdapterConfig{Enabled: true, MaxOutputBytes: 1 << 20},
		ChangedFiles:   []string{"common/math.py"},
		MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("Discover must degrade instead of failing: %v", err)
	}
	if result.Complete {
		t.Fatalf("discovery must not be complete while the configured query is unresolved: %+v", result)
	}
	reason := ""
	for _, candidate := range result.Reasons {
		if strings.Contains(candidate, "cquery") {
			reason = candidate
		}
	}
	if reason == "" {
		t.Fatalf("discovery must record why the configured query is missing: %v", result.Reasons)
	}
	// The query-derived target set is retained.
	wantUnits := []string{"//api:service_test", "//common:math_test", "//web:app_test"}
	gotUnits := make([]string, 0, len(result.Units))
	for _, unit := range result.Units {
		gotUnits = append(gotUnits, unit.Key)
	}
	if !reflect.DeepEqual(gotUnits, wantUnits) {
		t.Fatalf("retained units = %v, want %v", gotUnits, wantUnits)
	}
	if !reflect.DeepEqual(result.AffectedTargets, wantUnits) {
		t.Fatalf("retained affected targets = %v, want %v", result.AffectedTargets, wantUnits)
	}
	// No configured evidence is claimed, and the unconfigured evidence keeps
	// its own confidence.
	for _, target := range result.BuildTargets {
		switch target.EvidenceSource {
		case "bazel:cquery-graph", "bazel:cquery-target-set":
			t.Fatalf("configured evidence claimed although cquery failed: %+v", target)
		case "bazel:target-package-membership":
			if target.Confidence != 0.50 {
				t.Fatalf("unconfigured test target confidence = %v, want 0.50: %+v", target.Confidence, target)
			}
		case "bazel:query-graph":
			if target.Confidence != 0.80 {
				t.Fatalf("unconfigured graph confidence = %v, want 0.80: %+v", target.Confidence, target)
			}
		default:
			t.Fatalf("unexpected evidence source %q: %+v", target.EvidenceSource, target)
		}
	}
}

// TestBazelCQueryConfiguredOnlyTargetIsDiscovered proves that a test target
// which only the configured query resolves becomes a discovered unit with the
// configured evidence source.
func TestBazelCQueryConfiguredOnlyTargetIsDiscovered(t *testing.T) {
	ctx := context.Background()
	a := New()
	a.capture = bazelCQueryCapture(t, "//common:math_test\n//api:service_test\n//web:app_test\n//common:config_test\n", nil)

	result, err := a.Discover(ctx, adapters.DiscoverRequest{
		Root:           bazelFixtureRoot,
		Config:         adapters.AdapterConfig{Enabled: true, MaxOutputBytes: 1 << 20},
		MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !result.Complete || len(result.Reasons) != 0 {
		t.Fatalf("discovery must be complete: complete=%v reasons=%v", result.Complete, result.Reasons)
	}
	found := false
	for _, unit := range result.Units {
		if unit.Key == "//common:config_test" {
			found = true
			if unit.SelectableGranularity != "BAZEL_TEST_TARGET" || !unit.Filterable {
				t.Errorf("configured unit granularity mismatch: %+v", unit)
			}
		}
	}
	if !found {
		t.Fatalf("configured-only test target was not discovered: %+v", result.Units)
	}
	target := bazelFixtureTarget(t, result.BuildTargets, "//common:config_test")
	if target.EvidenceSource != "bazel:cquery-target-set" || target.Confidence != 0.60 {
		t.Fatalf("configured-only target evidence mismatch: %+v", target)
	}
}

// TestBazelDiscoveryIsDeterministicAndNeverRunsTests proves that the
// configured query path keeps discovery deterministic and that discovery
// never executes a test.
func TestBazelDiscoveryIsDeterministicAndNeverRunsTests(t *testing.T) {
	ctx := context.Background()
	discover := func() (adapters.DiscoveryResult, [][]string) {
		a := New()
		var invoked [][]string
		a.capture = bazelCQueryCapture(t, "", &invoked)
		result, err := a.Discover(ctx, adapters.DiscoverRequest{
			Root:           bazelFixtureRoot,
			Config:         adapters.AdapterConfig{Enabled: true, MaxOutputBytes: 1 << 20},
			ChangedFiles:   []string{"common/math.py"},
			MaxOutputBytes: 1 << 20,
		})
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		return result, invoked
	}
	first, firstInvoked := discover()
	second, _ := discover()
	if !reflect.DeepEqual(first.Units, second.Units) {
		t.Fatalf("discovery units are not deterministic:\n%+v\n%+v", first.Units, second.Units)
	}
	if !reflect.DeepEqual(first.BuildTargets, second.BuildTargets) {
		t.Fatalf("discovery build targets are not deterministic:\n%+v\n%+v", first.BuildTargets, second.BuildTargets)
	}
	if !reflect.DeepEqual(first.AffectedTargets, second.AffectedTargets) {
		t.Fatalf("discovery affected targets are not deterministic: %v vs %v", first.AffectedTargets, second.AffectedTargets)
	}
	configured := 0
	for _, args := range firstInvoked {
		if len(args) == 0 {
			continue
		}
		switch args[0] {
		case "query", "cquery", "--version":
		default:
			t.Fatalf("discovery executed a command that is not a query: %v", args)
		}
		if args[0] == "cquery" {
			configured++
		}
	}
	if configured != 2 {
		t.Fatalf("expected the configured test-target query and the configured graph query, got %d cquery invocations: %v", configured, firstInvoked)
	}
}
