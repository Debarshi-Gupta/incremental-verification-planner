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
	Coverage          []domain.CoverageObservation `json:"coverage,omitempty"`
	EvidenceWarnings  []string                  `json:"evidence_warnings,omitempty"`
	ExitCode          *int                      `json:"exit_code,omitempty"`
	ElapsedMillis     int64                     `json:"elapsed_ms,omitempty"`
	ComputeMillis     int64                     `json:"compute_ms,omitempty"`
	ReportPath        string                    `json:"report_path,omitempty"`
	ReportPaths       []string                  `json:"report_paths,omitempty"`
	UploadedReports   map[string]string         `json:"uploaded_reports,omitempty"`
	IsolatedWorkspace bool                      `json:"isolated_workspace,omitempty"`
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
	if !forceFullSuite && requiresAuthoritativeComparison(cfg, plan) {
		if plan.Status != domain.PlanReady {
			// A FULL_REQUIRED plan has no trustworthy subset to compare. Run the
			// authority once; invoking the normal path twice would duplicate the
			// same full-suite command in OBSERVE and final-CI modes.
			return c.runPlanCore(ctx, out, cfg, plan, requestedReport, true)
		}
		// OBSERVE and final CI FAST_FEEDBACK retain the planned run for comparison,
		// but a successful selective run is never sufficient to report success.
		root, rootErr := gitutil.RepositoryRoot(ctx, ".")
		comparisonStarted := time.Now().UTC()
		selectedErr := c.runPlanCore(ctx, out, cfg, plan, requestedReport, false)
		if rootErr == nil && hasOfflineFullRunSince(root, plan.ID, comparisonStarted) {
			if selectedErr != nil {
				return selectedErr
			}
			fmt.Fprintln(out, "Planning or execution service was unavailable; the configured offline full suite passed and is authoritative.")
			return nil
		}
		fullErr := c.runPlanCore(ctx, out, cfg, plan, requestedReport, true)
		if fullErr != nil && ctx.Err() == nil {
			root, rootErr := gitutil.RepositoryRoot(ctx, ".")
			if rootErr == nil && !hasPotentiallyStartedFullRun(root, plan.ID) {
				fullErr = c.runOfflineFullSuite(ctx, out, root, cfg, plan.ID, requestedReport, fullErr)
			}
		}
		if fullErr != nil {
			return fullErr
		}
		if selectedErr != nil {
			fmt.Fprintf(out, "The authoritative full suite passed; the planned subset differed: %s\n", security.Redact(selectedErr.Error(), 240))
			return selectedErr
		} else {
			fmt.Fprintln(out, "The planned subset passed before the authoritative full suite.")
		}
		fmt.Fprintln(out, "Authoritative full-suite verification passed.")
		return nil
	}
	err := c.runPlanCore(ctx, out, cfg, plan, requestedReport, forceFullSuite)
	if err != nil && ctx.Err() == nil && planningFallbackAllowed(err) {
		root, rootErr := gitutil.RepositoryRoot(ctx, ".")
		fullMode := forceFullSuite || plan.Status == domain.PlanFullRequired || plan.Mode == domain.ModeFull
		if rootErr == nil && (!fullMode || !hasPotentiallyStartedFullRun(root, plan.ID)) {
			return c.runOfflineFullSuite(ctx, out, root, cfg, plan.ID, requestedReport, err)
		}
	}
	return err
}

func hasOfflineFullRunSince(root string, planID uuid.UUID, since time.Time) bool {
	dir, err := safeWorkspacePath(root, filepath.Join(".ivp", "outbox"))
	if err != nil {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
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
		if json.Unmarshal(data, &item) == nil && item.PlanID == planID && item.FullSuite && item.State == "OFFLINE_COMPLETE" && !item.StartedAt.Before(since) {
			return true
		}
	}
	return false
}

func hasPotentiallyStartedFullRun(root string, planID uuid.UUID) bool {
	dir, err := safeWorkspacePath(root, filepath.Join(".ivp", "outbox"))
	if err != nil {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			continue
		}
		var item localRun
		if json.Unmarshal(data, &item) != nil || item.PlanID != planID || !item.FullSuite {
			continue
		}
		if item.State == "EXECUTING" || item.State == "RESULT_READY" || item.State == "UPLOADED" || item.State == "OFFLINE_COMPLETE" {
			return true
		}
	}
	return false
}

func requiresAuthoritativeComparison(cfg config.Config, plan domain.VerificationPlan) bool {
	if plan.Mode == domain.ModeObserve {
		return true
	}
	return plan.Mode == domain.ModeFastFeedback && os.Getenv("CI") != "" && cfg.Modes.FastFeedback.FinalCIFullSuiteRequired
}

func (c *commands) runPlanCore(ctx context.Context, out io.Writer, cfg config.Config, plan domain.VerificationPlan, requestedReport string, forceFullSuite bool) error {
	root, err := gitutil.RepositoryRoot(ctx, ".")
	if err != nil {
		return exitError{2, err}
	}
	return c.runPlanCoreAt(ctx, out, root, cfg, plan, requestedReport, forceFullSuite, false)
}

func (c *commands) runPlanCoreAt(ctx context.Context, out io.Writer, root string, cfg config.Config, plan domain.VerificationPlan, requestedReport string, forceFullSuite, isolatedWorkspace bool) error {
	requestedFullSuite := forceFullSuite || plan.Status == domain.PlanFullRequired || plan.Mode == domain.ModeFull
	// A completed local result is authoritative for its original run. Uploading
	// it must remain possible after the worktree changes or the plan expires.
	if pending, err := findPendingRun(root, plan.ID); err != nil {
		return exitError{6, err}
	} else if pending != nil {
		if pending.State == "RESULT_READY" {
			client, clientErr := newAPIClient(cfg)
			if clientErr != nil {
				return clientErr
			}
			repo, repoErr := client.repository(ctx, cfg.Repository.Slug)
			if repoErr != nil {
				return repoErr
			}
			if repo.ID != plan.RepositoryID {
				return exitError{5, errors.New("plan repository does not match the current checkout")}
			}
			if err := c.resumeRun(ctx, out, root, client, repo.ID, cfg, plan, pending); err != nil {
				return err
			}
			if pending.FullSuite == requestedFullSuite {
				return nil
			}
		}
		if pending.State == "START_REQUESTED" {
			client, clientErr := newAPIClient(cfg)
			if clientErr != nil {
				return clientErr
			}
			repo, repoErr := client.repository(ctx, cfg.Repository.Slug)
			if repoErr != nil {
				return repoErr
			}
			if repo.ID != plan.RepositoryID {
				return exitError{5, errors.New("plan repository does not match the current checkout")}
			}
			return c.resumeRun(ctx, out, root, client, repo.ID, cfg, plan, pending)
		}
		if pending.State == "EXECUTING" {
			return exitError{4, errors.New("a prior execution was interrupted; it was not rerun; inspect the local outbox record")}
		}
	}
	client, err := newAPIClient(cfg)
	if err != nil {
		return c.runOfflineFullSuite(ctx, out, root, cfg, plan.ID, requestedReport, err)
	}
	repo, err := client.repository(ctx, cfg.Repository.Slug)
	if err != nil {
		return c.runOfflineFullSuite(ctx, out, root, cfg, plan.ID, requestedReport, err)
	}
	if repo.ID != plan.RepositoryID {
		return exitError{5, errors.New("plan repository does not match the current checkout")}
	}
	worktree, err := gitutil.WorktreeFingerprint(ctx, root)
	if err != nil {
		return exitError{6, err}
	}
	if worktree != plan.WorktreeHash {
		return c.runOfflineFullSuite(ctx, out, root, cfg, plan.ID, requestedReport, errors.New("plan is stale: worktree fingerprint changed"))
	}
	fullSuite := requestedFullSuite
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
	var reportPaths []string
	for _, command := range commands {
		reportPaths = append(reportPaths, command.ReportPaths...)
	}
	local := &localRun{Version: 1, LocalID: uuid.New(), State: "START_REQUESTED", PlanID: plan.ID, WorktreeHash: plan.WorktreeHash, EnvironmentDigest: record.Snapshot.ToolchainDigest, StartKey: startKey, ResultKey: resultKey, FullSuite: fullSuite, ReportPaths: uniqueStrings(reportPaths), IsolatedWorkspace: isolatedWorkspace, StartedAt: time.Now().UTC(), Summary: api.ExecutionSummary{Total: 0, FailureClasses: map[string]int{}}}
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

// runOfflineFullSuite is the fail-closed path for unavailable or untrusted
// planning evidence. It runs only the locally configured argv and writes a
// local report; it never treats the result as an API-registered execution.
func (c *commands) runOfflineFullSuite(ctx context.Context, out io.Writer, root string, cfg config.Config, planID uuid.UUID, requestedReport string, cause error) error {
	argv := cfg.FullSuiteArgv()
	if len(argv) == 0 {
		printFallback(out, cfg)
		return exitError{3, fmt.Errorf("planning could not be trusted (%s) and no full-suite fallback is configured", security.Redact(cause.Error(), 240))}
	}
	_, record, indexErr := loadIndex(root)
	if indexErr != nil {
		record = indexRecord{}
	}
	commands, err := materializeFull(cfg, root, record)
	if err != nil {
		printFallback(out, cfg)
		return exitError{3, fmt.Errorf("cannot materialize configured full-suite fallback: %w", err)}
	}
	worktree, err := gitutil.WorktreeFingerprint(ctx, root)
	if err != nil {
		return exitError{6, err}
	}
	fmt.Fprintf(out, "Planning is unavailable or untrusted (%s); running the configured authoritative full suite.\n", security.Redact(cause.Error(), 240))
	runner, err := execution.NewRunner(root, execution.RunnerOptions{MaxConcurrent: 1, MaxOutputBytes: int64(cfg.Execution.MaxOutputBytes)})
	if err != nil {
		return exitError{6, err}
	}
	started := time.Now()
	result := runner.Run(ctx, commands[0])
	completed := time.Now().UTC()
	outcome := domain.OutcomeFullVerificationPass
	exitCode := 0
	summary := api.ExecutionSummary{Total: 1, FailureClasses: map[string]int{}}
	switch {
	case result.Status == execution.StatusCancelled:
		outcome, exitCode = domain.OutcomeInfraFailure, 8
		summary.FailureClasses["CANCELLED"]++
	case result.Status != execution.StatusCompleted || result.ExitCode == nil:
		outcome, exitCode = domain.OutcomeInfraFailure, 6
		summary.FailureClasses["INFRA_FAILURE"]++
	case *result.ExitCode != 0:
		outcome, exitCode = domain.OutcomeFullVerificationFail, 1
		summary.Failed = 1
	default:
		summary.Passed = 1
	}
	currentWorktree, fingerprintErr := gitutil.WorktreeFingerprint(ctx, root)
	if fingerprintErr != nil {
		outcome, exitCode = domain.OutcomeInfraFailure, 6
		summary.FailureClasses["WORKTREE_REVALIDATION_FAILED"]++
	} else if currentWorktree != worktree {
		outcome, exitCode = domain.OutcomeStale, 5
		summary.FailureClasses["WORKTREE_MUTATED"]++
	}
	if result.StdoutHash != "" {
		summary.OutputHashes = append(summary.OutputHashes, result.StdoutHash)
	}
	if result.StderrHash != "" {
		summary.OutputHashes = append(summary.OutputHashes, result.StderrHash)
	}
	local := &localRun{Version: 1, State: "OFFLINE_COMPLETE", PlanID: planID, LocalID: uuid.New(), WorktreeHash: worktree, FullSuite: true, StartedAt: started.UTC(), CompletedAt: completed, Outcome: outcome, Summary: summary, ExitCode: &exitCode, ElapsedMillis: completed.Sub(started).Milliseconds(), ComputeMillis: completed.Sub(started).Milliseconds(), CommandResults: []execution.Result{result}}
	path, err := saveLocalRun(root, local)
	if err != nil {
		return exitError{6, err}
	}
	reportRoot, err := safeWorkspacePath(root, filepath.Join(".ivp", "reports"))
	if err != nil {
		return exitError{6, err}
	}
	local.ReportPath = filepath.Join(reportRoot, local.LocalID.String()+".json")
	if err := writeReport(local.ReportPath, local); err != nil {
		return exitError{6, err}
	}
	if requestedReport != "" {
		if err := writeReport(requestedReport, local); err != nil {
			return exitError{6, err}
		}
	}
	if _, err := saveLocalRun(root, local); err != nil {
		return exitError{6, err}
	}
	fmt.Fprintf(out, "Outcome: %s\nVerification level: FULL\nLocal report: %s\nLocal outbox: %s\n", outcome, local.ReportPath, path)
	if exitCode != 0 {
		return exitError{exitCode, fmt.Errorf("authoritative full suite completed with outcome %s", outcome)}
	}
	return nil
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
		if err := uploadLocalArtifacts(ctx, root, client, local); err != nil {
			return err
		}
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
	anyTestFailure := false
	anyInfrastructureFailure := false
	anyCancellation := false
	if local.FullSuite {
		reportsBefore := execution.FingerprintReports(root, commands[0].ReportPaths)
		result := runner.Run(ctx, commands[0])
		local.CommandResults = append(local.CommandResults, result)
		var fullDetailsComplete bool
		local.UnitResults, fullDetailsComplete = collectFullSuiteUnitResults(ctx, root, record, result, reportsBefore)
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
			coverageBefore := execution.FingerprintReports(root, command.CoveragePaths)
			var attemptResults []execution.Result
			if command.CheckOnly {
				result := runner.Run(ctx, command)
				if result.Status == execution.StatusCompleted && result.ExitCode != nil {
					if *result.ExitCode == 0 {
						result.Outcome = domain.OutcomeTargetedPass
					} else {
						result.Outcome = domain.OutcomeTargetedFail
					}
				}
				attemptResults = append(attemptResults, result)
			} else {
				attemptResults = append(attemptResults, runner.RunAndParse(ctx, adapter, command, adapters.ParseRequest{ReportPaths: command.ReportPaths, MaxReportBytes: command.MaxOutputBytes}))
				for retry := 0; retry < cfg.Execution.FailureRetryCount && unit.UnitType != domain.UnitCheck && attemptResults[len(attemptResults)-1].Outcome == domain.OutcomeTargetedFail; retry++ {
					fmt.Fprintf(out, "Retrying failed test unit %s (attempt %d of %d).\n", security.Redact(planUnit.StableKey, 160), retry+2, cfg.Execution.FailureRetryCount+1)
					attemptResults = append(attemptResults, runner.RunAndParse(ctx, adapter, command, adapters.ParseRequest{ReportPaths: command.ReportPaths, MaxReportBytes: command.MaxOutputBytes}))
				}
			}
			completedAttempt := false
			hadTestFailure := false
			hadInfrastructureFailure := false
			hadCancellation := false
			for attemptIndex, result := range attemptResults {
				local.CommandResults = append(local.CommandResults, result)
				if result.Status == execution.StatusCompleted {
					completedAttempt = true
				}
				if result.Outcome == domain.OutcomeTargetedFail {
					hadTestFailure = true
				}
				status := "ERROR"
				switch result.Outcome {
				case domain.OutcomeTargetedPass:
					status = "PASSED"
				case domain.OutcomeTargetedFail:
					status = "FAILED"
				default:
					hadInfrastructureFailure = true
					hadCancellation = hadCancellation || result.Status == execution.StatusCancelled
					local.Summary.FailureClasses[result.ErrorClass]++
				}
				unitResult := api.ExecutionUnitResult{TestUnitKey: planUnit.StableKey, Attempt: attemptIndex + 1, Status: status, DurationMillis: result.Duration.Milliseconds(), ReportHash: result.StdoutHash, Summary: api.UnitResultSummary{FailureClass: result.ErrorClass}}
				unitResult.Summary.RetryCount = parsedRetryCount(result.Parsed)
				local.UnitResults = append(local.UnitResults, unitResult)
			}
			if !command.CheckOnly && len(command.CoveragePaths) > 0 && completedAttempt {
				observations, warnings := collectCoverageObservations(ctx, root, adapter, command.CoveragePaths, coverageBefore, planUnit.StableKey, record, local)
				local.Coverage = append(local.Coverage, observations...)
				local.EvidenceWarnings = append(local.EvidenceWarnings, warnings...)
			}
			local.Summary.Total++
			if hadTestFailure {
				local.Summary.Failed++
				anyTestFailure = true
			}
			if !hadTestFailure && !hadInfrastructureFailure {
				local.Summary.Passed++
			}
			if hadInfrastructureFailure {
				anyInfrastructureFailure = true
				anyCancellation = anyCancellation || hadCancellation
			}
			switch {
			case anyInfrastructureFailure:
				overall = domain.OutcomeInfraFailure
				if anyCancellation {
					exitCode = 8
				} else {
					exitCode = 6
				}
			case anyTestFailure:
				overall = domain.OutcomeTargetedFail
				exitCode = 1
			}
			if err := revalidateExecution(ctx, root, client, repositoryID, plan, record); err != nil {
				if errors.Is(err, errPlanStale) {
					overall, exitCode = domain.OutcomeStale, 5
				} else if ctx.Err() != nil {
					overall, exitCode = domain.OutcomeInfraFailure, 8
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
	if err = uploadLocalArtifacts(ctx, root, client, local); err != nil {
		return err
	}
	if err = uploadLocalResult(ctx, out, client, repositoryID, local, localPath, c.stdout); err != nil {
		return err
	}
	fmt.Fprintf(out, "Outcome: %s\nVerification level: %s\nReport: %s\n", local.Outcome, verificationLevel(local.Outcome), local.ReportPath)
	if exitCode != 0 {
		return exitError{exitCode, fmt.Errorf("verification completed with outcome %s", local.Outcome)}
	}
	return nil
}

func uploadLocalArtifacts(ctx context.Context, root string, client *apiClient, local *localRun) error {
	if local.ExecutionID == uuid.Nil || len(local.ReportPaths) == 0 {
		return nil
	}
	if local.UploadedReports == nil {
		local.UploadedReports = map[string]string{}
	}
	for _, reportPath := range local.ReportPaths {
		resolved, err := safeWorkspacePath(root, reportPath)
		if err != nil {
			return exitError{6, fmt.Errorf("configured report path is unsafe: %s", security.Redact(reportPath, 160))}
		}
		paths := []string{resolved}
		if strings.ContainsAny(resolved, "*?[") {
			paths, err = filepath.Glob(resolved)
			if err != nil {
				return exitError{6, errors.New("configured report pattern is invalid")}
			}
		}
		for _, candidate := range paths {
			rel, relErr := filepath.Rel(root, candidate)
			if relErr != nil {
				return exitError{6, errors.New("report file escaped repository root")}
			}
			file, pathErr := safeWorkspacePath(root, rel)
			if pathErr != nil {
				return exitError{6, errors.New("report file escaped repository root")}
			}
			info, statErr := os.Stat(file)
			if os.IsNotExist(statErr) {
				continue
			}
			if statErr != nil || !info.Mode().IsRegular() {
				return exitError{6, errors.New("configured report is not a regular file")}
			}
			if info.Size() <= 0 || info.Size() > adapters.DefaultReportLimit {
				return exitError{6, errors.New("configured report is empty or exceeds the upload size limit")}
			}
			ext := strings.ToLower(filepath.Ext(file))
			mediaType := ""
			switch ext {
			case ".json":
				mediaType = "application/json"
			case ".xml":
				mediaType = "application/xml"
			default:
				continue
			}
			key := reportPath + "|" + filepath.Base(file)
			if local.UploadedReports[key] != "" {
				continue
			}
			if len(local.Summary.SanitizedExcerptRefs) >= 100 {
				continue
			}
			data, readErr := os.ReadFile(file)
			if readErr != nil {
				return exitError{6, readErr}
			}
			artifactInfo, uploadErr := client.uploadArtifact(ctx, local.ExecutionID, mediaType, data)
			if uploadErr != nil {
				return uploadErr
			}
			local.UploadedReports[key] = artifactInfo.ID.String()
			local.Summary.SanitizedExcerptRefs = append(local.Summary.SanitizedExcerptRefs, artifactInfo.ID.String())
			if _, err := saveLocalRun(root, local); err != nil {
				return exitError{6, err}
			}
		}
	}
	if _, err := saveLocalRun(root, local); err != nil {
		return exitError{6, err}
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
	if requiresAuthoritativeComparison(cfg, plan) {
		if _, err := materializeFull(cfg, root, record); err != nil {
			return exitError{3, fmt.Errorf("authoritative full-suite command is unavailable: %w", err)}
		}
		fmt.Fprintf(c.stdout, "Plan %s is current; selected commands and the authoritative full-suite command materialized safely. Dry-run did not execute them.\n", plan.ID)
		return nil
	}
	fmt.Fprintf(c.stdout, "Plan %s is current and all selected commands materialized safely; dry-run did not execute them.\n", plan.ID)
	return nil
}

func planningFallbackAllowed(err error) bool {
	var coded exitError
	if errors.As(err, &coded) {
		return coded.code == 3 || coded.code == 4 || coded.code == 5 || coded.code == 6
	}
	return true
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

func collectFullSuiteUnitResults(ctx context.Context, root string, record indexRecord, run execution.Result, reportsBefore map[string]execution.ReportFingerprint) ([]api.ExecutionUnitResult, bool) {
	if run.Status != execution.StatusCompleted || run.ExitCode == nil {
		return nil, false
	}
	pathsByAdapter := make(map[string][]string)
	suiteUnitsByAdapter := make(map[string][]domain.TestUnit)
	for _, unit := range record.Units {
		if values, ok := unit.Metadata["report_paths"].([]any); ok {
			var paths []string
			for _, value := range values {
				if path, ok := value.(string); ok && path != "" {
					paths = append(paths, path)
				}
			}
			// Gradle and Maven discover immutable project/task-level units. Their
			// report directory is the reliable ownership boundary for test cases;
			// matching only on class names loses failures in multi-module builds.
			if (unit.Adapter == "gradle" || unit.Adapter == "maven") && len(paths) > 0 {
				suiteUnitsByAdapter[unit.Adapter] = append(suiteUnitsByAdapter[unit.Adapter], unit)
				continue
			}
			pathsByAdapter[unit.Adapter] = append(pathsByAdapter[unit.Adapter], paths...)
		}
	}
	var results []api.ExecutionUnitResult
	detailsComplete := len(pathsByAdapter) > 0 || len(suiteUnitsByAdapter) > 0
	attempts := map[string]int{}
	type fullSuiteUnitAggregate struct {
		duration   time.Duration
		passed     bool
		failed     bool
		errored    bool
		skipped    bool
		flaky      bool
		retryCount int
		caseStates map[string]map[string]bool
	}
	caseAggregates := map[string]*fullSuiteUnitAggregate{}
	for adapterID, units := range suiteUnitsByAdapter {
		adapter := adapterByID(adapterID)
		if adapter == nil {
			detailsComplete = false
			continue
		}
		for _, unit := range units {
			paths, _ := unit.Metadata["report_paths"].([]any)
			reportPatterns := make([]string, 0, len(paths))
			for _, value := range paths {
				if path, ok := value.(string); ok && path != "" {
					reportPatterns = append(reportPatterns, path)
				}
			}
			reportPaths := execution.FreshReports(root, reportPatterns, reportsBefore)
			if len(reportPaths) == 0 {
				detailsComplete = false
				continue
			}
			parsed, err := adapter.ParseResults(ctx, adapters.ParseRequest{Root: root, ReportPaths: reportPaths, Stdout: []byte(run.StdoutExcerpt), Stderr: []byte(run.StderrExcerpt), ExitCode: *run.ExitCode, MaxReportBytes: adapters.DefaultReportLimit})
			if err != nil || !parsed.Complete || !parsed.HasReliableReport {
				detailsComplete = false
				continue
			}
			matchedFailures := 0
			status := "PASSED"
			var caseDuration time.Duration
			for _, testCase := range parsed.Cases {
				caseStatus := canonicalCaseStatus(testCase.Status)
				if caseStatus == "" {
					detailsComplete = false
					continue
				}
				if caseStatus == "FAILED" || caseStatus == "ERROR" {
					matchedFailures++
					status = caseStatus
				} else if status != "FAILED" && status != "ERROR" && caseStatus == "SKIPPED" {
					status = "SKIPPED"
				}
				caseDuration += testCase.Duration
			}
			if parsed.Failed+parsed.Errors > matchedFailures {
				detailsComplete = false
			}
			duration := parsed.Duration
			if duration <= 0 {
				duration = caseDuration
			}
			retries := duplicateCaseCount(parsed.Cases)
			attempts[unit.StableKey]++
			failureClass := status
			if duplicateCaseFlaky(parsed.Cases) {
				failureClass = "FLAKY"
			}
			results = append(results, api.ExecutionUnitResult{TestUnitKey: unit.StableKey, Attempt: attempts[unit.StableKey], Status: status, DurationMillis: duration.Milliseconds(), ReportHash: run.StdoutHash, Summary: api.UnitResultSummary{FailureClass: failureClass, RetryCount: retries}})
		}
	}
	for adapterID, paths := range pathsByAdapter {
		adapter := adapterByID(adapterID)
		if adapter == nil {
			detailsComplete = false
			continue
		}
		freshPaths := execution.FreshReports(root, uniqueStrings(paths), reportsBefore)
		if len(freshPaths) == 0 {
			detailsComplete = false
			continue
		}
		parsed, err := adapter.ParseResults(ctx, adapters.ParseRequest{Root: root, ReportPaths: freshPaths, Stdout: []byte(run.StdoutExcerpt), Stderr: []byte(run.StderrExcerpt), ExitCode: *run.ExitCode, MaxReportBytes: adapters.DefaultReportLimit})
		if err != nil || !parsed.Complete || !parsed.HasReliableReport {
			detailsComplete = false
			continue // Full-suite pass/fail remains based on the configured command exit status.
		}
		matchedFailures := 0
		for _, testCase := range parsed.Cases {
			status := canonicalCaseStatus(testCase.Status)
			if status == "" {
				detailsComplete = false
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
			if status == "FAILED" || status == "ERROR" {
				matchedFailures++
			}
			aggregate := caseAggregates[matched.StableKey]
			if aggregate == nil {
				aggregate = &fullSuiteUnitAggregate{caseStates: map[string]map[string]bool{}}
				caseAggregates[matched.StableKey] = aggregate
			}
			aggregate.duration += testCase.Duration
			switch status {
			case "PASSED":
				aggregate.passed = true
			case "FAILED":
				aggregate.failed = true
			case "ERROR":
				aggregate.errored = true
			case "SKIPPED":
				aggregate.skipped = true
			}
			caseKey := testCase.ClassName + "\x00" + testCase.Name
			if aggregate.caseStates[caseKey] == nil {
				aggregate.caseStates[caseKey] = map[string]bool{}
			} else {
				aggregate.retryCount++
			}
			caseOutcome := "SKIP"
			if status == "PASSED" {
				caseOutcome = "PASS"
			} else if status == "FAILED" || status == "ERROR" {
				caseOutcome = "FAIL"
			}
			aggregate.caseStates[caseKey][caseOutcome] = true
			if aggregate.caseStates[caseKey]["PASS"] && aggregate.caseStates[caseKey]["FAIL"] {
				aggregate.flaky = true
			}
		}
		if parsed.Failed+parsed.Errors > matchedFailures {
			detailsComplete = false
		}
	}
	for stableKey, aggregate := range caseAggregates {
		status := "SKIPPED"
		if aggregate.passed {
			status = "PASSED"
		}
		if aggregate.failed {
			status = "FAILED"
		}
		if aggregate.errored {
			status = "ERROR"
		}
		failureClass := status
		if aggregate.flaky {
			failureClass = "FLAKY"
		}
		results = append(results, api.ExecutionUnitResult{TestUnitKey: stableKey, Attempt: 1, Status: status, DurationMillis: aggregate.duration.Milliseconds(), ReportHash: run.StdoutHash, Summary: api.UnitResultSummary{FailureClass: failureClass, RetryCount: aggregate.retryCount}})
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

func canonicalCaseStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "failed":
		return "FAILED"
	case "error":
		return "ERROR"
	case "skipped", "pending", "todo":
		return "SKIPPED"
	case "passed", "success", "successful":
		return "PASSED"
	default:
		return ""
	}
}

func parsedRetryCount(parsed *adapters.ParsedResults) int {
	if parsed == nil {
		return 0
	}
	return duplicateCaseCount(parsed.Cases)
}

func duplicateCaseCount(cases []adapters.TestCase) int {
	counts := make(map[string]int, len(cases))
	retries := 0
	for _, item := range cases {
		key := item.ClassName + "\x00" + item.Name
		if item.ClassName == "" && item.Name == "" {
			continue
		}
		counts[key]++
		if counts[key] > 1 {
			retries++
		}
	}
	return retries
}

func duplicateCaseFlaky(cases []adapters.TestCase) bool {
	seen := make(map[string]map[string]bool, len(cases))
	for _, item := range cases {
		if item.ClassName == "" && item.Name == "" {
			continue
		}
		key := item.ClassName + "\x00" + item.Name
		if seen[key] == nil {
			seen[key] = make(map[string]bool)
		}
		status := canonicalCaseStatus(item.Status)
		if status == "PASSED" {
			seen[key]["PASS"] = true
		} else if status == "FAILED" || status == "ERROR" {
			seen[key]["FAIL"] = true
		}
		if seen[key]["PASS"] && seen[key]["FAIL"] {
			return true
		}
	}
	return false
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

func coverageFormat(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".info", ".lcov":
		return "lcov"
	case ".json":
		return "coverage.py-json"
	case ".xml":
		return "xml"
	default:
		return ""
	}
}

func coverageFileHashes(root string, patterns []string) map[string]string {
	hashes := make(map[string]string)
	for _, pattern := range patterns {
		resolved, err := safeWorkspacePath(root, pattern)
		if err != nil {
			continue
		}
		files := []string{resolved}
		if strings.ContainsAny(resolved, "*?[") {
			files, err = filepath.Glob(resolved)
			if err != nil {
				continue
			}
		}
		for _, file := range files {
			rel, err := filepath.Rel(root, file)
			if err != nil {
				continue
			}
			resolved, err := safeWorkspacePath(root, rel)
			if err != nil {
				continue
			}
			info, err := os.Lstat(resolved)
			if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > adapters.DefaultReportLimit {
				continue
			}
			data, err := os.ReadFile(resolved)
			if err != nil {
				continue
			}
			digest := sha256.Sum256(data)
			hashes[filepath.Clean(resolved)] = hex.EncodeToString(digest[:])
		}
	}
	return hashes
}

func collectCoverageObservations(ctx context.Context, root string, adapter adapters.Adapter, reportPatterns []string, previous map[string]execution.ReportFingerprint, testUnitKey string, record indexRecord, local *localRun) ([]domain.CoverageObservation, []string) {
	current := coverageFileHashes(root, reportPatterns)
	if len(current) == 0 {
		return nil, []string{"configured coverage reports were missing, empty, or oversized"}
	}
	freshPaths := execution.FreshReports(root, reportPatterns, previous)
	freshSet := make(map[string]bool, len(freshPaths))
	for _, path := range freshPaths {
		freshSet[filepath.Clean(filepath.Join(root, filepath.FromSlash(path)))] = true
	}
	sourceNodes := make(map[string]domain.GraphNode)
	for _, node := range record.Nodes {
		if node.NodeType == domain.NodeSourceFile && node.Path != "" {
			sourceNodes[node.Path] = node
		}
	}
	var observations []domain.CoverageObservation
	var warnings []string
	for file, reportHash := range current {
		if !freshSet[filepath.Clean(file)] {
			continue // never reuse a report that was already present before this run
		}
		format := coverageFormat(file)
		if format == "" || adapter == nil {
			warnings = append(warnings, "coverage report format or adapter was unsupported")
			continue
		}
		rel, err := filepath.Rel(root, file)
		if err != nil {
			warnings = append(warnings, "coverage report path could not be normalized")
			continue
		}
		parsed, err := adapter.ParseCoverage(ctx, adapters.CoverageRequest{Root: root, ReportPaths: []string{filepath.ToSlash(rel)}, Format: format, MaxReportBytes: adapters.DefaultReportLimit})
		if err != nil || !parsed.Complete || len(parsed.Files) == 0 {
			warnings = append(warnings, "coverage report could not be parsed completely")
			continue
		}
		for path := range parsed.Files {
			node, exists := sourceNodes[filepath.ToSlash(path)]
			if !exists {
				warnings = append(warnings, "coverage referenced a source path outside the indexed repository inventory")
				continue
			}
			observations = append(observations, domain.CoverageObservation{
				TestUnitKey: testUnitKey, CoveredStableKey: node.StableKey, CoveredPath: node.Path,
				SourceType: adapter.ID() + ":" + parsed.Format, ReportHash: reportHash, RunID: local.ExecutionID,
				ObservedAt: time.Now().UTC(), EnvironmentFingerprint: local.EnvironmentDigest,
				BuildConfigDigest: record.Snapshot.BuildConfigDigest, TestConfigDigest: record.Snapshot.TestConfigDigest,
				Precision: domain.CoverageFile, SuiteLevel: true, TestSpecific: false, EvidenceConfidence: 0.65,
			})
		}
	}
	if len(observations) == 0 && len(warnings) == 0 {
		warnings = append(warnings, "coverage report was unchanged from before this execution")
	}
	return observations, uniqueStrings(warnings)
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
	currentPolicy, err := client.getTrustedPolicy(ctx, repositoryID)
	if err != nil {
		return fmt.Errorf("re-read and verify current policy: %w", err)
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
	request := api.ResultRequest{Outcome: local.Outcome, ExitCode: local.ExitCode, CompletedAt: local.CompletedAt, ElapsedMillis: local.ElapsedMillis, ComputeMillis: local.ComputeMillis, Summary: local.Summary, UnitResults: local.UnitResults, Coverage: local.Coverage}
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
		if v, ok := u.Metadata["runner"].(string); ok {
			result.Runner = v
		}
		if v, ok := u.Metadata["report_paths"].([]any); ok {
			for _, x := range v {
				if s, ok := x.(string); ok {
					result.ReportPaths = append(result.ReportPaths, s)
				}
			}
		}
		if v, ok := u.Metadata["coverage_paths"].([]any); ok {
			for _, x := range v {
				if s, ok := x.(string); ok {
					result.CoveragePaths = append(result.CoveragePaths, s)
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
