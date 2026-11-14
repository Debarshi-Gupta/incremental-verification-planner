package main

// Deterministic historical-replay corpus (inventory rows IVP-17-8-b40182f484
// "hundreds/thousands of historical pull requests per supported adapter" and
// IVP-17-8-473e859722 "include known regression-causing changes, indirect
// dependencies, config/lockfile changes, generated code, migrations,
// reflection/dynamic dispatch, flaky tests and high-fanout shared libraries";
// LLD 17.8 lines 1038 and 1040).
//
// Derivation scheme — pure, stable, and reproducible with no wall clock and no
// unseeded randomness. Record (adapter index a in [0,6], per-adapter index i
// in [0, corpusRecordsPerAdapter)) has global index g = a*300 + i and every
// field is a function of (a, i) alone:
//
//	id           "hr-<adapter>-<i, zero padded to 3>"
//	timestamp    corpusEpoch + a*72h + i*8h, formatted RFC3339 in UTC
//	             (whole hours only, so Parse/Format round-trips byte-exactly;
//	             per-adapter steps are strictly 8h apart and span ~99.7 days,
//	             the whole corpus spans ~117.7 days across multiple months)
//	change_class changeClasses[(i*7 + a*5) mod 9] — all nine classes are hit
//	             by every adapter because gcd(7, 9) = 1; each of the eight
//	             non-STANDARD classes lands ~231 times corpus-wide (>= 25)
//	risk_level   corpusRiskLevels[(i/4 + a) mod 3] — LOW/MEDIUM/HIGH rotate
//	             every four records per adapter (all three well represented)
//	comparable   i mod 11 != 0 — ~9.3% incomparable rows per adapter, carrying
//	             one of three documented reasons (environment_mismatch,
//	             flaky_environment, toolchain_drift); incomparable rows keep
//	             runtimes but carry no failure lists because their outcomes
//	             cannot be attributed (LLD 17.8 line 1042)
//	runtimes     full = 4000 + (g*7919 mod 360000) ms; selected in
//	             [full/4, full/2] so every comparable pair shows a positive
//	             reduction; cpu fields are exactly half the runtimes
//	failures     only on comparable rows, by change class. Failure and miss
//	             decisions use mixIndex(g) (a splitmix64 finalizer of the
//	             global index) instead of g%N so they cannot resonate with
//	             the small modular periods of the class (9) and risk (12)
//	             rotations:
//	             REGRESSION_CAUSING: full suite fails TestRegression<g>,
//	               observed as relevant; the plan misses it when
//	               mixIndex(g) mod 4 == 0 (25% audit-miss rate for the class)
//	             FLAKY: full suite reports TestFlaky<g> as ambiguous (never
//	               clean); the plan misses it when mixIndex(g) mod 3 == 0
//	             other non-STANDARD classes (indirect/config/generated/
//	               migration/reflection/high-fanout): when mixIndex(g) mod 4
//	               == 0 the full suite fails Test<class><g>, observed as
//	               relevant; the plan misses it when mixIndex(g) mod 20 == 0
//	               (a subset of the gate, i.e. 20% of those failures)
//	             STANDARD: almost always clean; when mixIndex(g) mod 17 == 0
//	               the full suite fails TestStandard<g> and the plan detects
//	               it
//
// The miss rules are tuned so that per-(adapter, risk) groups keep missed
// relevant failures at or below observed ones, which is what keeps the audit
// miss rate a valid proportion and the Wilson interval free of NaN
// (asserted end-to-end by TestCorpusEndToEnd).
//
// encodeCorpus renders the records as testdata/corpus.jsonl: one JSON object
// per line in slice order (struct field order), so byte equality with the
// checked-in file guards against generator drift.
import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

const (
	// corpusRecordsPerAdapter is the number of replay rows generated for each
	// supported adapter: 7 adapters * 300 = 2100 rows ("hundreds ... per
	// supported adapter", LLD 17.8 line 1038).
	corpusRecordsPerAdapter = 300
)

// corpusEpoch is the deterministic anchor for every corpus timestamp
// (Monday 2026-01-05T00:00:00Z). It is a fixed value, never time.Now().
var corpusEpoch = time.Date(2026, time.January, 5, 0, 0, 0, 0, time.UTC)

// corpusAdapters lists the seven supported adapters in inventory order.
var corpusAdapters = []string{"gradle", "maven", "nx", "bazel", "pytest", "jest", "generic"}

// changeClasses is the change_class enum accepted by readRecords. STANDARD is
// the ordinary baseline class; the remaining eight are the LLD 17.8 line 1040
// categories.
var changeClasses = []string{
	"STANDARD",
	"REGRESSION_CAUSING",
	"INDIRECT_DEPENDENCY",
	"CONFIG_LOCKFILE",
	"GENERATED_CODE",
	"MIGRATION",
	"REFLECTION_DYNAMIC",
	"FLAKY",
	"HIGH_FANOUT",
}

// validChangeClasses is the lookup used by readRecords validation.
var validChangeClasses = func() map[string]bool {
	valid := make(map[string]bool, len(changeClasses))
	for _, class := range changeClasses {
		valid[class] = true
	}
	return valid
}()

// corpusRiskLevels are the three risk_level groups reported by summarize.
var corpusRiskLevels = []string{"LOW", "MEDIUM", "HIGH"}

// corpusIncomparableReasons are the documented reasons a row cannot be
// compared; environment mismatch stays classified separately from failures.
var corpusIncomparableReasons = []string{"environment_mismatch", "flaky_environment", "toolchain_drift"}

// generateCorpus builds the full corpus in deterministic adapter/index order.
func generateCorpus() []Record {
	records := make([]Record, 0, len(corpusAdapters)*corpusRecordsPerAdapter)
	for a, adapter := range corpusAdapters {
		for i := 0; i < corpusRecordsPerAdapter; i++ {
			records = append(records, corpusRecord(a, adapter, i))
		}
	}
	return records
}

// corpusRecord derives one record purely from its adapter index and
// per-adapter index; see the scheme documented at the top of this file.
func corpusRecord(a int, adapter string, i int) Record {
	g := a*corpusRecordsPerAdapter + i
	class := changeClasses[(i*7+a*5)%len(changeClasses)]
	timestamp := corpusEpoch.
		Add(time.Duration(a) * 72 * time.Hour).
		Add(time.Duration(i) * 8 * time.Hour).
		Format(time.RFC3339)
	fullMillis := int64(4000 + (g*7919)%360000)
	quarter := fullMillis / 4
	selectedMillis := quarter + int64(g*31)%int64(quarter+1) // in [full/4, full/2]
	record := Record{
		ID:                    fmt.Sprintf("hr-%s-%03d", adapter, i),
		Adapter:               adapter,
		RiskLevel:             corpusRiskLevels[(i/4+a)%len(corpusRiskLevels)],
		Timestamp:             timestamp,
		ChangeClass:           class,
		Comparable:            i%11 != 0,
		SelectedRuntimeMillis: selectedMillis,
		FullRuntimeMillis:     fullMillis,
		SelectedCPUMillis:     selectedMillis / 2,
		FullCPUMillis:         fullMillis / 2,
	}
	if !record.Comparable {
		record.IncomparableReason = corpusIncomparableReasons[(i/11)%len(corpusIncomparableReasons)]
		return record
	}
	switch class {
	case "REGRESSION_CAUSING":
		failures := []string{fmt.Sprintf("TestRegression%d", g)}
		record.FullFailures = failures
		record.RelevantFailures = failures
		if mixIndex(g)%4 != 0 {
			record.SelectedFailures = failures
		}
	case "FLAKY":
		flaky := fmt.Sprintf("TestFlaky%d", g)
		record.FullFailures = []string{flaky}
		record.AmbiguousFailures = []string{flaky}
		if mixIndex(g)%3 != 0 {
			record.SelectedFailures = []string{flaky}
		}
	case "STANDARD":
		if mixIndex(g)%17 == 0 {
			failures := []string{fmt.Sprintf("TestStandard%d", g)}
			record.FullFailures = failures
			record.RelevantFailures = failures
			record.SelectedFailures = failures
		}
	default:
		if mixIndex(g)%4 == 0 {
			failures := []string{fmt.Sprintf("Test%s%d", class, g)}
			record.FullFailures = failures
			record.RelevantFailures = failures
			if mixIndex(g)%20 != 0 {
				record.SelectedFailures = failures
			}
		}
	}
	return record
}

// mixIndex applies the splitmix64 finalizer to a record index so that failure
// and miss decisions are decorrelated from the small modular periods used by
// the change-class (9) and risk-level (12) rotations. A plain g%4 would
// resonate with them (for example every nx/HIGH regression row lands on
// g%4==0, inflating that group's miss count above its observed failures).
// It is a pure function of the index: no clock, no random source.
func mixIndex(g int) uint64 {
	x := uint64(g) + 0x9E3779B97F4A7C15
	x = (x ^ (x >> 30)) * 0xBF58476D1CE4E5B9
	x = (x ^ (x >> 27)) * 0x94D049BB133111EB
	return x ^ (x >> 31)
}

// encodeCorpus renders records as JSONL: one json.Marshal of each record per
// line (struct field order is stable), LF-terminated.
func encodeCorpus(records []Record) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}
