package config

import "github.com/ivp/ivp/internal/domain"

func Defaults() Config {
	return Config{
		Version: 1,
		Analysis: AnalysisConfig{BaseRef: "origin/main", FollowRenames: true, IncludeUntracked: true,
			MaxChangedFilesForSelectivePlan: 500, GraphStalenessMinutes: 1440,
			CoverageStalenessDays: 30, RequireCleanDependencyResolution: true},
		Server: ServerConfig{ConnectTimeoutSeconds: 3, RequestTimeoutSeconds: 10, OfflinePolicy: "FULL_SUITE_FALLBACK"},
		Modes: ModesConfig{
			Default:      domain.ModeFastFeedback,
			Observe:      ObserveModeConfig{ExecuteFullSuite: true, SelectedPlanOnlyReport: true},
			FastFeedback: FastFeedbackModeConfig{FinalCIFullSuiteRequired: true, AllowSelectiveInnerLoop: true},
			RiskBasedCI:  RiskBasedCIModeConfig{EnabledByOrganizationPolicy: false, AuditSampleRate: 0.10, MinimumCalibrationRuns: 500, MinimumAuditRuns: 100},
		},
		Selection: SelectionConfig{LowRiskMaxScore: 24, MediumRiskMaxScore: 59,
			LowRiskMinWeightedCoverage: 0.95, MediumRiskMinWeightedCoverage: 0.99,
			MinimumSelectionConfidence: 0.98, MaxSelectiveEstimatedRuntimeFraction: 0.70,
			UnknownEvidenceAction: "FULL_SUITE", NonDeterministicTestAction: "INCLUDE_AND_LABEL", IfNoTestsSelected: "FULL_SUITE"},
		Execution: ExecutionConfig{FullSuiteArgv: []string{}, TimeoutSeconds: 3600, MaxOutputBytes: 1 << 20},
		Safety: SafetyConfig{
			HardFullSuitePaths: []string{
				"**/pom.xml", "**/build.gradle", "**/build.gradle.kts", "**/settings.gradle", "**/settings.gradle.kts",
				"**/gradle/libs.versions.toml", "**/gradle-wrapper.properties", "**/mvnw", "**/gradlew", "**/MODULE.bazel",
				"**/WORKSPACE*", "**/BUILD", "**/BUILD.bazel", "**/nx.json", "**/project.json", "**/pytest.ini",
				"**/pyproject.toml", "**/jest.config.*", "**/*lock*", "**/migrations/**", "**/db/migrate/**",
			},
			HardFullSuiteRiskLabels: []string{"SECURITY_CRITICAL", "AUTHENTICATION", "AUTHORIZATION", "CRYPTOGRAPHY", "PAYMENT", "DATA_MIGRATION", "PUBLIC_CONTRACT"},
			AlwaysRunCheckIDs:       []string{"format-check", "lint", "typecheck", "secret-scan", "dependency-audit"},
			Audit: AuditConfig{Enabled: true, DefaultMode: "SHADOW", SampleRate: 0.10,
				MinimumAuditsBeforeSelectiveCI: 100, QuarantineOnConfirmedMiss: true, QuarantineHours: 168, RepeatFailedTestCount: 2},
		},
		Adapters: AdaptersConfig{
			Gradle:  GradleConfig{Enabled: true, WrapperRequired: true, UnitTestTasks: []string{"test"}, IntegrationTestTasks: []string{"integrationTest"}},
			Maven:   MavenConfig{Enabled: true, WrapperPreferred: true, SurefireEnabled: true, FailsafeEnabled: true},
			Bazel:   BazelConfig{Enabled: true, UseConfiguredTargetPatterns: true},
			Nx:      NxConfig{Enabled: true, TestTargetNames: []string{"test", "e2e"}},
			Pytest:  PytestConfig{Enabled: true, ConfigFile: "pyproject.toml"},
			Jest:    JestConfig{Enabled: true, ConfigDiscovery: true},
			Generic: GenericConfig{Enabled: true, TestUnits: []GenericUnitConfig{}, Checks: []GenericUnitConfig{}},
		},
		Telemetry: TelemetryConfig{Enabled: true, CaptureRawStdout: false, CaptureFailureExcerpt: true, MaxExcerptBytes: 8192, RetentionDays: 90},
	}
}
