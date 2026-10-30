// Package native contains conservative helpers shared by native build adapters.
package native

import (
	"context"
	"errors"
	"os"
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
func (b Base) ValidateConfig(c adapters.AdapterConfig) error {
	for _, arg := range c.Options {
		if strings.ContainsRune(arg, 0) {
			return errors.New("adapter option contains NUL")
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
