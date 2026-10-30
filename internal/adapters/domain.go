package adapters

import (
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
)

// DomainUnit converts adapter discovery output into the shared persisted unit
// shape. The caller supplies IDs/snapshot provenance at ingestion time.
func DomainUnit(adapterID string, snapshotID uuid.UUID, u TestUnit) domain.TestUnit {
	kind := domain.UnitBuildTarget
	switch {
	case u.Kind == "CHECK":
		kind = domain.UnitCheck
	case u.Kind == "PROJECT_SUITE" || u.SelectableGranularity == "MAVEN_MODULE_GOAL":
		kind = domain.UnitProjectSuite
	case u.SelectableGranularity == "PYTEST_NODEID":
		kind = domain.UnitTestCase
	case u.SelectableGranularity == "JEST_TEST_FILE":
		kind = domain.UnitTestFile
	case u.SelectableGranularity == "TEST_METHOD":
		kind = domain.UnitTestMethod
	case u.SelectableGranularity == "TEST_CLASS":
		kind = domain.UnitTestClass
	case u.Kind == "TEST_CASE":
		kind = domain.UnitTestCase
	case u.Kind == "TEST_FILE":
		kind = domain.UnitTestFile
	case u.Kind == "TEST_METHOD":
		kind = domain.UnitTestMethod
	case u.Kind == "TEST_CLASS":
		kind = domain.UnitTestClass
	}
	return domain.TestUnit{ID: uuid.New(), SnapshotID: snapshotID, StableKey: u.Key, Adapter: adapterID, UnitType: kind, ModuleKey: u.Module, RiskLabels: append([]string(nil), u.RiskLabels...), Mandatory: u.Mandatory, EstimatedMillis: u.EstimatedDuration.Milliseconds(), Selectable: u.Filterable, SelectionGranularity: u.SelectableGranularity, TargetRef: u.Key, ExecutionGroup: u.ResourceGroup, SourcePaths: append([]string(nil), u.Paths...), Metadata: map[string]any{"display_name": u.DisplayName, "runner": u.Runner, "report_paths": append([]string(nil), u.ReportPaths...), "coverage_paths": append([]string(nil), u.CoveragePaths...), "dependencies": append([]string(nil), u.Dependencies...), "timeout_millis": u.Timeout.Milliseconds(), "estimated_duration_millis": u.EstimatedDuration.Milliseconds(), "trusted_config": u.TrustedConfig}}
}

// FromDomainUnit converts a stored unit back into the adapter's execution
// shape. Exact selection semantics remain owned by the adapter.
func FromDomainUnit(u domain.TestUnit) TestUnit {
	kind := string(u.UnitType)
	unit := TestUnit{Key: u.StableKey, Kind: kind, Module: u.ModuleKey, Paths: append([]string(nil), u.SourcePaths...), RiskLabels: append([]string(nil), u.RiskLabels...), Mandatory: u.Mandatory, SelectableGranularity: u.SelectionGranularity, Filterable: u.Selectable, ResourceGroup: u.ExecutionGroup, EstimatedDuration: time.Duration(u.EstimatedMillis) * time.Millisecond}
	if u.Metadata != nil {
		if v, ok := u.Metadata["display_name"].(string); ok {
			unit.DisplayName = v
		}
		if v, ok := u.Metadata["runner"].(string); ok {
			unit.Runner = v
		}
		unit.ReportPaths = stringSlice(u.Metadata["report_paths"])
		unit.CoveragePaths = stringSlice(u.Metadata["coverage_paths"])
		if v, ok := numericInt64(u.Metadata["timeout_millis"]); ok {
			unit.Timeout = time.Duration(v) * time.Millisecond
		}
		if v, ok := u.Metadata["trusted_config"].(bool); ok {
			unit.TrustedConfig = v
		}
		if u.UnitType == domain.UnitCheck {
			unit.Kind = "CHECK"
		}
	}
	return unit
}

func stringSlice(v any) []string {
	switch values := v.(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if s, ok := value.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
func numericInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	case float32:
		return int64(n), true
	default:
		return 0, false
	}
}
