package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/analysis"
	"github.com/ivp/ivp/internal/api"
	"github.com/ivp/ivp/internal/config"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/execution"
	"github.com/ivp/ivp/internal/gitutil"
	"github.com/ivp/ivp/internal/security"
)

type localRun struct {
	Version           int                       `json:"version"`
	State             string                    `json:"state"`
	PlanID            uuid.UUID                 `json:"plan_id"`
	ExecutionID       uuid.UUID                 `json:"execution_id,omitempty"`
	LocalID           uuid.UUID                 `json:"local_id"`
	WorktreeHash      string                    `json:"worktree_hash"`
	EnvironmentDigest string                    `json:"environment_digest"`
	StartKey          string                    `json:"start_idempotency_key"`
	ResultKey         string                    `json:"result_idempotency_key"`
	FullSuite         bool                      `json:"full_suite"`
	StartedAt         time.Time                 `json:"started_at"`
	CompletedAt       time.Time                 `json:"completed_at,omitempty"`
	Outcome           domain.ExecutionOutcome   `json:"outcome,omitempty"`
	Summary           api.ExecutionSummary      `json:"summary"`
	UnitResults       []api.ExecutionUnitResult `json:"unit_results,omitempty"`
	ExitCode          *int                      `json:"exit_code,omitempty"`
	ElapsedMillis     int64                     `json:"elapsed_ms,omitempty"`
	ComputeMillis     int64                     `json:"compute_ms,omitempty"`
	ReportPath        string                    `json:"report_path,omitempty"`
	CommandResults    []execution.Result        `json:"command_results,omitempty"`
}

func (c *commands) loadPlan(ctx context.Context, cfg config.Config, reference string) (domain.VerificationPlan, error) {
	if data, err := os.ReadFile(reference); err == nil {
		var plan domain.VerificationPlan
		if json.Unmarshal(data, &plan) != nil || plan.ID == uuid.Nil {
			return plan, exitError{2, errors.New("plan file is not a valid IVP plan JSON")}
		}
		return plan, nil
	}
	id, err := uuid.Parse(reference)
	if err != nil {
		return domain.VerificationPlan{}, exitError{2, errors.New("--plan must be a plan UUID or readable JSON file")}
	}
	client, err := newAPIClient(cfg)
	if err != nil {
		return domain.VerificationPlan{}, err
	}
	var plan domain.VerificationPlan
	if err := client.request(ctx, http.MethodGet, "/api/v1/plans/"+id.String(), nil, "", &plan); err != nil {
		return plan, err
	}
	return plan, nil
}

func (c *commands) runPlan(ctx context.Context, out io.Writer, cfg config.Config, plan domain.VerificationPlan, requestedReport string, forceFullSuite bool) error {
	root, err := gitutil.RepositoryRoot(ctx, ".")
	if err != nil {
		return exitError{2, err}
	}
	client, err := newAPIClient(cfg)
	if err != nil {
		printFallback(out, cfg)
		return err
	}
	repo, err := client.repository(ctx, cfg.Repository.Slug)
	if err != nil {
		printFallback(out, cfg)
		return err
	}
	if repo.ID != plan.RepositoryID {
		return exitError{5, errors.New("plan repository does not match the current checkout")}
	}
	// A completed local result is authoritative for its original run. Uploading
	// it must remain possible after the worktree changes or the plan expires.
	if pending, err := findPendingRun(root, plan.ID); err != nil {
		return exitError{6, err}
	} else if pending != nil {
		if pending.State == "RESULT_READY" {
			return c.resumeRun(ctx, out, root, client, repo.ID, cfg, plan, pending)
		}
		if pending.State == "START_REQUESTED" {
			return c.resumeRun(ctx, out, root, client, repo.ID, cfg, plan, pending)
		}
		if pending.State == "EXECUTING" {
			return exitError{4, errors.New("a prior execution was interrupted; it was not rerun; inspect the local outbox record")}
		}
	}
	worktree, err := gitutil.WorktreeFingerprint(ctx, root)
	if err != nil {
		return exitError{6, err}
	}
	if worktree != plan.WorktreeHash {
		printFallback(out, cfg)
		return exitError{5, errors.New("plan is stale: worktree fingerprint changed; nothing was executed")}
	}
	fullSuite := forceFullSuite || plan.Status == domain.PlanFullRequired || plan.Mode == domain.ModeFull
	if !fullSuite && plan.Status != domain.PlanReady {
		return exitError{5, fmt.Errorf("plan is not executable: %s", plan.Status)}
	}
	if runtime.GOOS == "windows" && !fullSuite {
		printFallback(out, cfg)
		return exitError{3, errors.New("Windows refuses selective execution; no tests were run")}
	}
	_, record, err := loadIndex(root)
	if err != nil {
		return err
	}
	if record.Snapshot.ID != plan.SnapshotID || record.Snapshot.WorktreeHash != plan.WorktreeHash {
		return exitError{5, errors.New("plan snapshot does not match the local index")}
	}
	commands := []adapters.ExecutionCommand{}
	if fullSuite {
		commands, err = materializeFull(cfg, root, record)
	} else {
		commands, err = materializeSelected(root, cfg, plan, record)
	}
	if err != nil {
		printFallback(out, cfg)
		return exitError{3, fmt.Errorf("safe command materialization failed: %w", err)}
	}
	if err := validatePlanLocal(plan, record, worktree, fullSuite); err != nil {
		printFallback(out, cfg)
		return exitError{5, err}
	}
	startKey, resultKey := newIdempotencyKey(), newIdempotencyKey()
	local := &localRun{Version: 1, LocalID: uuid.New(), State: "START_REQUESTED", PlanID: plan.ID, WorktreeHash: plan.WorktreeHash, EnvironmentDigest: record.Snapshot.ToolchainDigest, StartKey: startKey, ResultKey: resultKey, FullSuite: fullSuite, StartedAt: time.Now().UTC(), Summary: api.ExecutionSummary{Total: 0, FailureClasses: map[string]int{}}}
	path, err := saveLocalRun(root, local)
	if err != nil {
		return exitError{6, err}
	}
	start := api.ExecutionStartRequest{WorktreeHash: plan.WorktreeHash, EnvironmentDigest: record.Snapshot.ToolchainDigest, FullSuite: fullSuite}
	var executionRecord domain.Execution
	if err := client.request(ctx, http.MethodPost, "/api/v1/plans/"+plan.ID.String()+"/executions", start, startKey, &executionRecord); err != nil {
		return err
	}
	if executionRecord.ID == uuid.Nil {
		return exitError{6, errors.New("API returned invalid execution id")}
	}
	local.ExecutionID = executionRecord.ID
	local.State = "EXECUTING"
	if _, err = saveLocalRun(root, local); err != nil {
		return exitError{6, err}
	}
	return c.executeCommands(ctx, out, root, client, repo.ID, cfg, plan, record, commands, local, path, requestedReport)
}

func (c *commands) resumeRun(ctx context.Context, out io.Writer, root string, client *apiClient, repositoryID uuid.UUID, cfg config.Config, plan domain.VerificationPlan, local *localRun) error {
	path := localRunPath(root, local.LocalID)
	if local.State == "START_REQUESTED" {
		_, record, err := loadIndex(root)
		if err != nil {
			return err
		}
		start := api.ExecutionStartRequest{WorktreeHash: plan.WorktreeHash, EnvironmentDigest: record.Snapshot.ToolchainDigest, FullSuite: local.FullSuite}
		var execRecord domain.Execution
		if err := client.request(ctx, http.MethodPost, "/api/v1/plans/"+plan.ID.String()+"/executions", start, local.StartKey, &execRecord); err != nil {
			return err
		}
		if execRecord.ID == uuid.Nil {
			return exitError{6, errors.New("API returned invalid execution id")}
		}
		local.ExecutionID = execRecord.ID
		local.State = "EXECUTING"
		if _, err := saveLocalRun(root, local); err != nil {
			return exitError{6, err}
		}
		return exitError{4, errors.New("execution start was recovered, but the process ended before tests were launched; rerun with a newly generated plan")}
	}
	if local.State == "RESULT_READY" {
		return uploadLocalResult(ctx, out, client, repositoryID, local, path, c.stdout)
	}
	return exitError{4, errors.New("local execution record cannot be resumed safely")}
}

func (c *commands) executeCommands(ctx context.Context, out io.Writer, root string, client *apiClient, repositoryID uuid.UUID, cfg config.Config, plan domain.VerificationPlan, record indexRecord, commands []adapters.ExecutionCommand, local *localRun, localPath, requestedReport string) error {
	runner, err := execution.NewRunner(root, execution.RunnerOptions{MaxConcurrent: 1, MaxOutputBytes: int64(cfg.Execution.MaxOutputBytes)})
	if err != nil {
		return exitError{6, err}
	}
	start := time.Now()
	overall := domain.OutcomeTargetedPass
	if local.FullSuite {
		overall = domain.OutcomeFullVerificationPass
	}
	var exitCode int = 0
	if local.FullSuite {
		result := runner.Run(ctx, commands[0])
		local.CommandResults = append(local.CommandResults, result)
		var fullDetailsComplete bool
		local.UnitResults, fullDetailsComplete = collectFullSuiteUnitResults(ctx, root, record, result)
		if result.Status == execution.StatusCancelled {
			overall = domain.OutcomeInfraFailure
			exitCode = 8
		} else if result.Status != execution.StatusCompleted || result.ExitCode == nil {
			overall = domain.OutcomeInfraFailure
			exitCode = 6
		} else if *result.ExitCode != 0 {
			overall = domain.OutcomeFullVerificationFail
			exitCode = 1
		}
		local.Summary.Total = 1
		if overall == domain.OutcomeFullVerificationPass {
			local.Summary.Passed = 1
		} else if overall == domain.OutcomeFullVerificationFail {
			local.Summary.Failed = 1
			if !fullDetailsComplete {
				local.Summary.FailureClasses["FULL_SUITE_FAILURE_DETAILS_INCOMPLETE"]++
			}
		} else {
			local.Summary.FailureClasses["INFRA_FAILURE"]++
		}
	} else {
		planUnits := plannedUnits(plan)
		unitByID := make(map[uuid.UUID]domain.TestUnit, len(record.Units))
		for _, u := range record.Units {
			unitByID[u.ID] = u
		}
		for i, command := range commands {
			if err := ctx.Err(); err != nil {
				overall = domain.OutcomeInfraFailure
				exitCode = 8
				break
			}
			if err := revalidateExecution(ctx, root, client, repositoryID, plan, record); err != nil {
				if errors.Is(err, errPlanStale) {
					overall, exitCode = domain.OutcomeStale, 5
				} else {
					overall, exitCode = domain.OutcomeInfraFailure, 6
				}
				fmt.Fprintf(out, "Execution stopped before the next command: %s\n", security.Redact(err.Error(), 240))
				break
			}
			planUnit := planUnits[i]
			unit := unitByID[planUnit.UnitID]
			adapter := adapterByID(unit.Adapter)
			if adapter == nil {
				overall = domain.OutcomeInfraFailure
				exitCode = 6
				break
			}
			var result execution.Result
			if command.CheckOnly {
				result = runner.Run(ctx, command)
				if result.Status == execution.StatusCompleted && result.ExitCode != nil {
					if *result.ExitCode == 0 {
						result.Outcome = domain.OutcomeTargetedPass
					} else {
						result.Outcome = domain.OutcomeTargetedFail
					}
				}
			} else {
				result = runner.RunAndParse(ctx, adapter, command, adapters.ParseRequest{ReportPaths: command.ReportPaths, MaxReportBytes: command.MaxOutputBytes})
			}
			local.CommandResults = append(local.CommandResults, result)
			local.Summary.Total++
			status := "PASSED"
			if result.Outcome == domain.OutcomeTargetedPass {
				local.Summary.Passed++
			} else if result.Outcome == domain.OutcomeTargetedFail {
				status = "FAILED"
				local.Summary.Failed++
				overall = domain.OutcomeTargetedFail
				exitCode = 1
			} else {
				status = "ERROR"
				local.Summary.FailureClasses[result.ErrorClass]++
				overall = domain.OutcomeInfraFailure
				if exitCode == 0 {
					exitCode = 6
				}
			}
			unitResult := api.ExecutionUnitResult{TestUnitKey: planUnit.StableKey, Attempt: 1, Status: status, DurationMillis: result.Duration.Milliseconds(), ReportHash: result.StdoutHash, Summary: api.UnitResultSummary{FailureClass: result.ErrorClass}}
			local.UnitResults = append(local.UnitResults, unitResult)
			if err := revalidateExecution(ctx, root, client, repositoryID, plan, record); err != nil {
				if errors.Is(err, errPlanStale) {
					overall, exitCode = domain.OutcomeStale, 5
				} else {
					overall, exitCode = domain.OutcomeInfraFailure, 6
				}
				fmt.Fprintf(out, "Execution result was recorded, but revalidation failed: %s\n", security.Redact(err.Error(), 240))
				break
			}
		}
	}
	local.CompletedAt = time.Now().UTC()
	local.ElapsedMillis = local.CompletedAt.Sub(start).Milliseconds()
	local.ComputeMillis = local.ElapsedMillis
	local.Outcome = overall
	local.ExitCode = &exitCode
	local.State = "RESULT_READY"
	for _, result := range local.CommandResults {
		if result.StdoutHash != "" {
			local.Summary.OutputHashes = append(local.Summary.OutputHashes, result.StdoutHash)
		}
		if result.StderrHash != "" {
			local.Summary.OutputHashes = append(local.Summary.OutputHashes, result.StderrHash)
		}
	}
	current, hashErr := gitutil.WorktreeFingerprint(ctx, root)
	if hashErr != nil {
		return exitError{6, hashErr}
	}
	if current != plan.WorktreeHash {
		local.Outcome = domain.OutcomeStale
		local.ExitCode = ptrInt(5)
		local.State = "RESULT_READY"
		fmt.Fprintln(out, "Worktree changed during execution; the result is marked STALE.")
	}
	reportRoot, err := safeWorkspacePath(root, filepath.Join(".ivp", "reports"))
	if err != nil {
		return exitError{6, err}
	}
	if err = os.MkdirAll(reportRoot, 0o700); err != nil {
		return exitError{6, err}
	}
	local.ReportPath = filepath.Join(reportRoot, local.ExecutionID.String()+".json")
	if _, err = saveLocalRun(root, local); err != nil {
		return exitError{6, err}
	}
	if err = writeReport(local.ReportPath, local); err != nil {
		return exitError{6, err}
	}
	if requestedReport != "" {
		if err = writeReport(requestedReport, local); err != nil {
			return exitError{6, err}
		}
	}
	if err = uploadLocalResult(ctx, out, client, repositoryID, local, localRunPath(root, local.ExecutionID), c.stdout); err != nil {
		return err
	}
	fmt.Fprintf(out, "Outcome: %s\nVerification level: %s\nReport: %s\n", local.Outcome, verificationLevel(local.Outcome), local.ReportPath)
	if exitCode != 0 {
		return exitError{exitCode, fmt.Errorf("verification completed with outcome %s", local.Outcome)}
	}
	return nil
}

func (c *commands) materialized(ctx context.Context, cfg config.Config, plan domain.VerificationPlan, record indexRecord, root string) ([]adapters.ExecutionCommand, error) {
	var commands []adapters.ExecutionCommand
	byID := make(map[uuid.UUID]domain.TestUnit, len(record.Units))
	for _, u := range record.Units {
		byID[u.ID] = u
	}
	for _, pu := range plannedUnits(plan) {
		u, ok := byID[pu.UnitID]
		if !ok {
			return nil, fmt.Errorf("plan unit %q is absent from local snapshot", pu.StableKey)
		}
		a := adapterByID(u.Adapter)
		if a == nil {
			return nil, fmt.Errorf("adapter %q is unavailable", u.Adapter)
		}
		knownTargets := []string{}
		knownUnits := []adapters.TestUnit{}
		for _, candidate := range record.Units {
			if candidate.Adapter != u.Adapter {
				continue
			}
			knownTargets = append(knownTargets, candidate.TargetRef)
			knownUnits = append(knownUnits, domainAdapterUnit(candidate))
		}
		timeout := time.Duration(cfg.Execution.TimeoutSeconds) * time.Second
		cmd, err := a.Materialize(ctx, adapters.MaterializeRequest{Root: root, Config: cfg.ToAdapterConfig(u.Adapter, nil), TargetKeys: []string{pu.TargetRef}, KnownTargets: knownTargets, KnownUnits: knownUnits, ChangedFiles: changedPaths(record.ChangedFiles), BaseSHA: plan.BaseSHA, HeadSHA: plan.HeadSHA, Timeout: timeout, MaxOutputBytes: int64(cfg.Execution.MaxOutputBytes)})
		if err != nil {
			return nil, fmt.Errorf("materialize %s: %w", pu.StableKey, err)
		}
		commands = append(commands, cmd)
	}
	return commands, nil
}

func (c *commands) validateCommands(cfg config.Config, plan domain.VerificationPlan) error {
	root, err := gitutil.RepositoryRoot(context.Background(), ".")
	if err != nil {
		return exitError{2, err}
	}
	worktree, err := gitutil.WorktreeFingerprint(context.Background(), root)
	if err != nil {
		return exitError{6, err}
	}
	if worktree != plan.WorktreeHash {
		return exitError{5, errors.New("plan is stale: worktree fingerprint changed")}
	}
	if plan.Status == domain.PlanFullRequired || plan.Mode == domain.ModeFull {
		if len(cfg.FullSuiteArgv()) == 0 {
			return exitError{3, errors.New("plan requires full verification but execution.full_suite_argv is empty")}
		}
		fmt.Fprintln(c.stdout, "Plan requires a full suite; configured full-suite argv is valid and dry-run did not execute it.")
		return nil
	}
	if plan.Status != domain.PlanReady {
		return exitError{5, fmt.Errorf("plan is not executable: %s", plan.Status)}
	}
	_, record, err := loadIndex(root)
	if err != nil {
		return err
	}
	_, err = c.materialized(context.Background(), cfg, plan, record, root)
	if err != nil {
		return exitError{6, err}
	}
	fmt.Fprintf(c.stdout, "Plan %s is current and all selected commands materialized safely; dry-run did not execute them.\n", plan.ID)
	return nil
}

func materializeSelected(root string, cfg config.Config, plan domain.VerificationPlan, record indexRecord) ([]adapters.ExecutionCommand, error) {
	return (&commands{}).materialized(context.Background(), cfg, plan, record, root)
}

func plannedUnits(plan domain.VerificationPlan) []domain.PlanUnit {
	units := make([]domain.PlanUnit, 0, len(plan.SelectedUnits)+len(plan.MandatoryUnits))
	seen := make(map[string]bool, cap(units))
	for _, unit := range append(append([]domain.PlanUnit(nil), plan.SelectedUnits...), plan.MandatoryUnits...) {
		key := unit.StableKey
		if key == "" {
			key = unit.TargetRef
		}
		if key != "" && !seen[key] {
			seen[key] = true
			units = append(units, unit)
		}
	}
	return units
}
func materializeFull(cfg config.Config, root string, record indexRecord) ([]adapters.ExecutionCommand, error) {
	argv := cfg.FullSuiteArgv()
	if len(argv) == 0 {
		return nil, errors.New("execution.full_suite_argv is empty")
	}
	if strings.ContainsRune(argv[0], 0) {
		return nil, adapters.ErrUnsafeCommand
	}
	var reports []string
	for _, unit := range record.Units {
		if values, ok := unit.Metadata["report_paths"].([]any); ok {
			for _, value := range values {
				if path, ok := value.(string); ok && path != "" {
					reports = append(reports, path)
				}
			}
		}
	}
	sort.Strings(reports)
	reports = uniqueStrings(reports)
	return []adapters.ExecutionCommand{{Executable: argv[0], Args: append([]string(nil), argv[1:]...), WorkingDirectory: root, Environment: map[string]string{}, Timeout: time.Duration(cfg.Execution.TimeoutSeconds) * time.Second, MaxOutputBytes: int64(cfg.Execution.MaxOutputBytes), FullSuite: true, ReportPaths: reports}}, nil
}

func collectFullSuiteUnitResults(ctx context.Context, root string, record indexRecord, run execution.Result) ([]api.ExecutionUnitResult, bool) {
	if run.Status != execution.StatusCompleted || run.ExitCode == nil {
		return nil, false
	}
	pathsByAdapter := make(map[string][]string)
	for _, unit := range record.Units {
		if values, ok := unit.Metadata["report_paths"].([]any); ok {
			for _, value := range values {
				if path, ok := value.(string); ok && path != "" {
					pathsByAdapter[unit.Adapter] = append(pathsByAdapter[unit.Adapter], path)
				}
			}
		}
	}
	var results []api.ExecutionUnitResult
	detailsComplete := len(pathsByAdapter) > 0
	for adapterID, paths := range pathsByAdapter {
		adapter := adapterByID(adapterID)
		if adapter == nil {
			detailsComplete = false
			continue
		}
		parsed, err := adapter.ParseResults(ctx, adapters.ParseRequest{Root: root, ReportPaths: uniqueStrings(paths), Stdout: []byte(run.StdoutExcerpt), Stderr: []byte(run.StderrExcerpt), ExitCode: *run.ExitCode, MaxReportBytes: adapters.DefaultReportLimit})
		if err != nil || !parsed.Complete || !parsed.HasReliableReport {
			detailsComplete = false
			continue // Full-suite pass/fail remains based on the configured command exit status.
		}
		matchedFailures := 0
		attempts := map[string]int{}
		for _, testCase := range parsed.Cases {
			if testCase.Status != "failed" && testCase.Status != "error" && testCase.Status != "skipped" {
				continue
			}
			candidates := []string{testCase.Name, testCase.ClassName + "." + testCase.Name}
			var matched *domain.TestUnit
			ambiguous := false
			for _, unit := range record.Units {
				if unit.Adapter != adapterID {
					continue
				}
				for _, candidate := range candidates {
					if candidate != "" && (candidate == unit.TargetRef || candidate == unit.StableKey) {
						if matched != nil && matched.StableKey != unit.StableKey {
							ambiguous = true
						}
						matched = &unit
					}
				}
			}
			if matched == nil || ambiguous {
				detailsComplete = false
				continue
			}
			matchedFailures++
			status := "FAILED"
			if testCase.Status == "error" {
				status = "ERROR"
			} else if testCase.Status == "skipped" {
				status = "SKIPPED"
			}
			attempts[matched.StableKey]++
			results = append(results, api.ExecutionUnitResult{TestUnitKey: matched.StableKey, Attempt: attempts[matched.StableKey], Status: status, DurationMillis: testCase.Duration.Milliseconds(), ReportHash: run.StdoutHash, Summary: api.UnitResultSummary{FailureClass: status}})
		}
		if parsed.Failed+parsed.Errors > matchedFailures {
			detailsComplete = false
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].TestUnitKey != results[j].TestUnitKey {
			return results[i].TestUnitKey < results[j].TestUnitKey
		}
		return results[i].Attempt < results[j].Attempt
	})
	if len(results) == 0 && run.ExitCode != nil && *run.ExitCode != 0 {
		detailsComplete = false
	}
	return results, detailsComplete
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}
func validatePlanLocal(plan domain.VerificationPlan, record indexRecord, current string, fullSuite bool) error {
	if plan.ExpiresAt.IsZero() || !time.Now().Before(plan.ExpiresAt) {
		return errors.New("plan expired")
	}
	if plan.WorktreeHash != current {
		return errors.New("worktree fingerprint changed")
	}
	if plan.SnapshotID != record.Snapshot.ID {
		return errors.New("plan snapshot does not match the local index")
	}
	known := map[string]bool{}
	for _, u := range record.Units {
		known[u.TargetRef] = true
	}
	if fullSuite && (plan.Status == domain.PlanReady || plan.Status == domain.PlanPassed || plan.Status == domain.PlanFullRequired) {
		return nil
	}
	if plan.Status == domain.PlanReady {
		return execution.ValidatePlan(plan, execution.PlanRevalidation{CurrentWorktreeHash: current, PolicyVersion: plan.PolicyVersion, AlgorithmVersion: analysis.AlgorithmVersion, Now: time.Now().UTC(), KnownTargetRefs: known})
	}
	if plan.Status != domain.PlanFullRequired && plan.Mode != domain.ModeFull {
		return fmt.Errorf("plan is not executable: %s", plan.Status)
	}
	return nil
}

var errPlanStale = errors.New("plan evidence is stale")

func revalidateExecution(ctx context.Context, root string, client *apiClient, repositoryID uuid.UUID, plan domain.VerificationPlan, record indexRecord) error {
	current, err := gitutil.WorktreeFingerprint(ctx, root)
	if err != nil {
		return fmt.Errorf("recompute worktree fingerprint: %w", err)
	}
	if current != plan.WorktreeHash {
		return fmt.Errorf("%w: worktree fingerprint changed", errPlanStale)
	}
	if plan.AlgorithmVersion != analysis.AlgorithmVersion {
		return fmt.Errorf("%w: planner algorithm version changed", errPlanStale)
	}
	var currentPolicy struct {
		Version int64         `json:"version"`
		Policy  domain.Policy `json:"policy"`
	}
	if err := client.request(ctx, http.MethodGet, "/api/v1/repositories/"+repositoryID.String()+"/policy", nil, "", &currentPolicy); err != nil {
		return fmt.Errorf("re-read current policy: %w", err)
	}
	if currentPolicy.Version == 0 || currentPolicy.Version != plan.PolicyVersion {
		return fmt.Errorf("%w: policy version changed or is unavailable", errPlanStale)
	}
	known := make(map[string]bool, len(record.Units)*2)
	for _, unit := range record.Units {
		known[unit.TargetRef] = true
		known[unit.StableKey] = true
	}
	return execution.ValidatePlan(plan, execution.PlanRevalidation{
		CurrentWorktreeHash: current,
		PolicyVersion:       currentPolicy.Version,
		AlgorithmVersion:    analysis.AlgorithmVersion,
		Now:                 time.Now().UTC(),
		KnownTargetRefs:     known,
		MandatoryChecks:     currentPolicy.Policy.AlwaysRunCheckIDs,
	})
}
func loadIndex(root string) (string, indexRecord, error) {
	path, err := safeWorkspacePath(root, filepath.Join(".ivp", "index.json"))
	if err != nil {
		return "", indexRecord{}, exitError{6, err}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", indexRecord{}, exitError{6, errors.New("local snapshot metadata is unavailable; run ivp index")}
	}
	var record indexRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return "", record, exitError{6, errors.New("local snapshot metadata is malformed")}
	}
	return path, record, nil
}
func findPendingRun(root string, planID uuid.UUID) (*localRun, error) {
	dir, err := safeWorkspacePath(root, filepath.Join(".ivp", "outbox"))
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var item localRun
		if json.Unmarshal(data, &item) == nil && item.PlanID == planID && item.State != "UPLOADED" {
			return &item, nil
		}
	}
	return nil, nil
}
func saveLocalRun(root string, record *localRun) (string, error) {
	dir, err := safeWorkspacePath(root, filepath.Join(".ivp", "outbox"))
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if record.LocalID == uuid.Nil {
		record.LocalID = uuid.New()
	}
	path := filepath.Join(dir, record.LocalID.String()+".json")
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", err
	}
	if err = os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}
func localRunPath(root string, id uuid.UUID) string {
	dir, err := safeWorkspacePath(root, filepath.Join(".ivp", "outbox"))
	if err != nil {
		return ""
	}
	if id == uuid.Nil {
		return ""
	}
	return filepath.Join(dir, id.String()+".json")
}
func writeReport(path string, record *localRun) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
func uploadLocalResult(ctx context.Context, out io.Writer, client *apiClient, repositoryID uuid.UUID, local *localRun, path string, stdout io.Writer) error {
	if local.State != "RESULT_READY" && local.State != "UPLOADED" {
		return exitError{4, errors.New("local result is incomplete and will not be uploaded")}
	}
	if local.State == "UPLOADED" {
		fmt.Fprintf(out, "Result already uploaded: %s\n", local.ReportPath)
		return nil
	}
	request := api.ResultRequest{Outcome: local.Outcome, ExitCode: local.ExitCode, CompletedAt: local.CompletedAt, ElapsedMillis: local.ElapsedMillis, ComputeMillis: local.ComputeMillis, Summary: local.Summary, UnitResults: local.UnitResults}
	if err := client.request(ctx, http.MethodPost, "/api/v1/executions/"+local.ExecutionID.String()+"/results", request, local.ResultKey, nil); err != nil {
		fmt.Fprintf(stdout, "Result remains in the local outbox at %s; retry `ivp run --plan %s` to upload without rerunning tests.\n", path, local.PlanID)
		return err
	}
	local.State = "UPLOADED"
	root := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	if _, err := saveLocalRun(root, local); err != nil {
		return exitError{6, err}
	}
	return nil
}

func adapterByID(id string) adapters.Adapter {
	for _, a := range registry() {
		if a.ID() == id {
			return a
		}
	}
	return nil
}
func domainAdapterUnit(u domain.TestUnit) adapters.TestUnit {
	var result adapters.TestUnit
	result.Key = u.TargetRef
	result.Kind = string(u.UnitType)
	result.Paths = append([]string(nil), u.SourcePaths...)
	result.Mandatory = u.Mandatory
	result.SelectableGranularity = u.SelectionGranularity
	result.Filterable = u.Selectable
	result.RiskLabels = append([]string(nil), u.RiskLabels...)
	result.Module = u.ModuleKey
	result.ResourceGroup = u.ExecutionGroup
	if u.Metadata != nil {
		if v, ok := u.Metadata["report_paths"].([]any); ok {
			for _, x := range v {
				if s, ok := x.(string); ok {
					result.ReportPaths = append(result.ReportPaths, s)
				}
			}
		}
	}
	return result
}
func safeWorkspacePath(root, rel string) (string, error) { return adapters.ResolveWithin(root, rel) }
func verificationLevel(outcome domain.ExecutionOutcome) string {
	if outcome == domain.OutcomeFullVerificationPass || outcome == domain.OutcomeFullVerificationFail {
		return "FULL"
	}
	if outcome == domain.OutcomeTargetedPass || outcome == domain.OutcomeTargetedFail {
		return "TARGETED"
	}
	return "INCOMPLETE"
}
func ptrInt(n int) *int { return &n }
func writeRunDigest(result execution.Result) string {
	sum := sha256.Sum256([]byte(result.StdoutHash + result.StderrHash))
	return hex.EncodeToString(sum[:])
}
