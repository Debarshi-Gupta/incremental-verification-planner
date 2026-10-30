package main

import (
	"strings"
	"testing"
)

func TestSummarizeCountsAmbiguousAsMissAndExcludesIncomparable(t *testing.T) {
	records := []Record{
		{ID: "one", Adapter: "pytest", RiskLevel: "LOW", Comparable: true, SelectedRuntimeMillis: 10, FullRuntimeMillis: 100, SelectedFailures: []string{"a"}, RelevantFailures: []string{"a", "b"}, AmbiguousFailures: []string{"c"}},
		{ID: "two", Adapter: "pytest", RiskLevel: "LOW", Comparable: false},
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
	_, err = readRecords(strings.NewReader("{\"id\":\"x\",\"adapter\":\"jest\",\"risk_level\":\"LOW\",\"comparable\":true,\"selected_runtime_ms\":0,\"full_runtime_ms\":0}\n"))
	if err == nil {
		t.Fatal("comparable rows without runtimes must be rejected")
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
