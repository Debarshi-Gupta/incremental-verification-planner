// Package native contains conservative helpers shared by native build adapters.
package native

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ivp/ivp/internal/adapters"
)

type Base struct {
	Name   string
	Binary string
	Caps   adapters.Capabilities
}

func (b Base) Capabilities() adapters.Capabilities { return b.Caps }

// ValidateConfig rejects unsafe adapter configuration before any command is
// materialized. Besides NUL bytes, it validates the configured report and
// coverage paths (they must be repository-relative) and the full-suite argv
// (it must name an executable and carry no NUL bytes).
func (b Base) ValidateConfig(c adapters.AdapterConfig) error {
	for _, arg := range c.Options {
		if strings.ContainsRune(arg, 0) {
			return errors.New("adapter option contains NUL")
		}
	}
	if err := validateRepositoryPaths("coverage path", c.CoveragePaths); err != nil {
		return err
	}
	for _, target := range c.Targets {
		if err := validateRepositoryPaths(fmt.Sprintf("target %q report path", target.ID), target.ReportPaths); err != nil {
			return err
		}
		if err := validateRepositoryPaths(fmt.Sprintf("target %q coverage path", target.ID), target.CoveragePaths); err != nil {
			return err
		}
		for _, arg := range target.Argv {
			if strings.ContainsRune(arg, 0) {
				return fmt.Errorf("target %q argument contains NUL", target.ID)
			}
		}
	}
	return validateFullSuiteArgv(c.FullSuiteArgv)
}

// validateRepositoryPaths rejects paths that are not repository-relative
// globs: each path must be non-empty, free of NUL bytes, slash-separated,
// not absolute, and unable to escape the workspace. The rules mirror the
// repository-glob validation of the configuration layer, so a path accepted
// by config.Validate is accepted here too.
func validateRepositoryPaths(kind string, paths []string) error {
	for _, p := range paths {
		if p == "" {
			return fmt.Errorf("%s is empty", kind)
		}
		if strings.ContainsRune(p, 0) {
			return fmt.Errorf("%s %q contains NUL", kind, p)
		}
		if strings.Contains(p, `\`) {
			return fmt.Errorf("%s %q must use slash separators", kind, p)
		}
		if strings.HasPrefix(p, "/") || filepath.IsAbs(p) {
			return fmt.Errorf("%s %q must be repository-relative", kind, p)
		}
		if path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") {
			return fmt.Errorf("%s %q escapes the repository", kind, p)
		}
	}
	return nil
}

// validateFullSuiteArgv rejects a malformed full-suite command: the argv must
// name a non-empty executable and no argument may contain NUL bytes. The
// executable itself is resolved against the workspace when the command is
// materialized (ConfiguredCommand).
func validateFullSuiteArgv(argv []string) error {
	if len(argv) == 0 {
		return nil
	}
	if strings.TrimSpace(argv[0]) == "" {
		return errors.New("full-suite argv has an empty executable")
	}
	for _, arg := range argv {
		if strings.ContainsRune(arg, 0) {
			return errors.New("full-suite argv contains NUL")
		}
	}
	return nil
}
func (b Base) ParseResults(ctx context.Context, req adapters.ParseRequest) (adapters.ParsedResults, error) {
	return adapters.ParseReports(ctx, req)
}
func (b Base) ParseCoverage(_ context.Context, req adapters.CoverageRequest) (adapters.CoverageResult, error) {
	if len(req.ReportPaths) == 0 {
		return adapters.CoverageResult{Format: req.Format, Complete: false, Reasons: []string{"no coverage reports configured"}}, errors.New("no coverage reports configured")
	}
	return adapters.ParseCoverageReports(req.Root, req.ReportPaths, req.Format, req.MaxReportBytes)
}
func (b Base) Version(ctx context.Context, root string) (string, error) {
	stdout, stderr, err := adapters.RunCapture(ctx, root, b.Binary, []string{"--version"}, 64<<10)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(stdout))
	if line == "" {
		line = strings.TrimSpace(string(stderr))
	}
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return line, nil
}
func DetectFiles(root string, markers ...string) (adapters.Detection, error) {
	rootPath, err := adapters.ResolveWithin(root, ".")
	if err != nil {
		return adapters.Detection{}, err
	}
	var found []string
	for _, marker := range markers {
		p, e := adapters.ResolveWithin(rootPath, marker)
		if e == nil {
			if _, e = os.Stat(p); e == nil {
				found = append(found, filepath.ToSlash(marker))
			}
		}
	}
	return adapters.Detection{Detected: len(found) > 0, Root: rootPath, Evidence: found}, nil
}
func FindFirst(root string, names ...string) string {
	for _, name := range names {
		p, e := adapters.ResolveWithin(root, name)
		if e == nil {
			if st, e := os.Stat(p); e == nil && !st.IsDir() {
				return p
			}
		}
	}
	return ""
}
func Relative(root, abs string) string {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}

func ConfiguredCommand(root string, argv []string) (string, []string, error) {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return "", nil, adapters.ErrUnsafeCommand
	}
	for _, arg := range argv {
		if strings.ContainsRune(arg, 0) {
			return "", nil, adapters.ErrUnsafeCommand
		}
	}
	executable := argv[0]
	if filepath.IsAbs(executable) || strings.ContainsAny(executable, "/\\") {
		p, err := adapters.ResolveWithin(root, executable)
		if err != nil {
			return "", nil, err
		}
		st, err := os.Stat(p)
		if err != nil {
			return "", nil, err
		}
		if st.IsDir() {
			return "", nil, errors.New("configured executable is a directory")
		}
		executable = p
	}
	return executable, append([]string(nil), argv[1:]...), nil
}
