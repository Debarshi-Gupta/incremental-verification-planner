package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runGit executes git in dir and fails the test on error, returning combined
// output. No shell is involved, so paths with spaces are safe.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

// repoRoot resolves the repository root from the package directory
// (tests/historical-replay -> ../..), where the pinned toolchain lives.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// goBinary prefers the repo-pinned toolchain (house rule on this host) and
// falls back to go on PATH.
func goBinary(t *testing.T) string {
	t.Helper()
	name := "go"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	pinned := filepath.Join(repoRoot(t), ".tooling", "go", "bin", name)
	if _, err := os.Stat(pinned); err == nil {
		return pinned
	}
	path, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("no Go toolchain available: %v", err)
	}
	return path
}

func writeFixtureFile(t *testing.T, dir, relPath, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// buildFixtureRepo creates a two-commit Go module repository in t.TempDir():
// the base commit ships mathx.Add with a regression (a - b) whose TestAdd
// fails, the head commit fixes it (a + b). TestSmoke passes in both trees, so
// a narrow -run TestSmoke selection misses the regression on base — a
// constructed audit-miss scenario.
func buildFixtureRepo(t *testing.T) (repoDir, baseSHA, headSHA string) {
	t.Helper()
	repoDir = filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "init", "-q", "-b", "main")
	// Pin local repository behavior so host-global configuration (signing,
	// autocrlf, hooks) cannot change the fixture's commits or trees.
	runGit(t, repoDir, "config", "user.email", "fixture@example.com")
	runGit(t, repoDir, "config", "user.name", "Fixture Bot")
	runGit(t, repoDir, "config", "commit.gpgsign", "false")
	runGit(t, repoDir, "config", "core.autocrlf", "false")
	runGit(t, repoDir, "config", "core.hooksPath", filepath.Join(repoDir, ".no-hooks"))

	writeFixtureFile(t, repoDir, "go.mod", "module fixture\n\ngo 1.27\n")
	writeFixtureFile(t, repoDir, "mathx/add.go",
		"package mathx\n\n// Add is supposed to sum its operands.\nfunc Add(a, b int) int { return a - b }\n")
	writeFixtureFile(t, repoDir, "mathx/add_test.go", `package mathx

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Fatalf("Add(2, 3) = %d, want 5", got)
	}
}

func TestSmoke(t *testing.T) {
	if Add(0, 0) != 0 {
		t.Fatal("Add(0, 0) must be 0")
	}
}
`)
	runGit(t, repoDir, "add", "-A")
	runGit(t, repoDir, "commit", "-q", "-m", "base: ship Add with a regression")
	baseSHA = strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))

	writeFixtureFile(t, repoDir, "mathx/add.go",
		"package mathx\n\n// Add is supposed to sum its operands.\nfunc Add(a, b int) int { return a + b }\n")
	runGit(t, repoDir, "add", "-A")
	runGit(t, repoDir, "commit", "-q", "-m", "head: fix Add regression")
	headSHA = strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	return repoDir, baseSHA, headSHA
}

// TestReconstructTreeRejectsInvalidInput covers the guard rails: required
// arguments, a non-empty destination, a destination that is a file, and an
// unknown sha must all fail without touching anything.
func TestReconstructTreeRejectsInvalidInput(t *testing.T) {
	repoDir, _, _ := buildFixtureRepo(t)
	destRoot := t.TempDir()

	if err := reconstructTree(repoDir, "", filepath.Join(destRoot, "a")); err == nil {
		t.Fatal("empty sha must be rejected")
	}
	if err := reconstructTree("", "HEAD", filepath.Join(destRoot, "b")); err == nil {
		t.Fatal("empty repoDir must be rejected")
	}
	if err := reconstructTree(repoDir, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", filepath.Join(destRoot, "c")); err == nil {
		t.Fatal("unknown sha must be rejected")
	}
	occupied := filepath.Join(destRoot, "occupied")
	writeFixtureFile(t, occupied, "keep.txt", "keep")
	if err := reconstructTree(repoDir, "HEAD", occupied); err == nil {
		t.Fatal("non-empty destination must be rejected")
	}
	if _, err := os.Stat(filepath.Join(occupied, "keep.txt")); err != nil {
		t.Fatalf("rejected reconstruction must not modify the destination: %v", err)
	}
	asFile := filepath.Join(destRoot, "file.txt")
	writeFixtureFile(t, destRoot, "file.txt", "x")
	if err := reconstructTree(repoDir, "HEAD", asFile); err == nil {
		t.Fatal("destination that is a file must be rejected")
	}
}

// TestReconstructTreesAndDualRun is the genuine §17.8 dual execution on this
// host: it builds a real git fixture, reconstructs both trees with real git
// (git archive), and runs real `go test` full-suite and selected commands in
// them, asserting every captured exit code and stdout matches the constructed
// scenario — no mocks of git or of the test runner.
func TestReconstructTreesAndDualRun(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is required for tree reconstruction: %v", err)
	}
	repoDir, baseSHA, headSHA := buildFixtureRepo(t)
	treeRoot := t.TempDir()
	baseTree := filepath.Join(treeRoot, "base")
	headTree := filepath.Join(treeRoot, "head")

	if err := reconstructTree(repoDir, baseSHA, baseTree); err != nil {
		t.Fatalf("reconstruct base: %v", err)
	}
	if err := reconstructTree(repoDir, headSHA, headTree); err != nil {
		t.Fatalf("reconstruct head: %v", err)
	}
	// The reconstructed trees carry exactly the committed content, with no
	// .git metadata leaked into either destination.
	baseAdd, err := os.ReadFile(filepath.Join(baseTree, "mathx", "add.go"))
	if err != nil {
		t.Fatalf("base tree is missing mathx/add.go: %v", err)
	}
	headAdd, err := os.ReadFile(filepath.Join(headTree, "mathx", "add.go"))
	if err != nil {
		t.Fatalf("head tree is missing mathx/add.go: %v", err)
	}
	if !strings.Contains(string(baseAdd), "return a - b") {
		t.Fatalf("base tree does not contain the regression:\n%s", baseAdd)
	}
	if !strings.Contains(string(headAdd), "return a + b") {
		t.Fatalf("head tree does not contain the fix:\n%s", headAdd)
	}
	for _, tree := range []string{baseTree, headTree} {
		if _, err := os.Stat(filepath.Join(tree, "go.mod")); err != nil {
			t.Fatalf("tree %s is missing go.mod: %v", tree, err)
		}
		if _, err := os.Stat(filepath.Join(tree, ".git")); !os.IsNotExist(err) {
			t.Fatalf("tree %s must not contain .git metadata (err=%v)", tree, err)
		}
	}
	// Reconstruction is repeatable: a fresh destination yields the same bytes.
	againTree := filepath.Join(treeRoot, "again")
	if err := reconstructTree(repoDir, baseSHA, againTree); err != nil {
		t.Fatalf("second reconstruction: %v", err)
	}
	againAdd, err := os.ReadFile(filepath.Join(againTree, "mathx", "add.go"))
	if err != nil || string(againAdd) != string(baseAdd) {
		t.Fatalf("reconstruction is not deterministic: %v\n%s", err, againAdd)
	}

	goBin := goBinary(t)
	fullSuite := CommandSpec{Name: "full-suite", Path: goBin, Args: []string{"test", "./..."}}
	selected := CommandSpec{Name: "selected", Path: goBin, Args: []string{"test", "-run", "TestSmoke", "./..."}}

	// Dual execution per LLD 17.8 line 1039: full suite in the reconstructed
	// head tree, selected/plan command in the reconstructed base tree.
	dual, err := dualRun(headTree, baseTree, fullSuite, selected)
	if err != nil {
		t.Fatal(err)
	}
	if dual.FullSuite.Name != "full-suite" || dual.FullSuite.Tree != headTree {
		t.Fatalf("full suite did not run in the head tree: %+v", dual.FullSuite)
	}
	if !strings.Contains(dual.FullSuite.Command, "test ./...") {
		t.Fatalf("full suite command not recorded: %q", dual.FullSuite.Command)
	}
	if dual.FullSuite.ExitCode != 0 {
		t.Fatalf("head full suite must pass, exit %d\nstdout: %s\nstderr: %s",
			dual.FullSuite.ExitCode, dual.FullSuite.Stdout, dual.FullSuite.Stderr)
	}
	if !strings.Contains(dual.FullSuite.Stdout, "ok") {
		t.Fatalf("head full suite stdout must report ok:\n%s", dual.FullSuite.Stdout)
	}
	if dual.Selected.Name != "selected" || dual.Selected.Tree != baseTree {
		t.Fatalf("selected did not run in the base tree: %+v", dual.Selected)
	}
	if dual.Selected.ExitCode != 0 {
		t.Fatalf("narrow TestSmoke selection must pass on base, exit %d\nstdout: %s\nstderr: %s",
			dual.Selected.ExitCode, dual.Selected.Stdout, dual.Selected.Stderr)
	}

	// The remaining legs of the matrix, run directly in the reconstructed trees:
	// full suite on base FAILS with the regression test named in stdout (the
	// constructed regression), the narrow selection passes on head, and a plan
	// that selects the regression test catches the failure on base.
	baseFull, err := runInTree(baseTree, fullSuite)
	if err != nil {
		t.Fatal(err)
	}
	if baseFull.ExitCode == 0 {
		t.Fatalf("base full suite must fail:\n%s", baseFull.Stdout)
	}
	if !strings.Contains(baseFull.Stdout, "--- FAIL: TestAdd") {
		t.Fatalf("base full suite stdout must name TestAdd:\n%s", baseFull.Stdout)
	}
	if !strings.Contains(baseFull.Stdout, "FAIL") {
		t.Fatalf("base full suite stdout must report FAIL:\n%s", baseFull.Stdout)
	}
	headSelected, err := runInTree(headTree, selected)
	if err != nil {
		t.Fatal(err)
	}
	if headSelected.ExitCode != 0 {
		t.Fatalf("selected must pass on head, exit %d\n%s", headSelected.ExitCode, headSelected.Stdout)
	}
	planSelectsRegression := CommandSpec{Name: "plan-selects-regression", Path: goBin, Args: []string{"test", "-run", "TestAdd", "./..."}}
	baseSelected, err := runInTree(baseTree, planSelectsRegression)
	if err != nil {
		t.Fatal(err)
	}
	if baseSelected.ExitCode == 0 {
		t.Fatalf("a plan selecting TestAdd must fail on base:\n%s", baseSelected.Stdout)
	}
	if !strings.Contains(baseSelected.Stdout, "--- FAIL: TestAdd") {
		t.Fatalf("selected stdout must name TestAdd:\n%s", baseSelected.Stdout)
	}
	// Spawning a missing executable is an error, not an exit code.
	if _, err := runInTree(baseTree, CommandSpec{Name: "missing", Path: filepath.Join(baseTree, "no-such-binary")}); err == nil {
		t.Fatal("spawning a missing executable must be an error")
	}
}
