package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// updateCorpus rewrites testdata/corpus.jsonl from generateCorpus(); it is
// only for intentional scheme changes:
//
//	go test ./tests/historical-replay/ -run TestCorpusFileMatchesGenerator -update
var updateCorpus = flag.Bool("update", false, "regenerate testdata/corpus.jsonl from generateCorpus()")

const corpusFile = "testdata/corpus.jsonl"

// TestCorpusFileMatchesGenerator regenerates the corpus in memory and asserts
// byte equality with the checked-in testdata/corpus.jsonl, guarding against
// generator drift. With -update the checked-in file is rewritten instead.
func TestCorpusFileMatchesGenerator(t *testing.T) {
	generated, err := encodeCorpus(generateCorpus())
	if err != nil {
		t.Fatal(err)
	}
	if *updateCorpus {
		if err := os.MkdirAll(filepath.Dir(corpusFile), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(corpusFile, generated, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("regenerated %s (%d bytes)", corpusFile, len(generated))
	}
	checkedIn, err := os.ReadFile(corpusFile)
	if err != nil {
		t.Fatalf("checked-in corpus missing (run with -update to create it): %v", err)
	}
	if !bytes.Equal(generated, checkedIn) {
		t.Fatalf("checked-in corpus drifted: generator produced %d bytes, file has %d bytes; if the change is intentional rerun with -update",
			len(generated), len(checkedIn))
	}
}

// TestCorpusComposition asserts the §17.8 corpus shape: >= 300 records for
// each of the seven supported adapters (>= 2100 total), strictly increasing
// per-adapter timestamps spanning multiple months, every one of the eight
// non-STANDARD change classes >= 25 times, and a non-degenerate mix of
// comparable/incomparable, failing/clean, flaky and risk-level rows.
func TestCorpusComposition(t *testing.T) {
	requiredAdapters := []string{"gradle", "maven", "nx", "bazel", "pytest", "jest", "generic"}
	records := generateCorpus()
	perAdapter := map[string]int{}
	perClass := map[string]int{}
	perRisk := map[string]int{}
	perReason := map[string]int{}
	firstTS := map[string]time.Time{}
	lastTS := map[string]time.Time{}
	comparable, incomparable, withRelevant, withAmbiguous, clean := 0, 0, 0, 0, 0
	var overallMin, overallMax time.Time
	for _, record := range records {
		perAdapter[record.Adapter]++
		perClass[record.ChangeClass]++
		perRisk[record.RiskLevel]++
		ts, err := time.Parse(time.RFC3339, record.Timestamp)
		if err != nil {
			t.Fatalf("record %s: %v", record.ID, err)
		}
		if overallMin.IsZero() || ts.Before(overallMin) {
			overallMin = ts
		}
		if ts.After(overallMax) {
			overallMax = ts
		}
		if previous, ok := lastTS[record.Adapter]; ok && !ts.After(previous) {
			t.Fatalf("record %s: timestamp %s is not strictly after previous %s for adapter %s",
				record.ID, record.Timestamp, previous.Format(time.RFC3339), record.Adapter)
		}
		if _, ok := firstTS[record.Adapter]; !ok {
			firstTS[record.Adapter] = ts
		}
		lastTS[record.Adapter] = ts
		if record.Comparable {
			comparable++
			if len(record.RelevantFailures) > 0 {
				withRelevant++
			}
			if len(record.AmbiguousFailures) > 0 {
				withAmbiguous++
			}
			if len(record.FullFailures) == 0 {
				clean++
			}
		} else {
			incomparable++
			perReason[record.IncomparableReason]++
		}
	}
	if len(records) < 2100 {
		t.Fatalf("corpus has %d records, want >= 2100", len(records))
	}
	for _, adapter := range requiredAdapters {
		if perAdapter[adapter] < 300 {
			t.Fatalf("adapter %s has %d records, want >= 300", adapter, perAdapter[adapter])
		}
		span := lastTS[adapter].Sub(firstTS[adapter])
		if span < 60*24*time.Hour {
			t.Fatalf("adapter %s timestamps span %s, want >= 60 days", adapter, span)
		}
	}
	if len(perAdapter) != len(requiredAdapters) {
		t.Fatalf("corpus contains %d adapters, want exactly the 7 supported ones: %v", len(perAdapter), perAdapter)
	}
	for _, class := range changeClasses[1:] { // the eight non-STANDARD classes
		if perClass[class] < 25 {
			t.Fatalf("change_class %s appears %d times, want >= 25", class, perClass[class])
		}
	}
	if perClass["STANDARD"] == 0 {
		t.Fatal("STANDARD baseline class must be represented")
	}
	if comparable == 0 || incomparable == 0 {
		t.Fatalf("mix must contain comparable (%d) and incomparable (%d) rows", comparable, incomparable)
	}
	if withRelevant == 0 || withAmbiguous == 0 || clean == 0 {
		t.Fatalf("mix must contain failing (%d), flaky-ambiguous (%d) and clean (%d) rows", withRelevant, withAmbiguous, clean)
	}
	for _, risk := range corpusRiskLevels {
		if perRisk[risk] == 0 {
			t.Fatalf("risk level %s missing from corpus: %v", risk, perRisk)
		}
	}
	for _, reason := range corpusIncomparableReasons {
		if perReason[reason] == 0 {
			t.Fatalf("incomparable reason %q missing from corpus: %v", reason, perReason)
		}
	}
	overallSpan := overallMax.Sub(overallMin)
	if overallSpan < 60*24*time.Hour {
		t.Fatalf("corpus timestamps span %s, want multiple months (>= 60 days)", overallSpan)
	}
}

// TestCorpusEndToEnd runs readRecords (strict schema) plus summarize over the
// checked-in corpus file and asserts sensible aggregates: full coverage of all
// rows in the per-group summaries, positive runtime reduction inside (0,1),
// observed failures with finite Wilson intervals and a regression recall in
// [0,1], and incomparable rows counted separately from failures.
func TestCorpusEndToEnd(t *testing.T) {
	file, err := os.Open(corpusFile)
	if err != nil {
		t.Fatalf("open corpus: %v", err)
	}
	defer file.Close()
	records, err := readRecords(file)
	if err != nil {
		t.Fatalf("readRecords rejected the checked-in corpus: %v", err)
	}
	if len(records) < 2100 {
		t.Fatalf("checked-in corpus has %d records, want >= 2100", len(records))
	}
	summaries, err := summarize(records)
	if err != nil {
		t.Fatal(err)
	}
	// Group count: every (adapter, risk_level) combination that occurs.
	wantGroups := map[string]bool{}
	for _, record := range records {
		wantGroups[record.Adapter+"|"+record.RiskLevel] = true
	}
	if len(summaries) != len(wantGroups) {
		t.Fatalf("got %d summary groups, want %d", len(summaries), len(wantGroups))
	}
	totalComparable, totalIncomparable, totalObserved, totalMissed := 0, 0, 0, 0
	for _, s := range summaries {
		if s.ComparableRuns == 0 {
			t.Fatalf("group %s/%s has no comparable runs", s.Adapter, s.RiskLevel)
		}
		totalComparable += s.ComparableRuns
		totalIncomparable += s.IncomparableRuns
		totalObserved += s.ObservedRelevantFailures
		totalMissed += s.MissedRelevantFailures
		if s.MeanRuntimeReduction <= 0 || s.MeanRuntimeReduction >= 1 {
			t.Fatalf("group %s/%s runtime reduction %v not in (0,1)", s.Adapter, s.RiskLevel, s.MeanRuntimeReduction)
		}
		if s.RegressionRecall < 0 || s.RegressionRecall > 1 {
			t.Fatalf("group %s/%s recall %v outside [0,1]", s.Adapter, s.RiskLevel, s.RegressionRecall)
		}
		if math.IsNaN(s.AuditMissRate) || s.AuditMissRate < 0 || s.AuditMissRate > 1 {
			t.Fatalf("group %s/%s miss rate %v outside [0,1]", s.Adapter, s.RiskLevel, s.AuditMissRate)
		}
		if math.IsNaN(s.AuditMissRateWilsonLow) || math.IsNaN(s.AuditMissRateWilsonHigh) ||
			s.AuditMissRateWilsonLow < 0 || s.AuditMissRateWilsonHigh > 1 ||
			s.AuditMissRateWilsonLow > s.AuditMissRateWilsonHigh {
			t.Fatalf("group %s/%s Wilson interval [%v,%v] invalid",
				s.Adapter, s.RiskLevel, s.AuditMissRateWilsonLow, s.AuditMissRateWilsonHigh)
		}
		if s.ComputeReduction == nil {
			t.Fatalf("group %s/%s missing CPU reduction", s.Adapter, s.RiskLevel)
		}
	}
	// Every input row is classified exactly once as comparable or incomparable.
	if totalComparable+totalIncomparable != len(records) {
		t.Fatalf("summaries cover %d rows, input has %d", totalComparable+totalIncomparable, len(records))
	}
	if totalComparable == 0 || totalIncomparable == 0 {
		t.Fatalf("expected both comparable (%d) and incomparable (%d) rows", totalComparable, totalIncomparable)
	}
	// Failure accounting exercised: observed relevant failures exist, some are
	// missed by the plan (audit misses with Wilson CIs), and the miss count is
	// finite/consistent.
	if totalObserved == 0 {
		t.Fatal("corpus must contain observed relevant failures")
	}
	if totalMissed == 0 {
		t.Fatal("corpus must contain audit misses so Wilson intervals are exercised")
	}
	// Sanity: JSON encoding of the report shape round-trips.
	encoded, err := json.Marshal(summaries)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []Summary
	if err := json.Unmarshal(encoded, &decoded); err != nil || len(decoded) != len(summaries) {
		t.Fatalf("summary JSON round-trip failed: %v", err)
	}
}
