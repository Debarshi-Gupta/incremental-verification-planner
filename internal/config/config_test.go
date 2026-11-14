package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ivp/ivp/internal/domain"
)

func TestDefaultsImplementSafePlannerAndExecutionPolicy(t *testing.T) {
	cfg := Defaults()
	if err := Validate(cfg); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	if cfg.Selection.MinimumSelectionConfidence != 0.98 || cfg.Selection.LowRiskMinWeightedCoverage != 0.95 || cfg.Selection.MediumRiskMinWeightedCoverage != 0.99 {
		t.Fatalf("unexpected default selection policy: %+v", cfg.Selection)
	}
	if cfg.Modes.RiskBasedCI.EnabledByOrganizationPolicy {
		t.Fatal("risk-based CI must be disabled by default")
	}
	issues := cfg.ReadinessIssues()
	if len(issues) == 0 || !strings.Contains(strings.Join(issues, " "), "full_suite_argv") {
		t.Fatalf("expected missing full-suite command readiness issue, got %v", issues)
	}
}

func TestShippedDefaultConfigLoadsWithExplicitEmptyFallback(t *testing.T) {
	filename := filepath.Join("..", "..", "config", "default-config.yaml")
	cfg, err := LoadFile(filename)
	if err != nil {
		t.Fatalf("shipped default config is invalid: %v", err)
	}
	if len(cfg.Execution.FullSuiteArgv) != 0 || cfg.Execution.TimeoutSeconds != 3600 || cfg.Execution.MaxOutputBytes != 1<<20 {
		t.Fatalf("unexpected default execution policy: %+v", cfg.Execution)
	}
}

func TestDecodeRejectsUnknownFieldsAndLoweredSafetyThresholds(t *testing.T) {
	for name, input := range map[string]string{
		"unknown":            "unknown_setting: true\n",
		"confidence":         "selection:\n  minimum_selection_confidence: 0.97\n",
		"coverage":           "selection:\n  low_risk_min_weighted_coverage: 0.90\n",
		"fallback policy":    "server:\n  offline_policy: IGNORE\n",
		"multiple documents": "version: 1\n---\nversion: 1\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(strings.NewReader(input), Defaults()); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}
}

func TestDecodePreservesDefaultsForOmittedKeys(t *testing.T) {
	cfg, err := Decode(strings.NewReader("version: 1\nrepository:\n  slug: team/app\n"), Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Analysis.BaseRef != "origin/main" || !cfg.Safety.Audit.Enabled || cfg.Adapters.Pytest.ConfigFile != "pyproject.toml" {
		t.Fatalf("omitted config fields did not retain product defaults: %+v", cfg)
	}
}

func TestRepositoryConfigCannotRemoveOrganizationMandatoryChecks(t *testing.T) {
	orgCfg := Defaults()
	orgCfg.Server.Endpoint = "https://ivp.example.test"
	orgCfg.Safety.AlwaysRunCheckIDs = append(orgCfg.Safety.AlwaysRunCheckIDs, "org-release-gate")
	org := &OrganizationPolicy{Version: 7, Config: orgCfg, MandatoryCheckIDs: []string{"org-security-gate"}, HardFullSuiteRiskLabels: []string{"TENANT_BOUNDARY"}}
	repo := `safety:
  always_run_check_ids: [repo-format]
  hard_full_suite_risk_labels: [REPO_CRITICAL]
`
	cfg, err := Effective(org, []byte(repo), CLIOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"org-release-gate", "org-security-gate", "repo-format", "format-check"} {
		if !containsString(cfg.Safety.AlwaysRunCheckIDs, required) {
			t.Errorf("mandatory check %q was lost: %v", required, cfg.Safety.AlwaysRunCheckIDs)
		}
	}
	for _, required := range []string{"TENANT_BOUNDARY", "REPO_CRITICAL"} {
		if !containsString(cfg.Safety.HardFullSuiteRiskLabels, required) {
			t.Errorf("hard risk label %q was lost: %v", required, cfg.Safety.HardFullSuiteRiskLabels)
		}
	}
	if cfg.Server.Endpoint != orgCfg.Server.Endpoint {
		t.Fatalf("repository config changed trusted endpoint: %q", cfg.Server.Endpoint)
	}
}

func TestRepositoryConfigCannotWeakenOrganizationOrEnableRiskBasedCI(t *testing.T) {
	orgCfg := Defaults()
	orgCfg.Server.Endpoint = "https://ivp.example.test"
	org := &OrganizationPolicy{Config: orgCfg}
	for _, input := range []string{
		"selection:\n  low_risk_max_score: 25\n",
		"analysis:\n  max_changed_files_for_selective_plan: 1000\n",
		"analysis:\n  flake_minimum_comparable_observations: 1\n",
		"server:\n  endpoint: https://attacker.invalid\n",
		"analysis:\n  require_clean_dependency_resolution: false\n",
		"modes:\n  default: RISK_BASED_CI\n  risk_based_ci:\n    enabled_by_organization_policy: true\n",
	} {
		if _, err := Effective(org, []byte(input), CLIOverrides{}); err == nil {
			t.Errorf("unsafe repository override accepted: %s", input)
		}
	}
}

func TestExecutionFallbackMustBeArgvAndShellsAreRejectedAcrossPlatforms(t *testing.T) {
	for _, argv := range [][]string{
		{`C:\Windows\System32\cmd.exe`, "/c", "pytest"},
		{`C:\tools\powershell.exe`, "-Command", "pytest"},
		{"env", "-S", "bash -c echo unsafe"},
		{"busybox.exe", "sh", "-c", "unsafe"},
	} {
		cfg := Defaults()
		cfg.Execution.FullSuiteArgv = argv
		if err := Validate(cfg); err == nil {
			t.Errorf("unsafe full-suite argv was accepted: %#v", argv)
		}
	}
	cfg := Defaults()
	cfg.Execution.FullSuiteArgv = []string{"./gradlew", "check"}
	if err := Validate(cfg); err != nil {
		t.Fatalf("safe argv array rejected: %v", err)
	}
}

func TestOrganizationEndpointCannotBeSetByRepositoryConfig(t *testing.T) {
	org := &OrganizationPolicy{Config: Defaults()}
	if _, err := Effective(org, []byte("server:\n  endpoint: https://repo-controlled.invalid\n"), CLIOverrides{}); err == nil {
		t.Fatal("repository config supplied a server endpoint when the organization had none")
	}
}

func TestGenericConfigRejectsShellsTraversalAndDependencyCycles(t *testing.T) {
	cases := []string{
		`adapters:
  generic:
    test_units:
      - id: x
        argv: [bash, -c, "echo unsafe"]
        timeout_seconds: 10
`,
		`adapters:
  generic:
    test_units:
      - id: x
        argv: [pytest, "{changed_files}.txt"]
        timeout_seconds: 10
`,
		`adapters:
  generic:
    test_units:
      - id: x
        argv: [pytest]
        cwd: ../outside
        timeout_seconds: 10
`,
		`adapters:
  generic:
    test_units:
      - id: x
        argv: [pytest]
        depends_on: [y]
        timeout_seconds: 10
      - id: y
        argv: [pytest]
        depends_on: [x]
        timeout_seconds: 10
`,
	}
	for i, input := range cases {
		if _, err := Decode(strings.NewReader(input), Defaults()); err == nil {
			t.Errorf("unsafe generic config %d was accepted", i)
		}
	}
}

func TestRiskBasedCINeedsExplicitApprovalAndCompletedCalibration(t *testing.T) {
	orgConfig := Defaults()
	orgConfig.Execution.FullSuiteArgv = []string{"./scripts/verify-all"}
	org := OrganizationPolicy{Config: orgConfig, AllowRiskBasedCI: true, OwnerApprovedRiskBasedCI: true,
		ObservedCalibrationRuns: 500, ObservedAuditRuns: 100}
	input := `modes:
  default: RISK_BASED_CI
  risk_based_ci:
    enabled_by_organization_policy: true
`
	cfg, err := Effective(&org, []byte(input), CLIOverrides{})
	if err != nil {
		t.Fatalf("approved calibrated mode rejected: %v", err)
	}
	if cfg.Modes.Default != domain.ModeRiskBasedCI {
		t.Fatalf("mode not applied: %s", cfg.Modes.Default)
	}
	org.OwnerApprovedRiskBasedCI = false
	if _, err := Effective(&org, []byte(input), CLIOverrides{}); err == nil {
		t.Fatal("risk-based CI accepted without organization owner approval")
	}
}

func TestReviewedExclusionsAreValidatedAndOrganizationScoped(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	valid := ReviewedExclusionConfig{UnitKey: "unit:legacy-suite", Reviewer: "release-owner", ReviewedAt: "2020-01-01", Rationale: "reviewed flaky legacy suite"}

	// Runtime-only rules the published schema cannot express.
	for name, mutate := range map[string]func(*ReviewedExclusionConfig){
		"future review date":         func(e *ReviewedExclusionConfig) { e.ReviewedAt = "2999-01-01" },
		"nonexistent calendar date":  func(e *ReviewedExclusionConfig) { e.ReviewedAt = "2026-02-30" },
		"whitespace-padded reviewer": func(e *ReviewedExclusionConfig) { e.Reviewer = " release-owner " },
		"empty rationale":            func(e *ReviewedExclusionConfig) { e.Rationale = "" },
	} {
		cfg := Defaults()
		entry := valid
		mutate(&entry)
		cfg.Safety.ReviewedExclusions = []ReviewedExclusionConfig{entry}
		if err := Validate(cfg); err == nil {
			t.Errorf("%s accepted by Validate", name)
		}
	}
	duplicate := Defaults()
	duplicate.Safety.ReviewedExclusions = []ReviewedExclusionConfig{valid, valid}
	if err := Validate(duplicate); err == nil {
		t.Error("duplicate reviewed unit key accepted by Validate")
	}
	todayEntry := Defaults()
	todayEntry.Safety.ReviewedExclusions = []ReviewedExclusionConfig{{UnitKey: valid.UnitKey, Reviewer: valid.Reviewer, ReviewedAt: today, Rationale: valid.Rationale}}
	if err := Validate(todayEntry); err != nil {
		t.Errorf("review dated today rejected: %v", err)
	}

	// Reviewed exclusions are organization scope: a repository may carry only
	// a byte-for-byte subset of the organization's reviewed entries.
	orgCfg := Defaults()
	orgCfg.Safety.ReviewedExclusions = []ReviewedExclusionConfig{
		valid,
		{UnitKey: "unit:noisy-metrics", Reviewer: "sre-lead", ReviewedAt: "2020-06-01", Rationale: "reviewed metrics exporter suite"},
	}
	org := &OrganizationPolicy{Config: orgCfg}
	exactSubset := `safety:
  reviewed_exclusions:
    - unit_key: unit:legacy-suite
      reviewer: release-owner
      reviewed_at: '2020-01-01'
      rationale: reviewed flaky legacy suite
`
	cfg, err := Effective(org, []byte(exactSubset), CLIOverrides{})
	if err != nil {
		t.Fatalf("exact reviewed subset rejected: %v", err)
	}
	if len(cfg.Safety.ReviewedExclusions) != 1 || cfg.Safety.ReviewedExclusions[0].UnitKey != "unit:legacy-suite" {
		t.Fatalf("reviewed subset not carried through: %+v", cfg.Safety.ReviewedExclusions)
	}
	for name, input := range map[string]string{
		"unreviewed exclusion added": `safety:
  reviewed_exclusions:
    - unit_key: unit:legacy-suite
      reviewer: release-owner
      reviewed_at: '2020-01-01'
      rationale: reviewed flaky legacy suite
    - unit_key: unit:unreviewed
      reviewer: repo-owner
      reviewed_at: '2020-01-01'
      rationale: repository claims a review
`,
		"reviewed entry altered": `safety:
  reviewed_exclusions:
    - unit_key: unit:legacy-suite
      reviewer: repo-owner
      reviewed_at: '2020-01-01'
      rationale: reviewed flaky legacy suite
`,
	} {
		if _, err := Effective(org, []byte(input), CLIOverrides{}); err == nil {
			t.Errorf("%s accepted by organization constraints", name)
		}
	}
}

// LLD 6.3 Stage 3 rule 3's "narrower, safe exception" and ADR 0019: a
// hard-path exception is a five-field, org-scoped, date-validated policy
// entry whose rule vocabulary is closed to the three rule-3 codes.
func TestHardPathExceptionsAreValidatedAndOrganizationScoped(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	valid := HardPathExceptionConfig{Path: "package-lock.json", Rule: "LOCKFILE_CHANGE", Reviewer: "build-owner", ReviewedAt: "2020-01-01", Rationale: "reviewed dependency refresh"}

	// Runtime-only rules the published schema cannot express.
	for name, mutate := range map[string]func(*HardPathExceptionConfig){
		"future review date":         func(e *HardPathExceptionConfig) { e.ReviewedAt = "2999-01-01" },
		"nonexistent calendar date":  func(e *HardPathExceptionConfig) { e.ReviewedAt = "2026-02-30" },
		"whitespace-padded reviewer": func(e *HardPathExceptionConfig) { e.Reviewer = " build-owner " },
		"whitespace-padded path":     func(e *HardPathExceptionConfig) { e.Path = " package-lock.json" },
		"empty rationale":            func(e *HardPathExceptionConfig) { e.Rationale = "" },
		"unknown rule":               func(e *HardPathExceptionConfig) { e.Rule = "SECURITY_CRITICAL_CHANGE" },
		"non-exceptable rule":        func(e *HardPathExceptionConfig) { e.Rule = "CONFIG_OR_TOOLCHAIN_CHANGE" },
		"non-glob path":              func(e *HardPathExceptionConfig) { e.Path = "../outside/repo" },
	} {
		cfg := Defaults()
		entry := valid
		mutate(&entry)
		cfg.Safety.HardPathExceptions = []HardPathExceptionConfig{entry}
		if err := Validate(cfg); err == nil {
			t.Errorf("%s accepted by Validate", name)
		}
	}
	// Duplicates are keyed by path + rule: the same path under another rule
	// is a distinct reviewed entry.
	duplicate := Defaults()
	duplicate.Safety.HardPathExceptions = []HardPathExceptionConfig{valid, valid}
	if err := Validate(duplicate); err == nil {
		t.Error("duplicate path+rule exception accepted by Validate")
	}
	otherRule := valid
	otherRule.Rule = "MIGRATION_CHANGE"
	twoRules := Defaults()
	twoRules.Safety.HardPathExceptions = []HardPathExceptionConfig{valid, otherRule}
	if err := Validate(twoRules); err != nil {
		t.Errorf("same path under two rules rejected: %v", err)
	}
	todayEntry := Defaults()
	todayEntry.Safety.HardPathExceptions = []HardPathExceptionConfig{{Path: valid.Path, Rule: valid.Rule, Reviewer: valid.Reviewer, ReviewedAt: today, Rationale: valid.Rationale}}
	if err := Validate(todayEntry); err != nil {
		t.Errorf("exception dated today rejected: %v", err)
	}

	// Hard-path exceptions are organization scope too (ADR 0019 §2): a
	// repository may carry only a byte-for-byte subset of the organization's
	// reviewed entries, keyed by path + rule.
	orgCfg := Defaults()
	orgCfg.Safety.HardPathExceptions = []HardPathExceptionConfig{
		valid,
		{Path: "yarn.lock", Rule: "LOCKFILE_CHANGE", Reviewer: "build-owner", ReviewedAt: "2020-06-01", Rationale: "reviewed workspace lock refresh"},
	}
	org := &OrganizationPolicy{Config: orgCfg}
	exactSubset := `safety:
  hard_path_exceptions:
    - path: package-lock.json
      rule: LOCKFILE_CHANGE
      reviewer: build-owner
      reviewed_at: '2020-01-01'
      rationale: reviewed dependency refresh
`
	cfg, err := Effective(org, []byte(exactSubset), CLIOverrides{})
	if err != nil {
		t.Fatalf("exact exception subset rejected: %v", err)
	}
	if len(cfg.Safety.HardPathExceptions) != 1 || cfg.Safety.HardPathExceptions[0].Path != "package-lock.json" {
		t.Fatalf("exception subset not carried through: %+v", cfg.Safety.HardPathExceptions)
	}
	for name, input := range map[string]string{
		"unreviewed exception added": `safety:
  hard_path_exceptions:
    - path: package-lock.json
      rule: LOCKFILE_CHANGE
      reviewer: build-owner
      reviewed_at: '2020-01-01'
      rationale: reviewed dependency refresh
    - path: Cargo.lock
      rule: LOCKFILE_CHANGE
      reviewer: repo-owner
      reviewed_at: '2020-01-01'
      rationale: repository claims a review
`,
		"reviewed exception altered": `safety:
  hard_path_exceptions:
    - path: package-lock.json
      rule: LOCKFILE_CHANGE
      reviewer: repo-owner
      reviewed_at: '2020-01-01'
      rationale: reviewed dependency refresh
`,
	} {
		if _, err := Effective(org, []byte(input), CLIOverrides{}); err == nil {
			t.Errorf("%s accepted by organization constraints", name)
		}
	}
}

// LLD 7.3: flakiness is classified only after a minimum number of comparable
// observations. The gate ships as analysis.flake_minimum_comparable_observations
// with a default of 5 (aligned with LLD line 226's five-observation rule),
// appears in both shipped templates, and rejects 0/negative values.
func TestFlakeMinimumComparableObservationsDefaultValidationAndTemplates(t *testing.T) {
	cfg := Defaults()
	if cfg.Analysis.FlakeMinimumComparableObservations != 5 {
		t.Fatalf("default flake_minimum_comparable_observations = %d, want 5", cfg.Analysis.FlakeMinimumComparableObservations)
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	one := Defaults()
	one.Analysis.FlakeMinimumComparableObservations = 1
	if err := Validate(one); err != nil {
		t.Errorf("minimum of 1 rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"zero":     func(c *Config) { c.Analysis.FlakeMinimumComparableObservations = 0 },
		"negative": func(c *Config) { c.Analysis.FlakeMinimumComparableObservations = -1 },
	} {
		cfg := Defaults()
		mutate(&cfg)
		err := Validate(cfg)
		if err == nil {
			t.Errorf("%s flake_minimum_comparable_observations accepted by Validate", name)
			continue
		}
		if !strings.Contains(err.Error(), "analysis.flake_minimum_comparable_observations") {
			t.Errorf("%s rejection does not name the key: %v", name, err)
		}
	}
	for _, name := range []string{
		filepath.Join("..", "..", "config", "default-config.yaml"),
		filepath.Join("..", "..", "internal", "cli", "default-config.yaml"),
	} {
		template, err := LoadFile(name)
		if err != nil {
			t.Fatalf("shipped template %s does not load: %v", name, err)
		}
		if template.Analysis.FlakeMinimumComparableObservations != 5 {
			t.Errorf("template %s carries flake_minimum_comparable_observations = %d, want 5", name, template.Analysis.FlakeMinimumComparableObservations)
		}
	}
	// A repository may raise the evidence bar but never lower it below the
	// organization baseline (mirrors the history-window precedence rule).
	raised := "analysis:\n  flake_minimum_comparable_observations: 10\n"
	org := &OrganizationPolicy{Config: Defaults()}
	cfg, err := Effective(org, []byte(raised), CLIOverrides{})
	if err != nil {
		t.Fatalf("repository raising the flake minimum rejected: %v", err)
	}
	if cfg.Analysis.FlakeMinimumComparableObservations != 10 {
		t.Fatalf("raised flake minimum not applied: %d", cfg.Analysis.FlakeMinimumComparableObservations)
	}
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

// TestMavenProviderTogglesReachAdapterConfig pins the tri-state plumbing for
// adapters.maven.surefire_enabled / failsafe_enabled: the product defaults
// keep both providers enabled, an explicit false reaches the adapter view as
// an explicit false, an omitted key stays enabled, and a configuration that
// never carried the key stays unset instead of reading as "disabled".
func TestMavenProviderTogglesReachAdapterConfig(t *testing.T) {
	view := Defaults().ToAdapterConfig("maven", nil)
	if view.SurefireEnabled == nil || !*view.SurefireEnabled {
		t.Fatalf("default surefire toggle = %v, want enabled", view.SurefireEnabled)
	}
	if view.FailsafeEnabled == nil || !*view.FailsafeEnabled {
		t.Fatalf("default failsafe toggle = %v, want enabled", view.FailsafeEnabled)
	}

	disabled, err := Decode(strings.NewReader("adapters:\n  maven:\n    surefire_enabled: false\n"), Defaults())
	if err != nil {
		t.Fatalf("decode of an explicit surefire opt-out failed: %v", err)
	}
	if disabled.Adapters.Maven.SurefireEnabled == nil || *disabled.Adapters.Maven.SurefireEnabled {
		t.Fatalf("decoded surefire toggle = %v, want explicit false", disabled.Adapters.Maven.SurefireEnabled)
	}
	if disabled.Adapters.Maven.FailsafeEnabled == nil || !*disabled.Adapters.Maven.FailsafeEnabled {
		t.Fatalf("omitted failsafe toggle = %v, want enabled", disabled.Adapters.Maven.FailsafeEnabled)
	}
	view = disabled.ToAdapterConfig("maven", nil)
	if view.SurefireEnabled == nil || *view.SurefireEnabled {
		t.Fatalf("adapter view surefire toggle = %v, want explicit false", view.SurefireEnabled)
	}
	if view.FailsafeEnabled == nil || !*view.FailsafeEnabled {
		t.Fatalf("adapter view failsafe toggle = %v, want enabled", view.FailsafeEnabled)
	}

	unset := Config{}.ToAdapterConfig("maven", nil)
	if unset.SurefireEnabled != nil || unset.FailsafeEnabled != nil {
		t.Fatalf("an unset provider key must stay unset in the adapter view, got %v/%v", unset.SurefireEnabled, unset.FailsafeEnabled)
	}

	other := Defaults().ToAdapterConfig("gradle", nil)
	if other.SurefireEnabled != nil || other.FailsafeEnabled != nil {
		t.Fatalf("a non-maven adapter view must not carry maven provider toggles: %+v", other)
	}
}
