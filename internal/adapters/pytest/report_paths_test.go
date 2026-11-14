package pytest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/execution"
)

// P0-7 regression: discovery must advertise a report-path pattern that matches
// the hashed report Materialize actually writes
// (`.ivp/reports/pytest-<hash>.xml`); the fixed `.ivp/reports/pytest.xml` name
// matched nothing (ADR 0003).
func TestDiscoveryReportPathMatchesMaterializedReport(t *testing.T) {
	root := t.TempDir()
	fixture := []byte("tests/test_api.py::test_reads_health\n<Function tests/test_api.py::test_reads_health>\ncollected 1 item\n")
	discovery := parseNodeIDs(root, fixture)
	if !discovery.Complete || len(discovery.Units) == 0 {
		t.Fatalf("discovery failed: complete=%v reasons=%v", discovery.Complete, discovery.Reasons)
	}
	patterns := discovery.Units[0].ReportPaths
	if len(patterns) != 1 || !strings.Contains(patterns[0], "*") {
		t.Fatalf("discovery report paths are not glob patterns: %v", patterns)
	}

	command, err := New().Materialize(context.Background(), adapters.MaterializeRequest{
		Root: root, TargetKeys: []string{discovery.Units[0].Key},
		KnownTargets: []string{discovery.Units[0].Key},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(command.ReportPaths) != 1 || !strings.HasPrefix(command.ReportPaths[0], ".ivp/reports/pytest-") {
		t.Fatalf("materialized report path unexpected: %v", command.ReportPaths)
	}
	// Simulate the completed run: write the hashed report the command declares.
	absReport := filepath.Join(root, filepath.FromSlash(command.ReportPaths[0]))
	if err := os.MkdirAll(filepath.Dir(absReport), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absReport, []byte("<testsuite tests=\"1\"></testsuite>"), 0o600); err != nil {
		t.Fatal(err)
	}

	fresh := execution.FreshReports(root, patterns, nil)
	if len(fresh) != 1 || filepath.ToSlash(fresh[0]) != filepath.ToSlash(command.ReportPaths[0]) {
		t.Fatalf("discovery pattern %v did not resolve the materialized report %s; fresh=%v", patterns, command.ReportPaths[0], fresh)
	}
}
