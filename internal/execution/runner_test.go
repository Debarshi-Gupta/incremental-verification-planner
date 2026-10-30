package execution

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/adapters/builtin"
	"github.com/ivp/ivp/internal/domain"
)

func TestRunnerChildProcess(t *testing.T) {
	if os.Getenv("IVP_TEST_CHILD") != "1" {
		return
	}
	switch os.Getenv("IVP_TEST_MODE") {
	case "sleep":
		time.Sleep(10 * time.Second)
	case "large":
		fmt.Print(strings.Repeat("x", 100<<10))
	default:
		fmt.Printf("\x1b[31mSECRET=%s\x1b[0m\n", os.Getenv("IVP_TEST_SECRET"))
	}
	os.Exit(0)
}

func TestRunnerBoundsOutputAndRedactsSecrets(t *testing.T) {
	root := t.TempDir()
	runner, err := NewRunner(root, RunnerOptions{MaxOutputBytes: 1024, AllowedEnvironment: []string{"PATH", "SYSTEMROOT", "IVP_TEST_CHILD", "IVP_TEST_MODE", "IVP_TEST_SECRET"}})
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := adapters.ExecutionCommand{Executable: executable, Args: []string{"-test.run=^TestRunnerChildProcess$"}, WorkingDirectory: root, Environment: map[string]string{"IVP_TEST_CHILD": "1", "IVP_TEST_SECRET": "secret-value"}, Timeout: 5 * time.Second, MaxOutputBytes: 1024, FullSuite: true, CheckOnly: true}
	result := runner.RunAndParse(context.Background(), nil, command, adapters.ParseRequest{})
	if result.Outcome != domain.OutcomeFullVerificationPass {
		t.Fatalf("unexpected outcome: %+v", result)
	}
	if strings.Contains(result.StdoutExcerpt, "secret-value") || strings.Contains(result.StdoutExcerpt, "\x1b") {
		t.Fatalf("output was not sanitized: %q", result.StdoutExcerpt)
	}
	if !strings.Contains(result.StdoutExcerpt, "[REDACTED]") {
		t.Fatalf("secret value was not redacted: %q", result.StdoutExcerpt)
	}
	command.Environment["IVP_TEST_MODE"] = "large"
	result = runner.Run(context.Background(), command)
	if !result.OutputTruncated || len(result.StdoutExcerpt) > 1024 {
		t.Fatalf("output limit failed: truncated=%v bytes=%d", result.OutputTruncated, len(result.StdoutExcerpt))
	}
}

func TestRunnerTimeoutAndWindowsSelectiveRefusal(t *testing.T) {
	root := t.TempDir()
	runner, err := NewRunner(root, RunnerOptions{AllowedEnvironment: []string{"PATH", "SYSTEMROOT", "IVP_TEST_CHILD", "IVP_TEST_MODE"}})
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := adapters.ExecutionCommand{Executable: exe, Args: []string{"-test.run=^TestRunnerChildProcess$"}, WorkingDirectory: root, Environment: map[string]string{"IVP_TEST_CHILD": "1", "IVP_TEST_MODE": "sleep"}, Timeout: 300 * time.Millisecond, FullSuite: true, CheckOnly: true}
	result := runner.Run(context.Background(), command)
	if result.Status != StatusTimedOut {
		t.Fatalf("expected timeout, got %+v", result)
	}
	command.FullSuite = false
	command.Filtered = false
	result = runner.Run(context.Background(), command)
	if isWindows() && result.Status != StatusEscalated {
		t.Fatalf("Windows selective execution was not refused: %+v", result)
	}
}

func TestRunnerRejectsRelativeExecutableTraversal(t *testing.T) {
	runner, err := NewRunner(t.TempDir(), RunnerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result := runner.Run(context.Background(), adapters.ExecutionCommand{Executable: "..\\outside.exe", WorkingDirectory: "."})
	if result.Status != StatusInfraError || result.ErrorClass != "INVALID_COMMAND" {
		t.Fatalf("relative executable traversal was not rejected: %+v", result)
	}
}

func TestValidatePlanRechecksWorktreePolicyTargetsAndMandatoryChecks(t *testing.T) {
	now := time.Now().UTC()
	plan := domain.VerificationPlan{ID: uuid.New(), Status: domain.PlanReady, Mode: domain.ModeFastFeedback, WorktreeHash: "tree-a", PolicyVersion: 2, AlgorithmVersion: "algo-v1", ExpiresAt: now.Add(time.Minute), SelectedUnits: []domain.PlanUnit{{StableKey: "tests/unit", TargetRef: "tests/unit"}}}
	valid := PlanRevalidation{CurrentWorktreeHash: "tree-a", PolicyVersion: 2, AlgorithmVersion: "algo-v1", Now: now, KnownTargetRefs: map[string]bool{"tests/unit": true}, MandatoryChecks: []string{"tests/unit"}}
	if err := ValidatePlan(plan, valid); err != nil {
		t.Fatal(err)
	}
	valid.CurrentWorktreeHash = "tree-b"
	if err := ValidatePlan(plan, valid); err == nil {
		t.Fatal("changed worktree was accepted")
	}
	valid.CurrentWorktreeHash = "tree-a"
	valid.MandatoryChecks = []string{"secret-scan"}
	if err := ValidatePlan(plan, valid); err == nil {
		t.Fatal("missing mandatory check was accepted")
	}
}

func TestRunPlanStopsAfterWorktreeMutation(t *testing.T) {
	root := t.TempDir()
	runner, err := NewRunner(root, RunnerOptions{AllowedEnvironment: []string{"PATH", "SYSTEMROOT", "IVP_TEST_CHILD"}})
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	plan := domain.VerificationPlan{ID: uuid.New(), Status: domain.PlanReady, Mode: domain.ModeFastFeedback, WorktreeHash: "tree-a", PolicyVersion: 1, AlgorithmVersion: "v1", ExpiresAt: now.Add(time.Minute), SelectedUnits: []domain.PlanUnit{{StableKey: "test-one", TargetRef: "test-one"}, {StableKey: "test-two", TargetRef: "test-two"}}}
	current := "tree-a"
	materialized := 0
	validations := 0
	validate := func(context.Context) (PlanRevalidation, error) {
		validations++
		if validations >= 2 {
			current = "tree-b"
		}
		return PlanRevalidation{CurrentWorktreeHash: current, PolicyVersion: 1, AlgorithmVersion: "v1", Now: time.Now().UTC(), KnownTargetRefs: map[string]bool{"test-one": true, "test-two": true}}, nil
	}
	result := runner.RunPlan(context.Background(), plan, builtin.All()[0], validate, func(_ context.Context, u domain.PlanUnit) (adapters.ExecutionCommand, error) {
		materialized++
		return adapters.ExecutionCommand{Executable: exe, Args: []string{"-test.run=^TestRunnerChildProcess$"}, WorkingDirectory: root, Environment: map[string]string{"IVP_TEST_CHILD": "1"}, Timeout: 5 * time.Second, FullSuite: true, CheckOnly: true, TargetKey: u.TargetRef}, nil
	}, nil)
	// RunPlan performs validation after each command; this callback changes the
	// fingerprint after the first process has completed.
	if result.Status != StatusStalePlan || result.Outcome != domain.OutcomeStale {
		t.Fatalf("expected stale-plan result, got %+v", result)
	}
	if len(result.Results) != 1 || materialized != 1 {
		t.Fatalf("planner should stop after the first mutated command: results=%d materialized=%d", len(result.Results), materialized)
	}
}
