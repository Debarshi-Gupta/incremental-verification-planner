package telemetry

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var uuidPathComponent = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var boundedMetricLabels = func() map[string]struct{} {
	values := strings.Fields(`unknown gradle maven bazel nx pytest jest generic FULL OBSERVE FAST_FEEDBACK RISK_BASED_CI READY FULL_REQUIRED STALE EXECUTING PASSED FAILED CANCELLED ERROR TARGETED_PASS FULL_VERIFICATION_PASS TARGETED_FAIL FULL_VERIFICATION_FAIL INFRA_FAILURE NO_MISS_OBSERVED POSSIBLE_MISS CONFIRMED_MISS NOT_ATTRIBUTABLE INCOMPARABLE AUDIT_FAILED TEST_CASE TEST_METHOD TEST_FILE TEST_CLASS BUILD_TARGET PROJECT_SUITE CHECK UNSUPPORTED_ADAPTER GRAPH_INCOMPLETE GRAPH_STALE GRAPH_CONFLICT COVERAGE_STALE COVERAGE_MISSING CONFIG_OR_TOOLCHAIN_CHANGE LOCKFILE_CHANGE PUBLIC_CONTRACT_CHANGE MIGRATION_CHANGE SECURITY_CRITICAL_CHANGE HIGH_DEPENDENCY_FANOUT TOO_MANY_CHANGED_FILES NO_TESTS_SELECTED LOW_SELECTION_CONFIDENCE LOW_WEIGHTED_COVERAGE MANDATORY_CHECK_UNRESOLVED PLAN_ECONOMICALLY_UNHELPFUL POLICY_UNAVAILABLE PLAN_STALE QUARANTINE_ACTIVE ADAPTER_DISCOVERY_FAILED REPORT_PARSE_FAILED ENVIRONMENT_MISMATCH WORKTREE_MUTATED AUDIT_MISS AUDIT_CONFIRMED_MISS AUDIT_POSSIBLE_MISS API_REQUEST_INVALID DATABASE_ERROR INTERNAL_ERROR application/json application/xml text/xml success error upload download delete`)
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}()

type Metrics struct {
	Registry            *prometheus.Registry
	PlanRequests        *prometheus.CounterVec
	PlanDuration        *prometheus.HistogramVec
	SelectedUnits       *prometheus.CounterVec
	FullFallback        *prometheus.CounterVec
	ExecutionDuration   *prometheus.HistogramVec
	TestCompute         *prometheus.CounterVec
	EstimatedActual     *prometheus.HistogramVec
	AuditRuns           *prometheus.CounterVec
	AuditMisses         *prometheus.CounterVec
	SelectionConfidence prometheus.Histogram
	WeightedCoverage    prometheus.Histogram
	GraphCompleteness   prometheus.Gauge
	AdapterErrors       *prometheus.CounterVec
	Quarantines         *prometheus.CounterVec
	APIRequests         *prometheus.CounterVec
	APILatency          *prometheus.HistogramVec
	ArtifactUploads     *prometheus.CounterVec
	ArtifactBytes       *prometheus.CounterVec
	ArtifactStoreErrors *prometheus.CounterVec
}

func NewMetrics() *Metrics {
	r := prometheus.NewRegistry()
	m := &Metrics{Registry: r,
		PlanRequests:        prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ivp_plan_requests_total", Help: "Plan requests by mode, adapter, and outcome."}, []string{"mode", "adapter", "outcome"}),
		PlanDuration:        prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "ivp_plan_duration_seconds", Help: "Plan generation latency by adapter.", Buckets: prometheus.DefBuckets}, []string{"adapter"}),
		SelectedUnits:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ivp_selected_units_total", Help: "Selected verification units by adapter and type."}, []string{"adapter", "unit_type"}),
		FullFallback:        prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ivp_full_fallback_total", Help: "Full verification fallbacks by reason and adapter."}, []string{"reason_code", "adapter"}),
		ExecutionDuration:   prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "ivp_execution_duration_seconds", Help: "Execution duration by adapter, mode, and outcome.", Buckets: prometheus.DefBuckets}, []string{"adapter", "mode", "outcome"}),
		TestCompute:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ivp_test_compute_seconds_total", Help: "Accumulated test compute duration."}, []string{"adapter"}),
		EstimatedActual:     prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "ivp_plan_estimated_vs_actual_duration_ratio", Help: "Estimated duration divided by actual duration.", Buckets: []float64{.1, .25, .5, .75, 1, 1.25, 1.5, 2, 3, 5, 10}}, []string{"adapter"}),
		AuditRuns:           prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ivp_audit_runs_total", Help: "Audit runs by result and adapter."}, []string{"outcome", "adapter"}),
		AuditMisses:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ivp_audit_misses_total", Help: "Possible and confirmed audit misses."}, []string{"severity", "adapter"}),
		SelectionConfidence: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "ivp_selection_confidence", Help: "Distribution of plan selection confidence.", Buckets: []float64{0, .25, .4, .5, .6, .7, .8, .9, .95, 1}}),
		WeightedCoverage:    prometheus.NewHistogram(prometheus.HistogramOpts{Name: "ivp_weighted_coverage", Help: "Distribution of weighted plan coverage estimates.", Buckets: []float64{0, .25, .4, .5, .6, .7, .8, .9, .95, 1}}),
		GraphCompleteness:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "ivp_graph_completeness", Help: "Graph completeness score for the most recently processed graph."}),
		AdapterErrors:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ivp_adapter_errors_total", Help: "Adapter errors by adapter and error class."}, []string{"adapter", "error_class"}),
		Quarantines:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ivp_policy_quarantines_total", Help: "Selection quarantines by reason."}, []string{"reason"}),
		APIRequests:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ivp_api_requests_total", Help: "API requests by route template and status."}, []string{"route", "status"}),
		APILatency:          prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "ivp_api_request_duration_seconds", Help: "API request latency by route template.", Buckets: prometheus.DefBuckets}, []string{"route"}),
		ArtifactUploads:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ivp_artifact_uploads_total", Help: "Sanitized test report uploads by media type."}, []string{"media_type"}),
		ArtifactBytes:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ivp_artifact_bytes_total", Help: "Sanitized test report bytes accepted by media type."}, []string{"media_type"}),
		ArtifactStoreErrors: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ivp_artifact_store_errors_total", Help: "Artifact object-store failures by bounded operation."}, []string{"operation"}),
	}
	r.MustRegister(m.PlanRequests, m.PlanDuration, m.SelectedUnits, m.FullFallback, m.ExecutionDuration, m.TestCompute, m.EstimatedActual, m.AuditRuns, m.AuditMisses, m.SelectionConfidence, m.WeightedCoverage, m.GraphCompleteness, m.AdapterErrors, m.Quarantines, m.APIRequests, m.APILatency, m.ArtifactUploads, m.ArtifactBytes, m.ArtifactStoreErrors)
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
}
func (m *Metrics) RecordAPI(route string, status int, seconds float64) {
	route = routeTemplate(route)
	m.APIRequests.WithLabelValues(route, strconv.Itoa(status)).Inc()
	m.APILatency.WithLabelValues(route).Observe(seconds)
}
func (m *Metrics) AuditRun(outcome, adapter string) {
	m.AuditRuns.WithLabelValues(label(outcome, "unknown"), label(adapter, "unknown")).Inc()
}
func (m *Metrics) AuditMiss(severity, adapter string) {
	m.AuditMisses.WithLabelValues(label(severity, "possible"), label(adapter, "unknown")).Inc()
}
func (m *Metrics) Quarantine(reason string) {
	m.Quarantines.WithLabelValues(label(reason, "unknown")).Inc()
}
func (m *Metrics) Plan(mode, adapter, outcome string, seconds, confidence, coverage float64) {
	m.PlanRequests.WithLabelValues(label(mode, "unknown"), label(adapter, "unknown"), label(outcome, "unknown")).Inc()
	m.PlanDuration.WithLabelValues(label(adapter, "unknown")).Observe(seconds)
	m.SelectionConfidence.Observe(confidence)
	m.WeightedCoverage.Observe(coverage)
}
func (m *Metrics) Fallback(reason, adapter string) {
	m.FullFallback.WithLabelValues(label(reason, "unknown"), label(adapter, "unknown")).Inc()
}
func (m *Metrics) AdapterError(adapter, class string) {
	m.AdapterErrors.WithLabelValues(label(adapter, "unknown"), label(class, "unknown")).Inc()
}
func (m *Metrics) Execution(adapter, mode, outcome string, seconds float64) {
	m.ExecutionDuration.WithLabelValues(label(adapter, "unknown"), label(mode, "unknown"), label(outcome, "unknown")).Observe(seconds)
}
func (m *Metrics) Selected(adapter, unitType string, count float64) {
	m.SelectedUnits.WithLabelValues(label(adapter, "unknown"), label(unitType, "unknown")).Add(count)
}
func (m *Metrics) Compute(adapter string, seconds float64) {
	if seconds > 0 {
		m.TestCompute.WithLabelValues(label(adapter, "unknown")).Add(seconds)
	}
}
func (m *Metrics) EstimateRatio(adapter string, estimated, actual float64) {
	if actual > 0 {
		m.EstimatedActual.WithLabelValues(label(adapter, "unknown")).Observe(estimated / actual)
	}
}
func (m *Metrics) SetGraphCompleteness(value float64) { m.GraphCompleteness.Set(value) }
func (m *Metrics) ArtifactUploaded(mediaType string, sizeBytes int64) {
	mediaType = label(mediaType, "unknown")
	m.ArtifactUploads.WithLabelValues(mediaType).Inc()
	if sizeBytes > 0 {
		m.ArtifactBytes.WithLabelValues(mediaType).Add(float64(sizeBytes))
	}
}
func (m *Metrics) ArtifactStoreError(operation string) {
	m.ArtifactStoreErrors.WithLabelValues(label(operation, "unknown")).Inc()
}

func label(value, fallback string) string {
	if _, ok := boundedMetricLabels[value]; !ok {
		return fallback
	}
	return value
}
func routeTemplate(path string) string {
	// Callers should pass chi route patterns; this strips accidental identifiers
	// if a request path is supplied instead, keeping Prometheus labels bounded.
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, p := range parts {
		if uuidPathComponent.MatchString(p) {
			parts[i] = "{id}"
		}
	}
	canonical := "/" + strings.Join(parts, "/")
	allowed := map[string]struct{}{
		"/identity.me": {}, "/repositories.create": {}, "/repositories.list": {}, "/repositories.get": {},
		"/snapshots.create": {}, "/policy.get": {}, "/policy.update": {}, "/plans.create": {},
		"/plans.get": {}, "/executions.start": {}, "/executions.result": {}, "/executions.get": {},
		"/audits.submit": {}, "/metrics.repository": {}, "/quarantine.create": {}, "/quarantine.acknowledge": {},
		"/artifacts.upload": {}, "/artifacts.get": {},
	}
	if _, ok := allowed[canonical]; !ok {
		return "unknown"
	}
	return canonical
}
