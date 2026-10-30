package config

import (
	"path/filepath"
	"strings"
	"testing"

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

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
