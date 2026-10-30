package config

import (
	"fmt"
	"math"
	"net/url"
	"path"
	"strings"

	"github.com/ivp/ivp/internal/domain"
)

var validModes = map[domain.PlanMode]bool{
	domain.ModeFull: true, domain.ModeObserve: true, domain.ModeFastFeedback: true, domain.ModeRiskBasedCI: true,
}

func Validate(c Config) error {
	if c.Version != 1 {
		return invalid("version must be 1")
	}
	if strings.TrimSpace(c.Repository.Slug) == "" && c.Repository.Slug != "" {
		return invalid("repository.slug must not be whitespace")
	}
	if strings.TrimSpace(c.Analysis.BaseRef) == "" {
		return invalid("analysis.base_ref must not be empty")
	}
	if c.Analysis.MaxChangedFilesForSelectivePlan < 1 || c.Analysis.GraphStalenessMinutes < 1 || c.Analysis.CoverageStalenessDays < 1 {
		return invalid("analysis limits and staleness periods must be positive")
	}
	if !c.Analysis.RequireCleanDependencyResolution {
		return invalid("analysis.require_clean_dependency_resolution must be true for selective planning")
	}
	if c.Server.ConnectTimeoutSeconds < 1 || c.Server.RequestTimeoutSeconds < c.Server.ConnectTimeoutSeconds {
		return invalid("server timeouts must be positive and request timeout must cover connect timeout")
	}
	if c.Server.Endpoint != "" {
		u, err := url.Parse(c.Server.Endpoint)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return invalid("server.endpoint must be an http(s) URL without credentials, query, or fragment")
		}
	}
	if c.Server.OfflinePolicy != "FULL_SUITE_FALLBACK" {
		return invalid("server.offline_policy must be FULL_SUITE_FALLBACK")
	}
	if !validModes[c.Modes.Default] {
		return invalid("modes.default is not a supported plan mode")
	}
	if math.IsNaN(c.Modes.RiskBasedCI.AuditSampleRate) || math.IsInf(c.Modes.RiskBasedCI.AuditSampleRate, 0) || c.Modes.RiskBasedCI.AuditSampleRate < 0 || c.Modes.RiskBasedCI.AuditSampleRate > 1 || c.Modes.RiskBasedCI.MinimumCalibrationRuns < 0 || c.Modes.RiskBasedCI.MinimumAuditRuns < 0 {
		return invalid("risk_based_ci calibration and audit values are outside their valid range")
	}
	if c.Modes.RiskBasedCI.EnabledByOrganizationPolicy && c.Modes.Default != domain.ModeRiskBasedCI {
		// It is valid for policy to permit the mode without choosing it as default.
	}
	if c.Selection.LowRiskMaxScore < 0 || c.Selection.LowRiskMaxScore > 24 || c.Selection.MediumRiskMaxScore < 25 || c.Selection.MediumRiskMaxScore > 59 {
		return invalid("risk score thresholds must preserve the default safe bands (low <=24, medium <=59)")
	}
	if c.Selection.LowRiskMaxScore >= c.Selection.MediumRiskMaxScore {
		return invalid("low risk threshold must be below medium risk threshold")
	}
	for name, value := range map[string]float64{
		"selection.low_risk_min_weighted_coverage":           c.Selection.LowRiskMinWeightedCoverage,
		"selection.medium_risk_min_weighted_coverage":        c.Selection.MediumRiskMinWeightedCoverage,
		"selection.minimum_selection_confidence":             c.Selection.MinimumSelectionConfidence,
		"selection.max_selective_estimated_runtime_fraction": c.Selection.MaxSelectiveEstimatedRuntimeFraction,
		"safety.audit.sample_rate":                           c.Safety.Audit.SampleRate,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
			return invalid("%s must be in [0,1]", name)
		}
	}
	if c.Selection.LowRiskMinWeightedCoverage < 0.95 || c.Selection.MediumRiskMinWeightedCoverage < 0.99 || c.Selection.MinimumSelectionConfidence < 0.98 {
		return invalid("selection coverage and confidence thresholds cannot be lower than safe defaults")
	}
	if c.Selection.MaxSelectiveEstimatedRuntimeFraction <= 0 || c.Selection.MaxSelectiveEstimatedRuntimeFraction > 0.70 {
		return invalid("selection.max_selective_estimated_runtime_fraction must be in (0,0.70]")
	}
	if c.Selection.UnknownEvidenceAction != "FULL_SUITE" || c.Selection.IfNoTestsSelected != "FULL_SUITE" {
		return invalid("unknown or empty selection evidence must use FULL_SUITE")
	}
	if c.Selection.NonDeterministicTestAction != "INCLUDE_AND_LABEL" {
		return invalid("non_deterministic_test_action must be INCLUDE_AND_LABEL")
	}
	if c.Safety.Audit.SampleRate < 0 || c.Safety.Audit.SampleRate > 1 || c.Safety.Audit.MinimumAuditsBeforeSelectiveCI < 0 || c.Safety.Audit.QuarantineHours < 1 || c.Safety.Audit.RepeatFailedTestCount < 1 {
		return invalid("safety.audit values are outside their valid range")
	}
	if c.Execution.TimeoutSeconds < 1 || c.Execution.MaxOutputBytes < 1024 || c.Execution.MaxOutputBytes > 64<<20 {
		return invalid("execution timeout and output bounds are invalid")
	}
	if err := validateArgv("execution.full_suite_argv", c.Execution.FullSuiteArgv, false); err != nil {
		return err
	}
	if c.Safety.Audit.DefaultMode != "SHADOW" && c.Safety.Audit.DefaultMode != "AUDIT" {
		return invalid("safety.audit.default_mode must be SHADOW or AUDIT")
	}
	if c.Telemetry.MaxExcerptBytes < 0 || c.Telemetry.MaxExcerptBytes > 1<<20 || c.Telemetry.RetentionDays < 1 {
		return invalid("telemetry excerpt and retention limits are invalid")
	}
	for _, group := range [][]string{c.Safety.HardFullSuitePaths, c.Safety.CriticalPathPatterns, c.Safety.DocumentationPathPatterns} {
		for _, pattern := range group {
			if err := validateGlob(pattern); err != nil {
				return err
			}
		}
	}
	for _, label := range c.Safety.HardFullSuiteRiskLabels {
		if strings.TrimSpace(label) == "" {
			return invalid("safety.hard_full_suite_risk_labels contains an empty label")
		}
	}
	for _, id := range c.Safety.AlwaysRunCheckIDs {
		if strings.TrimSpace(id) == "" {
			return invalid("safety.always_run_check_ids contains an empty ID")
		}
	}
	if err := validateGeneric(c.Adapters.Generic); err != nil {
		return err
	}
	if err := validateAdapterStrings(c); err != nil {
		return err
	}
	if c.Adapters.Pytest.ConfigFile != "" {
		if _, err := domain.NormalizeRepoPath(c.Adapters.Pytest.ConfigFile); err != nil {
			return invalid("adapters.pytest.config_file: %v", err)
		}
	}
	return nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, fmt.Sprintf(format, args...))
}

func validateGlob(pattern string) error {
	if pattern == "" || strings.Contains(pattern, `\`) || strings.HasPrefix(pattern, "/") || strings.HasPrefix(pattern, "../") || path.Clean(pattern) != pattern {
		return invalid("invalid repository glob %q", pattern)
	}
	for _, part := range strings.Split(pattern, "/") {
		if part == ".." || part == "." {
			return invalid("invalid repository glob %q", pattern)
		}
		if part != "**" {
			if _, err := path.Match(part, "placeholder"); err != nil {
				return invalid("invalid repository glob %q: %v", pattern, err)
			}
		}
	}
	return nil
}

func validateGeneric(g GenericConfig) error {
	units := append(append([]GenericUnitConfig{}, g.TestUnits...), g.Checks...)
	ids := make(map[string]GenericUnitConfig, len(units))
	for i, u := range units {
		if u.ID == "" || strings.TrimSpace(u.ID) != u.ID {
			return invalid("generic unit %d has an empty or whitespace-padded id", i)
		}
		if _, exists := ids[u.ID]; exists {
			return invalid("generic unit id %q is duplicated", u.ID)
		}
		ids[u.ID] = u
		if err := validateArgv("generic unit "+u.ID, u.Argv, true); err != nil {
			return err
		}
		if u.CWD != "" && u.CWD != "." {
			if _, err := domain.NormalizeRepoPath(u.CWD); err != nil {
				return invalid("generic unit %q cwd: %v", u.ID, err)
			}
		}
		if u.TimeoutSeconds < 1 || u.EstimatedDurationSeconds < 0 {
			return invalid("generic unit %q timeout must be positive and estimate nonnegative", u.ID)
		}
		for _, pattern := range u.MapsPaths {
			if err := validateGlob(pattern); err != nil {
				return invalid("generic unit %q: %v", u.ID, err)
			}
		}
	}
	state := make(map[string]uint8, len(ids))
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 1 {
			return invalid("generic unit dependency cycle includes %q", id)
		}
		if state[id] == 2 {
			return nil
		}
		state[id] = 1
		for _, dep := range ids[id].DependsOn {
			if _, ok := ids[dep]; !ok {
				return invalid("generic unit %q depends on unknown unit %q", id, dep)
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for id := range ids {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func validateArgv(name string, argv []string, required bool) error {
	if len(argv) == 0 {
		if required {
			return invalid("%s requires argv with an executable", name)
		}
		return nil
	}
	if strings.TrimSpace(argv[0]) == "" {
		return invalid("%s argv requires a nonempty executable", name)
	}
	base := strings.ToLower(path.Base(strings.ReplaceAll(argv[0], `\`, "/")))
	base = strings.TrimSuffix(base, ".exe")
	if base == "sh" || base == "bash" || base == "dash" || base == "zsh" || base == "cmd" || base == "powershell" || base == "pwsh" {
		return invalid("%s must not invoke a shell", name)
	}
	if base == "busybox" && len(argv) > 1 {
		launcher := strings.ToLower(strings.TrimSuffix(path.Base(strings.ReplaceAll(argv[1], `\`, "/")), ".exe"))
		if launcher == "sh" || launcher == "ash" || launcher == "bash" {
			return invalid("%s must not invoke a shell through busybox", name)
		}
	}
	if base == "env" {
		var envFields []string
		for _, arg := range argv[1:] {
			envFields = append(envFields, strings.Fields(arg)...)
		}
		for i, field := range envFields {
			lowerArg := strings.ToLower(strings.TrimSuffix(path.Base(strings.ReplaceAll(field, `\`, "/")), ".exe"))
			if lowerArg == "sh" || lowerArg == "bash" || lowerArg == "dash" || lowerArg == "zsh" || lowerArg == "ash" || lowerArg == "cmd" || lowerArg == "powershell" || lowerArg == "pwsh" {
				return invalid("%s must not invoke a shell through env", name)
			}
			if i+1 < len(envFields) && ((lowerArg == "python" || lowerArg == "python3" || lowerArg == "pypy" || lowerArg == "pypy3") && envFields[i+1] == "-c" || lowerArg == "node" && envFields[i+1] == "-e") {
				return invalid("%s argv must not enable an embedded command interpreter", name)
			}
		}
	}
	for i, arg := range argv {
		if strings.ContainsAny(arg, "\r\n\x00") {
			return invalid("%s argv[%d] contains an invalid control character", name, i)
		}
		if strings.Contains(arg, "{") || strings.Contains(arg, "}") {
			switch arg {
			case "{changed_files}", "{selected_tests}", "{base_sha}", "{head_sha}":
			default:
				return invalid("%s argv[%d] contains an invalid or partial placeholder", name, i)
			}
		}
		lower := strings.ToLower(arg)
		if (base == "python" || base == "python3" || base == "pypy" || base == "pypy3") && i > 0 && lower == "-c" || base == "node" && i > 0 && lower == "-e" {
			return invalid("%s argv must not enable an embedded command interpreter", name)
		}
	}
	return nil
}

func validateAdapterStrings(c Config) error {
	for _, group := range [][]string{c.Adapters.Gradle.UnitTestTasks, c.Adapters.Gradle.IntegrationTestTasks, c.Adapters.Nx.TestTargetNames} {
		for _, value := range group {
			if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n\x00") {
				return invalid("adapter task/target names must be nonempty single-line values")
			}
		}
	}
	return nil
}
