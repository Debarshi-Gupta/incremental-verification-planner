package main

import (
	"strings"
	"testing"
)

func TestSummarizeCountsAmbiguousAsMissAndExcludesIncomparable(t *testing.T) {
	records := []Record{
		{ID: "one", Adapter: "pytest", RiskLevel: "LOW", Timestamp: "2026-01-01T00:00:00Z", ChangeClass: "STANDARD", Comparable: true, SelectedRuntimeMillis: 10, FullRuntimeMillis: 100, SelectedFailures: []string{"a"}, RelevantFailures: []string{"a", "b"}, AmbiguousFailures: []string{"c"}},
		{ID: "two", Adapter: "pytest", RiskLevel: "LOW", Timestamp: "2026-01-02T00:00:00Z", ChangeClass: "FLAKY", Comparable: false},
	}
	summaries, err := summarize(records)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 {
		t.Fatalf("got %d groups", len(summaries))
	}
	s := summaries[0]
	if s.ComparableRuns != 1 || s.IncomparableRuns != 1 {
		t.Fatalf("unexpected run counts: %+v", s)
	}
	if s.DetectedRelevantFailures != 1 || s.MissedRelevantFailures != 2 || s.ObservedRelevantFailures != 2 {
		t.Fatalf("unexpected miss counts: %+v", s)
	}
	if s.AuditMissRate != 1 {
		t.Fatalf("miss rate = %v, want 1", s.AuditMissRate)
	}
	if s.MeanRuntimeReduction < 0.89 || s.MeanRuntimeReduction > 0.91 {
		t.Fatalf("runtime reduction = %v", s.MeanRuntimeReduction)
	}
}

func TestReadRecordsStrictAndBounds(t *testing.T) {
	_, err := readRecords(strings.NewReader("{\"id\":\"x\",\"adapter\":\"jest\",\"risk_level\":\"LOW\",\"extra\":1}\n"))
	if err == nil {
		t.Fatal("unknown fields must be rejected")
	}
	_, err = readRecords(strings.NewReader("{\"id\":\"x\",\"adapter\":\"jest\",\"risk_level\":\"LOW\",\"timestamp\":\"2026-01-01T00:00:00Z\",\"change_class\":\"STANDARD\",\"comparable\":true,\"selected_runtime_ms\":0,\"full_runtime_ms\":0}\n"))
	if err == nil {
		t.Fatal("comparable rows without runtimes must be rejected")
	}
}

func TestReadRecordsRequiresTimestampAndChangeClass(t *testing.T) {
	const base = "\"id\":\"x\",\"adapter\":\"jest\",\"risk_level\":\"LOW\","
	cases := []struct {
		name string
		line string
	}{
		{"missing timestamp", "{" + base + "\"change_class\":\"STANDARD\"}\n"},
		{"missing change class", "{" + base + "\"timestamp\":\"2026-01-01T00:00:00Z\"}\n"},
		{"timestamp not RFC3339", "{" + base + "\"timestamp\":\"2026-01-01 00:00:00\",\"change_class\":\"STANDARD\"}\n"},
		{"timestamp not parseable", "{" + base + "\"timestamp\":\"yesterday\",\"change_class\":\"STANDARD\"}\n"},
		{"change class not in enum", "{" + base + "\"timestamp\":\"2026-01-01T00:00:00Z\",\"change_class\":\"SOMETHING_ELSE\"}\n"},
		{"empty change class", "{" + base + "\"timestamp\":\"2026-01-01T00:00:00Z\",\"change_class\":\"\"}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := readRecords(strings.NewReader(tc.line)); err == nil {
				t.Fatalf("expected rejection for %s", tc.name)
			}
		})
	}
	// Every declared enum member must be accepted.
	for _, class := range changeClasses {
		line := "{" + base + "\"timestamp\":\"2026-01-01T00:00:00Z\",\"change_class\":\"" + class + "\"}\n"
		records, err := readRecords(strings.NewReader(line))
		if err != nil {
			t.Fatalf("change_class %s must be accepted: %v", class, err)
		}
		if len(records) != 1 || records[0].ChangeClass != class {
			t.Fatalf("change_class %s did not round-trip: %+v", class, records)
		}
	}
}

func TestWilsonBoundaries(t *testing.T) {
	lo, hi := wilson(0, 100)
	if lo < 0 || hi <= 0 || hi >= 0.05 {
		t.Fatalf("unexpected zero-miss interval: [%f,%f]", lo, hi)
	}
	lo, hi = wilson(100, 100)
	if lo <= 0.95 || hi > 1 {
		t.Fatalf("unexpected all-miss interval: [%f,%f]", lo, hi)
	}
}
