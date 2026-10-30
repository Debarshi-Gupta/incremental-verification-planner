package bazel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
	buildTargets := map[string]adapters.BuildTarget{}
	buildDependencies := map[string]map[string]bool{}
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
		// A Bazel test target is an exact, supported selection unit even though
		// Bazel does not filter individual tests within that target here.
		pattern := "**"
		if pkg != "." {
			pattern = strings.TrimSuffix(pkg, "/") + "/**"
		}
		buildTargets[label] = adapters.BuildTarget{Key: label, SourcePattern: pattern, EvidenceSource: "bazel:target-package-membership", Confidence: 0.50}
		paths := []string(nil)
		if pkg != "." {
			paths = []string{pkg}
		}
		result.Units = append(result.Units, adapters.TestUnit{Key: label, DisplayName: label, Kind: "BUILD_TARGET", Paths: paths, BuildTargets: []string{label}, SelectableGranularity: "BAZEL_TEST_TARGET", Filterable: true, Timeout: 45 * time.Minute})
	}
	testLabels := make([]string, 0, len(result.Units))
	for _, unit := range result.Units {
		testLabels = append(testLabels, unit.Key)
	}
	sort.Strings(testLabels)
	for start := 0; start < len(testLabels); start += 50 {
		end := min(start+50, len(testLabels))
		expr := `deps(set(` + strings.Join(testLabels[start:end], " ") + `))`
		graph, _, queryErr := adapters.RunCapture(ctx, req.Root, a.tool(req.Root), []string{"query", expr, "--output=graph"}, req.MaxOutputBytes)
		if queryErr != nil {
			result.Complete = false
			result.Reasons = append(result.Reasons, "Bazel dependency graph query failed")
			continue
		}
		for _, line := range strings.Split(string(graph), "\n") {
			parts := strings.SplitN(strings.TrimSpace(line), "->", 2)
			if len(parts) != 2 {
				continue
			}
			from := bazelGraphLabel(parts[0])
			to := bazelGraphLabel(strings.TrimSuffix(strings.TrimSpace(parts[1]), ";"))
			if !validLabel(from) || !validLabel(to) {
				result.Complete = false
				result.Reasons = append(result.Reasons, "Bazel dependency graph contained an unsupported label")
				continue
			}
			for _, label := range []string{from, to} {
				if _, exists := buildTargets[label]; !exists {
					buildTargets[label] = adapters.BuildTarget{Key: label, SourcePattern: bazelPackagePattern(label), EvidenceSource: "bazel:query-graph", Confidence: 0.80}
				}
			}
			if buildDependencies[from] == nil {
				buildDependencies[from] = map[string]bool{}
			}
			buildDependencies[from][to] = true
		}
	}
	buildKeys := make([]string, 0, len(buildTargets))
	for key := range buildTargets {
		buildKeys = append(buildKeys, key)
	}
	sort.Strings(buildKeys)
	for _, key := range buildKeys {
		target := buildTargets[key]
		for dependency := range buildDependencies[key] {
			target.Dependencies = append(target.Dependencies, dependency)
		}
		sort.Strings(target.Dependencies)
		result.BuildTargets = append(result.BuildTargets, target)
	}
	knownTests := make(map[string]bool, len(result.Units))
	for _, unit := range result.Units {
		knownTests[unit.Key] = true
	}
	affected := make(map[string]bool)
	changedLabels := make([]string, 0, len(req.ChangedFiles))
	seenChangedLabels := make(map[string]bool)
	for _, changed := range req.ChangedFiles {
		if _, pathErr := adapters.ResolveWithin(req.Root, changed); pathErr != nil {
			result.Complete = false
			result.Reasons = append(result.Reasons, "Bazel related-test input escaped the repository")
			continue
		}
		if _, statErr := os.Stat(filepath.Join(req.Root, filepath.FromSlash(changed))); statErr != nil {
			// Deleted paths and unavailable files cannot be queried as Bazel file
			// targets, so retain a complete-verification requirement.
			result.Complete = false
			result.Reasons = append(result.Reasons, "Bazel related-test input is unavailable")
			continue
		}
		pkg, base := filepath.ToSlash(filepath.Dir(changed)), filepath.Base(changed)
		if pkg == "." {
			pkg = ""
		}
		fileLabel := "//" + pkg + ":" + base
		if !validLabel(fileLabel) {
			result.Complete = false
			result.Reasons = append(result.Reasons, "Bazel changed path cannot be represented as a safe target label")
			continue
		}
		if !seenChangedLabels[fileLabel] {
			seenChangedLabels[fileLabel] = true
			changedLabels = append(changedLabels, fileLabel)
		}
	}
	// Batch file targets to avoid starting a Bazel server/query process for
	// every changed path in a large change set.
	for start := 0; start < len(changedLabels); start += 50 {
		end := min(start+50, len(changedLabels))
		expr := `kind(".*_test rule", rdeps(//..., set(` + strings.Join(changedLabels[start:end], " ") + `)))`
		related, _, queryErr := adapters.RunCapture(ctx, req.Root, a.tool(req.Root), []string{"query", expr, "--output=label"}, req.MaxOutputBytes)
		if queryErr != nil {
			result.Complete = false
			result.Reasons = append(result.Reasons, "Bazel related-test query failed")
			continue
		}
		for _, line := range strings.Split(string(related), "\n") {
			label := strings.TrimSpace(line)
			if label == "" {
				continue
			}
			if !validLabel(label) || !knownTests[label] {
				result.Complete = false
				result.Reasons = append(result.Reasons, "Bazel related-test query returned an undiscovered test target")
				continue
			}
			affected[label] = true
		}
	}
	for label := range affected {
		result.AffectedTargets = append(result.AffectedTargets, label)
	}
	sort.Strings(result.AffectedTargets)
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
	commandName := "test"
	if len(req.Config.CoveragePaths) > 0 {
		commandName = "coverage"
	}
	args := []string{commandName}
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
	return adapters.ExecutionCommand{Executable: tool, Args: args, WorkingDirectory: cwd, Environment: map[string]string{}, Timeout: timeout, RedactEnv: append([]string(nil), req.Config.RedactEnv...), MaxOutputBytes: maxOut, TargetKey: strings.Join(req.TargetKeys, ","), ReportPaths: []string{report}, CoveragePaths: append([]string(nil), req.Config.CoveragePaths...)}, nil
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
			status := "FAILED"
			if strings.EqualFold(ev.TestResult.Status, "PASSED") {
				resultPassed++
				status = "PASSED"
			} else {
				resultFailed++
			}
			if ev.TestResult.Label != "" {
				out.Cases = append(out.Cases, adapters.TestCase{Name: ev.TestResult.Label, Status: status})
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

func bazelGraphLabel(value string) string {
	value = strings.TrimSpace(value)
	if separator := strings.Index(value, "["); separator >= 0 {
		value = value[:separator]
	}
	value = strings.TrimSpace(strings.TrimSuffix(value, ";"))
	value = strings.Trim(value, "\"' \t\r\n")
	return value
}

func bazelPackagePattern(label string) string {
	if !validLabel(label) {
		return ""
	}
	pkg := strings.TrimPrefix(strings.SplitN(label, ":", 2)[0], "//")
	if pkg == "" {
		return "**"
	}
	return strings.TrimSuffix(pkg, "/") + "/**"
}

var _ adapters.Adapter = (*Adapter)(nil)
