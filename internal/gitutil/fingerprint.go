package gitutil

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ivp/ivp/internal/domain"
)

func TreeFingerprint(ctx context.Context, root string) (string, error) {
	repoRoot, err := RepositoryRoot(ctx, root)
	if err != nil {
		return "", err
	}
	out, err := git(ctx, repoRoot, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", fmt.Errorf("list workspace files: %w", err)
	}
	set := make(map[string]bool)
	for _, item := range strings.Split(out, "\x00") {
		if item == "" {
			continue
		}
		if !utf8.ValidString(item) {
			return "", errors.New("workspace path is not valid UTF-8; selective planning is unsafe")
		}
		if _, err := domain.NormalizeRepoPath(item); err != nil {
			return "", fmt.Errorf("workspace path is unsafe: %w", err)
		}
		set[item] = true
	}
	paths := make([]string, 0, len(set))
	for p := range set {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		info, err := safeLstat(repoRoot, p)
		if errors.Is(err, os.ErrNotExist) {
			writeRecord(h, p, []byte("MISSING"))
			continue
		}
		if err != nil {
			return "", fmt.Errorf("inspect workspace path %q: %w", p, err)
		}
		mode := info.Mode().String()
		fileHash, err := hashWorkingPath(repoRoot, p)
		if errors.Is(err, os.ErrNotExist) {
			writeRecord(h, p, []byte(mode+"\x00MISSING"))
			continue
		}
		if err != nil {
			return "", fmt.Errorf("hash workspace path %q: %w", p, err)
		}
		if info.IsDir() {
			fileHash = "DIRECTORY_OR_SUBMODULE"
		}
		writeRecord(h, p, []byte(mode+"\x00"+fileHash))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func WorktreeFingerprint(ctx context.Context, root string) (string, error) {
	repoRoot, err := RepositoryRoot(ctx, root)
	if err != nil {
		return "", err
	}
	tree, err := TreeFingerprint(ctx, repoRoot)
	if err != nil {
		return "", err
	}
	head, err := git(ctx, repoRoot, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("fingerprint HEAD: %w", err)
	}
	index, err := git(ctx, repoRoot, "ls-files", "--stage", "-z")
	if err != nil {
		return "", fmt.Errorf("fingerprint index: %w", err)
	}
	indexDigest := sha256Hex([]byte(index))
	submodules, err := SubmoduleDigest(ctx, repoRoot)
	if err != nil {
		return "", err
	}
	return digestStrings(strings.TrimSpace(head), tree, indexDigest, submodules)
}

func SubmoduleDigest(ctx context.Context, root string) (string, error) {
	repoRoot, err := RepositoryRoot(ctx, root)
	if err != nil {
		return "", err
	}
	return submoduleDigestAt(ctx, repoRoot, make(map[string]bool), 0)
}

func submoduleDigestAt(ctx context.Context, repoRoot string, visited map[string]bool, depth int) (string, error) {
	if depth > 32 {
		return "", errors.New("submodule nesting exceeds the safe maximum depth")
	}
	key := strings.ToLower(filepath.Clean(repoRoot))
	if visited[key] {
		return "", errors.New("submodule repository cycle detected")
	}
	visited[key] = true
	defer delete(visited, key)
	index, err := git(ctx, repoRoot, "ls-files", "--stage", "-z")
	if err != nil {
		return "", fmt.Errorf("read submodule index: %w", err)
	}
	type gitlink struct {
		Path     string `json:"path"`
		OID      string `json:"oid"`
		Stage    string `json:"stage"`
		State    string `json:"state"`
		Tree     string `json:"tree,omitempty"`
		Worktree string `json:"worktree,omitempty"`
		Children string `json:"children,omitempty"`
	}
	var links []gitlink
	for _, record := range strings.Split(index, "\x00") {
		if record == "" {
			continue
		}
		separator := strings.IndexByte(record, '\t')
		if separator < 0 {
			return "", errors.New("malformed NUL-separated Git index entry")
		}
		fields := strings.Fields(record[:separator])
		if len(fields) != 3 || fields[0] != "160000" {
			continue
		}
		relative := record[separator+1:]
		if _, err := domain.NormalizeRepoPath(relative); err != nil {
			return "", fmt.Errorf("unsafe submodule path: %w", err)
		}
		link := gitlink{Path: relative, OID: fields[1], Stage: fields[2]}
		full := filepath.Join(repoRoot, filepath.FromSlash(relative))
		info, statErr := safeLstat(repoRoot, relative)
		if errors.Is(statErr, os.ErrNotExist) {
			link.State = "UNINITIALIZED"
		} else if statErr != nil {
			return "", fmt.Errorf("inspect submodule %q: %w", relative, statErr)
		} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			link.State = "INVALID_PATH"
		} else {
			head, headErr := git(ctx, full, "rev-parse", "--verify", "HEAD^{commit}")
			if headErr != nil {
				if _, markerErr := os.Lstat(filepath.Join(full, ".git")); errors.Is(markerErr, os.ErrNotExist) {
					link.State = "UNINITIALIZED"
				} else {
					return "", fmt.Errorf("read checked-out submodule %q: %w", relative, headErr)
				}
			} else {
				link.State = "INITIALIZED"
				link.OID += ":HEAD=" + strings.TrimSpace(head)
				link.Tree, err = TreeFingerprint(ctx, full)
				if err != nil {
					return "", fmt.Errorf("fingerprint submodule %q: %w", relative, err)
				}
				childIndex, indexErr := git(ctx, full, "ls-files", "--stage", "-z")
				if indexErr != nil {
					return "", fmt.Errorf("fingerprint submodule index %q: %w", relative, indexErr)
				}
				link.Worktree = sha256Hex([]byte(childIndex))
				link.Children, err = submoduleDigestAt(ctx, filepath.Clean(full), visited, depth+1)
				if err != nil {
					return "", err
				}
			}
		}
		links = append(links, link)
	}
	sort.Slice(links, func(i, j int) bool {
		if links[i].Path != links[j].Path {
			return links[i].Path < links[j].Path
		}
		return links[i].Stage < links[j].Stage
	})
	b, err := json.Marshal(links)
	if err != nil {
		return "", err
	}
	return sha256Hex(b), nil
}

// EnvironmentFingerprint hashes only caller-approved environment/toolchain
// metadata. It never returns raw values and refuses names that commonly carry
// credentials or secrets.
func EnvironmentFingerprint(toolchain, allowlistedEnv map[string]string) (string, error) {
	if err := validateFingerprintKeys(toolchain); err != nil {
		return "", fmt.Errorf("toolchain fingerprint: %w", err)
	}
	if err := validateFingerprintKeys(allowlistedEnv); err != nil {
		return "", fmt.Errorf("environment fingerprint: %w", err)
	}
	canonical := struct {
		Toolchain map[string]string `json:"toolchain"`
		Env       map[string]string `json:"env"`
	}{copyMap(toolchain), copyMap(allowlistedEnv)}
	b, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("serialize environment fingerprint: %w", err)
	}
	return sha256Hex(b), nil
}

func safeLstat(root, repoPath string) (os.FileInfo, error) {
	if _, err := domain.NormalizeRepoPath(repoPath); err != nil {
		return nil, err
	}
	parts := strings.Split(repoPath, "/")
	current := root
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if i < len(parts)-1 && info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("path traverses a symlink at %q", strings.Join(parts[:i+1], "/"))
		}
		if i < len(parts)-1 && !info.IsDir() {
			return nil, fmt.Errorf("non-directory path component %q", strings.Join(parts[:i+1], "/"))
		}
		if i == len(parts)-1 {
			return info, nil
		}
	}
	return nil, os.ErrNotExist
}

func validateFingerprintKeys(values map[string]string) error {
	for name := range values {
		lower := strings.ToLower(name)
		for _, sensitive := range []string{"secret", "token", "password", "credential", "authorization", "private_key", "apikey", "api_key"} {
			if strings.Contains(lower, sensitive) {
				return fmt.Errorf("sensitive key %q cannot be fingerprinted", name)
			}
		}
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\r\n\x00") {
			return errors.New("fingerprint key is empty or malformed")
		}
	}
	return nil
}

func digestStrings(values ...string) (string, error) {
	b, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return sha256Hex(b), nil
}

func writeRecord(dst hash.Hash, key string, value []byte) {
	_, _ = io.WriteString(dst, strconv.Itoa(len(key)))
	_, _ = io.WriteString(dst, ":")
	_, _ = io.WriteString(dst, key)
	_, _ = io.WriteString(dst, ":")
	_, _ = dst.Write(value)
	_, _ = io.WriteString(dst, "\n")
}

func copyMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
