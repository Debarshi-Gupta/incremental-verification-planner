package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/api"
	"github.com/ivp/ivp/internal/config"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/gitutil"
)

func (c *commands) requestPlan(ctx context.Context, out io.Writer, cfg config.Config, baseRef, headRef, modeText, format, outputPath string) (domain.VerificationPlan, error) {
	if strings.TrimSpace(baseRef) == "" {
		return domain.VerificationPlan{}, exitError{2, errors.New("base ref is required")}
	}
	mode := cfg.Modes.Default
	if modeText != "" {
		mode = domain.PlanMode(strings.ToUpper(strings.TrimSpace(modeText)))
	}
	switch mode {
	case domain.ModeFull, domain.ModeObserve, domain.ModeFastFeedback, domain.ModeRiskBasedCI:
	default:
		return domain.VerificationPlan{}, exitError{2, errors.New("mode must be FULL, OBSERVE, FAST_FEEDBACK, or RISK_BASED_CI")}
	}
	// Index evidence is immutable and tied to the requested base and current
	// worktree. Rebuilding here also avoids trusting an old local cache.
	indexCfg := cfg
	indexCfg.Analysis.BaseRef = baseRef
	if err := c.index(ctx, io.Discard, indexCfg, false); err != nil {
		var coded exitError
		if !errors.As(err, &coded) || coded.code != 6 {
			return domain.VerificationPlan{}, err
		}
	}
	root, err := gitutil.RepositoryRoot(ctx, ".")
	if err != nil {
		return domain.VerificationPlan{}, exitError{6, err}
	}
	indexPath, err := adapters.ResolveWithin(root, filepath.Join(".ivp", "index.json"))
	if err != nil {
		return domain.VerificationPlan{}, exitError{6, errors.New("local snapshot metadata is unavailable")}
	}
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return domain.VerificationPlan{}, exitError{6, errors.New("local snapshot metadata is unavailable; run ivp index")}
	}
	var record indexRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return domain.VerificationPlan{}, exitError{6, errors.New("local snapshot metadata is malformed")}
	}
	if headRef != "" {
		resolved, err := gitOutput(ctx, "rev-parse", "--verify", "--end-of-options", headRef+"^{commit}")
		if err != nil || resolved != record.Snapshot.CommitSHA {
			return domain.VerificationPlan{}, exitError{5, errors.New("requested head ref does not match current indexed HEAD")}
		}
	}
	currentWorktree, err := gitutil.WorktreeFingerprint(ctx, root)
	if err != nil {
		return domain.VerificationPlan{}, exitError{6, err}
	}
	if currentWorktree != record.Snapshot.WorktreeHash {
		return domain.VerificationPlan{}, exitError{5, errors.New("worktree changed after snapshot creation; regenerate the index")}
	}
	client, err := newAPIClient(cfg)
	if err != nil {
		printFallback(out, cfg)
		return domain.VerificationPlan{}, err
	}
	repo, err := client.repository(ctx, cfg.Repository.Slug)
	if err != nil {
		printFallback(out, cfg)
		return domain.VerificationPlan{}, err
	}
	if repo.ID != record.Snapshot.RepositoryID {
		return domain.VerificationPlan{}, exitError{5, errors.New("indexed snapshot belongs to another repository")}
	}
	policy, err := client.getTrustedPolicy(ctx, repo.ID)
	if err != nil {
		printFallback(out, cfg)
		return domain.VerificationPlan{}, err
	}
	if format != "text" && format != "json" {
		return domain.VerificationPlan{}, exitError{2, errors.New("plan format must be text or json")}
	}
	changed, err := gitutil.DetectChanges(ctx, root, baseRef)
	if err != nil {
		return domain.VerificationPlan{}, exitError{6, fmt.Errorf("revalidate change set: %w", err)}
	}
	if changed.WorktreeHash != record.Snapshot.WorktreeHash || changed.BaseSHA != record.Snapshot.BaseSHA || changed.HeadSHA != record.Snapshot.CommitSHA {
		return domain.VerificationPlan{}, exitError{5, errors.New("change set or worktree changed after the index was created")}
	}
	request := api.PlanRequest{RequestID: uuid.NewString(), SnapshotID: record.Snapshot.ID.String(), BaseSHA: changed.BaseSHA, HeadSHA: changed.HeadSHA, WorktreeHash: changed.WorktreeHash, ChangedFiles: changed.Files, Mode: mode,
		ClientCapabilities: api.ClientCapabilities{OS: runtimePlatform(), Arch: runtimeArch(), Adapters: record.Adapters, AdapterVersions: record.AdapterVersions},
		ExecutionContext:   api.ExecutionContext{CI: os.Getenv("CI") != "", ToolchainDigest: record.Snapshot.ToolchainDigest, EnvironmentClass: environmentClass()}}
	var plan domain.VerificationPlan
	if err := client.request(ctx, http.MethodPost, "/api/v1/repositories/"+repo.ID.String()+"/plans", request, "", &plan); err != nil {
		printFallback(out, cfg)
		return plan, err
	}
	if plan.ID == uuid.Nil {
		return plan, exitError{6, errors.New("IVP API returned an invalid plan")}
	}
	if plan.PolicyVersion != policy.Version {
		return plan, exitError{5, errors.New("effective policy changed while the plan was being created")}
	}
	if outputPath != "" {
		encoded, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return plan, exitError{6, err}
		}
		if err := os.WriteFile(outputPath, encoded, 0o600); err != nil {
			return plan, exitError{6, err}
		}
	}
	if format == "json" {
		encoded, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return plan, exitError{6, err}
		}
		fmt.Fprintln(out, string(encoded))
	} else {
		printPlan(out, plan, outputPath)
	}
	return plan, nil
}

func printPlan(out io.Writer, plan domain.VerificationPlan, path string) {
	fmt.Fprintf(out, "Plan: %s\nMode: %s\nBase: %s\nHead: %s\nStatus: %s\nRisk: %s (%d)\nSelection confidence: %.3f\nWeighted coverage: %.3f\nEstimated duration: %s\n", plan.ID, plan.Mode, plan.BaseSHA, plan.HeadSHA, plan.Status, plan.RiskLevel, plan.RiskScore, plan.SelectionConfidence, plan.WeightedCoverage, time.Duration(plan.EstimatedDurationMillis)*time.Millisecond)
	fmt.Fprintf(out, "Explanation: %s\n", plan.Explanation)
	if len(plan.EscalationReasons) > 0 {
		fmt.Fprintln(out, "Escalation reasons:")
		for _, reason := range plan.EscalationReasons {
			fmt.Fprintf(out, "  - %s: %s\n", reason.Code, reason.Detail)
		}
	}
	fmt.Fprintf(out, "Selected units (%d):\n", len(plan.SelectedUnits))
	for i, unit := range plan.SelectedUnits {
		if i >= 50 {
			fmt.Fprintf(out, "  ... %d more; use --format json\n", len(plan.SelectedUnits)-i)
			break
		}
		fmt.Fprintf(out, "  - %s [%s] reasons=%s: %s\n", unit.StableKey, unit.UnitType, strings.Join(reasonStrings(unit.ReasonCodes), ","), unit.Explanation)
	}
	if len(plan.OmittedUnits) > 0 {
		fmt.Fprintf(out, "Omitted units (%d):\n", len(plan.OmittedUnits))
		for i, unit := range plan.OmittedUnits {
			if i >= 50 {
				fmt.Fprintf(out, "  ... %d more; use --format json\n", len(plan.OmittedUnits)-i)
				break
			}
			fmt.Fprintf(out, "  - %s reasons=%s: %s\n", unit.StableKey, strings.Join(omitStrings(unit.ReasonCodes), ","), unit.Explanation)
		}
	}
	if plan.Status == domain.PlanFullRequired {
		fmt.Fprintln(out, "Verification level: FULL_REQUIRED; local executor must run the configured full-suite argv.")
	}
	if path != "" {
		fmt.Fprintf(out, "Plan report: %s\n", path)
	}
}
func reasonStrings(values []domain.SelectedReasonCode) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}
func omitStrings(values []domain.OmissionReasonCode) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}
func printFallback(out io.Writer, cfg config.Config) {
	argv, why := fallbackInstruction(cfg)
	if argv == "" {
		fmt.Fprintf(out, "Safe full-suite fallback is not configured: %s\n", why)
	} else {
		fmt.Fprintf(out, "Safe full-suite fallback argv: %s\n", argv)
	}
}
func runtimePlatform() string { return runtime.GOOS }
func runtimeArch() string     { return runtime.GOARCH }
func environmentClass() string {
	if os.Getenv("CI") != "" {
		return "ci"
	}
	return "developer-workstation"
}
