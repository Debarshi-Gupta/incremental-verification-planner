package main

import (
	"encoding/json"
	"math/rand"
	"testing"
	"time"
)

func mustParseTS(t *testing.T, ts string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t.Fatalf("timestamp %q: %v", ts, err)
	}
	return parsed
}

// TestSplitPeriodsChronologicalExhaustiveAndDisjoint asserts the core
// anti-leakage properties: the three periods are disjoint and jointly
// exhaustive over the input, every train timestamp is strictly before every
// calibration timestamp which is strictly before every evaluation timestamp,
// the documented 60/20/20-of-span proportions hold, and the reported
// boundaries chain (train.End == cal.Start, cal.End == eval.Start).
func TestSplitPeriodsChronologicalExhaustiveAndDisjoint(t *testing.T) {
	records := generateCorpus()
	split, err := splitPeriods(records)
	if err != nil {
		t.Fatal(err)
	}
	periods := []struct {
		name   string
		period Period
	}{
		{"train", split.Train},
		{"calibration", split.Calibration},
		{"evaluation", split.Evaluation},
	}
	// Disjoint and exhaustive: every record appears exactly once overall.
	seen := make(map[string]string, len(records))
	for _, p := range periods {
		for _, record := range p.period.Records {
			if previous, ok := seen[record.ID]; ok {
				t.Fatalf("record %s appears in both %s and %s", record.ID, previous, p.name)
			}
			seen[record.ID] = p.name
		}
	}
	if len(seen) != len(records) {
		t.Fatalf("split covers %d records, want %d", len(seen), len(records))
	}
	// No future leakage: max(train) < min(calibration) < min(evaluation).
	if len(split.Train.Records) == 0 || len(split.Calibration.Records) == 0 || len(split.Evaluation.Records) == 0 {
		t.Fatalf("all periods must be non-empty for the corpus: train=%d calibration=%d evaluation=%d",
			len(split.Train.Records), len(split.Calibration.Records), len(split.Evaluation.Records))
	}
	maxTrain := split.Train.Records[len(split.Train.Records)-1]
	minCal := split.Calibration.Records[0]
	minEval := split.Evaluation.Records[0]
	maxTrainTS := mustParseTS(t, maxTrain.Timestamp)
	minCalTS := mustParseTS(t, minCal.Timestamp)
	minEvalTS := mustParseTS(t, minEval.Timestamp)
	if !maxTrainTS.Before(minCalTS) {
		t.Fatalf("train max %s is not before calibration min %s", maxTrainTS, minCalTS)
	}
	if !minCalTS.Before(minEvalTS) {
		t.Fatalf("calibration min %s is not before evaluation min %s", minCalTS, minEvalTS)
	}
	// Proportions of the time span: 60/20/20 (integer arithmetic tolerance).
	span := mustParseTS(t, records[len(records)-1].Timestamp).Sub(mustParseTS(t, records[0].Timestamp))
	if span <= 0 {
		t.Fatalf("corpus span must be positive, got %s", span)
	}
	trainWidth := split.Train.End.Sub(split.Train.Start)
	calWidth := split.Calibration.End.Sub(split.Calibration.Start)
	evalWidth := split.Evaluation.End.Sub(split.Evaluation.Start)
	tolerance := time.Millisecond // span/5 truncation costs < 5ns per boundary
	if diff := trainWidth - span*3/5; diff > tolerance || diff < -tolerance {
		t.Fatalf("train width %s != 60%% of span %s (diff %s)", trainWidth, span*3/5, diff)
	}
	if diff := calWidth - span/5; diff > tolerance || diff < -tolerance {
		t.Fatalf("calibration width %s != 20%% of span %s (diff %s)", calWidth, span/5, diff)
	}
	if diff := evalWidth - span/5; diff > tolerance || diff < -tolerance {
		t.Fatalf("evaluation width %s != 20%% of span %s (diff %s)", evalWidth, span/5, diff)
	}
	// Boundaries chain and windows start/end on the observed extremes.
	if !split.Train.End.Equal(split.Calibration.Start) {
		t.Fatalf("train end %s must equal calibration start %s", split.Train.End, split.Calibration.Start)
	}
	if !split.Calibration.End.Equal(split.Evaluation.Start) {
		t.Fatalf("calibration end %s must equal evaluation start %s", split.Calibration.End, split.Evaluation.Start)
	}
	if !split.Train.Start.Equal(mustParseTS(t, records[0].Timestamp)) {
		t.Fatalf("train start %s must be the earliest timestamp", split.Train.Start)
	}
	if !split.Evaluation.End.Equal(mustParseTS(t, records[len(records)-1].Timestamp)) {
		t.Fatalf("evaluation end %s must be the latest timestamp", split.Evaluation.End)
	}
}

// TestSplitPeriodsShuffleInvariant asserts that assignment depends only on
// timestamps: shuffling the input (fixed seed) yields a byte-identical split,
// including record order inside each period.
func TestSplitPeriodsShuffleInvariant(t *testing.T) {
	records := generateCorpus()
	original, err := splitPeriods(records)
	if err != nil {
		t.Fatal(err)
	}
	shuffled := append([]Record(nil), records...)
	random := rand.New(rand.NewSource(42)) // fixed seed: deterministic test
	random.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})
	permuted, err := splitPeriods(shuffled)
	if err != nil {
		t.Fatal(err)
	}
	originalJSON, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	permutedJSON, err := json.Marshal(permuted)
	if err != nil {
		t.Fatal(err)
	}
	if string(originalJSON) != string(permutedJSON) {
		t.Fatalf("split changed under input shuffling:\noriginal %d bytes\nshuffled %d bytes", len(originalJSON), len(permutedJSON))
	}
}

// TestSplitPeriodsDegenerateInputs documents the bootstrap edge cases: empty
// input, a single record, and all-equal timestamps must not panic and must
// produce the deterministic documented split (everything in evaluation, train
// and calibration empty, boundaries collapsed onto the common timestamp).
func TestSplitPeriodsDegenerateInputs(t *testing.T) {
	single := []Record{{
		ID: "only", Adapter: "jest", RiskLevel: "LOW",
		Timestamp: "2026-06-01T00:00:00Z", ChangeClass: "STANDARD",
	}}
	equal := make([]Record, 5)
	for i := range equal {
		equal[i] = Record{
			ID: "same", Adapter: "jest", RiskLevel: "LOW",
			Timestamp: "2026-06-01T00:00:00Z", ChangeClass: "STANDARD",
		}
	}
	cases := []struct {
		name    string
		records []Record
	}{
		{"single record", single},
		{"all-equal timestamps", equal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			split, err := splitPeriods(tc.records)
			if err != nil {
				t.Fatalf("%s must not error: %v", tc.name, err)
			}
			if len(split.Train.Records) != 0 || len(split.Calibration.Records) != 0 {
				t.Fatalf("train/calibration must be empty for degenerate span: %d/%d records",
					len(split.Train.Records), len(split.Calibration.Records))
			}
			if len(split.Evaluation.Records) != len(tc.records) {
				t.Fatalf("evaluation has %d records, want %d", len(split.Evaluation.Records), len(tc.records))
			}
			common := mustParseTS(t, tc.records[0].Timestamp)
			if !split.Train.Start.Equal(common) || !split.Train.End.Equal(common) ||
				!split.Calibration.Start.Equal(common) || !split.Calibration.End.Equal(common) ||
				!split.Evaluation.Start.Equal(common) || !split.Evaluation.End.Equal(common) {
				t.Fatalf("degenerate boundaries must collapse onto %s: %+v", common, split)
			}
		})
	}
	t.Run("empty input", func(t *testing.T) {
		split, err := splitPeriods(nil)
		if err != nil {
			t.Fatalf("empty input must not error: %v", err)
		}
		if len(split.Train.Records) != 0 || len(split.Calibration.Records) != 0 || len(split.Evaluation.Records) != 0 {
			t.Fatal("empty input must yield empty periods")
		}
		if !split.Train.Start.IsZero() || !split.Evaluation.End.IsZero() {
			t.Fatal("empty input must yield zero-valued boundaries")
		}
	})
	t.Run("non-RFC3339 timestamp is an error", func(t *testing.T) {
		bad := []Record{{ID: "bad", Adapter: "jest", RiskLevel: "LOW", Timestamp: "not-a-time", ChangeClass: "STANDARD"}}
		if _, err := splitPeriods(bad); err == nil {
			t.Fatal("expected an error for a non-RFC3339 timestamp")
		}
	})
}
