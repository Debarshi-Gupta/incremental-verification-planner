package security

import (
	"errors"
	"path"
	"strings"
)

// NormalizeRepoPath accepts only canonical repository-relative slash paths.
// It rejects absolute paths, drive paths, NULs, and traversal segments.
func NormalizeRepoPath(value string) (string, error) {
	if value == "" || strings.ContainsRune(value, 0) || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") || strings.Contains(value, ":") {
		return "", errors.New("path must be a repository-relative slash path")
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != value {
		return "", errors.New("path is not canonical or escapes the repository")
	}
	return clean, nil
}
