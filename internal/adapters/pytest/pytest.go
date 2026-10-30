package pytest

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/adapters/native"
)

type Adapter struct{ native.Base }

func New() *Adapter {
	return &Adapter{native.Base{Name: "pytest", Binary: "pytest", Caps: adapters.Capabilities{SelectableGranularity: "PYTEST_NODEID", DependencyPrecision: "TEST_FILE_AND_CONFIG", CoveragePrecision: "FILE", DiscoveryReliable: true, ResultFormats: []string{"junit-xml"}, SupportsCoverage: true, SupportsFiltering: true}}}
}
func (a *Adapter) ID() string { return "pytest" }
func (a *Adapter) Detect(_ context.Context, root string) (adapters.Detection, error) {
	return native.DetectFiles(root, "pytest.ini", "pyproject.toml", "setup.cfg", "tox.ini")
}
func (a *Adapter) Version(ctx context.Context, root string) (string, error) {
	out, _, e := adapters.RunCapture(ctx, root, a.executable(), []string{"--version"}, 64<<10)
	if e != nil {
		return "", e
	}
	return strings.TrimSpace(string(out)), nil
}
func (a *Adapter) executable() string { return "pytest" }

var nodeIDLine = regexp.MustCompile(`^[^\s:][^\r\n]*\.py(::[^\r\n]+)?$`)

func parseNodeIDs(root string, output []byte) adapters.DiscoveryResult {
	result := adapters.DiscoveryResult{Complete: true}
	seen := map[string]bool{}
	for _, raw := range strings.Split(string(output), "\n") {
		line := strings.TrimSpace(raw)
		line = strings.TrimPrefix(line, "<Function ")
		line = strings.TrimSuffix(line, ">")
		if !nodeIDLine.MatchString(line) {
			continue
		}
		file := strings.SplitN(line, "::", 2)[0]
		if _, err := adapters.ResolveWithin(root, file); err != nil {
			result.Complete = false
			result.Reasons = append(result.Reasons, "pytest collected a path outside the repository")
			continue
		}
		if seen[line] {
			continue
		}
		seen[line] = true
		result.Units = append(result.Units, adapters.TestUnit{Key: filepath.ToSlash(line), DisplayName: line, Kind: "TEST_CASE", Module: filepath.ToSlash(filepath.Dir(file)), Paths: []string{filepath.ToSlash(file)}, SelectableGranularity: "PYTEST_NODEID", Filterable: true, Timeout: 10 * time.Minute, ReportPaths: []string{".ivp/reports/pytest.xml"}})
	}
	if len(result.Units) == 0 {
		result.Complete = false
		result.Reasons = append(result.Reasons, "pytest collection returned no test node IDs")
	}
	return result
}

func (a *Adapter) Discover(ctx context.Context, req adapters.DiscoverRequest) (adapters.DiscoveryResult, error) {
	if e := a.ValidateConfig(req.Config); e != nil {
		return adapters.DiscoveryResult{Complete: false, Reasons: []string{e.Error()}}, e
	}
	args := append([]string(nil), req.Config.Options...)
	args = append(args, "--collect-only", "-q")
	out, _, e := adapters.RunCapture(ctx, req.Root, a.executable(), args, req.MaxOutputBytes)
	if e != nil {
		return adapters.DiscoveryResult{Complete: false, Reasons: []string{"pytest collection failed"}}, e
	}
	result := parseNodeIDs(req.Root, out)
	result.Version, _ = a.Version(ctx, req.Root)
	return result, nil
}
func (a *Adapter) Materialize(_ context.Context, req adapters.MaterializeRequest) (adapters.ExecutionCommand, error) {
	if e := a.ValidateConfig(req.Config); e != nil {
		return adapters.ExecutionCommand{}, e
	}
	if req.FullSuite {
		if len(req.Config.FullSuiteArgv) == 0 {
			return adapters.ExecutionCommand{}, errors.New("pytest full-suite command must be configured")
		}
		return configuredFull(req)
	}
	if e := adapters.ValidateTargetKeys(req.TargetKeys, req.KnownTargets, nil); e != nil {
		return adapters.ExecutionCommand{}, e
	}
	if len(req.TargetKeys) == 0 {
		return adapters.ExecutionCommand{}, adapters.ErrUnknownTarget
	}
	args := append([]string(nil), req.Config.Options...)
	key := strings.Join(req.TargetKeys, ",")
	report := filepath.ToSlash(filepath.Join(".ivp", "reports", "pytest-"+shortHash(key)+".xml"))
	args = append(args, "-q", "--junitxml="+report)
	for _, key := range req.TargetKeys {
		if strings.ContainsAny(key, "\r\n\x00") || strings.HasPrefix(key, "/") || strings.Contains(key, "\\") {
			return adapters.ExecutionCommand{}, fmt.Errorf("invalid pytest node ID %q", key)
		}
		file := strings.SplitN(key, "::", 2)[0]
		if _, e := adapters.ResolveWithin(req.Root, file); e != nil {
			return adapters.ExecutionCommand{}, e
		}
		args = append(args, key)
	}
	cwd, e := adapters.ResolveWithin(req.Root, ".")
	if e != nil {
		return adapters.ExecutionCommand{}, e
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	maxOut := req.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = 1 << 20
	}
	return adapters.ExecutionCommand{Executable: a.executable(), Args: args, WorkingDirectory: cwd, Environment: map[string]string{}, Timeout: timeout, RedactEnv: append([]string(nil), req.Config.RedactEnv...), MaxOutputBytes: maxOut, TargetKey: key, ReportPaths: []string{report}, Filtered: true}, nil
}
func configuredFull(req adapters.MaterializeRequest) (adapters.ExecutionCommand, error) {
	exe, args, e := native.ConfiguredCommand(req.Root, req.Config.FullSuiteArgv)
	if e != nil {
		return adapters.ExecutionCommand{}, e
	}
	cwd, e := adapters.ResolveWithin(req.Root, ".")
	if e != nil {
		return adapters.ExecutionCommand{}, e
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	maxOut := req.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = 1 << 20
	}
	return adapters.ExecutionCommand{Executable: exe, Args: args, WorkingDirectory: cwd, Environment: map[string]string{}, Timeout: timeout, RedactEnv: append([]string(nil), req.Config.RedactEnv...), MaxOutputBytes: maxOut, FullSuite: true}, nil
}
func (a *Adapter) ValidateConfig(c adapters.AdapterConfig) error { return a.Base.ValidateConfig(c) }
func (a *Adapter) ParseResults(ctx context.Context, r adapters.ParseRequest) (adapters.ParsedResults, error) {
	return a.Base.ParseResults(ctx, r)
}
func (a *Adapter) ParseCoverage(ctx context.Context, r adapters.CoverageRequest) (adapters.CoverageResult, error) {
	return a.Base.ParseCoverage(ctx, r)
}
func shortHash(value string) string {
	var sum uint64 = 1469598103934665603
	for i := 0; i < len(value); i++ {
		sum ^= uint64(value[i])
		sum *= 1099511628211
	}
	return strconv.FormatUint(sum, 16)
}

var _ adapters.Adapter = (*Adapter)(nil)
