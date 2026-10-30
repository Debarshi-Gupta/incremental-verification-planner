package bazel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	return &Adapter{native.Base{Name: "bazel", Binary: "bazel", Caps: adapters.Capabilities{SelectableGranularity: "BAZEL_TEST_TARGET", DependencyPrecision: "CONFIGURED_TARGET_GRAPH", CoveragePrecision: "TARGET_OR_FILE", DiscoveryReliable: true, ResultFormats: []string{"bazel-bep-json"}, SupportsCoverage: true, SupportsFiltering: false}}}
}
func (a *Adapter) ID() string { return "bazel" }
func (a *Adapter) tool(root string) string {
	if native.FindFirst(root, ".bazelversion") != "" {
		return "bazelisk"
	}
	return "bazel"
}
func (a *Adapter) Detect(_ context.Context, root string) (adapters.Detection, error) {
	return native.DetectFiles(root, "MODULE.bazel", "WORKSPACE.bazel", "WORKSPACE", "BUILD.bazel", "BUILD")
}
func (a *Adapter) Version(ctx context.Context, root string) (string, error) {
	out, _, e := adapters.RunCapture(ctx, root, a.tool(root), []string{"--version"}, 64<<10)
	if e != nil {
		return "", e
	}
	return strings.TrimSpace(string(out)), nil
}

var labelPattern = regexp.MustCompile(`^(//[A-Za-z0-9._/+@=-]*:[A-Za-z0-9._/+@=-]+|//[A-Za-z0-9._/+@=-]+|@[A-Za-z0-9._+-]+//[A-Za-z0-9._/+@=-]*:[A-Za-z0-9._/+@=-]+)$`)

func (a *Adapter) Discover(ctx context.Context, req adapters.DiscoverRequest) (adapters.DiscoveryResult, error) {
	if e := a.ValidateConfig(req.Config); e != nil {
		return adapters.DiscoveryResult{Complete: false, Reasons: []string{e.Error()}}, e
	}
	expr := `kind(".*_test rule", //...)`
	out, _, e := adapters.RunCapture(ctx, req.Root, a.tool(req.Root), []string{"query", expr, "--output=label"}, req.MaxOutputBytes)
	if e != nil {
		return adapters.DiscoveryResult{Complete: false, Reasons: []string{"Bazel query failed; target graph is incomplete"}}, e
	}
	result := adapters.DiscoveryResult{Complete: true}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		label := strings.TrimSpace(line)
		if label == "" {
			continue
		}
		if !validLabel(label) {
			result.Complete = false
			result.Reasons = append(result.Reasons, "Bazel query returned an unsupported, external, or invalid target label")
			continue
		}
		if seen[label] {
			continue
		}
		seen[label] = true
		pkg := strings.TrimPrefix(strings.SplitN(label, ":", 2)[0], "//")
		if pkg == "" {
			pkg = "."
		}
		if _, pathErr := adapters.ResolveWithin(req.Root, pkg); pathErr != nil {
			result.Complete = false
			result.Reasons = append(result.Reasons, "Bazel target package escapes repository")
			continue
		}
		result.Units = append(result.Units, adapters.TestUnit{Key: label, DisplayName: label, Kind: "BUILD_TARGET", Paths: []string{pkg}, SelectableGranularity: "BAZEL_TEST_TARGET", Filterable: false, Timeout: 45 * time.Minute})
	}
	if len(result.Units) == 0 {
		result.Complete = false
		result.Reasons = append(result.Reasons, "Bazel query returned no test targets")
	}
	v, _ := a.Version(ctx, req.Root)
	result.Version = v
	return result, nil
}
func (a *Adapter) Materialize(_ context.Context, req adapters.MaterializeRequest) (adapters.ExecutionCommand, error) {
	if e := a.ValidateConfig(req.Config); e != nil {
		return adapters.ExecutionCommand{}, e
	}
	tool := a.tool(req.Root)
	if req.FullSuite {
		if len(req.Config.FullSuiteArgv) == 0 {
			return adapters.ExecutionCommand{}, errors.New("Bazel full test pattern must be configured")
		}
		return configuredFull(req)
	}
	if e := adapters.ValidateTargetKeys(req.TargetKeys, req.KnownTargets, nil); e != nil {
		return adapters.ExecutionCommand{}, e
	}
	if len(req.TargetKeys) == 0 {
		return adapters.ExecutionCommand{}, adapters.ErrUnknownTarget
	}
	args := []string{"test"}
	for _, opt := range req.Config.Options {
		if strings.HasPrefix(opt, "--build_event_json_file") {
			return adapters.ExecutionCommand{}, errors.New("Bazel build event output path is controlled by IVP")
		}
		args = append(args, opt)
	}
	for _, label := range req.TargetKeys {
		if !validLabel(label) {
			return adapters.ExecutionCommand{}, fmt.Errorf("invalid Bazel label %q", label)
		}
		args = append(args, label)
	}
	cwd, e := adapters.ResolveWithin(req.Root, ".")
	if e != nil {
		return adapters.ExecutionCommand{}, e
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 45 * time.Minute
	}
	maxOut := req.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = 1 << 20
	}
	report := filepath.ToSlash(filepath.Join(".ivp", "reports", "bazel-"+shortHash(strings.Join(req.TargetKeys, ","))+".jsonl"))
	hasBEP := false
	for _, arg := range args {
		if strings.HasPrefix(arg, "--build_event_json_file") {
			hasBEP = true
		}
	}
	if !hasBEP {
		args = append(args, "--build_event_json_file="+report)
	}
	return adapters.ExecutionCommand{Executable: tool, Args: args, WorkingDirectory: cwd, Environment: map[string]string{}, Timeout: timeout, RedactEnv: append([]string(nil), req.Config.RedactEnv...), MaxOutputBytes: maxOut, TargetKey: strings.Join(req.TargetKeys, ","), ReportPaths: []string{report}}, nil
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
		timeout = 60 * time.Minute
	}
	maxOut := req.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = 1 << 20
	}
	return adapters.ExecutionCommand{Executable: exe, Args: args, WorkingDirectory: cwd, Environment: map[string]string{}, Timeout: timeout, RedactEnv: append([]string(nil), req.Config.RedactEnv...), MaxOutputBytes: maxOut, FullSuite: true}, nil
}
func (a *Adapter) ValidateConfig(c adapters.AdapterConfig) error { return a.Base.ValidateConfig(c) }
func (a *Adapter) ParseResults(_ context.Context, r adapters.ParseRequest) (adapters.ParsedResults, error) {
	limit := r.MaxReportBytes
	if limit <= 0 {
		limit = adapters.DefaultReportLimit
	}
	data, err := adapters.ReadReportSet(r.Root, r.ReportPaths, limit)
	if err != nil {
		return adapters.ParsedResults{Complete: false}, err
	}
	if len(data) == 0 {
		return adapters.ParsedResults{Complete: false, Reasons: []string{"Bazel BEP report missing"}}, errors.New("Bazel BEP report missing")
	}
	return parseBEP(data[0], r.ExitCode)
}
func (a *Adapter) ParseCoverage(ctx context.Context, r adapters.CoverageRequest) (adapters.CoverageResult, error) {
	return a.Base.ParseCoverage(ctx, r)
}

type bepEvent struct {
	TestResult *struct {
		Status             string `json:"status"`
		Label              string `json:"label"`
		TestActionExitCode int    `json:"testActionExitCode"`
	} `json:"testResult"`
	TestSummary *struct {
		OverallStatus string `json:"overallStatus"`
		TotalRunCount int    `json:"totalRunCount"`
		Failed        int    `json:"failed"`
		Passed        int    `json:"passed"`
		TotalNumTests int    `json:"totalNumTests"`
	} `json:"testSummary"`
}

func parseBEP(data []byte, exitCode int) (adapters.ParsedResults, error) {
	out := adapters.ParsedResults{Complete: true, HasReliableReport: true}
	dec := json.NewDecoder(bytes.NewReader(data))
	var summaryTests, summaryPassed, summaryFailed int
	var resultTests, resultPassed, resultFailed int
	for {
		var ev bepEvent
		err := dec.Decode(&ev)
		if err != nil {
			if err == io.EOF {
				break
			}
			return adapters.ParsedResults{Complete: false, Reasons: []string{"malformed Bazel BEP JSON"}}, err
		}
		if ev.TestSummary != nil {
			s := ev.TestSummary
			tests := s.TotalNumTests
			if tests == 0 {
				tests = s.TotalRunCount
			}
			failed := s.Failed
			if strings.EqualFold(s.OverallStatus, "FAILED") && failed == 0 {
				failed = 1
			}
			passed := s.Passed
			if passed == 0 && tests > failed {
				passed = tests - failed
			}
			summaryTests += tests
			summaryFailed += failed
			summaryPassed += passed
		} else if ev.TestResult != nil {
			resultTests++
			if strings.EqualFold(ev.TestResult.Status, "PASSED") {
				resultPassed++
			} else {
				resultFailed++
			}
		}
	}
	if summaryTests > 0 {
		out.Tests = summaryTests
		out.Passed = summaryPassed
		out.Failed = summaryFailed
	} else {
		out.Tests = resultTests
		out.Passed = resultPassed
		out.Failed = resultFailed
	}
	if out.Tests == 0 {
		out.ZeroTests = true
		out.Reasons = append(out.Reasons, "Bazel BEP reported no tests")
	}
	if exitCode != 0 && out.Failed == 0 {
		out.Errors++
		out.Reasons = append(out.Reasons, "Bazel exited unsuccessfully without a reported test failure")
	}
	return out, nil
}
func shortHash(value string) string {
	var sum uint64 = 1469598103934665603
	for i := 0; i < len(value); i++ {
		sum ^= uint64(value[i])
		sum *= 1099511628211
	}
	return strconv.FormatUint(sum, 16)
}
func validLabel(label string) bool {
	if !labelPattern.MatchString(label) || !strings.HasPrefix(label, "//") {
		return false
	}
	pkg := strings.TrimPrefix(strings.SplitN(label, ":", 2)[0], "//")
	for _, segment := range strings.Split(pkg, "/") {
		if segment == ".." || segment == "." {
			return false
		}
	}
	return true
}

var _ adapters.Adapter = (*Adapter)(nil)
