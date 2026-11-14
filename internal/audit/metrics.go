package audit

import (
	"sort"
	"strings"
)

// MetricValue is one calibration metric computed from persisted evidence.
//
// Every metric states its own denominator and reports whether it is defined.
// A metric whose denominator is zero is never NaN and never silently
// presented as a measured zero: it carries Defined=false and a
// NoDataReason so a dashboard can distinguish "no evidence" from "measured
// zero" (LLD 10.6, 19.10).
type MetricValue struct {
	Name         string  `json:"name"`
	Value        float64 `json:"value"`
	Numerator    float64 `json:"numerator"`
	Denominator  float64 `json:"denominator"`
	SampleSize   int     `json:"sample_size"`
	Defined      bool    `json:"defined"`
	NoDataReason string  `json:"no_data_reason,omitempty"`
}

func ratio(name string, numerator, denominator float64, sampleSize int, noDataReason string) MetricValue {
	if denominator <= 0 {
		return MetricValue{Name: name, Value: 0, Numerator: numerator, Denominator: 0, SampleSize: sampleSize, Defined: false, NoDataReason: noDataReason}
	}
	return MetricValue{Name: name, Value: numerator / denominator, Numerator: numerator, Denominator: denominator, SampleSize: sampleSize, Defined: true}
}

// ComparableRunEvidence is one comparable selective/full execution pair. The
// pair is comparable only when the audit recorded it as comparable (LLD
// 10.3), so the evidence that reaches the metric is already restricted to
// runs whose full-suite result can be attributed to the selective run.
type ComparableRunEvidence struct {
	Adapter               string
	RiskLevel             string
	AlgorithmVersion      string
	SelectedRuntimeMillis int64
	FullRuntimeMillis     int64
	SelectedCPUMillis     int64
	FullCPUMillis         int64
}

// FailureEvidence holds the failures one full-suite audit observed.
//
// ObservedFailureKeys are the units that failed in the authoritative
// full-suite run. MissedFailureKeys are the units the selective plan did not
// execute. UnrelatedFailureKeys are the failures an auditor explicitly
// reviewed as unrelated to the change, so they are excluded from the
// relevance denominator (LLD 10.4 NOT_ATTRIBUTABLE).
type FailureEvidence struct {
	Adapter              string
	RiskLevel            string
	AlgorithmVersion     string
	ObservedFailureKeys  []string
	MissedFailureKeys    []string
	UnrelatedFailureKeys []string
}

// PlanUnitEvidence is one selected unit of a plan together with the reason
// codes that justify its selection.
type PlanUnitEvidence struct {
	Adapter              string
	StableKey            string
	ReasonCodes          []string
	HasRelevanceEvidence bool
}

// PlanEvidence is one plan's calibration-relevant outcome.
type PlanEvidence struct {
	Adapter            string
	RiskLevel          string
	AlgorithmVersion   string
	Escalated          bool
	DiscoveryError     bool
	ReportParsingError bool
}

// relevanceReasonCodes are the selected-unit reason codes that assert a
// relation between a selected unit and the change under verification
// (LLD Appendix A). MANDATORY_POLICY_CHECK and FALLBACK_FULL_SUITE are
// deliberately excluded: a mandatory gate is selected by policy and the
// fallback unit stands for the whole suite, so neither carries evidence that
// the unit itself is relevant to the change.
var relevanceReasonCodes = map[string]bool{
	"DIRECTLY_CHANGED_TEST":        true,
	"COVERS_CHANGED_LINE":          true,
	"COVERS_CHANGED_SYMBOL":        true,
	"AFFECTED_BUILD_TARGET":        true,
	"REVERSE_DEPENDENCY_CONSUMER":  true,
	"PUBLIC_CONTRACT_CONSUMER":     true,
	"INTEGRATION_BOUNDARY":         true,
	"HISTORICAL_FAILURE_RELEVANCE": true,
	"CONFIGURED_PATH_MAPPING":      true,
	"OWNER_MAPPING":                true,
	"HIGH_RISK_COVERAGE":           true,
}

// HasRelevanceEvidence reports whether a selected unit carries at least one
// reason code that ties it to the change under verification.
func HasRelevanceEvidence(reasonCodes []string) bool {
	for _, code := range reasonCodes {
		if relevanceReasonCodes[strings.TrimSpace(code)] {
			return true
		}
	}
	return false
}

// RuntimeReduction is 1 - selected_runtime/full_runtime over comparable runs
// (LLD 10.6). The ratio is computed over the summed runtimes of the
// comparable runs so a single slow run cannot dominate the result, and the
// sample size is reported with it.
func RuntimeReduction(runs []ComparableRunEvidence) MetricValue {
	var selected, full int64
	sample := 0
	for _, run := range runs {
		if run.FullRuntimeMillis <= 0 {
			continue
		}
		selected += run.SelectedRuntimeMillis
		full += run.FullRuntimeMillis
		sample++
	}
	if sample == 0 {
		return MetricValue{Name: "runtime_reduction", SampleSize: 0, Defined: false, NoDataReason: "no comparable run with a measured full-suite runtime"}
	}
	return ratio("runtime_reduction", float64(full-selected), float64(full), sample, "")
}

// ComputeReduction is 1 - selected_cpu/full_cpu over the comparable runs that
// carry CPU measurements (LLD 10.6). Runs without CPU metrics are excluded
// from both the numerator and the denominator rather than counted as zero CPU.
func ComputeReduction(runs []ComparableRunEvidence) MetricValue {
	var selected, full int64
	sample := 0
	for _, run := range runs {
		if run.FullCPUMillis <= 0 || run.SelectedCPUMillis <= 0 {
			continue
		}
		selected += run.SelectedCPUMillis
		full += run.FullCPUMillis
		sample++
	}
	if sample == 0 {
		return MetricValue{Name: "compute_reduction", SampleSize: 0, Defined: false, NoDataReason: "no comparable run with CPU metrics"}
	}
	return ratio("compute_reduction", float64(full-selected), float64(full), sample, "")
}

// RegressionRecall is the missed relevant failure count divided by the total
// relevant failures observed by full-suite audits (LLD 10.6). A failure that
// an auditor reviewed as unrelated to the change is excluded from the
// denominator. The ratio therefore rises as the selective verification misses
// more of the failures the authoritative full suite found.
func RegressionRecall(evidence []FailureEvidence) MetricValue {
	total, missed := 0, 0
	for _, item := range evidence {
		unrelated := keySet(item.UnrelatedFailureKeys)
		missedKeys := keySet(item.MissedFailureKeys)
		for _, key := range uniqueKeys(item.ObservedFailureKeys) {
			if unrelated[key] {
				continue
			}
			total++
			if missedKeys[key] {
				missed++
			}
		}
	}
	if total == 0 {
		return MetricValue{Name: "regression_recall", SampleSize: 0, Defined: false, NoDataReason: "no full-suite audit observed a relevant failure"}
	}
	return ratio("regression_recall", float64(missed), float64(total), total, "")
}

// SelectionPrecision is the fraction of selected units that carry defined
// relevance evidence (LLD 10.6). It is a diagnostic only: a unit without
// relevance evidence may still be valuable.
func SelectionPrecision(units []PlanUnitEvidence) MetricValue {
	if len(units) == 0 {
		return MetricValue{Name: "selection_precision", SampleSize: 0, Defined: false, NoDataReason: "no selected unit was recorded"}
	}
	withEvidence := 0
	for _, unit := range units {
		// Relevance evidence is either declared explicitly (evidence that is
		// not reason-code based, e.g. a configured path mapping) or derived
		// from the reason codes that tie the unit to the change.
		if unit.HasRelevanceEvidence || HasRelevanceEvidence(unit.ReasonCodes) {
			withEvidence++
		}
	}
	return ratio("selection_precision", float64(withEvidence), float64(len(units)), len(units), "")
}

// FallbackEscalationRate is the share of plans that escalated to full
// verification (LLD 10.6).
func FallbackEscalationRate(plans []PlanEvidence) MetricValue {
	if len(plans) == 0 {
		return MetricValue{Name: "fallback_escalation_rate", SampleSize: 0, Defined: false, NoDataReason: "no plan was recorded"}
	}
	escalated := 0
	for _, plan := range plans {
		if plan.Escalated {
			escalated++
		}
	}
	return ratio("fallback_escalation_rate", float64(escalated), float64(len(plans)), len(plans), "")
}

// TestDiscoveryErrorRate is the share of plans that could not rely on
// adapter test discovery (LLD 10.6).
func TestDiscoveryErrorRate(plans []PlanEvidence) MetricValue {
	if len(plans) == 0 {
		return MetricValue{Name: "test_discovery_error_rate", SampleSize: 0, Defined: false, NoDataReason: "no plan was recorded"}
	}
	failed := 0
	for _, plan := range plans {
		if plan.DiscoveryError {
			failed++
		}
	}
	return ratio("test_discovery_error_rate", float64(failed), float64(len(plans)), len(plans), "")
}

// ReportParsingErrorRate is the share of plans whose report or evidence
// parsing failed (LLD 10.6).
func ReportParsingErrorRate(plans []PlanEvidence) MetricValue {
	if len(plans) == 0 {
		return MetricValue{Name: "report_parsing_error_rate", SampleSize: 0, Defined: false, NoDataReason: "no plan was recorded"}
	}
	failed := 0
	for _, plan := range plans {
		if plan.ReportParsingError {
			failed++
		}
	}
	return ratio("report_parsing_error_rate", float64(failed), float64(len(plans)), len(plans), "")
}

// BreakdownBy computes one metric per distinct value of a dimension. The
// dimension key is a plain string so a dashboard can render the breakdown
// without knowing the metric's internals (LLD 10.6 requires the metrics to be
// calculable by repository, adapter, risk level, changed-file class, and
// selection algorithm version).
func BreakdownBy[T any](items []T, key func(T) string, compute func([]T) MetricValue) map[string]MetricValue {
	groups := map[string][]T{}
	for _, item := range items {
		name := key(item)
		groups[name] = append(groups[name], item)
	}
	out := make(map[string]MetricValue, len(groups))
	for name, group := range groups {
		out[name] = compute(group)
	}
	return out
}

// SortedBreakdownKeys returns the breakdown keys in a stable order so a
// rendered report does not reorder between requests.
func SortedBreakdownKeys(breakdown map[string]MetricValue) []string {
	keys := make([]string, 0, len(breakdown))
	for key := range breakdown {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func keySet(keys []string) map[string]bool {
	set := make(map[string]bool, len(keys))
	for _, key := range keys {
		set[key] = true
	}
	return set
}

func uniqueKeys(keys []string) []string {
	seen := keySet(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if seen[key] {
			out = append(out, key)
			delete(seen, key)
		}
	}
	sort.Strings(out)
	return out
}
