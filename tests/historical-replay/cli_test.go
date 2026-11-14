package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestRunReportsPeriodsWithoutBreakingTopLevelFields runs the CLI over the
// checked-in corpus and asserts the §17.8 row IVP-17-8-0044156b18 output:
// each chronological period's boundary, record count and summaries are
// reported, while every pre-existing top-level field (input_records, whole-
// input summaries, risk_based_ci_eligible=false, eligibility_note) is kept
// unchanged so current consumers do not break.
func TestRunReportsPeriodsWithoutBreakingTopLevelFields(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{corpusFile}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}
	var report struct {
		InputRecords int       `json:"input_records"`
		Summaries    []Summary `json:"summaries"`
		Periods      []struct {
			Period      string    `json:"period"`
			Start       string    `json:"start"`
			End         string    `json:"end"`
			RecordCount int       `json:"record_count"`
			Summaries   []Summary `json:"summaries"`
		} `json:"periods"`
		RiskBasedCIEligible bool   `json:"risk_based_ci_eligible"`
		EligibilityNote     string `json:"eligibility_note"`
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		t.Fatalf("decode CLI output: %v\n%s", err, stdout.String())
	}
	// Pre-existing top-level contract (extend, don't replace).
	if report.InputRecords < 2100 {
		t.Fatalf("input_records = %d, want >= 2100", report.InputRecords)
	}
	if len(report.Summaries) == 0 {
		t.Fatal("whole-input summaries must still be reported")
	}
	if report.RiskBasedCIEligible {
		t.Fatal("risk_based_ci_eligible must remain false (LLD 17.8 line 1044)")
	}
	const wantNote = "Eligibility remains disabled until organization-approved thresholds are met on representative held-out data."
	if report.EligibilityNote != wantNote {
		t.Fatalf("eligibility_note = %q, want %q", report.EligibilityNote, wantNote)
	}
	// Period extension: three named periods, RFC3339 boundaries, counts that
	// sum to the input, chained boundaries, and per-period summaries.
	if len(report.Periods) != 3 {
		t.Fatalf("got %d periods, want 3", len(report.Periods))
	}
	wantNames := []string{"train", "calibration", "evaluation"}
	total := 0
	var boundaries []time.Time
	for i, period := range report.Periods {
		if period.Period != wantNames[i] {
			t.Fatalf("period[%d] = %q, want %q", i, period.Period, wantNames[i])
		}
		start, err := time.Parse(time.RFC3339, period.Start)
		if err != nil {
			t.Fatalf("period %s start %q not RFC3339: %v", period.Period, period.Start, err)
		}
		end, err := time.Parse(time.RFC3339, period.End)
		if err != nil {
			t.Fatalf("period %s end %q not RFC3339: %v", period.Period, period.End, err)
		}
		if end.Before(start) {
			t.Fatalf("period %s ends (%s) before it starts (%s)", period.Period, period.End, period.Start)
		}
		boundaries = append(boundaries, start, end)
		total += period.RecordCount
		if period.RecordCount == 0 {
			t.Fatalf("period %s has no records for this corpus", period.Period)
		}
		if len(period.Summaries) == 0 {
			t.Fatalf("period %s reports no summaries", period.Period)
		}
	}
	if total != report.InputRecords {
		t.Fatalf("period record counts sum to %d, want input_records %d", total, report.InputRecords)
	}
	// Chained boundaries: train.End == cal.Start, cal.End == eval.Start.
	if !boundaries[1].Equal(boundaries[2]) || !boundaries[3].Equal(boundaries[4]) {
		t.Fatalf("period boundaries are not chained: %v", boundaries)
	}
	// Strict chronological ordering of the windows (no future leakage).
	if !(boundaries[1].Before(boundaries[4]) && boundaries[0].Before(boundaries[1]) && boundaries[2].Before(boundaries[3])) {
		t.Fatalf("period windows are not chronologically ordered: %v", boundaries)
	}
}

// TestRunRejectsBadUsageAndInput locks the pre-existing CLI exit codes.
func TestRunRejectsBadUsageAndInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"a", "b"}, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("two positional args: exit %d, want 2", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"testdata/does-not-exist.jsonl"}, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("missing file: exit %d, want 2", code)
	}
	stdout.Reset()
	stderr.Reset()
	bad := "{\"id\":\"x\",\"adapter\":\"jest\",\"risk_level\":\"LOW\"}\n" // no timestamp/change_class
	if code := run([]string{"-"}, strings.NewReader(bad), &stdout, &stderr); code != 2 {
		t.Fatalf("invalid input: exit %d, want 2", code)
	}
}
