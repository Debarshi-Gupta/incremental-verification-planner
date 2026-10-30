package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/security"
	"github.com/spf13/cobra"
)

func renderReport(cmd *cobra.Command, name, format string) error {
	data, err := os.ReadFile(name)
	if err != nil {
		return exitError{4, errors.New("run reports must be retrieved from the configured IVP API or supplied as a JSON file")}
	}
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
		for _, key := range []string{"id", "mode", "status", "risk_level", "explanation", "outcome"} {
			if val, ok := report[key]; ok {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %v\n", strings.ReplaceAll(key, "_", " "), val)
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
