package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectChangesIncludesStagedUnstagedUntrackedRenameAndDelete(t *testing.T) {
	root := initializedRepo(t)
	writeFile(t, root, "src/change.go", "package sample\nfunc Changed() int { return 2 }\n")
	gitTestCommand(t, root, "add", "src/change.go")
	writeFile(t, root, "src/change.go", "package sample\nfunc Changed() int { return 3 }\n") // also unstaged
	gitTestCommand(t, root, "mv", "src/old.txt", "src/new.txt")
	gitTestCommand(t, root, "rm", "src/gone.txt")
	writeFile(t, root, "src/untracked file.txt", "new content\n")

	changes, err := DetectChanges(context.Background(), root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Files) != 4 {
		t.Fatalf("expected modified, renamed, deleted, and untracked paths; got %+v", changes.Files)
	}
	byPath := make(map[string]struct {
		status  string
		oldPath string
		newHash string
		hunks   int
	})
	for _, changed := range changes.Files {
		byPath[changed.Path] = struct {
			status  string
			oldPath string
			newHash string
			hunks   int
		}{string(changed.Status), changed.OldPath, changed.NewHash, len(changed.DiffHunks)}
	}
	if item := byPath["src/change.go"]; item.status != "MODIFIED" || item.newHash == "" || item.hunks == 0 {
		t.Errorf("staged plus unstaged edit was not captured: %+v", item)
	}
	if item := byPath["src/new.txt"]; item.status != "RENAMED" || item.oldPath != "src/old.txt" || item.newHash == "" {
		t.Errorf("rename was not captured with both paths: %+v", item)
	}
	if item := byPath["src/gone.txt"]; item.status != "DELETED" || item.newHash != "" {
		t.Errorf("deletion was not retained: %+v", item)
	}
	if item := byPath["src/untracked file.txt"]; item.status != "ADDED" || item.newHash == "" {
		t.Errorf("untracked file was not captured: %+v", item)
	}
	if changes.BaseSHA == "" || changes.HeadSHA == "" || changes.TreeHash == "" || changes.WorktreeHash == "" || changes.SubmoduleDigest == "" {
		t.Fatalf("change set is missing repository fingerprints: %+v", changes)
	}
}

func TestFingerprintsTrackContentAndEnvironmentIsAllowlisted(t *testing.T) {
	root := initializedRepo(t)
	before, err := WorktreeFingerprint(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "src/change.go", "package sample\nfunc Changed() int { return 9 }\n")
	after, err := WorktreeFingerprint(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("worktree fingerprint did not change after content mutation")
	}
	first, err := EnvironmentFingerprint(map[string]string{"go": "1.27", "os": "linux"}, map[string]string{"LANG": "C", "CI": "true"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnvironmentFingerprint(map[string]string{"os": "linux", "go": "1.27"}, map[string]string{"CI": "true", "LANG": "C"})
	if err != nil {
		t.Fatal(err)
	}
	if first != second || strings.Contains(first, "linux") || strings.Contains(first, "true") {
		t.Fatalf("environment fingerprint is not canonical or exposed raw values: %q %q", first, second)
	}
	if _, err := EnvironmentFingerprint(nil, map[string]string{"SERVICE_API_TOKEN": "do-not-hash"}); err == nil {
		t.Fatal("secret-like environment key was accepted")
	}
}

func TestGitPathAndHunkValidation(t *testing.T) {
	for _, raw := range [][]byte{[]byte("../outside"), []byte("src\\outside"), {0xff}} {
		if _, err := canonicalGitPath(raw); err == nil {
			t.Errorf("unsafe Git path %q was accepted", raw)
		}
	}
	if got, ok := parseHunkRange("@@ -2,0 +3,0 @@"); !ok || got.Start != 2 || got.End != 2 {
		t.Fatalf("deletion hunk was not represented safely: %+v, %v", got, ok)
	}
	if got, ok := parseHunkRange("@@ -2,1 +3,4 @@ function"); !ok || got.Start != 3 || got.End != 6 {
		t.Fatalf("addition hunk parsed incorrectly: %+v, %v", got, ok)
	}
	if _, err := hashWorkingPath(t.TempDir(), "../escape"); err == nil {
		t.Fatal("hashWorkingPath accepted repository traversal")
	}
}

func initializedRepo(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp(".", ".ivp-git-test-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filepath.Walk(root, func(filename string, info os.FileInfo, walkErr error) error {
			if walkErr != nil || info == nil || info.Mode()&os.ModeSymlink != 0 {
				return nil
			}
			_ = os.Chmod(filename, 0o700)
			return nil
		})
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove temporary Git repository %q: %v", root, err)
		}
	})
	gitTestCommand(t, root, "init", "-q")
	gitTestCommand(t, root, "config", "user.email", "ivp-test@example.invalid")
	gitTestCommand(t, root, "config", "user.name", "IVP tests")
	writeFile(t, root, "src/change.go", "package sample\nfunc Changed() int { return 1 }\n")
	writeFile(t, root, "src/old.txt", "rename me\n")
	writeFile(t, root, "src/gone.txt", "delete me\n")
	gitTestCommand(t, root, "add", ".")
	gitTestCommand(t, root, "commit", "-q", "-m", "initial")
	return root
}

func writeFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitTestCommand(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
}
