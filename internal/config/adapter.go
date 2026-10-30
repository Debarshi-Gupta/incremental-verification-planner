package config

import (
	"time"

	"github.com/ivp/ivp/internal/adapters"
)

// ToAdapterConfig creates the narrow adapter view from a validated application
// config. The executable allowlist is supplied by trusted policy, not repo YAML.
func (c Config) ToAdapterConfig(adapterID string, allowedExecutables []string) adapters.AdapterConfig {
	result := adapters.AdapterConfig{
		Enabled:            adapterEnabled(c, adapterID),
		FullSuiteArgv:      append([]string(nil), c.Execution.FullSuiteArgv...),
		MaxOutputBytes:     int64(c.Execution.MaxOutputBytes),
		AllowedExecutables: append([]string(nil), allowedExecutables...),
	}
	switch adapterID {
	case "gradle":
		result.CoveragePaths = append([]string(nil), c.Adapters.Gradle.CoveragePaths...)
	case "maven":
		result.CoveragePaths = append([]string(nil), c.Adapters.Maven.CoveragePaths...)
	case "bazel":
		result.CoveragePaths = append([]string(nil), c.Adapters.Bazel.CoveragePaths...)
	case "nx":
		result.CoveragePaths = append([]string(nil), c.Adapters.Nx.CoveragePaths...)
	case "pytest":
		result.CoveragePaths = append([]string(nil), c.Adapters.Pytest.CoveragePaths...)
	case "jest":
		result.CoveragePaths = append([]string(nil), c.Adapters.Jest.CoveragePaths...)
	case "generic":
		// Each generic unit owns its own report and coverage configuration.
	default:
		return result
	}
	if adapterID != "generic" {
		return result
	}
	for _, unit := range c.Adapters.Generic.TestUnits {
		result.Targets = append(result.Targets, toTargetSpec(unit, "TEST"))
	}
	for _, check := range c.Adapters.Generic.Checks {
		result.Targets = append(result.Targets, toTargetSpec(check, "CHECK"))
	}
	return result
}

func adapterEnabled(c Config, id string) bool {
	switch id {
	case "gradle":
		return c.Adapters.Gradle.Enabled
	case "maven":
		return c.Adapters.Maven.Enabled
	case "bazel":
		return c.Adapters.Bazel.Enabled
	case "nx":
		return c.Adapters.Nx.Enabled
	case "pytest":
		return c.Adapters.Pytest.Enabled
	case "jest":
		return c.Adapters.Jest.Enabled
	case "generic":
		return c.Adapters.Generic.Enabled
	default:
		return false
	}
}

func toTargetSpec(u GenericUnitConfig, fallbackKind string) adapters.TargetSpec {
	return adapters.TargetSpec{
		ID: u.ID, Kind: fallbackKind, DisplayName: u.DisplayName, Argv: append([]string(nil), u.Argv...), CWD: u.CWD,
		MapsPaths: append([]string(nil), u.MapsPaths...), DependsOn: append([]string(nil), u.DependsOn...),
		ReportPaths: append([]string(nil), u.ReportPaths...), CoveragePaths: append([]string(nil), u.CoveragePaths...),
		RiskLabels: append([]string(nil), u.RiskLabels...), Mandatory: u.Mandatory,
		Timeout:       time.Duration(u.TimeoutSeconds) * time.Second,
		ResourceGroup: u.ResourceGroup,
	}
}
