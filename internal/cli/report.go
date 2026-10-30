package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/config"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/security"
	"github.com/spf13/cobra"
)

func renderReport(cmd *cobra.Command, name, format string) error {
	data, err := os.ReadFile(name)
	if err != nil {
		return exitError{4, errors.New("report file is unavailable")}
	}
	return renderReportData(cmd, data, format)
}

func (c *commands) report(ctx context.Context, cmd *cobra.Command, cfg config.Config, reference, format string) error {
	if data, err := os.ReadFile(reference); err == nil {
		return renderReportData(cmd, data, format)
	}
	id, err := uuid.Parse(reference)
	if err != nil {
		return exitError{2, errors.New("report reference must be an execution UUID or readable JSON file")}
	}
	client, err := newAPIClient(cfg)
	if err != nil {
		return err
	}
	var value map[string]any
	if err := client.request(ctx, http.MethodGet, "/api/v1/executions/"+id.String(), nil, "", &value); err != nil {
		return err
	}
	planIDText, _ := value["plan_id"].(string)
	planID, parseErr := uuid.Parse(planIDText)
	if parseErr != nil {
		return exitError{6, errors.New("execution response did not include a valid plan id")}
	}
	var plan domain.VerificationPlan
	if err := client.request(ctx, http.MethodGet, "/api/v1/plans/"+planID.String(), nil, "", &plan); err != nil {
		return err
	}
	if plan.ID != planID {
		return exitError{6, errors.New("plan response did not match the execution's plan id")}
	}
	// Keep execution fields at the report root for existing JSON/SARIF consumers,
	// and attach the immutable plan rationale required for a human review.
	value["plan"] = plan
	value["mode"] = plan.Mode
	value["status"] = plan.Status
	value["risk_level"] = plan.RiskLevel
	value["risk_score"] = plan.RiskScore
	value["explanation"] = plan.Explanation
	value["selection_confidence"] = plan.SelectionConfidence
	value["weighted_coverage"] = plan.WeightedCoverage
	value["estimated_duration_ms"] = plan.EstimatedDurationMillis
	value["selected_units"] = plan.SelectedUnits
	value["omitted_units"] = plan.OmittedUnits
	value["escalation_reasons"] = plan.EscalationReasons
	data, err := json.Marshal(value)
	if err != nil {
		return exitError{6, err}
	}
	return renderReportData(cmd, data, format)
}

func renderReportData(cmd *cobra.Command, data []byte, format string) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return exitError{2, fmt.Errorf("report JSON is invalid: %w", err)}
	}
	switch strings.ToLower(format) {
	case "json":
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(value)
	case "sarif":
		report := toSARIF(value)
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	case "text":
		var report map[string]any
		if err := json.Unmarshal(data, &report); err != nil {
			return exitError{2, err}
		}
		for _, key := range []string{"id", "mode", "status", "risk_level", "risk_score", "selection_confidence", "weighted_coverage", "estimated_duration_ms", "explanation", "outcome"} {
			if val, ok := report[key]; ok {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", strings.ReplaceAll(key, "_", " "), security.Redact(fmt.Sprint(val), 500))
			}
		}
		for _, key := range []string{"selected_units", "omitted_units", "escalation_reasons"} {
			items, ok := report[key].([]any)
			if !ok {
				continue
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %d\n", strings.ReplaceAll(key, "_", " "), len(items))
			for _, raw := range items {
				item, ok := raw.(map[string]any)
				if !ok {
					fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", security.Redact(fmt.Sprint(raw), 500))
					continue
				}
				label := item["stable_key"]
				if label == nil {
					label = item["code"]
				}
				detail := item["explanation"]
				if detail == nil {
					detail = item["detail"]
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  - %s: %s\n", security.Redact(fmt.Sprint(label), 240), security.Redact(fmt.Sprint(detail), 500))
			}
		}
		if units, ok := report["unit_results"].([]any); ok {
			fmt.Fprintf(cmd.OutOrStdout(), "Unit results: %d\n", len(units))
			for _, raw := range units {
				unit, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  %v %v (%v ms)\n", unit["test_unit_key"], unit["status"], unit["duration_ms"])
			}
		}
		return nil
	default:
		return exitError{2, errors.New("report format must be text, json, or sarif")}
	}
}

func toSARIF(value any) map[string]any {
	report := map[string]any{"version": "2.1.0", "$schema": "https://json.schemastore.org/sarif-2.1.0.json", "runs": []any{}}
	data, _ := json.Marshal(value)
	var record map[string]any
	_ = json.Unmarshal(data, &record)
	results := make([]any, 0)
	if outcome, ok := record["outcome"].(string); ok && outcome != "" {
		level := "note"
		switch outcome {
		case "TARGETED_FAIL", "FULL_VERIFICATION_FAIL", "INFRA_FAILURE", "STALE":
			level = "error"
		}
		results = append(results, map[string]any{
			"ruleId":  "IVP." + outcome,
			"level":   level,
			"message": map[string]string{"text": "Verification completed with outcome " + outcome + "."},
		})
	}
	if units, ok := record["unit_results"].([]any); ok {
		for _, item := range units {
			unit, ok := item.(map[string]any)
			if !ok || (unit["status"] != "FAILED" && unit["status"] != "ERROR") {
				continue
			}
			key, _ := unit["test_unit_key"].(string)
			results = append(results, map[string]any{
				"ruleId":  "IVP.UNIT_FAILURE",
				"level":   "error",
				"message": map[string]string{"text": "Verification unit failed: " + security.Redact(key, 240)},
			})
		}
	}
	run := map[string]any{
		"tool":       map[string]any{"driver": map[string]any{"name": "IVP"}},
		"results":    results,
		"properties": map[string]any{"verification_level": verificationLevel(domain.ExecutionOutcome(asString(record["outcome"]))), "plan_id": record["plan_id"]},
	}
	report["runs"] = []any{run}
	return report
}

func asString(value any) string {
	result, _ := value.(string)
	return result
}
