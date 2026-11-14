package main

// Chronological train/calibration/evaluation split for historical replay
// (inventory row IVP-17-8-0044156b18, LLD 17.8 line 1043): partition records
// by timestamp so that no future test outcome can leak into the state used to
// construct earlier selection decisions.
//
// Proportions (fixed, documented): the split covers 60/20/20 of the observed
// TIME SPAN (max timestamp - min timestamp), not of the record count:
//
//	train       [min, min + 3*(span/5))          ~60% of the span
//	calibration [min + 3*(span/5), min + 4*(span/5))  ~20%
//	evaluation  [min + 4*(span/5), max]          ~20% (max inclusive)
//
// Boundaries come only from min/max timestamps, and assignment is by parsed
// timestamp alone, so a record's period never depends on input order.
// Boundaries use integer nanosecond arithmetic (span/5 truncated), which is
// deterministic and cannot overflow time.Duration.
//
// Degenerate inputs are deterministic and documented rather than errors:
//   - empty input: all three periods are empty with zero-valued boundaries;
//   - a single record or all-equal timestamps: span = 0, both boundaries
//     collapse onto the common timestamp, and every record lands in the
//     evaluation period (train and calibration stay empty) so that no
//     contemporaneous outcome is ever used as prior selection state.
import (
	"fmt"
	"sort"
	"time"
)

const (
	spanDivisor    = 5 // span is divided into five equal parts...
	trainSpanParts = 3 // train consumes 3 parts (60%); boundary1 = min + 3*(span/5)
	evalSpanParts  = 4 // calibration consumes the 4th part (20%); boundary2 = min + 4*(span/5)
)

// Period is one chronological slice of the replay input.
type Period struct {
	Name    string    `json:"name"`
	Start   time.Time `json:"start"` // inclusive lower boundary
	End     time.Time `json:"end"`   // exclusive upper boundary for train/calibration; inclusive for evaluation (End = last timestamp)
	Records []Record  `json:"records"`
}

// PeriodSplit holds the three periods. Within each period records are ordered
// by (timestamp, id), so equal inputs (up to record order) always produce the
// same split bytes.
type PeriodSplit struct {
	Train       Period `json:"train"`
	Calibration Period `json:"calibration"`
	Evaluation  Period `json:"evaluation"`
}

// splitPeriods partitions records into train/calibration/evaluation periods by
// RFC3339 timestamp. It returns an error only when a record carries a
// non-RFC3339 timestamp; all structural edge cases (empty input, single
// record, all-equal timestamps) are handled with the documented deterministic
// behavior above and never panic.
func splitPeriods(records []Record) (PeriodSplit, error) {
	split := PeriodSplit{
		Train:       Period{Name: "train"},
		Calibration: Period{Name: "calibration"},
		Evaluation:  Period{Name: "evaluation"},
	}
	if len(records) == 0 {
		return split, nil
	}
	type stamped struct {
		record Record
		at     time.Time
	}
	stampedRecords := make([]stamped, len(records))
	for i, record := range records {
		at, err := time.Parse(time.RFC3339, record.Timestamp)
		if err != nil {
			return PeriodSplit{}, fmt.Errorf("record %q: timestamp %q is not RFC3339: %w", record.ID, record.Timestamp, err)
		}
		stampedRecords[i] = stamped{record: record, at: at}
	}
	// Sort first, then assign: each period inherits the (timestamp, id) order
	// regardless of input order, which makes the split shuffle-invariant.
	sort.SliceStable(stampedRecords, func(i, j int) bool {
		if !stampedRecords[i].at.Equal(stampedRecords[j].at) {
			return stampedRecords[i].at.Before(stampedRecords[j].at)
		}
		return stampedRecords[i].record.ID < stampedRecords[j].record.ID
	})
	earliest := stampedRecords[0].at
	latest := stampedRecords[len(stampedRecords)-1].at
	step := latest.Sub(earliest) / spanDivisor
	boundary1 := earliest.Add(trainSpanParts * step)
	boundary2 := earliest.Add(evalSpanParts * step)
	for _, sr := range stampedRecords {
		switch {
		case sr.at.Before(boundary1):
			split.Train.Records = append(split.Train.Records, sr.record)
		case sr.at.Before(boundary2):
			split.Calibration.Records = append(split.Calibration.Records, sr.record)
		default:
			split.Evaluation.Records = append(split.Evaluation.Records, sr.record)
		}
	}
	split.Train.Start, split.Train.End = earliest, boundary1
	split.Calibration.Start, split.Calibration.End = boundary1, boundary2
	split.Evaluation.Start, split.Evaluation.End = boundary2, latest
	return split, nil
}
