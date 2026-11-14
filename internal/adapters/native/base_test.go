package native

import (
	"testing"

	"github.com/ivp/ivp/internal/adapters"
)

// Native adapters must reject unsafe configuration before any command is
// materialized: report and coverage paths have to stay inside the
// repository, and the full-suite argv has to name an executable.
func TestValidateConfigRejectsUnsafePathsAndArgv(t *testing.T) {
	base := Base{Name: "gradle", Binary: "gradle"}
	if err := base.ValidateConfig(adapters.AdapterConfig{
		CoveragePaths: []string{"**/build/reports/jacoco/**/*.xml"},
		Targets:       []adapters.TargetSpec{{ID: "unit", ReportPaths: []string{"build/test-results/**/TEST-*.xml"}}},
		FullSuiteArgv: []string{"gradlew", "test"},
	}); err != nil {
		t.Fatalf("clean configuration rejected: %v", err)
	}
	for name, cfg := range map[string]adapters.AdapterConfig{
		"absolute coverage path":      {CoveragePaths: []string{"/etc/passwd"}},
		"escaping coverage path":      {CoveragePaths: []string{"../outside/report.xml"}},
		"backslash coverage path":     {CoveragePaths: []string{"build\\reports\\out.xml"}},
		"empty coverage path":         {CoveragePaths: []string{""}},
		"absolute report path":        {Targets: []adapters.TargetSpec{{ID: "unit", ReportPaths: []string{"/tmp/report.xml"}}}},
		"escaping report path":        {Targets: []adapters.TargetSpec{{ID: "unit", ReportPaths: []string{"../outside/TEST-*.xml"}}}},
		"empty report path":           {Targets: []adapters.TargetSpec{{ID: "unit", ReportPaths: []string{""}}}},
		"empty full-suite executable": {FullSuiteArgv: []string{"  "}},
		"full-suite NUL argument":     {FullSuiteArgv: []string{"gradlew", "test", "a\x00b"}},
		"option NUL":                  {Options: []string{"--x\x00"}},
		"target argument NUL":         {Targets: []adapters.TargetSpec{{ID: "unit", Argv: []string{"a\x00b"}}}},
	} {
		if err := base.ValidateConfig(cfg); err == nil {
			t.Errorf("%s accepted by ValidateConfig", name)
		}
	}
}
