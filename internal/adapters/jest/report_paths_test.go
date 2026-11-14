package jest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/execution"
)

// P0-7 regression (same defect as pytest): discovery advertises a glob over
// the hashed report Materialize writes (`.ivp/reports/jest-<hash>.json`);
// the fixed `.ivp/reports/jest.json` name matched nothing (ADR 0003).
func TestDiscoveryReportPathMatchesMaterializedReport(t *testing.T) {
	root := t.TempDir()
	unit := discoveredUnit("tests/a.test.js")
	wantPatterns := []string{".ivp/reports/jest*.json", "build/jest-junit/jest*.xml"}
	if len(unit.ReportPaths) != len(wantPatterns) || unit.ReportPaths[0] != wantPatterns[0] || unit.ReportPaths[1] != wantPatterns[1] {
		t.Fatalf("jest discovery report paths changed: %v", unit.ReportPaths)
	}

	command, err := New().Materialize(context.Background(), adapters.MaterializeRequest{
		Root: root, TargetKeys: []string{unit.Key}, KnownTargets: []string{unit.Key},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(command.ReportPaths) != 2 || !strings.HasPrefix(command.ReportPaths[0], ".ivp/reports/jest-") || !strings.HasPrefix(command.ReportPaths[1], "build/jest-junit/jest-") {
		t.Fatalf("materialized report path unexpected: %v", command.ReportPaths)
	}
	for _, report := range command.ReportPaths {
		absReport := filepath.Join(root, filepath.FromSlash(report))
		if err := os.MkdirAll(filepath.Dir(absReport), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absReport, []byte(`{"numTotalTests":0}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	fresh := execution.FreshReports(root, unit.ReportPaths, nil)
	if len(fresh) != 2 {
		t.Fatalf("discovery patterns %v did not resolve the materialized reports %v; fresh=%v", unit.ReportPaths, command.ReportPaths, fresh)
	}
	if filepath.ToSlash(fresh[0]) != filepath.ToSlash(command.ReportPaths[0]) {
		t.Fatalf("discovery pattern %v did not resolve the materialized report %s; fresh=%v", unit.ReportPaths, command.ReportPaths[0], fresh)
	}
}
