package config

import "github.com/ivp/ivp/internal/domain"

type Config struct {
	Version    int              `yaml:"version" json:"version"`
	Repository RepositoryConfig `yaml:"repository" json:"repository"`
	Analysis   AnalysisConfig   `yaml:"analysis" json:"analysis"`
	Server     ServerConfig     `yaml:"server" json:"server"`
	Modes      ModesConfig      `yaml:"modes" json:"modes"`
	Selection  SelectionConfig  `yaml:"selection" json:"selection"`
	Safety     SafetyConfig     `yaml:"safety" json:"safety"`
	Execution  ExecutionConfig  `yaml:"execution" json:"execution"`
	Adapters   AdaptersConfig   `yaml:"adapters" json:"adapters"`
	Telemetry  TelemetryConfig  `yaml:"telemetry" json:"telemetry"`
}

type RepositoryConfig struct {
	Slug          string `yaml:"slug" json:"slug"`
	DefaultBranch string `yaml:"default_branch" json:"default_branch"`
}

type AnalysisConfig struct {
	BaseRef                          string `yaml:"base_ref" json:"base_ref"`
	FollowRenames                    bool   `yaml:"follow_renames" json:"follow_renames"`
	IncludeUntracked                 bool   `yaml:"include_untracked" json:"include_untracked"`
	MaxChangedFilesForSelectivePlan  int    `yaml:"max_changed_files_for_selective_plan" json:"max_changed_files_for_selective_plan"`
	GraphStalenessMinutes            int    `yaml:"graph_staleness_minutes" json:"graph_staleness_minutes"`
	CoverageStalenessDays            int    `yaml:"coverage_staleness_days" json:"coverage_staleness_days"`
	HistoryWindowDays                int    `yaml:"history_window_days" json:"history_window_days"`
	RequireCleanDependencyResolution bool   `yaml:"require_clean_dependency_resolution" json:"require_clean_dependency_resolution"`
}

type ServerConfig struct {
	Endpoint              string `yaml:"endpoint" json:"endpoint"`
	ConnectTimeoutSeconds int    `yaml:"connect_timeout_seconds" json:"connect_timeout_seconds"`
	RequestTimeoutSeconds int    `yaml:"request_timeout_seconds" json:"request_timeout_seconds"`
	OfflinePolicy         string `yaml:"offline_policy" json:"offline_policy"`
}

type ModesConfig struct {
	Default      domain.PlanMode        `yaml:"default" json:"default"`
	Observe      ObserveModeConfig      `yaml:"observe" json:"observe"`
	FastFeedback FastFeedbackModeConfig `yaml:"fast_feedback" json:"fast_feedback"`
	RiskBasedCI  RiskBasedCIModeConfig  `yaml:"risk_based_ci" json:"risk_based_ci"`
}

type ObserveModeConfig struct {
	ExecuteFullSuite       bool `yaml:"execute_full_suite" json:"execute_full_suite"`
	SelectedPlanOnlyReport bool `yaml:"selected_plan_only_report" json:"selected_plan_only_report"`
}

type FastFeedbackModeConfig struct {
	FinalCIFullSuiteRequired bool `yaml:"final_ci_full_suite_required" json:"final_ci_full_suite_required"`
	AllowSelectiveInnerLoop  bool `yaml:"allow_selective_inner_loop" json:"allow_selective_inner_loop"`
}

type RiskBasedCIModeConfig struct {
	EnabledByOrganizationPolicy bool    `yaml:"enabled_by_organization_policy" json:"enabled_by_organization_policy"`
	AuditSampleRate             float64 `yaml:"audit_sample_rate" json:"audit_sample_rate"`
	MinimumCalibrationRuns      int     `yaml:"minimum_calibration_runs" json:"minimum_calibration_runs"`
	MinimumAuditRuns            int     `yaml:"minimum_audit_runs" json:"minimum_audit_runs"`
}

type SelectionConfig struct {
	LowRiskMaxScore                      int     `yaml:"low_risk_max_score" json:"low_risk_max_score"`
	MediumRiskMaxScore                   int     `yaml:"medium_risk_max_score" json:"medium_risk_max_score"`
	LowRiskMinWeightedCoverage           float64 `yaml:"low_risk_min_weighted_coverage" json:"low_risk_min_weighted_coverage"`
	MediumRiskMinWeightedCoverage        float64 `yaml:"medium_risk_min_weighted_coverage" json:"medium_risk_min_weighted_coverage"`
	MinimumSelectionConfidence           float64 `yaml:"minimum_selection_confidence" json:"minimum_selection_confidence"`
	MaxSelectiveEstimatedRuntimeFraction float64 `yaml:"max_selective_estimated_runtime_fraction" json:"max_selective_estimated_runtime_fraction"`
	UnknownEvidenceAction                string  `yaml:"unknown_evidence_action" json:"unknown_evidence_action"`
	NonDeterministicTestAction           string  `yaml:"non_deterministic_test_action" json:"non_deterministic_test_action"`
	IfNoTestsSelected                    string  `yaml:"if_no_tests_selected" json:"if_no_tests_selected"`
}

type SafetyConfig struct {
	HardFullSuitePaths        []string    `yaml:"hard_full_suite_paths" json:"hard_full_suite_paths"`
	HardFullSuiteRiskLabels   []string    `yaml:"hard_full_suite_risk_labels" json:"hard_full_suite_risk_labels"`
	AlwaysRunCheckIDs         []string    `yaml:"always_run_check_ids" json:"always_run_check_ids"`
	CriticalPathPatterns      []string    `yaml:"critical_path_patterns" json:"critical_path_patterns"`
	DocumentationPathPatterns []string    `yaml:"documentation_path_patterns" json:"documentation_path_patterns"`
	Audit                     AuditConfig `yaml:"audit" json:"audit"`
}

type AuditConfig struct {
	Enabled                        bool    `yaml:"enabled" json:"enabled"`
	DefaultMode                    string  `yaml:"default_mode" json:"default_mode"`
	SampleRate                     float64 `yaml:"sample_rate" json:"sample_rate"`
	MinimumAuditsBeforeSelectiveCI int     `yaml:"minimum_audits_before_selective_ci" json:"minimum_audits_before_selective_ci"`
	QuarantineOnConfirmedMiss      bool    `yaml:"quarantine_on_confirmed_miss" json:"quarantine_on_confirmed_miss"`
	QuarantineHours                int     `yaml:"quarantine_hours" json:"quarantine_hours"`
	RepeatFailedTestCount          int     `yaml:"repeat_failed_test_count" json:"repeat_failed_test_count"`
}

// ExecutionConfig is local-only execution metadata. full_suite_argv is always
// passed as an argv array and is never placed in a server-generated plan.
type ExecutionConfig struct {
	FullSuiteArgv  []string `yaml:"full_suite_argv" json:"full_suite_argv"`
	TimeoutSeconds int      `yaml:"timeout_seconds" json:"timeout_seconds"`
	MaxOutputBytes int      `yaml:"max_output_bytes" json:"max_output_bytes"`
	FailureRetryCount int    `yaml:"failure_retry_count" json:"failure_retry_count"`
}

type AdaptersConfig struct {
	Gradle  GradleConfig  `yaml:"gradle" json:"gradle"`
	Maven   MavenConfig   `yaml:"maven" json:"maven"`
	Bazel   BazelConfig   `yaml:"bazel" json:"bazel"`
	Nx      NxConfig      `yaml:"nx" json:"nx"`
	Pytest  PytestConfig  `yaml:"pytest" json:"pytest"`
	Jest    JestConfig    `yaml:"jest" json:"jest"`
	Generic GenericConfig `yaml:"generic" json:"generic"`
}

type GradleConfig struct {
	Enabled              bool     `yaml:"enabled" json:"enabled"`
	WrapperRequired      bool     `yaml:"wrapper_required" json:"wrapper_required"`
	UnitTestTasks        []string `yaml:"unit_test_tasks" json:"unit_test_tasks"`
	IntegrationTestTasks []string `yaml:"integration_test_tasks" json:"integration_test_tasks"`
	CoveragePaths        []string `yaml:"coverage_paths" json:"coverage_paths"`
}

type MavenConfig struct {
	Enabled          bool `yaml:"enabled" json:"enabled"`
	WrapperPreferred bool `yaml:"wrapper_preferred" json:"wrapper_preferred"`
	SurefireEnabled  bool `yaml:"surefire_enabled" json:"surefire_enabled"`
	FailsafeEnabled  bool `yaml:"failsafe_enabled" json:"failsafe_enabled"`
	CoveragePaths    []string `yaml:"coverage_paths" json:"coverage_paths"`
}

type BazelConfig struct {
	Enabled                     bool `yaml:"enabled" json:"enabled"`
	UseConfiguredTargetPatterns bool `yaml:"use_configured_target_patterns" json:"use_configured_target_patterns"`
	CoveragePaths               []string `yaml:"coverage_paths" json:"coverage_paths"`
}

type NxConfig struct {
	Enabled         bool     `yaml:"enabled" json:"enabled"`
	TestTargetNames []string `yaml:"test_target_names" json:"test_target_names"`
	CoveragePaths   []string `yaml:"coverage_paths" json:"coverage_paths"`
}

type PytestConfig struct {
	Enabled    bool   `yaml:"enabled" json:"enabled"`
	ConfigFile string `yaml:"config_file" json:"config_file"`
	CoveragePaths []string `yaml:"coverage_paths" json:"coverage_paths"`
}

type JestConfig struct {
	Enabled         bool `yaml:"enabled" json:"enabled"`
	ConfigDiscovery bool `yaml:"config_discovery" json:"config_discovery"`
	CoveragePaths   []string `yaml:"coverage_paths" json:"coverage_paths"`
}

type GenericConfig struct {
	Enabled   bool                `yaml:"enabled" json:"enabled"`
	TestUnits []GenericUnitConfig `yaml:"test_units" json:"test_units"`
	Checks    []GenericUnitConfig `yaml:"checks" json:"checks"`
}

type GenericUnitConfig struct {
	ID                       string   `yaml:"id" json:"id"`
	DisplayName              string   `yaml:"display_name" json:"display_name"`
	Argv                     []string `yaml:"argv" json:"argv"`
	CWD                      string   `yaml:"cwd" json:"cwd"`
	MapsPaths                []string `yaml:"maps_paths" json:"maps_paths"`
	DependsOn                []string `yaml:"depends_on" json:"depends_on"`
	ReportPaths              []string `yaml:"report_paths" json:"report_paths"`
	CoveragePaths            []string `yaml:"coverage_paths" json:"coverage_paths"`
	RiskLabels               []string `yaml:"risk_labels" json:"risk_labels"`
	Mandatory                bool     `yaml:"mandatory" json:"mandatory"`
	TimeoutSeconds           int      `yaml:"timeout_seconds" json:"timeout_seconds"`
	EstimatedDurationSeconds int      `yaml:"estimated_duration_seconds" json:"estimated_duration_seconds"`
	ResourceGroup            string   `yaml:"resource_group" json:"resource_group"`
}

type TelemetryConfig struct {
	Enabled               bool `yaml:"enabled" json:"enabled"`
	CaptureRawStdout      bool `yaml:"capture_raw_stdout" json:"capture_raw_stdout"`
	CaptureFailureExcerpt bool `yaml:"capture_failure_excerpt" json:"capture_failure_excerpt"`
	MaxExcerptBytes       int  `yaml:"max_excerpt_bytes" json:"max_excerpt_bytes"`
	RetentionDays         int  `yaml:"retention_days" json:"retention_days"`
}

// OrganizationPolicy carries security constraints that repository YAML cannot
// weaken. Config is the organization's baseline config.
type OrganizationPolicy struct {
	Version                  int64
	Config                   Config
	MandatoryCheckIDs        []string
	HardFullSuitePaths       []string
	HardFullSuiteRiskLabels  []string
	AllowRiskBasedCI         bool
	OwnerApprovedRiskBasedCI bool
	ObservedCalibrationRuns  int
	ObservedAuditRuns        int
}

// CLIOverrides only exposes the two non-security-sensitive request settings
// permitted by this package. RiskBasedCI cannot be enabled through the CLI.
type CLIOverrides struct {
	Mode    *domain.PlanMode
	BaseRef *string
}
