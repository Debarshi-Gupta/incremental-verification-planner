package bazel

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ivp/ivp/internal/adapters"
)

// bepMixedReport is a build-event report with one passing, one failing, one
// skipped, one timed out and one flaky test target.
const bepMixedReport = `{"id":{"testResult":{"label":"//app:passing_test"}},"testResult":{"label":"//app:passing_test","status":"PASSED","testActionExitCode":0}}
{"id":{"testResult":{"label":"//app:failing_test"}},"testResult":{"label":"//app:failing_test","status":"FAILED","testActionExitCode":1}}
{"id":{"testResult":{"label":"//app:skipped_test"}},"testResult":{"label":"//app:skipped_test","status":"SKIPPED","skipped":true}}
{"id":{"testResult":{"label":"//app:timeout_test"}},"testResult":{"label":"//app:timeout_test","status":"TIMEOUT","timeout":true}}
{"id":{"testResult":{"label":"//app:flaky_test"}},"testResult":{"label":"//app:flaky_test","status":"FLAKY","runAttempts":[{"status":"FAILED"},{"status":"PASSED"}]}}
`

// writeBEPReport materializes a build-event report below root.
func writeBEPReport(t *testing.T, root, relative, content string) {
	t.Helper()
	absolute := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatalf("create report directory: %v", err)
	}
	if err := os.WriteFile(absolute, []byte(content), 0o600); err != nil {
		t.Fatalf("write report: %v", err)
	}
}

// TestParseBEPClassifiesSkippedTimeoutAndFlakyOutcomes proves that the BEP
// parser classifies the canonical outcomes of the shared adapters vocabulary:
// passed, failed, skipped, timeout and flaky.
func TestParseBEPClassifiesSkippedTimeoutAndFlakyOutcomes(t *testing.T) {
	got, err := parseBEP([]byte(bepMixedReport), 1)
	if err != nil {
		t.Fatalf("parseBEP: %v", err)
	}
	if !got.Complete || !got.HasReliableReport {
		t.Fatalf("result is not complete and reliable: %+v", got)
	}
	if got.Tests != 5 || got.Passed != 2 || got.Failed != 1 || got.Skipped != 1 || got.Errors != 1 {
		t.Fatalf("BEP counters = tests %d passed %d failed %d skipped %d errors %d, want 5/2/1/1/1: %+v", got.Tests, got.Passed, got.Failed, got.Skipped, got.Errors, got)
	}
	want := []adapters.TestCase{
		{Name: "//app:failing_test", Status: "failed"},
		{Name: "//app:flaky_test", Status: "failed"},
		{Name: "//app:flaky_test", Status: "passed"},
		{Name: "//app:passing_test", Status: "passed"},
		{Name: "//app:skipped_test", Status: "skipped"},
		{Name: "//app:timeout_test", Status: "error"},
	}
	if len(got.Cases) != len(want) {
		t.Fatalf("parsed %d cases, want %d: %+v", len(got.Cases), len(want), got.Cases)
	}
	for i, expected := range want {
		if got.Cases[i].Name != expected.Name || got.Cases[i].Status != expected.Status {
			t.Fatalf("case %d = %+v, want %+v", i, got.Cases[i], expected)
		}
	}
	// The flaky target is the one whose attempts disagree.
	flaky := ""
	for _, reason := range got.Reasons {
		if strings.Contains(reason, "flaky") {
			flaky = reason
		}
	}
	if flaky == "" {
		t.Fatalf("the flaky test target was not reported: %v", got.Reasons)
	}
}

// TestParseBEPTimeoutFlagIsClassifiedAsError proves that the explicit
// timeout flag of a build-event record is classified as an error even when
// the status enum does not name the timeout.
func TestParseBEPTimeoutFlagIsClassifiedAsError(t *testing.T) {
	got, err := parseBEP([]byte(`{"testResult":{"label":"//app:slow_test","status":"FAILED","timeout":true}}`+"\n"), 1)
	if err != nil {
		t.Fatalf("parseBEP: %v", err)
	}
	if got.Errors != 1 || got.Failed != 0 || got.Tests != 1 {
		t.Fatalf("timeout counters = tests %d failed %d errors %d, want 1/0/1: %+v", got.Tests, got.Failed, got.Errors, got)
	}
	if len(got.Cases) != 1 || got.Cases[0].Status != "error" {
		t.Fatalf("timeout case = %+v, want one error case", got.Cases)
	}
}

// TestParseResultsMergesEveryDeclaredReportFile proves that every declared
// report file is consumed and that the merged result does not depend on the
// order in which the report paths were declared.
func TestParseResultsMergesEveryDeclaredReportFile(t *testing.T) {
	root := t.TempDir()
	first := ".ivp/reports/bazel-first.jsonl"
	second := ".ivp/reports/bazel-second.jsonl"
	writeBEPReport(t, root, first, `{"testResult":{"label":"//app:alpha_test","status":"PASSED","testActionExitCode":0}}`+"\n")
	writeBEPReport(t, root, second, `{"testResult":{"label":"//app:beta_test","status":"FAILED","testActionExitCode":1}}`+"\n")

	adapter := New()
	forward, err := adapter.ParseResults(context.Background(), adapters.ParseRequest{Root: root, ReportPaths: []string{first, second}, ExitCode: 1})
	if err != nil {
		t.Fatalf("ParseResults: %v", err)
	}
	reverse, err := adapter.ParseResults(context.Background(), adapters.ParseRequest{Root: root, ReportPaths: []string{second, first}, ExitCode: 1})
	if err != nil {
		t.Fatalf("ParseResults: %v", err)
	}
	if forward.Tests != 2 || forward.Passed != 1 || forward.Failed != 1 {
		t.Fatalf("merged counters = tests %d passed %d failed %d, want 2/1/1: %+v", forward.Tests, forward.Passed, forward.Failed, forward)
	}
	want := []adapters.TestCase{
		{Name: "//app:alpha_test", Status: "passed"},
		{Name: "//app:beta_test", Status: "failed"},
	}
	if !reflect.DeepEqual(forward.Cases, want) {
		t.Fatalf("merged cases = %+v, want %+v", forward.Cases, want)
	}
	if !reflect.DeepEqual(forward, reverse) {
		t.Fatalf("merged result depends on the report order:\n%+v\n%+v", forward, reverse)
	}
}

// TestParseResultsConsumesEveryGlobMatchedReportFile proves that a glob
// report pattern consumes every matching build-event report.
func TestParseResultsConsumesEveryGlobMatchedReportFile(t *testing.T) {
	root := t.TempDir()
	writeBEPReport(t, root, ".ivp/reports/bazel-a.jsonl", `{"testResult":{"label":"//app:alpha_test","status":"PASSED","testActionExitCode":0}}`+"\n")
	writeBEPReport(t, root, ".ivp/reports/bazel-b.jsonl", `{"testResult":{"label":"//app:beta_test","status":"PASSED","testActionExitCode":0}}`+"\n")
	writeBEPReport(t, root, ".ivp/reports/other-c.jsonl", `{"testResult":{"label":"//app:gamma_test","status":"PASSED","testActionExitCode":0}}`+"\n")

	got, err := New().ParseResults(context.Background(), adapters.ParseRequest{Root: root, ReportPaths: []string{".ivp/reports/bazel-*.jsonl"}, ExitCode: 0})
	if err != nil {
		t.Fatalf("ParseResults: %v", err)
	}
	if got.Tests != 2 || got.Passed != 2 {
		t.Fatalf("glob counters = tests %d passed %d, want 2/2: %+v", got.Tests, got.Passed, got)
	}
	for _, testCase := range got.Cases {
		if testCase.Name == "//app:gamma_test" {
			t.Fatalf("the glob pattern consumed a report it does not match: %+v", got.Cases)
		}
	}
}

// TestParseResultsReadsTheMaterializedReportFile proves that the report file
// the Materialize command writes is parsed, and that a second declared report
// is merged with it instead of being dropped.
func TestParseResultsReadsTheMaterializedReportFile(t *testing.T) {
	root := t.TempDir()
	adapter := New()
	command, err := adapter.Materialize(context.Background(), adapters.MaterializeRequest{
		Root:         root,
		Config:       adapters.AdapterConfig{Enabled: true},
		TargetKeys:   []string{"//app:alpha_test"},
		KnownTargets: []string{"//app:alpha_test"},
	})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if len(command.ReportPaths) != 1 {
		t.Fatalf("materialized report paths = %v, want one", command.ReportPaths)
	}
	writeBEPReport(t, root, command.ReportPaths[0], `{"testResult":{"label":"//app:alpha_test","status":"PASSED","testActionExitCode":0}}`+"\n")
	writeBEPReport(t, root, ".ivp/reports/bazel-extra.jsonl", `{"testResult":{"label":"//app:beta_test","status":"PASSED","testActionExitCode":0}}`+"\n")

	got, err := adapter.ParseResults(context.Background(), adapters.ParseRequest{Root: root, ReportPaths: []string{command.ReportPaths[0], ".ivp/reports/bazel-extra.jsonl"}, ExitCode: 0})
	if err != nil {
		t.Fatalf("ParseResults: %v", err)
	}
	if got.Tests != 2 || got.Passed != 2 {
		t.Fatalf("counters = tests %d passed %d, want 2/2: %+v", got.Tests, got.Passed, got)
	}
}
