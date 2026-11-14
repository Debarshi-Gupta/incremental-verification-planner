package config

import (
	"fmt"
	"math"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

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
	if c.Analysis.MaxChangedFilesForSelectivePlan < 1 || c.Analysis.GraphStalenessMinutes < 1 || c.Analysis.CoverageStalenessDays < 1 || c.Analysis.HistoryWindowDays < 1 || c.Analysis.HistoryWindowDays > 3650 {
		return invalid("analysis limits and staleness periods must be positive")
	}
	if c.Analysis.FlakeMinimumComparableObservations < 1 {
		return invalid("analysis.flake_minimum_comparable_observations must be an integer of at least 1; flakiness is classified only after a minimum number of comparable observations (LLD 7.3)")
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
	if !c.Modes.Observe.ExecuteFullSuite {
		return invalid("modes.observe.execute_full_suite must be true; OBSERVE always runs the authoritative full suite")
	}
	if !c.Modes.FastFeedback.FinalCIFullSuiteRequired {
		return invalid("modes.fast_feedback.final_ci_full_suite_required must be true; FAST_FEEDBACK never replaces the final CI full suite")
	}
	if math.IsNaN(c.Modes.RiskBasedCI.AuditSampleRate) || math.IsInf(c.Modes.RiskBasedCI.AuditSampleRate, 0) || c.Modes.RiskBasedCI.AuditSampleRate < 0 || c.Modes.RiskBasedCI.AuditSampleRate > 1 || c.Modes.RiskBasedCI.MinimumCalibrationRuns < 1 || c.Modes.RiskBasedCI.MinimumAuditRuns < 1 {
		return invalid("risk_based_ci calibration and audit values are outside their valid range; calibration and audit minimums must be at least 1")
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
	if c.Safety.Audit.SampleRate < 0 || c.Safety.Audit.SampleRate > 1 || c.Safety.Audit.MinimumAuditsBeforeSelectiveCI < 1 || c.Safety.Audit.QuarantineHours < 1 || c.Safety.Audit.RepeatFailedTestCount < 1 {
		return invalid("safety.audit values are outside their valid range; minimum_audits_before_selective_ci must be at least 1")
	}
	if c.Safety.Audit.ScheduledCadenceHours < 1 || c.Safety.Audit.ScheduledCadenceHours > 8760 {
		return invalid("safety.audit.scheduled_cadence_hours must be between 1 and 8760 hours; the scheduled-audit cadence is a positive interval")
	}
	if c.Safety.Audit.AcknowledgementSLAHours < 1 || c.Safety.Audit.AcknowledgementSLAHours > 8760 {
		return invalid("safety.audit.acknowledgement_sla_hours must be between 1 and 8760 hours; the audit response SLA is a positive interval")
	}
	if !c.Safety.Audit.QuarantineOnConfirmedMiss {
		return invalid("safety.audit.quarantine_on_confirmed_miss must be true; a confirmed miss quarantines the affected scope")
	}
	if c.Execution.TimeoutSeconds < 1 || c.Execution.MaxOutputBytes < 1024 || c.Execution.MaxOutputBytes > 64<<20 {
		return invalid("execution timeout and output bounds are invalid")
	}
	if c.Execution.FailureRetryCount < 0 || c.Execution.FailureRetryCount > 3 {
		return invalid("execution.failure_retry_count must be between 0 and 3")
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
	if c.Telemetry.CaptureRawStdout {
		return invalid("telemetry.capture_raw_stdout must be false; raw stdout capture is not supported, only sanitized excerpts and hashes are retained")
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
	if err := validateReviewedExclusions(c.Safety.ReviewedExclusions); err != nil {
		return err
	}
	if err := validateHardPathExceptions(c.Safety.HardPathExceptions); err != nil {
		return err
	}
	if err := validateGeneric(c.Adapters.Generic); err != nil {
		return err
	}
	if err := validateAdapterStrings(c); err != nil {
		return err
	}
	for adapter, paths := range map[string][]string{
		"gradle": c.Adapters.Gradle.CoveragePaths, "maven": c.Adapters.Maven.CoveragePaths,
		"bazel": c.Adapters.Bazel.CoveragePaths, "nx": c.Adapters.Nx.CoveragePaths,
		"pytest": c.Adapters.Pytest.CoveragePaths, "jest": c.Adapters.Jest.CoveragePaths,
	} {
		for _, pattern := range paths {
			if err := validateGlob(pattern); err != nil {
				return invalid("adapters.%s.coverage_paths: %v", adapter, err)
			}
		}
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

// Field patterns for reviewed exclusions. They are byte-identical to the
// patterns published in schemas/ivp-config.schema.json so the schema and
// config.Validate reach the same verdict on every value (ADR 0011).
var (
	reviewedFieldPattern = regexp.MustCompile(`^\S(?:.*\S)?$`)
	reviewedAtPattern    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// validateReviewedExclusions enforces the reviewed-exclusion contract (ADR
// 0013): every field is mandatory and trimmed, reviewed_at is a real calendar
// date in UTC that is not in the future, and each unit key is reviewed at
// most once. Calendar validity and the future-date rule are runtime-only
// (the schema cannot express them); the trimmed-field and date-shape rules
// mirror the published schema patterns exactly.
func validateReviewedExclusions(entries []ReviewedExclusionConfig) error {
	seen := make(map[string]bool, len(entries))
	for i, entry := range entries {
		for _, field := range []struct{ name, value string }{
			{"unit_key", entry.UnitKey},
			{"reviewer", entry.Reviewer},
			{"rationale", entry.Rationale},
		} {
			if !reviewedFieldPattern.MatchString(field.value) {
				return invalid("safety.reviewed_exclusions[%d].%s must be a single trimmed nonempty value", i, field.name)
			}
		}
		if !reviewedAtPattern.MatchString(entry.ReviewedAt) {
			return invalid("safety.reviewed_exclusions[%d].reviewed_at must be a calendar date in YYYY-MM-DD form", i)
		}
		date, err := time.Parse("2006-01-02", entry.ReviewedAt)
		if err != nil || date.Format("2006-01-02") != entry.ReviewedAt {
			return invalid("safety.reviewed_exclusions[%d].reviewed_at %q is not a real calendar date", i, entry.ReviewedAt)
		}
		if date.After(time.Now().UTC()) {
			return invalid("safety.reviewed_exclusions[%d].reviewed_at %s is in the future", i, entry.ReviewedAt)
		}
		if seen[entry.UnitKey] {
			return invalid("safety.reviewed_exclusions reviews unit key %q more than once", entry.UnitKey)
		}
		seen[entry.UnitKey] = true
	}
	return nil
}

// hardPathExceptionRules is the closed vocabulary of hard rules a narrow
// exception may cover (ADR 0019 §1): the rule-3 path escalation codes. The
// label-based rule-4 codes are absent by design — labels are never
// exceptable — and CONFIG_OR_TOOLCHAIN_CHANGE is absent because the LLD's
// reviewed exception attaches to the lockfile, migration, and schema-contract
// clauses of rule 3 specifically (LLD 6.3 Stage 3 line 437).
var hardPathExceptionRules = []string{
	string(domain.EscLockfileChange),
	string(domain.EscMigrationChange),
	string(domain.EscPublicContractChange),
}

// validateHardPathExceptions enforces the narrow-exception contract (ADR
// 0019): every field is mandatory and trimmed, path is a valid repository
// glob, rule is one of the three rule-3 escalation codes, reviewed_at is a
// real calendar date in UTC that is not in the future, and each path+rule
// pair is reviewed at most once. Calendar validity and the future-date rule
// are runtime-only (the schema cannot express them); the trimmed-field and
// date-shape rules mirror the published schema patterns exactly.
func validateHardPathExceptions(entries []HardPathExceptionConfig) error {
	seen := make(map[string]bool, len(entries))
	for i, entry := range entries {
		for _, field := range []struct{ name, value string }{
			{"path", entry.Path},
			{"rule", entry.Rule},
			{"reviewer", entry.Reviewer},
			{"rationale", entry.Rationale},
		} {
			if !reviewedFieldPattern.MatchString(field.value) {
				return invalid("safety.hard_path_exceptions[%d].%s must be a single trimmed nonempty value", i, field.name)
			}
		}
		if err := validateGlob(entry.Path); err != nil {
			return invalid("safety.hard_path_exceptions[%d].path: %v", i, err)
		}
		if !slices.Contains(hardPathExceptionRules, entry.Rule) {
			return invalid("safety.hard_path_exceptions[%d].rule %q must be one of %s", i, entry.Rule, strings.Join(hardPathExceptionRules, ", "))
		}
		if !reviewedAtPattern.MatchString(entry.ReviewedAt) {
			return invalid("safety.hard_path_exceptions[%d].reviewed_at must be a calendar date in YYYY-MM-DD form", i)
		}
		date, err := time.Parse("2006-01-02", entry.ReviewedAt)
		if err != nil || date.Format("2006-01-02") != entry.ReviewedAt {
			return invalid("safety.hard_path_exceptions[%d].reviewed_at %q is not a real calendar date", i, entry.ReviewedAt)
		}
		if date.After(time.Now().UTC()) {
			return invalid("safety.hard_path_exceptions[%d].reviewed_at %s is in the future", i, entry.ReviewedAt)
		}
		key := entry.Path + "\x00" + entry.Rule
		if seen[key] {
			return invalid("safety.hard_path_exceptions reviews path %q for rule %s more than once", entry.Path, entry.Rule)
		}
		seen[key] = true
	}
	return nil
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
		for _, pattern := range append(append([]string(nil), u.ReportPaths...), u.CoveragePaths...) {
			if err := validateGlob(pattern); err != nil {
				return invalid("generic unit %q report/coverage path: %v", u.ID, err)
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
