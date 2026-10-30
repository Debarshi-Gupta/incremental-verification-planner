package gitutil

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ivp/ivp/internal/domain"
)

type ChangeSet struct {
	RepositoryRoot  string               `json:"repository_root"`
	BaseSHA         string               `json:"base_sha"`
	HeadSHA         string               `json:"head_sha"`
	Files           []domain.ChangedFile `json:"files"`
	TreeHash        string               `json:"tree_hash"`
	WorktreeHash    string               `json:"worktree_hash"`
	SubmoduleDigest string               `json:"submodule_digest"`
}

type nameStatus struct {
	status  domain.ChangeStatus
	oldPath string
	path    string
}

func DetectChanges(ctx context.Context, root, baseRef string) (ChangeSet, error) {
	repoRoot, err := RepositoryRoot(ctx, root)
	if err != nil {
		return ChangeSet{}, err
	}
	if strings.TrimSpace(baseRef) == "" || strings.HasPrefix(baseRef, "-") || strings.ContainsAny(baseRef, "\x00\r\n") {
		return ChangeSet{}, errors.New("base ref is empty or unsafe")
	}
	base, err := git(ctx, repoRoot, "rev-parse", "--verify", "--end-of-options", baseRef+"^{commit}")
	if err != nil {
		return ChangeSet{}, fmt.Errorf("resolve base ref: %w", err)
	}
	head, err := git(ctx, repoRoot, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return ChangeSet{}, fmt.Errorf("resolve HEAD: %w", err)
	}
	statuses, err := diffNameStatus(ctx, repoRoot, strings.TrimSpace(base))
	if err != nil {
		return ChangeSet{}, err
	}
	untracked, err := untrackedFiles(ctx, repoRoot)
	if err != nil {
		return ChangeSet{}, err
	}
	for _, p := range untracked {
		if _, exists := statuses[p]; !exists {
			statuses[p] = nameStatus{status: domain.ChangeAdded, path: p}
		}
	}
	paths := make([]string, 0, len(statuses))
	for p := range statuses {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	files := make([]domain.ChangedFile, 0, len(statuses))
	for _, key := range paths {
		entry := statuses[key]
		oldHash := ""
		oldPath := entry.oldPath
		if oldPath == "" && entry.status != domain.ChangeAdded {
			oldPath = entry.path
		}
		if oldPath != "" {
			oldHash, err = objectHashAt(ctx, repoRoot, strings.TrimSpace(base), oldPath)
			if err != nil {
				return ChangeSet{}, fmt.Errorf("hash base path %q: %w", oldPath, err)
			}
		}
		newHash, err := hashWorkingPath(repoRoot, entry.path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return ChangeSet{}, fmt.Errorf("hash changed path %q: %w", entry.path, err)
		}
		hunks, err := diffHunks(ctx, repoRoot, strings.TrimSpace(base), oldPath, entry.path)
		if err != nil {
			return ChangeSet{}, fmt.Errorf("read diff hunks for %q: %w", entry.path, err)
		}
		changed := domain.ChangedFile{Path: entry.path, OldPath: entry.oldPath, Status: entry.status, OldHash: oldHash, NewHash: newHash, DiffHunks: hunks}
		if entry.status == domain.ChangeDeleted {
			changed.Path = entry.path
			changed.OldPath = ""
			changed.OldHash = oldHash
		}
		if err := domain.ValidateChangedFile(changed); err != nil {
			return ChangeSet{}, fmt.Errorf("invalid git path/status: %w", err)
		}
		files = append(files, changed)
	}
	treeHash, err := TreeFingerprint(ctx, repoRoot)
	if err != nil {
		return ChangeSet{}, err
	}
	worktreeHash, err := WorktreeFingerprint(ctx, repoRoot)
	if err != nil {
		return ChangeSet{}, err
	}
	submodules, err := SubmoduleDigest(ctx, repoRoot)
	if err != nil {
		return ChangeSet{}, err
	}
	return ChangeSet{RepositoryRoot: repoRoot, BaseSHA: strings.TrimSpace(base), HeadSHA: strings.TrimSpace(head), Files: files,
		TreeHash: treeHash, WorktreeHash: worktreeHash, SubmoduleDigest: submodules}, nil
}

func RepositoryRoot(ctx context.Context, root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository path: %w", err)
	}
	resolved, err := git(ctx, absolute, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("find repository root: %w", err)
	}
	return filepath.Clean(strings.TrimSpace(resolved)), nil
}

func diffNameStatus(ctx context.Context, root, base string) (map[string]nameStatus, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--name-status", "-z", "--find-renames", "--find-copies=50%", base, "--")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff changed paths: %w", commandError(err, cmd, out))
	}
	parts := bytes.Split(out, []byte{0})
	result := make(map[string]nameStatus)
	for i := 0; i < len(parts) && len(parts[i]) > 0; {
		status := string(parts[i])
		i++
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			if i+1 >= len(parts) {
				return nil, errors.New("malformed NUL-separated rename/copy diff output")
			}
			oldPath, err := canonicalGitPath(parts[i])
			if err != nil {
				return nil, err
			}
			newPath, err := canonicalGitPath(parts[i+1])
			if err != nil {
				return nil, err
			}
			i += 2
			change := domain.ChangeRenamed
			if strings.HasPrefix(status, "C") {
				change = domain.ChangeCopied
			}
			result[newPath] = nameStatus{status: change, oldPath: oldPath, path: newPath}
			continue
		}
		if i >= len(parts) {
			return nil, errors.New("malformed NUL-separated name-status output")
		}
		p, err := canonicalGitPath(parts[i])
		if err != nil {
			return nil, err
		}
		i++
		change := domain.ChangeModified
		switch status {
		case "A":
			change = domain.ChangeAdded
		case "D":
			change = domain.ChangeDeleted
		case "T":
			change = domain.ChangeTypeChanged
		case "M":
			change = domain.ChangeModified
		default:
			return nil, fmt.Errorf("unsupported git diff status %q", status)
		}
		result[p] = nameStatus{status: change, path: p}
	}
	return result, nil
}

func untrackedFiles(ctx context.Context, root string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=none")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git status: %w", commandError(err, cmd, out))
	}
	parts := bytes.Split(out, []byte{0})
	var paths []string
	for i := 0; i < len(parts); i++ {
		if len(parts[i]) == 0 {
			continue
		}
		line := string(parts[i])
		if strings.HasPrefix(line, "? ") {
			p, err := canonicalGitPath([]byte(strings.TrimPrefix(line, "? ")))
			if err != nil {
				return nil, err
			}
			paths = append(paths, p)
			continue
		}
		if strings.HasPrefix(line, "2 ") {
			// Porcelain v2 rename/copy records include the original path in the
			// following NUL field. It is not untracked, but must be consumed.
			i++
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func diffHunks(ctx context.Context, root, base, oldPath, newPath string) ([]domain.LineRange, error) {
	paths := make([]string, 0, 2)
	if oldPath != "" {
		paths = append(paths, literalPathspec(oldPath))
	}
	if newPath != "" && newPath != oldPath {
		paths = append(paths, literalPathspec(newPath))
	}
	args := []string{"diff", "--no-ext-diff", "--no-color", "--unified=0", "--find-renames", base, "--"}
	args = append(args, paths...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	reader := bufio.NewReaderSize(pipe, 64*1024)
	var ranges []domain.LineRange
	for {
		line, readErr := reader.ReadString('\n')
		if strings.HasPrefix(line, "@@ ") {
			r, ok := parseHunkRange(line)
			if ok {
				ranges = append(ranges, r)
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return nil, fmt.Errorf("read git diff: %w", readErr)
			}
			break
		}
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("git diff hunks: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return ranges, nil
}

func parseHunkRange(line string) (domain.LineRange, bool) {
	// Unified diff: @@ -oldStart[,oldCount] +newStart[,newCount] @@.
	close := strings.Index(line, " @@")
	if close < 0 {
		return domain.LineRange{}, false
	}
	parts := strings.Fields(line[3:close])
	if len(parts) < 2 || !strings.HasPrefix(parts[1], "+") {
		return domain.LineRange{}, false
	}
	start, count, ok := parseHunkSide(strings.TrimPrefix(parts[1], "+"))
	if !ok {
		return domain.LineRange{}, false
	}
	if count == 0 {
		if start > 1 {
			start-- // represent deletion at the adjacent retained line for path coverage
		} else {
			start = 1
		}
		count = 1
	}
	return domain.LineRange{Start: start, End: start + count - 1}, true
}

func parseHunkSide(value string) (int, int, bool) {
	parts := strings.SplitN(value, ",", 2)
	start, err := strconv.Atoi(parts[0])
	if err != nil || start < 0 {
		return 0, 0, false
	}
	count := 1
	if len(parts) == 2 {
		count, err = strconv.Atoi(parts[1])
		if err != nil || count < 0 {
			return 0, 0, false
		}
	}
	return start, count, true
}

func objectHashAt(ctx context.Context, root, commit, repoPath string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-tree", "-r", "-z", commit, "--", literalPathspec(repoPath))
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read base tree hash: %w", commandError(err, cmd, out))
	}
	for _, record := range bytes.Split(out, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		tab := bytes.IndexByte(record, '\t')
		if tab < 0 {
			continue
		}
		fields := strings.Fields(string(record[:tab]))
		if len(fields) == 3 && (fields[1] == "blob" || fields[1] == "commit") {
			return fields[2], nil
		}
	}
	return "", nil
}

func hashWorkingPath(root, repoPath string) (string, error) {
	if _, err := domain.NormalizeRepoPath(repoPath); err != nil {
		return "", err
	}
	full := filepath.Join(root, filepath.FromSlash(repoPath))
	info, err := safeLstat(root, repoPath)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(full)
		if err != nil {
			return "", err
		}
		return sha256Hex([]byte("symlink\x00" + filepath.ToSlash(target))), nil
	}
	if !info.Mode().IsRegular() {
		return "", nil
	}
	file, err := os.Open(full)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func canonicalGitPath(raw []byte) (string, error) {
	if !utf8.Valid(raw) {
		return "", errors.New("git path is not valid UTF-8; selective planning is unsafe")
	}
	p := string(raw)
	if _, err := domain.NormalizeRepoPath(p); err != nil {
		return "", err
	}
	return p, nil
}

func literalPathspec(p string) string { return ":(literal)" + p }

func git(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "", commandError(err, cmd, out)
	}
	return string(out), nil
}

func commandError(err error, cmd *exec.Cmd, stdout []byte) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		message := strings.TrimSpace(string(exit.Stderr))
		if message == "" {
			message = strings.TrimSpace(string(stdout))
		}
		if len(message) > 1024 {
			message = message[:1024]
		}
		return fmt.Errorf("git %s failed: %w: %s", strings.Join(cmd.Args[1:], " "), err, message)
	}
	return err
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
