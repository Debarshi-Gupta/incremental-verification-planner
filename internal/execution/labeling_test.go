package execution

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/domain"
)

// LLD 7.1: a passing result is labelled by what actually ran - a full-suite
// run is FULL_VERIFICATION_PASS and a selective run is TARGETED_PASS, never
// the other way round.
func TestPassingResultIsLabelledByWhatActuallyRan(t *testing.T) {
	root := t.TempDir()
	runner, err := NewRunner(root, RunnerOptions{MaxOutputBytes: 1024, AllowedEnvironment: []string{"PATH", "SYSTEMROOT", "IVP_TEST_CHILD"}})
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := adapters.ExecutionCommand{Executable: executable, Args: []string{"-test.run=^TestRunnerChildProcess$"},
		WorkingDirectory: root, Environment: map[string]string{"IVP_TEST_CHILD": "1"}, Timeout: 5 * time.Second,
		MaxOutputBytes: 1024, CheckOnly: true}

	// A full-suite run that passes is a full-verification pass.
	full := command
	full.FullSuite = true
	result := runner.RunAndParse(context.Background(), nil, full, adapters.ParseRequest{})
	if result.Outcome != domain.OutcomeFullVerificationPass {
		t.Fatalf("a passing full-suite run was labelled %q, want %q", result.Outcome, domain.OutcomeFullVerificationPass)
	}

	// A selective run that passes is a targeted pass. The runner refuses
	// selective execution on Windows, so that half is exercised where the
	// platform allows it.
	selective := command
	selective.FullSuite = false
	result = runner.RunAndParse(context.Background(), nil, selective, adapters.ParseRequest{})
	if runtime.GOOS == "windows" {
		if result.Outcome == domain.OutcomeFullVerificationPass {
			t.Fatalf("a refused selective run was labelled %q", result.Outcome)
		}
		return
	}
	if result.Outcome != domain.OutcomeTargetedPass {
		t.Fatalf("a passing selective run was labelled %q, want %q", result.Outcome, domain.OutcomeTargetedPass)
	}
}
