package config

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ivp/ivp/internal/domain"
)

// Effective resolves product defaults, organization policy, repository YAML,
// and permitted per-invocation overrides in the LLD precedence order.
func Effective(org *OrganizationPolicy, repoYAML []byte, cli CLIOverrides) (Config, error) {
	base := Defaults()
	var orgPolicy OrganizationPolicy
	if org != nil {
		orgPolicy = *org
		if orgPolicy.Config.Version != 0 {
			base = orgPolicy.Config
		}
	}
	if err := Validate(base); err != nil {
		return base, fmt.Errorf("organization baseline: %w", err)
	}
	cfg := base
	if len(bytes.TrimSpace(repoYAML)) != 0 {
		var err error
		cfg, err = Decode(bytes.NewReader(repoYAML), cfg)
		if err != nil {
			return cfg, err
		}
	}
	if err := enforceOrganizationConstraints(&cfg, base, orgPolicy); err != nil {
		return cfg, err
	}
	if cli.BaseRef != nil {
		if strings.TrimSpace(*cli.BaseRef) == "" {
			return cfg, invalid("CLI base ref must not be empty")
		}
		cfg.Analysis.BaseRef = *cli.BaseRef
	}
	if cli.Mode != nil {
		if !validModes[*cli.Mode] || *cli.Mode == domain.ModeRiskBasedCI {
			return cfg, invalid("CLI cannot request an unsupported or risk-based CI mode")
		}
		cfg.Modes.Default = *cli.Mode
	}
	if cfg.Modes.Default == domain.ModeRiskBasedCI && !riskBasedCIPermitted(cfg, orgPolicy) {
		return cfg, invalid("RISK_BASED_CI is disabled until organization permission, owner approval, and calibration requirements are met")
	}
	if cfg.Modes.Default == domain.ModeRiskBasedCI && len(cfg.Execution.FullSuiteArgv) == 0 {
		return cfg, invalid("RISK_BASED_CI requires a configured local full-suite argv for high-risk changes and audits")
	}
	if err := Validate(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func enforceOrganizationConstraints(cfg *Config, baseline Config, org OrganizationPolicy) error {
	// Immutable trust settings can only be supplied by organization policy.
	if cfg.Server.Endpoint != baseline.Server.Endpoint {
		return invalid("repository config cannot replace or supply the organization server endpoint")
	}
	if cfg.Server.OfflinePolicy != baseline.Server.OfflinePolicy {
		return invalid("repository config cannot change offline trust behavior")
	}
	if cfg.Execution.FailureRetryCount < baseline.Execution.FailureRetryCount {
		return invalid("repository config cannot weaken the organization failure retry policy")
	}
	if baseline.Analysis.RequireCleanDependencyResolution && !cfg.Analysis.RequireCleanDependencyResolution {
		return invalid("repository config cannot disable clean dependency resolution")
	}
	if cfg.Analysis.MaxChangedFilesForSelectivePlan > baseline.Analysis.MaxChangedFilesForSelectivePlan || cfg.Analysis.GraphStalenessMinutes > baseline.Analysis.GraphStalenessMinutes || cfg.Analysis.CoverageStalenessDays > baseline.Analysis.CoverageStalenessDays || cfg.Analysis.HistoryWindowDays > baseline.Analysis.HistoryWindowDays {
		return invalid("repository config cannot weaken changed-file or evidence-staleness limits")
	}
	if cfg.Analysis.FlakeMinimumComparableObservations < baseline.Analysis.FlakeMinimumComparableObservations {
		return invalid("repository config cannot lower analysis.flake_minimum_comparable_observations below the organization baseline")
	}
	if cfg.Selection.LowRiskMaxScore > baseline.Selection.LowRiskMaxScore || cfg.Selection.MediumRiskMaxScore > baseline.Selection.MediumRiskMaxScore ||
		cfg.Selection.LowRiskMinWeightedCoverage < baseline.Selection.LowRiskMinWeightedCoverage ||
		cfg.Selection.MediumRiskMinWeightedCoverage < baseline.Selection.MediumRiskMinWeightedCoverage ||
		cfg.Selection.MinimumSelectionConfidence < baseline.Selection.MinimumSelectionConfidence ||
		cfg.Selection.MaxSelectiveEstimatedRuntimeFraction > baseline.Selection.MaxSelectiveEstimatedRuntimeFraction {
		return invalid("repository config cannot weaken risk, coverage, confidence, or runtime policy")
	}
	if baseline.Modes.Observe.ExecuteFullSuite && !cfg.Modes.Observe.ExecuteFullSuite {
		return invalid("repository config cannot disable the authoritative OBSERVE full-suite run")
	}
	if baseline.Modes.FastFeedback.FinalCIFullSuiteRequired && !cfg.Modes.FastFeedback.FinalCIFullSuiteRequired {
		return invalid("repository config cannot remove the final CI full-suite requirement")
	}
	if baseline.Safety.Audit.Enabled && !cfg.Safety.Audit.Enabled {
		return invalid("repository config cannot disable organization-required audits")
	}
	if baseline.Safety.Audit.QuarantineOnConfirmedMiss && !cfg.Safety.Audit.QuarantineOnConfirmedMiss {
		return invalid("repository config cannot disable quarantine on a confirmed miss")
	}
	if cfg.Safety.Audit.SampleRate < baseline.Safety.Audit.SampleRate || cfg.Safety.Audit.MinimumAuditsBeforeSelectiveCI < baseline.Safety.Audit.MinimumAuditsBeforeSelectiveCI || cfg.Safety.Audit.QuarantineHours < baseline.Safety.Audit.QuarantineHours || cfg.Safety.Audit.RepeatFailedTestCount > baseline.Safety.Audit.RepeatFailedTestCount {
		return invalid("repository config cannot weaken audit or quarantine requirements")
	}
	// The scheduled-audit cadence and the audit response SLA are safety
	// intervals: a longer interval means fewer full verifications and a
	// slower miss response. A repository may tighten them but never weaken
	// the organization baseline (LLD 10.2, 15.3).
	if cfg.Safety.Audit.ScheduledCadenceHours > baseline.Safety.Audit.ScheduledCadenceHours {
		return invalid("repository config cannot lengthen safety.audit.scheduled_cadence_hours beyond the organization baseline")
	}
	if cfg.Safety.Audit.AcknowledgementSLAHours > baseline.Safety.Audit.AcknowledgementSLAHours {
		return invalid("repository config cannot lengthen safety.audit.acknowledgement_sla_hours beyond the organization baseline")
	}
	if cfg.Modes.RiskBasedCI.MinimumCalibrationRuns < baseline.Modes.RiskBasedCI.MinimumCalibrationRuns || cfg.Modes.RiskBasedCI.MinimumAuditRuns < baseline.Modes.RiskBasedCI.MinimumAuditRuns || cfg.Modes.RiskBasedCI.AuditSampleRate < baseline.Modes.RiskBasedCI.AuditSampleRate {
		return invalid("repository config cannot lower risk-based CI calibration or audit requirements")
	}
	if cfg.Modes.RiskBasedCI.EnabledByOrganizationPolicy && !org.AllowRiskBasedCI {
		return invalid("repository config cannot enable risk-based CI without organization permission")
	}
	// Reviewed exclusions are organization scope: a repository may carry only
	// a subset of the organization's reviewed entries, byte-for-byte as the
	// organization reviewed them. A repository can neither invent an
	// exclusion nor alter a reviewer, date, or rationale (ADR 0013).
	approved := make(map[string]ReviewedExclusionConfig, len(baseline.Safety.ReviewedExclusions))
	for _, entry := range baseline.Safety.ReviewedExclusions {
		approved[entry.UnitKey] = entry
	}
	for _, entry := range cfg.Safety.ReviewedExclusions {
		reviewed, ok := approved[entry.UnitKey]
		if !ok {
			return invalid("repository config cannot add a reviewed exclusion that the organization has not reviewed")
		}
		if reviewed != entry {
			return invalid("repository config cannot alter the organization-reviewed exclusion for unit %q", entry.UnitKey)
		}
	}
	// Hard-path exceptions follow the same organization scope (ADR 0019 §2):
	// a repository may carry only a subset of the organization's reviewed
	// exceptions, byte-for-byte as the organization reviewed them, keyed by
	// path + rule.
	approvedExceptions := make(map[[2]string]HardPathExceptionConfig, len(baseline.Safety.HardPathExceptions))
	for _, entry := range baseline.Safety.HardPathExceptions {
		approvedExceptions[[2]string{entry.Path, entry.Rule}] = entry
	}
	for _, entry := range cfg.Safety.HardPathExceptions {
		reviewed, ok := approvedExceptions[[2]string{entry.Path, entry.Rule}]
		if !ok {
			return invalid("repository config cannot add a hard-path exception that the organization has not reviewed")
		}
		if reviewed != entry {
			return invalid("repository config cannot alter the organization-reviewed hard-path exception for path %q rule %s", entry.Path, entry.Rule)
		}
	}
	if cfg.Server.Endpoint == "" {
		cfg.Server.Endpoint = baseline.Server.Endpoint
	}
	// Required organization gates and hard rules are monotonic unions. A repo
	// may add stricter rules but cannot remove inherited ones.
	cfg.Safety.AlwaysRunCheckIDs = union(cfg.Safety.AlwaysRunCheckIDs, baseline.Safety.AlwaysRunCheckIDs, org.MandatoryCheckIDs)
	cfg.Safety.HardFullSuitePaths = union(cfg.Safety.HardFullSuitePaths, baseline.Safety.HardFullSuitePaths, org.HardFullSuitePaths)
	cfg.Safety.HardFullSuiteRiskLabels = union(cfg.Safety.HardFullSuiteRiskLabels, baseline.Safety.HardFullSuiteRiskLabels, org.HardFullSuiteRiskLabels)
	if cfg.Modes.RiskBasedCI.EnabledByOrganizationPolicy && !riskBasedCIPermitted(*cfg, org) {
		return invalid("RISK_BASED_CI requires explicit organization enablement, owner approval, and completed calibration")
	}
	return Validate(*cfg)
}

func riskBasedCIPermitted(cfg Config, org OrganizationPolicy) bool {
	if !org.AllowRiskBasedCI || !org.OwnerApprovedRiskBasedCI || !cfg.Modes.RiskBasedCI.EnabledByOrganizationPolicy {
		return false
	}
	return org.ObservedCalibrationRuns >= cfg.Modes.RiskBasedCI.MinimumCalibrationRuns &&
		org.ObservedAuditRuns >= cfg.Modes.RiskBasedCI.MinimumAuditRuns &&
		org.ObservedAuditRuns >= cfg.Safety.Audit.MinimumAuditsBeforeSelectiveCI
}

func union(groups ...[]string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, group := range groups {
		for _, item := range group {
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			out = append(out, item)
		}
	}
	slices.Sort(out)
	return out
}

func ToDomainPolicy(c Config, version int64, org *OrganizationPolicy) domain.Policy {
	policy := domain.Policy{
		Version:                            version,
		LowRiskMaxScore:                    c.Selection.LowRiskMaxScore,
		MediumRiskMaxScore:                 c.Selection.MediumRiskMaxScore,
		LowRiskMinWeightedCoverage:         c.Selection.LowRiskMinWeightedCoverage,
		MediumRiskMinWeightedCoverage:      c.Selection.MediumRiskMinWeightedCoverage,
		MinimumSelectionConfidence:         c.Selection.MinimumSelectionConfidence,
		MaxSelectiveRuntimeFraction:        c.Selection.MaxSelectiveEstimatedRuntimeFraction,
		MaxChangedFilesForSelectivePlan:    c.Analysis.MaxChangedFilesForSelectivePlan,
		GraphStaleness:                     time.Duration(c.Analysis.GraphStalenessMinutes) * time.Minute,
		CoverageStaleness:                  time.Duration(c.Analysis.CoverageStalenessDays) * 24 * time.Hour,
		HistoryWindow:                      time.Duration(c.Analysis.HistoryWindowDays) * 24 * time.Hour,
		FlakeMinimumComparableObservations: c.Analysis.FlakeMinimumComparableObservations,
		DependencyFanoutEscalation:         250,
		DependencyCap:                      100,
		MinimumCostFloorMillis:             1000,
		StartupOverheadMillis:              1000,
		HardFullSuitePaths:                 append([]string(nil), c.Safety.HardFullSuitePaths...),
		HardFullSuiteRiskLabels:            append([]string(nil), c.Safety.HardFullSuiteRiskLabels...),
		AlwaysRunCheckIDs:                  append([]string(nil), c.Safety.AlwaysRunCheckIDs...),
		CriticalPathPatterns:               append([]string(nil), c.Safety.CriticalPathPatterns...),
		DocumentationPathPatterns:          append([]string(nil), c.Safety.DocumentationPathPatterns...),
		ReviewedExclusions:                 make([]domain.ReviewedExclusion, 0, len(c.Safety.ReviewedExclusions)),
		HardPathExceptions:                 make([]domain.HardPathException, 0, len(c.Safety.HardPathExceptions)),
		RiskBasedCIEnabled:                 c.Modes.RiskBasedCI.EnabledByOrganizationPolicy,
		// LLD 13.3: traversal is processed in bounded batches. A reached bound
		// marks the graph incomplete and the plan escalates; it never silently
		// truncates the traversal and returns a confident selective plan.
		TraversalLimits: domain.TraversalLimits{MaxNodes: 100000, MaxDepth: 64},
		// LLD 10.2 and 15.3: every repository runs a full verification at
		// least once per cadence, and an audit miss must be acknowledged
		// within the response SLA.
		AuditSchedule: domain.AuditSchedule{
			Cadence:            time.Duration(c.Safety.Audit.ScheduledCadenceHours) * time.Hour,
			AcknowledgementSLA: time.Duration(c.Safety.Audit.AcknowledgementSLAHours) * time.Hour,
		},
		MinimumCalibrationRuns:           c.Modes.RiskBasedCI.MinimumCalibrationRuns,
		MinimumAuditRuns:                 c.Modes.RiskBasedCI.MinimumAuditRuns,
		ObservedCalibrationRuns:          0,
		ObservedAuditRuns:                0,
		FullSuiteTargetRef:               fullSuiteTargetRef(c),
		PolicyAvailable:                  true,
		HighRiskSelectiveFeedbackAllowed: c.Modes.FastFeedback.AllowSelectiveInnerLoop,
	}
	for _, u := range append(append([]GenericUnitConfig{}, c.Adapters.Generic.TestUnits...), c.Adapters.Generic.Checks...) {
		if len(u.MapsPaths) == 0 {
			continue
		}
		policy.PathMappings = append(policy.PathMappings, domain.PathMapping{Pattern: u.MapsPaths[0], UnitKeys: []string{u.ID}, Reason: "generic adapter path mapping"})
		for _, pattern := range u.MapsPaths[1:] {
			policy.PathMappings = append(policy.PathMappings, domain.PathMapping{Pattern: pattern, UnitKeys: []string{u.ID}, Reason: "generic adapter path mapping"})
		}
	}
	for _, entry := range c.Safety.ReviewedExclusions {
		policy.ReviewedExclusions = append(policy.ReviewedExclusions, domain.ReviewedExclusion{
			UnitKey:    entry.UnitKey,
			Reviewer:   entry.Reviewer,
			ReviewedAt: entry.ReviewedAt,
			Rationale:  entry.Rationale,
		})
	}
	for _, entry := range c.Safety.HardPathExceptions {
		policy.HardPathExceptions = append(policy.HardPathExceptions, domain.HardPathException{
			Path:       entry.Path,
			Rule:       entry.Rule,
			Reviewer:   entry.Reviewer,
			ReviewedAt: entry.ReviewedAt,
			Rationale:  entry.Rationale,
		})
	}
	if org != nil {
		policy.RiskBasedCIApproved = org.AllowRiskBasedCI && org.OwnerApprovedRiskBasedCI
		policy.ObservedCalibrationRuns = org.ObservedCalibrationRuns
		policy.ObservedAuditRuns = org.ObservedAuditRuns
	}
	policy.Quarantined = false // quarantine state is dynamic repository/adapter policy state
	return policy
}

func fullSuiteTargetRef(c Config) string {
	if len(c.Execution.FullSuiteArgv) == 0 {
		return ""
	}
	return "full-suite"
}
