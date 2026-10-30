package telemetry

import "testing"

func TestMetricLabelsAreBounded(t *testing.T) {
	if got := label("customer-supplied-repository-name", "unknown"); got != "unknown" {
		t.Fatalf("unbounded label accepted: %q", got)
	}
	if got := label("pytest", "unknown"); got != "pytest" {
		t.Fatalf("known adapter label rejected: %q", got)
	}
	if got := routeTemplate("/repositories.create"); got != "/repositories.create" {
		t.Fatalf("known route template rejected: %q", got)
	}
	if got := routeTemplate("/repositories/sensitive-slug"); got != "unknown" {
		t.Fatalf("request path leaked into route labels: %q", got)
	}
}

func TestAllPlannerMetricsAreRegistered(t *testing.T) {
	m := NewMetrics()
	m.Plan("FAST_FEEDBACK", "pytest", "READY", 0.1, 0.9, 0.8)
	m.Selected("pytest", "TEST_CASE", 2)
	m.Fallback("GRAPH_STALE", "pytest")
	m.Execution("pytest", "FAST_FEEDBACK", "PASSED", 0.2)
	m.Compute("pytest", 0.15)
	m.EstimateRatio("pytest", 0.2, 0.15)
	m.AuditRun("NO_MISS_OBSERVED", "pytest")
	m.AuditMiss("CONFIRMED_MISS", "pytest")
	m.Quarantine("AUDIT_MISS")
	m.AdapterError("pytest", "REPORT_PARSE_FAILED")
	m.SetGraphCompleteness(0.95)
	m.RecordAPI("/plans.create", 201, 0.01)
	m.ArtifactUploaded("application/json", 128)
	m.ArtifactStoreError("upload")

	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool, len(families))
	for _, family := range families {
		got[family.GetName()] = true
	}
	want := []string{
		"ivp_plan_requests_total",
		"ivp_plan_duration_seconds",
		"ivp_selected_units_total",
		"ivp_full_fallback_total",
		"ivp_execution_duration_seconds",
		"ivp_test_compute_seconds_total",
		"ivp_plan_estimated_vs_actual_duration_ratio",
		"ivp_audit_runs_total",
		"ivp_audit_misses_total",
		"ivp_selection_confidence",
		"ivp_weighted_coverage",
		"ivp_graph_completeness",
		"ivp_adapter_errors_total",
		"ivp_policy_quarantines_total",
		"ivp_api_requests_total",
		"ivp_api_request_duration_seconds",
		"ivp_artifact_uploads_total",
		"ivp_artifact_bytes_total",
		"ivp_artifact_store_errors_total",
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("required metric %q was not registered", name)
		}
	}
}
