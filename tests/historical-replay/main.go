// historical-replay summarizes paired, fingerprint-comparable IVP and full-suite runs.
// It does not invent repository history or infer that a pass proves unobserved safety.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
)

type Record struct {
	ID                    string   `json:"id"`
	Adapter               string   `json:"adapter"`
	RiskLevel             string   `json:"risk_level"`
	Comparable            bool     `json:"comparable"`
	IncomparableReason    string   `json:"incomparable_reason,omitempty"`
	SelectedRuntimeMillis int64    `json:"selected_runtime_ms"`
	FullRuntimeMillis     int64    `json:"full_runtime_ms"`
	SelectedCPUMillis     int64    `json:"selected_cpu_ms,omitempty"`
	FullCPUMillis         int64    `json:"full_cpu_ms,omitempty"`
	SelectedFailures      []string `json:"selected_failures,omitempty"`
	FullFailures          []string `json:"full_failures,omitempty"`
	RelevantFailures      []string `json:"relevant_failures,omitempty"`
	AmbiguousFailures     []string `json:"ambiguous_failures,omitempty"`
}

type Summary struct {
	Adapter                  string   `json:"adapter"`
	RiskLevel                string   `json:"risk_level"`
	ComparableRuns           int      `json:"comparable_runs"`
	IncomparableRuns         int      `json:"incomparable_runs"`
	ObservedRelevantFailures int      `json:"observed_relevant_failures"`
	DetectedRelevantFailures int      `json:"detected_relevant_failures"`
	MissedRelevantFailures   int      `json:"missed_relevant_failures"`
	AuditMissRate            float64  `json:"audit_miss_rate"`
	AuditMissRateWilsonLow   float64  `json:"audit_miss_rate_wilson_95_low"`
	AuditMissRateWilsonHigh  float64  `json:"audit_miss_rate_wilson_95_high"`
	RegressionRecall         float64  `json:"observed_regression_recall"`
	MeanRuntimeReduction     float64  `json:"mean_runtime_reduction"`
	ComputeReduction         *float64 `json:"compute_reduction,omitempty"`
}

func main() {
	input := os.Stdin
	if len(os.Args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: historical-replay [records.jsonl]")
		os.Exit(2)
	}
	if len(os.Args) == 2 && os.Args[1] != "-" {
		file, err := os.Open(os.Args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "open replay input:", err)
			os.Exit(2)
		}
		defer file.Close()
		input = file
	}
	records, err := readRecords(input)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid replay input:", err)
		os.Exit(2)
	}
	summaries, err := summarize(records)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot summarize replay input:", err)
		os.Exit(2)
	}
	out := json.NewEncoder(os.Stdout)
	out.SetIndent("", "  ")
	if err := out.Encode(struct {
		InputRecords        int       `json:"input_records"`
		Summaries           []Summary `json:"summaries"`
		RiskBasedCIEligible bool      `json:"risk_based_ci_eligible"`
		EligibilityNote     string    `json:"eligibility_note"`
	}{len(records), summaries, false, "Eligibility remains disabled until organization-approved thresholds are met on representative held-out data."}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func readRecords(r io.Reader) ([]Record, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var records []Record
	line := 0
	for scanner.Scan() {
		line++
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var item Record
		decoder := json.NewDecoder(strings.NewReader(scanner.Text()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&item); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if item.ID == "" || item.Adapter == "" || item.RiskLevel == "" {
			return nil, fmt.Errorf("line %d: id, adapter, and risk_level are required", line)
		}
		if item.SelectedRuntimeMillis < 0 || item.FullRuntimeMillis < 0 || item.SelectedCPUMillis < 0 || item.FullCPUMillis < 0 {
			return nil, fmt.Errorf("line %d: durations must be nonnegative", line)
		}
		if item.Comparable && (item.SelectedRuntimeMillis <= 0 || item.FullRuntimeMillis <= 0) {
			return nil, fmt.Errorf("line %d: comparable run requires positive selected and full runtimes", line)
		}
		records = append(records, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func summarize(records []Record) ([]Summary, error) {
	type key struct{ adapter, risk string }
	groups := map[key][]Record{}
	for _, record := range records {
		groups[key{record.Adapter, record.RiskLevel}] = append(groups[key{record.Adapter, record.RiskLevel}], record)
	}
	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].adapter != keys[j].adapter {
			return keys[i].adapter < keys[j].adapter
		}
		return keys[i].risk < keys[j].risk
	})
	result := make([]Summary, 0, len(keys))
	for _, k := range keys {
		var s Summary
		s.Adapter, s.RiskLevel = k.adapter, k.risk
		var cpuSelected, cpuFull int64
		cpuPairs := 0
		for _, r := range groups[k] {
			if !r.Comparable {
				s.IncomparableRuns++
				continue
			}
			s.ComparableRuns++
			if r.FullRuntimeMillis > 0 {
				// Accumulate each run's fraction, not a ratio of aggregate totals.
				s.MeanRuntimeReduction += 1 - float64(r.SelectedRuntimeMillis)/float64(r.FullRuntimeMillis)
			}
			if r.FullCPUMillis > 0 && r.SelectedCPUMillis > 0 {
				cpuSelected += r.SelectedCPUMillis
				cpuFull += r.FullCPUMillis
				cpuPairs++
			}
			selected := set(r.SelectedFailures)
			for _, failure := range r.RelevantFailures {
				s.ObservedRelevantFailures++
				if selected[failure] {
					s.DetectedRelevantFailures++
				} else {
					s.MissedRelevantFailures++
				}
			}
			// Ambiguous attribution is counted as a possible miss, never as clean.
			for _, failure := range r.AmbiguousFailures {
				if !selected[failure] {
					s.MissedRelevantFailures++
				}
			}
		}
		if s.ComparableRuns > 0 {
			s.MeanRuntimeReduction /= float64(s.ComparableRuns)
		}
		if s.ObservedRelevantFailures > 0 {
			s.RegressionRecall = float64(s.DetectedRelevantFailures) / float64(s.ObservedRelevantFailures)
		}
		if s.ObservedRelevantFailures > 0 {
			s.AuditMissRate = float64(s.MissedRelevantFailures) / float64(s.ObservedRelevantFailures)
			s.AuditMissRateWilsonLow, s.AuditMissRateWilsonHigh = wilson(s.MissedRelevantFailures, s.ObservedRelevantFailures)
		}
		if cpuPairs > 0 {
			value := 1 - float64(cpuSelected)/float64(cpuFull)
			s.ComputeReduction = &value
		}
		result = append(result, s)
	}
	return result, nil
}

func set(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func wilson(successes, total int) (float64, float64) {
	if total == 0 {
		return 0, 0
	}
	const z = 1.959963984540054
	n, p := float64(total), float64(successes)/float64(total)
	denominator := 1 + z*z/n
	center := (p + z*z/(2*n)) / denominator
	margin := z * math.Sqrt(p*(1-p)/n+z*z/(4*n*n)) / denominator
	return max(0, center-margin), min(1, center+margin)
}
