package telemetry

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// LLD 15.3: every incident the LLD names must have an alert rule, and every
// metric an alert rule uses must actually be exposed.
func TestAlertRulesCoverTheRequiredIncidents(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "observability", "ivp-alerts.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	for rule, signal := range map[string]string{
		"IVPDatabaseUnavailable":        "kube_pod_status_ready",
		"IVPMigrationJobFailed":         "kube_job_status_failed",
		"IVPArtifactVolumeCapacityLow":  "kubelet_volume_stats_available_bytes",
		"IVPRetentionJobFailed":         "retention",
		"IVPAuditMissUnacknowledgedSLA": "ivp_audit_miss_unacknowledged_sla_breaches",
		"IVPCrossTenantViolation":       "ivp_cross_tenant_violations_total",
	} {
		if !strings.Contains(content, rule) {
			t.Errorf("the alert rule %q is missing", rule)
		}
		if !strings.Contains(content, signal) {
			t.Errorf("the alert rule %q does not use the expected signal %q", rule, signal)
		}
	}
}

// The metrics the alert rules depend on must be registered and exposed.
func TestAlertSupportMetricsAreExposed(t *testing.T) {
	metrics := NewMetrics()
	metrics.CrossTenantViolations.Inc()
	metrics.AuditMissUnacknowledged.Set(3)
	metrics.AuditMissUnacknowledgedBreaches.Set(1)
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, name := range []string{
		"ivp_cross_tenant_violations_total",
		"ivp_audit_miss_unacknowledged_hours",
		"ivp_audit_miss_unacknowledged_sla_breaches",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("the metric %q is not exposed:\n%s", name, body)
		}
	}
}
