package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestInterruptDuringRunPersistsCancelledAndExitsEight is the regression test
// for P0-5: Ctrl-C must be wired through signal.NotifyContext in main.go, the
// in-flight full-suite command must be cancelled, the cancelled result must be
// persisted locally, and the process must exit with code 8 (LLD 16 row 15).
// The scenario uses the offline full-suite fallback so no API server is
// required: `ivp verify` cannot plan (no endpoint configured) and therefore
// runs the configured authoritative full suite.
func TestInterruptDuringRunPersistsCancelledAndExitsEight(t *testing.T) {
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go"+exeSuffix())
	if _, err := os.Stat(goTool); err != nil {
		t.Fatalf("toolchain go not found at %s: %v", goTool, err)
	}
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	// The marker lives outside the repository: the run path re-fingerprints the
	// worktree after the command, and a file created inside the repo would mark
	// the result STALE instead of CANCELLED.
	marker := filepath.Join(base, "full-suite-started.txt")
	// Config validation forbids shell argv (injection safety), so the
	// long-running full-suite command is a purpose-built helper binary: it
	// drops the start marker and then sleeps until the runner kills it.
	helperDir := filepath.Join(base, "helpersuite")
	if err := os.MkdirAll(helperDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const helperSource = `package main

import (
	"os"
	"time"
)

func main() {
	if err := os.WriteFile(os.Args[1], []byte("started"), 0o600); err != nil {
		os.Exit(2)
	}
	time.Sleep(300 * time.Second)
}
`
	if err := os.WriteFile(filepath.Join(helperDir, "main.go"), []byte(helperSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(helperDir, "go.mod"), []byte("module ivptesthelper\n\ngo 1.27.3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(base, "helpersuite"+exeSuffix())
	buildHelper := exec.Command(goTool, "build", "-o", helper, ".")
	buildHelper.Dir = helperDir
	if out, err := buildHelper.CombinedOutput(); err != nil {
		t.Fatalf("go build full-suite helper: %v\n%s", err, out)
	}
	configJSON, err := json.Marshal(map[string]any{"execution": map[string]any{
		"full_suite_argv": []any{helper, marker}, "timeout_seconds": 600,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".ivp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".ivp", "config.yaml"), configJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init", "-q")
	gitIn(t, repo, "config", "user.email", "ivp-test@example.invalid")
	gitIn(t, repo, "config", "user.name", "IVP tests")
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "initial")

	// Build the real ivp binary: the interrupted-signal wiring lives in package
	// main, so only an end-to-end run can regress it.
	binary := filepath.Join(base, "ivp"+exeSuffix())
	buildCtx, buildCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer buildCancel()
	if out, buildErr := exec.CommandContext(buildCtx, goTool, "build", "-o", binary, ".").CombinedOutput(); buildErr != nil {
		t.Fatalf("go build ./cmd/ivp: %v\n%s", buildErr, out)
	}

	cmd := exec.Command(binary, "verify", "--base", "HEAD", "--mode", "FAST_FEEDBACK")
	cmd.Dir = repo
	var stdout strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	isolateProcess(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// Wait until the full-suite command has actually started (it writes the
	// marker before sleeping) so the interrupt lands mid-execution.
	deadline := time.Now().Add(60 * time.Second)
	started := false
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			started = true
			break
		}
		select {
		case earlyErr := <-done:
			t.Fatalf("ivp verify exited (%v) before the full-suite command started; output:\n%s", earlyErr, stdout.String())
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !started {
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("full-suite command never started; output:\n%s", stdout.String())
	}

	if err := sendInterrupt(cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("sending interrupt failed: %v; output:\n%s", err, stdout.String())
	}

	select {
	case <-done:
		if exit := cmd.ProcessState.ExitCode(); exit != 8 {
			t.Fatalf("interrupted run exited with %d, want 8; output:\n%s", exit, stdout.String())
		}
	case <-time.After(45 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("ivp did not exit after the interrupt; output:\n%s", stdout.String())
	}

	// The cancelled result must be persisted locally with its cancellation
	// evidence, independent of any server upload.
	reports, err := filepath.Glob(filepath.Join(repo, ".ivp", "reports", "*.json"))
	if err != nil || len(reports) == 0 {
		t.Fatalf("no local report persisted (glob err=%v); output:\n%s", err, stdout.String())
	}
	var persisted bool
	for _, report := range reports {
		data, readErr := os.ReadFile(report)
		if readErr != nil {
			t.Fatalf("read report %s: %v", report, readErr)
		}
		var decoded struct {
			ExitCode *int   `json:"exit_code"`
			Outcome  string `json:"outcome"`
			Summary  struct {
				FailureClasses map[string]int `json:"failure_classes"`
			} `json:"summary"`
			CommandResults []struct {
				Status     string `json:"status"`
				ErrorClass string `json:"error_class"`
			} `json:"command_results"`
		}
		if json.Unmarshal(data, &decoded) != nil {
			t.Fatalf("report %s is not valid JSON: %s", report, data)
		}
		if decoded.ExitCode != nil && *decoded.ExitCode == 8 && decoded.Summary.FailureClasses["CANCELLED"] > 0 {
			persisted = true
			continue
		}
		t.Logf("report %s: exit_code=%v outcome=%s failure_classes=%v command_results=%+v",
			report, decoded.ExitCode, decoded.Outcome, decoded.Summary.FailureClasses, decoded.CommandResults)
	}
	if !persisted {
		t.Fatalf("persisted report lacks CANCELLED failure class and exit_code 8; files=%v output:\n%s", reports, stdout.String())
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}
