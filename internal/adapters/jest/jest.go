package jest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/adapters/native"
)

type Adapter struct{ native.Base }

func New() *Adapter {
	return &Adapter{native.Base{Name: "jest", Binary: "jest", Caps: adapters.Capabilities{SelectableGranularity: "JEST_TEST_FILE", DependencyPrecision: "RELATED_TEST_CANDIDATES", CoveragePrecision: "FILE", DiscoveryReliable: true, ResultFormats: []string{"jest-json", "junit-xml"}, SupportsCoverage: true, SupportsFiltering: true}}}
}
func (a *Adapter) ID() string { return "jest" }
func (a *Adapter) Detect(_ context.Context, root string) (adapters.Detection, error) {
	return native.DetectFiles(root, "jest.config.js", "jest.config.cjs", "jest.config.mjs", "jest.config.ts", "package.json")
}
func (a *Adapter) prefix(root string) (string, []string) {
	if p := native.FindFirst(root, "node_modules/.bin/jest"); p != "" {
		return p, nil
	}
	return "npx", []string{"--no-install", "jest"}
}
func (a *Adapter) run(ctx context.Context, root string, args []string, limit int64) ([]byte, []byte, error) {
	exe, prefix := a.prefix(root)
	return adapters.RunCapture(ctx, root, exe, append(prefix, args...), limit)
}
func (a *Adapter) Version(ctx context.Context, root string) (string, error) {
	out, stderr, e := a.run(ctx, root, []string{"--version"}, 64<<10)
	if e != nil {
		return "", e
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		s = strings.TrimSpace(string(stderr))
	}
	return s, nil
}
func (a *Adapter) Discover(ctx context.Context, req adapters.DiscoverRequest) (adapters.DiscoveryResult, error) {
	if e := a.ValidateConfig(req.Config); e != nil {
		return adapters.DiscoveryResult{Complete: false, Reasons: []string{e.Error()}}, e
	}
	args := append([]string(nil), req.Config.Options...)
	args = append(args, "--listTests", "--json")
	out, _, e := a.run(ctx, req.Root, args, req.MaxOutputBytes)
	if e != nil {
		return adapters.DiscoveryResult{Complete: false, Reasons: []string{"Jest test discovery failed"}}, e
	}
	var paths []string
	if e = json.Unmarshal(out, &paths); e != nil {
		return adapters.DiscoveryResult{Complete: false, Reasons: []string{"Jest --listTests returned malformed JSON"}}, e
	}
	result := adapters.DiscoveryResult{Complete: true}
	seen := map[string]bool{}
	absRoot, e := adapters.ResolveWithin(req.Root, ".")
	if e != nil {
		return adapters.DiscoveryResult{Complete: false, Reasons: []string{"repository root could not be resolved"}}, e
	}
	for _, raw := range paths {
		rel := filepath.ToSlash(raw)
		if filepath.IsAbs(raw) {
			abs, e := adapters.ResolveWithin(absRoot, raw)
			if e != nil {
				result.Complete = false
				result.Reasons = append(result.Reasons, "Jest discovered a path outside the repository")
				continue
			}
			rel, e = filepath.Rel(absRoot, abs)
			if e != nil {
				result.Complete = false
				continue
			}
			rel = filepath.ToSlash(rel)
		}
		if _, e := adapters.ResolveWithin(absRoot, rel); e != nil {
			result.Complete = false
			result.Reasons = append(result.Reasons, "Jest discovered an invalid test path")
			continue
		}
		if seen[rel] {
			continue
		}
		seen[rel] = true
		result.Units = append(result.Units, adapters.TestUnit{Key: rel, DisplayName: rel, Kind: "TEST_FILE", Paths: []string{rel}, SelectableGranularity: "JEST_TEST_FILE", Filterable: true, Timeout: 15 * time.Minute, ReportPaths: []string{".ivp/reports/jest.json"}})
	}
	if len(result.Units) == 0 {
		result.Complete = false
		result.Reasons = append(result.Reasons, "Jest discovered no test files")
	}
	// Jest's dependency resolver can provide change-set-specific related-test
	// candidates without executing tests. Missing/deleted inputs are treated as
	// incomplete because the resolver cannot prove their consumers this run.
	changedInputs := make([]string, 0, len(req.ChangedFiles))
	for _, changed := range req.ChangedFiles {
		if _, err := adapters.ResolveWithin(req.Root, changed); err != nil {
			result.Complete = false
			result.Reasons = append(result.Reasons, "Jest related-test input escaped the repository")
			continue
		}
		info, err := os.Stat(filepath.Join(req.Root, filepath.FromSlash(changed)))
		if err != nil || !info.Mode().IsRegular() {
			result.Complete = false
			result.Reasons = append(result.Reasons, "Jest related-test input is unavailable")
			continue
		}
		changedInputs = append(changedInputs, filepath.ToSlash(changed))
	}
	if len(changedInputs) > 0 {
		relatedArgs := append([]string(nil), req.Config.Options...)
		relatedArgs = append(relatedArgs, "--listTests", "--json", "--findRelatedTests")
		relatedArgs = append(relatedArgs, changedInputs...)
		relatedOutput, _, relatedErr := a.run(ctx, req.Root, relatedArgs, req.MaxOutputBytes)
		if relatedErr != nil {
			result.Complete = false
			result.Reasons = append(result.Reasons, "Jest related-test candidate discovery failed")
		} else {
			var relatedPaths []string
			if json.Unmarshal(relatedOutput, &relatedPaths) != nil {
				result.Complete = false
				result.Reasons = append(result.Reasons, "Jest related-test candidate output was malformed")
			} else {
				relatedSet := map[string]bool{}
				for _, raw := range relatedPaths {
					rel := filepath.ToSlash(raw)
					if filepath.IsAbs(raw) {
						abs, pathErr := adapters.ResolveWithin(absRoot, raw)
						if pathErr != nil {
							result.Complete = false
							continue
						}
						relPath, pathErr := filepath.Rel(absRoot, abs)
						if pathErr != nil {
							result.Complete = false
							continue
						}
						rel = filepath.ToSlash(relPath)
					}
					if !seen[rel] {
						result.Complete = false
						result.Reasons = append(result.Reasons, "Jest related-test candidate was absent from discovered tests")
						continue
					}
					relatedSet[rel] = true
				}
			for rel := range relatedSet {
				result.AffectedTargets = append(result.AffectedTargets, rel)
			}
			sort.Strings(result.AffectedTargets)
			}
		}
	}
	result.Version, _ = a.Version(ctx, req.Root)
	return result, nil
}
func (a *Adapter) Materialize(_ context.Context, req adapters.MaterializeRequest) (adapters.ExecutionCommand, error) {
	if e := a.ValidateConfig(req.Config); e != nil {
		return adapters.ExecutionCommand{}, e
	}
	if req.FullSuite {
		if len(req.Config.FullSuiteArgv) == 0 {
			return adapters.ExecutionCommand{}, errors.New("Jest full-suite command must be configured")
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
	args = append(args, "--runTestsByPath")
	if len(req.Config.CoveragePaths) > 0 {
		args = append(args, "--coverage", "--coverageReporters=lcov")
	}
	for _, key := range req.TargetKeys {
		if filepath.IsAbs(key) || strings.ContainsAny(key, "\r\n\x00") || strings.Contains(key, "\\") {
			return adapters.ExecutionCommand{}, fmt.Errorf("invalid Jest test path %q", key)
		}
		if _, e := adapters.ResolveWithin(req.Root, key); e != nil {
			return adapters.ExecutionCommand{}, e
		}
		args = append(args, key)
	}
	key := strings.Join(req.TargetKeys, ",")
	report := filepath.ToSlash(filepath.Join(".ivp", "reports", "jest-"+shortHash(key)+".json"))
	args = append(args, "--json", "--outputFile="+report)
	exe, prefix := a.prefix(req.Root)
	args = append(prefix, args...)
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
	return adapters.ExecutionCommand{Executable: exe, Args: args, WorkingDirectory: cwd, Environment: map[string]string{}, Timeout: timeout, RedactEnv: append([]string(nil), req.Config.RedactEnv...), MaxOutputBytes: maxOut, TargetKey: key, ReportPaths: []string{report}, CoveragePaths: append([]string(nil), req.Config.CoveragePaths...), Filtered: true}, nil
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
	return fmt.Sprintf("%x", sum)
}

var _ adapters.Adapter = (*Adapter)(nil)
