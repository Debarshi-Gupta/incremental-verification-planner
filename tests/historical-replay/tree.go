package main

// Base/head tree reconstruction and dual execution for historical replay
// (inventory row IVP-17-8-afbad6c53e, LLD 17.8 line 1039: "Reconstruct
// base/head trees and run both full suite and IVP plan on reproducible
// toolchains").
//
// Why `git archive --format=zip` + Go's archive/zip instead of
// `git worktree add --detach` or `git archive | tar -x` on this Windows host:
//   - it is read-only on the source repository: no worktree registry entries
//     are created and none need pruning later, so reconstruction/cleanup
//     cannot fail on Windows file locks the way `git worktree remove --force`
//     can while handles on worktree files are still open;
//   - unlike `git archive | tar -x` it needs no tar.exe (or shell pipe): the
//     zip stream is decoded with the Go standard library, so git is the only
//     external tool and the test exercises real git on this host;
//   - the destination contains exactly the committed tree — no .git metadata
//     leaks into the reconstructed tree, matching "reconstruct ... trees".
import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CommandSpec describes a command executed inside a reconstructed tree.
type CommandSpec struct {
	Name string   `json:"name"`
	Path string   `json:"path"`
	Args []string `json:"args,omitempty"`
}

// CommandLine renders the command for reports and assertions.
func (c CommandSpec) CommandLine() string {
	return strings.Join(append([]string{c.Path}, c.Args...), " ")
}

// RunResult captures one execution: which tree it ran in, the exact command,
// its exit code, and captured stdout/stderr. A non-zero ExitCode is data, not
// an error — replaying a base tree legitimately fails when the base carries a
// regression.
type RunResult struct {
	Name     string `json:"name"`
	Tree     string `json:"tree"`
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// DualRun is the pair of §17.8 executions over reconstructed trees.
type DualRun struct {
	FullSuite RunResult `json:"full_suite"` // full-suite command in the head tree
	Selected  RunResult `json:"selected"`   // selected/plan command in the base tree
}

// reconstructTree materializes the exact tree of commit sha into destDir,
// which must be absent or empty. See the package note above for why the
// git-archive-zip pipeline is used. The temporary archive is always removed.
func reconstructTree(repoDir, sha, destDir string) error {
	if strings.TrimSpace(repoDir) == "" || strings.TrimSpace(sha) == "" || strings.TrimSpace(destDir) == "" {
		return fmt.Errorf("reconstructTree: repoDir, sha, and destDir are required")
	}
	if info, err := os.Stat(destDir); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("reconstructTree: destination %s exists and is not a directory", destDir)
		}
		entries, err := os.ReadDir(destDir)
		if err != nil {
			return fmt.Errorf("reconstructTree: read destination %s: %w", destDir, err)
		}
		if len(entries) > 0 {
			return fmt.Errorf("reconstructTree: destination %s must be empty", destDir)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reconstructTree: stat destination %s: %w", destDir, err)
	}
	archive, err := os.CreateTemp("", "ivp-reconstruct-*.zip")
	if err != nil {
		return fmt.Errorf("reconstructTree: create archive: %w", err)
	}
	archivePath := archive.Name()
	defer os.Remove(archivePath)
	defer archive.Close()
	// git archive streams the tree of sha; cmd.Dir keeps it all in-process
	// (no shell), so paths with spaces are safe.
	cmd := exec.Command("git", "archive", "--format=zip", sha)
	cmd.Dir = repoDir
	cmd.Stdout = archive
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if err := archive.Close(); err != nil {
		return fmt.Errorf("reconstructTree: close archive: %w", err)
	}
	if runErr != nil {
		return fmt.Errorf("reconstructTree: git archive %s: %w: %s", sha, runErr, strings.TrimSpace(stderr.String()))
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("reconstructTree: create destination %s: %w", destDir, err)
	}
	return extractZipTree(archivePath, destDir)
}

// extractZipTree unpacks a git-archive zip into destDir, preserving the
// executable bit and rejecting any entry that would escape the destination.
func extractZipTree(archivePath, destDir string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("extractZipTree: open archive: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("extractZipTree: stat archive: %w", err)
	}
	reader, err := zip.NewReader(file, info.Size())
	if err != nil {
		return fmt.Errorf("extractZipTree: read archive: %w", err)
	}
	for _, entry := range reader.File {
		if err := extractZipEntry(entry, destDir); err != nil {
			return err
		}
	}
	return nil
}

func extractZipEntry(entry *zip.File, destDir string) error {
	name := filepath.Clean(filepath.FromSlash(entry.Name))
	if name == "." || name == "" {
		return nil
	}
	if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return fmt.Errorf("extractZipEntry: archive entry %q escapes destination", entry.Name)
	}
	target := filepath.Join(destDir, name)
	mode := entry.Mode()
	switch {
	case entry.FileInfo().IsDir() || strings.HasSuffix(entry.Name, "/"):
		if err := os.MkdirAll(target, 0o755); err != nil {
			return fmt.Errorf("extractZipEntry: mkdir %s: %w", target, err)
		}
		return nil
	case mode&os.ModeSymlink != 0:
		link, err := entry.Open()
		if err != nil {
			return fmt.Errorf("extractZipEntry: open symlink %s: %w", entry.Name, err)
		}
		targetText, err := io.ReadAll(io.LimitReader(link, 1<<20))
		link.Close()
		if err != nil {
			return fmt.Errorf("extractZipEntry: read symlink %s: %w", entry.Name, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("extractZipEntry: mkdir for %s: %w", entry.Name, err)
		}
		// Requires symlink privilege on Windows; a failure is reported, never
		// silently skipped, because the tree would then not be exact.
		if err := os.Symlink(string(targetText), target); err != nil {
			return fmt.Errorf("extractZipEntry: symlink %s: %w", entry.Name, err)
		}
		return nil
	default:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("extractZipEntry: mkdir for %s: %w", entry.Name, err)
		}
		source, err := entry.Open()
		if err != nil {
			return fmt.Errorf("extractZipEntry: open %s: %w", entry.Name, err)
		}
		defer source.Close()
		perms := mode.Perm()
		if perms == 0 {
			perms = 0o644
		}
		destination, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perms)
		if err != nil {
			return fmt.Errorf("extractZipEntry: create %s: %w", target, err)
		}
		if _, err := io.Copy(destination, source); err != nil {
			destination.Close()
			return fmt.Errorf("extractZipEntry: write %s: %w", target, err)
		}
		if err := destination.Close(); err != nil {
			return fmt.Errorf("extractZipEntry: close %s: %w", target, err)
		}
		return nil
	}
}

// runInTree executes spec inside treeDir (the working directory), capturing
// exit code plus stdout and stderr. A non-zero exit is captured as data; only
// a failure to spawn the process is an error. The child environment pins
// GOTOOLCHAIN/GOPROXY/GOWORK so runs are reproducible on this host and never
// reach the network or an ambient go.work.
func runInTree(treeDir string, spec CommandSpec) (RunResult, error) {
	result := RunResult{Name: spec.Name, Tree: treeDir, Command: spec.CommandLine()}
	if spec.Path == "" {
		return RunResult{}, fmt.Errorf("runInTree: command %q has no executable path", spec.Name)
	}
	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Dir = treeDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// os/exec deduplicates environment entries keeping the last occurrence
	// (case-insensitively on Windows), so these pins override ambient values.
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOPROXY=off", "GOWORK=off")
	runErr := cmd.Run()
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	if runErr == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return RunResult{}, fmt.Errorf("runInTree: spawn %q in %s: %w", spec.CommandLine(), treeDir, runErr)
}

// dualRun performs the two §17.8 executions: the full-suite command in the
// reconstructed head tree and the selected/plan command in the reconstructed
// base tree. Both results (exit code + stdout) are returned; neither a
// failing suite nor a failing selection is an error.
func dualRun(headTree, baseTree string, fullSuite, selected CommandSpec) (DualRun, error) {
	full, err := runInTree(headTree, fullSuite)
	if err != nil {
		return DualRun{}, fmt.Errorf("dualRun full suite: %w", err)
	}
	selectedRun, err := runInTree(baseTree, selected)
	if err != nil {
		return DualRun{}, fmt.Errorf("dualRun selected: %w", err)
	}
	return DualRun{FullSuite: full, Selected: selectedRun}, nil
}
