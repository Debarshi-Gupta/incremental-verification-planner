package analysis

import (
	"math"
	"strings"

	"github.com/ivp/ivp/internal/domain"
)

type RiskAssessment struct {
	Score         int
	Level         domain.RiskLevel
	Contributions []domain.RiskContribution
	UnknownReason string
}

// CalculateRisk implements the LLD's six bounded risk features. Each feature
// remains visible in the plan so the aggregate score is auditable.
func CalculateRisk(changes []domain.ChangedFile, impact ImpactResult, evidence EvidenceSnapshot, policy domain.Policy, mappedWeight float64, req domain.PlanRequest) RiskAssessment {
	if len(changes) == 0 || len(impact.Entities) == 0 {
		return RiskAssessment{Level: domain.RiskUnknown, UnknownReason: "changed entities could not be classified"}
	}
	criticalityPoints := 0
	maxFanout := 0
	for _, entity := range impact.Entities {
		points := 12
		switch entity.Criticality {
		case "CRITICAL":
			points = 25
		case "HIGH":
			points = 18
		case "NORMAL":
			points = 10
		case "LOW":
			points = 3
		}
		if points > criticalityPoints {
			criticalityPoints = points
		}
		if entity.ReverseFanout > maxFanout {
			maxFanout = entity.ReverseFanout
		}
	}
	contributions := []domain.RiskContribution{
		{Feature: "changed_entity_criticality", Value: float64(criticalityPoints) / 25, Points: clampInt(criticalityPoints, 0, 25), Reason: "maximum criticality of changed entities; unknown defaults to HIGH"},
	}
	cap := policy.DependencyCap
	if cap < 1 {
		cap = 100
	}
	fanout := float64(maxFanout)
	if fanout > float64(cap) {
		fanout = float64(cap)
	}
	denom := math.Log1p(float64(cap))
	fanoutPoints := 0
	if denom > 0 {
		fanoutPoints = int(math.Round(20 * math.Log1p(fanout) / denom))
	}
	contributions = append(contributions, domain.RiskContribution{Feature: "reverse_dependency_reach", Value: fanout, Points: clampInt(fanoutPoints, 0, 20), Reason: "bounded logarithmic reverse dependency fanout"})

	changePoints := 3
	changeReason := "ordinary source change"
	for _, f := range changes {
		lower := strings.ToLower(f.Path)
		if isHardConfigPath(f.Path, policy.HardFullSuitePaths) {
			changePoints = max(changePoints, 15)
			changeReason = "build, test, lockfile, policy, schema, or global configuration change"
		} else if isMigrationPath(lower) || isContractPath(f.Path) {
			changePoints = max(changePoints, 12)
			changeReason = "migration or public contract change"
		} else if f.IsGenerated || f.Status == domain.ChangeTypeChanged || f.Status == domain.ChangeRenamed || f.Status == domain.ChangeDeleted {
			changePoints = max(changePoints, 8)
			changeReason = "generated, deleted, renamed, or type-changed file"
		}
	}
	contributions = append(contributions, domain.RiskContribution{Feature: "change_class", Value: float64(changePoints) / 15, Points: clampInt(changePoints, 0, 15), Reason: changeReason})

	gap := 1 - clampFloat(mappedWeight, 0, 1)
	gapPoints := int(math.Round(20 * gap))
	gapReason := "mapped changed-entity weight contributes to verification coverage"
	if evidence.GraphMissingEdges || evidence.GraphHasConflicts || evidence.GraphHasCycles || !evidence.GraphComplete || !evidence.Snapshot.IsComplete {
		gapPoints = max(gapPoints, 18)
		gapReason = "impact graph completeness or consistency is limited"
	}
	contributions = append(contributions, domain.RiskContribution{Feature: "evidence_gap", Value: gap, Points: clampInt(gapPoints, 0, 20), Reason: gapReason})

	historyPoints := 0
	for _, h := range evidence.History {
		if h.Comparable && h.FailureCount > 0 && h.Outcome != "CANCELLED" && h.Outcome != "INFRA_FAILURE" {
			historyPoints = max(historyPoints, min(h.FailureCount*2, 10))
		}
	}
	contributions = append(contributions, domain.RiskContribution{Feature: "regression_history", Value: float64(historyPoints) / 10, Points: clampInt(historyPoints, 0, 10), Reason: "comparable historical failures related to this change"})

	envPoints := 0
	envReason := "toolchain and execution environment are comparable"
	if !evidence.ToolchainComparable || req.EnvironmentFingerprint == "" {
		envPoints = 10
		envReason = "toolchain or environment comparability is unknown"
	} else {
		lowSample := false
		for _, u := range evidence.Units {
			if u.DurationSampleCount < 5 {
				lowSample = true
				break
			}
		}
		if lowSample {
			envPoints = 3
			envReason = "some runtime estimates use conservative low-sample defaults"
		}
	}
	contributions = append(contributions, domain.RiskContribution{Feature: "environment_uncertainty", Value: float64(envPoints) / 10, Points: clampInt(envPoints, 0, 10), Reason: envReason})

	score := 0
	for _, c := range contributions {
		score += c.Points
	}
	if score > 100 {
		score = 100
	}
	level := domain.RiskUnknown
	switch {
	case score <= policy.LowRiskMaxScore:
		level = domain.RiskLow
	case score <= policy.MediumRiskMaxScore:
		level = domain.RiskMedium
	default:
		level = domain.RiskHigh
	}
	return RiskAssessment{Score: score, Level: level, Contributions: contributions}
}

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func clampFloat(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
