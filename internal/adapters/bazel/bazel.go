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

type Adapter struct {
	native.Base
	// capture is an injectable command seam for fixture tests. When nil (the
	// default), every command runs through adapters.RunCapture unchanged.
	capture func(ctx context.Context, root, executable string, args []string, limit int64) ([]byte, []byte, error)
}

func New() *Adapter {
	return &Adapter{Base: native.Base{Name: "bazel", Binary: "bazel", Caps: adapters.Capabilities{SelectableGranularity: "BAZEL_TEST_TARGET", DependencyPrecision: "CONFIGURED_TARGET_GRAPH", CoveragePrecision: "TARGET_OR_FILE", DiscoveryReliable: true, ResultFormats: []string{"bazel-bep-json"}, SupportsCoverage: true, SupportsFiltering: false}}}
}

func (a *Adapter) runCapture(ctx context.Context, root, executable string, args []string, limit int64) ([]byte, []byte, error) {
	if a.capture != nil {
		return a.capture(ctx, root, executable, args, limit)
	}
	return adapters.RunCapture(ctx, root, executable, args, limit)
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
	out, _, e := a.runCapture(ctx, root, a.tool(root), []string{"--version"}, 64<<10)
	if e != nil {
		return "", e
	}
	return strings.TrimSpace(string(out)), nil
}

var labelPattern = regexp.MustCompile(`^(//[A-Za-z0-9._/+@=-]*:[A-Za-z0-9._/+@=-]+|//[A-Za-z0-9._/+@=-]+|@[A-Za-z0-9._+-]+//[A-Za-z0-9._/+@=-]*:[A-Za-z0-9._/+@=-]+)$`)

// Evidence sources for the discovered build-target graph. The unconfigured
// sources describe the conservative `bazel query` over-approximation; the
// configured sources describe what `bazel cquery` resolved after Bazel
// applied the repository's configuration transitions.
const (
	evidencePackageMembership = "bazel:target-package-membership"
	evidenceQueryGraph        = "bazel:query-graph"
	evidenceConfiguredGraph   = "bazel:cquery-graph"
	evidenceConfiguredTargets = "bazel:cquery-target-set"
)

// Confidences for the evidence sources above. The configured sources are
// marginally stronger than their unconfigured counterparts because Bazel
// resolved the real configuration transitions (select branches, platform
// constraints, toolchain resolution) before answering. None of them attests
// test-to-source coverage, so none of them reaches 1.0.
const (
	confidencePackageMembership = 0.50
	confidenceQueryGraph        = 0.80
	confidenceConfiguredGraph   = 0.85
	confidenceConfiguredTargets = 0.60
)

// Query expressions. The unconfigured query reports every test rule in the
// package graph; the configured query resolves the test rules that survive
// the repository's configuration transitions, which is the set the build
// actually builds and therefore the set that decides target selection.
const (
	bazelTestRuleExpr           = `kind(".*_test rule", //...)`
	bazelConfiguredTestRuleExpr = `kind(".*_test rule", deps(//...))`
)

// bazelQueryBatchSize bounds how many labels are sent to a single Bazel
// query so a large repository cannot build an unbounded command line.
const bazelQueryBatchSize = 50

func (a *Adapter) Discover(ctx context.Context, req adapters.DiscoverRequest) (adapters.DiscoveryResult, error) {
	if e := a.ValidateConfig(req.Config); e != nil {
		return adapters.DiscoveryResult{Complete: false, Reasons: []string{e.Error()}}, e
	}
	result := adapters.DiscoveryResult{Complete: true}
	buildTargets := map[string]adapters.BuildTarget{}
	buildDependencies := map[string]map[string]bool{}
	units := map[string]adapters.TestUnit{}

	// The unconfigured query is the conservative over-approximation of the
	// target set: it reports every test rule of the package graph.
	out, _, e := a.runCapture(ctx, req.Root, a.tool(req.Root), []string{"query", bazelTestRuleExpr, "--output=label"}, req.MaxOutputBytes)
	if e != nil {
		return adapters.DiscoveryResult{Complete: false, Reasons: []string{"Bazel query failed; target graph is incomplete"}}, e
	}
	for _, label := range bazelLabels(string(out)) {
		unit, target, reason := bazelTestTarget(req.Root, label, "query")
		if reason != "" {
			result.Complete = false
			result.Reasons = append(result.Reasons, reason)
			continue
		}
		units[label] = unit
		buildTargets[label] = target
	}

	// The configured query resolves the test-target set that survives the
	// repository's configuration transitions. It analyses the build graph
	// but never runs a test.
	configured, cqueryErr := a.configuredTestTargets(ctx, req)
	if cqueryErr != nil {
		// Degrade honestly: the query-derived target set is retained, but
		// the discovery no longer claims configured evidence and the caller
		// falls back to full verification.
		result.Complete = false
		result.Reasons = append(result.Reasons, "Bazel cquery failed; the configured test-target set is unavailable and the query-derived target set was retained")
	} else {
		for _, label := range configured {
			unit, target, reason := bazelTestTarget(req.Root, label, "cquery")
			if reason != "" {
				result.Complete = false
				result.Reasons = append(result.Reasons, reason)
				continue
			}
			// The configured query resolved this label as a test target
			// under the current configuration, which is stronger evidence
			// than package membership alone.
			target.EvidenceSource = evidenceConfiguredTargets
			target.Confidence = confidenceConfiguredTargets
			buildTargets[label] = target
			if _, known := units[label]; !known {
				units[label] = unit
			}
		}
	}

	// Stable unit order keeps discovery deterministic independently of the
	// order in which Bazel answered the queries.
	testLabels := make([]string, 0, len(units))
	for label := range units {
		testLabels = append(testLabels, label)
	}
	sort.Strings(testLabels)
	for _, label := range testLabels {
		result.Units = append(result.Units, units[label])
	}

	// The unconfigured dependency graph is the conservative over-approximation
	// of the build edges; the configured graph adds the edges that survive the
	// configuration transitions. Both are merged so that no dependency is
	// ever dropped from the impact analysis.
	for start := 0; start < len(testLabels); start += bazelQueryBatchSize {
		end := min(start+bazelQueryBatchSize, len(testLabels))
		expr := `deps(set(` + strings.Join(testLabels[start:end], " ") + `))`
		graph, _, queryErr := a.runCapture(ctx, req.Root, a.tool(req.Root), []string{"query", expr, "--output=graph"}, req.MaxOutputBytes)
		if queryErr != nil {
			result.Complete = false
			result.Reasons = append(result.Reasons, "Bazel dependency graph query failed")
			continue
		}
		mergeBazelGraph(&result, buildTargets, buildDependencies, string(graph), evidenceQueryGraph, confidenceQueryGraph)
	}
	if cqueryErr == nil {
		for start := 0; start < len(testLabels); start += bazelQueryBatchSize {
			end := min(start+bazelQueryBatchSize, len(testLabels))
			expr := `deps(set(` + strings.Join(testLabels[start:end], " ") + `))`
			graph, _, graphErr := a.runCapture(ctx, req.Root, a.tool(req.Root), []string{"cquery", expr, "--output=graph"}, req.MaxOutputBytes)
			if graphErr != nil {
				result.Complete = false
				result.Reasons = append(result.Reasons, "Bazel cquery dependency graph failed; the configured dependency graph is incomplete")
				continue
			}
			mergeBazelGraph(&result, buildTargets, buildDependencies, string(graph), evidenceConfiguredGraph, confidenceConfiguredGraph)
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
	for start := 0; start < len(changedLabels); start += bazelQueryBatchSize {
		end := min(start+bazelQueryBatchSize, len(changedLabels))
		expr := `kind(".*_test rule", rdeps(//..., set(` + strings.Join(changedLabels[start:end], " ") + `)))`
		related, _, queryErr := a.runCapture(ctx, req.Root, a.tool(req.Root), []string{"query", expr, "--output=label"}, req.MaxOutputBytes)
		if queryErr != nil {
			result.Complete = false
			result.Reasons = append(result.Reasons, "Bazel related-test query failed")
			continue
		}
		for _, label := range bazelLabels(string(related)) {
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

// configuredTestTargets resolves the configured test-target set with
// `bazel cquery`. Configuration decides which test rules the build really
// builds, so the configured query is the authority on target selection; it
// only analyses the build graph and never executes a test.
func (a *Adapter) configuredTestTargets(ctx context.Context, req adapters.DiscoverRequest) ([]string, error) {
	out, _, err := a.runCapture(ctx, req.Root, a.tool(req.Root), []string{"cquery", bazelConfiguredTestRuleExpr, "--output=label"}, req.MaxOutputBytes)
	if err != nil {
		return nil, err
	}
	return bazelLabels(string(out)), nil
}

// bazelTestTarget validates a Bazel label and derives the test unit and the
// build-target entry for it. The returned reason is empty when the label is
// a safe, in-repository test target; source names the query that produced
// the label.
func bazelTestTarget(root, label, source string) (adapters.TestUnit, adapters.BuildTarget, string) {
	if !validLabel(label) {
		return adapters.TestUnit{}, adapters.BuildTarget{}, "Bazel " + source + " returned an unsupported, external, or invalid target label"
	}
	pkg := strings.TrimPrefix(strings.SplitN(label, ":", 2)[0], "//")
	if pkg == "" {
		pkg = "."
	}
	if _, pathErr := adapters.ResolveWithin(root, pkg); pathErr != nil {
		return adapters.TestUnit{}, adapters.BuildTarget{}, "Bazel target package escapes repository"
	}
	// A Bazel test target is an exact, supported selection unit even though
	// Bazel does not filter individual tests within that target here.
	pattern := "**"
	if pkg != "." {
		pattern = strings.TrimSuffix(pkg, "/") + "/**"
	}
	paths := []string(nil)
	if pkg != "." {
		paths = []string{pkg}
	}
	unit := adapters.TestUnit{Key: label, DisplayName: label, Kind: "BUILD_TARGET", Paths: paths, BuildTargets: []string{label}, SelectableGranularity: "BAZEL_TEST_TARGET", Filterable: true, Timeout: 45 * time.Minute}
	target := adapters.BuildTarget{Key: label, SourcePattern: pattern, EvidenceSource: evidencePackageMembership, Confidence: confidencePackageMembership}
	return unit, target, ""
}

// mergeBazelGraph records the dependency edges of one graph output. The
// edges of the unconfigured and the configured graph are merged so the
// impact analysis keeps every observed dependency; a label that only the
// configured graph knows carries the configured evidence source.
func mergeBazelGraph(result *adapters.DiscoveryResult, buildTargets map[string]adapters.BuildTarget, buildDependencies map[string]map[string]bool, output, evidence string, confidence float64) {
	for _, line := range strings.Split(output, "\n") {
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
			if _, exists := buildTargets[label]; exists {
				continue
			}
			buildTargets[label] = adapters.BuildTarget{Key: label, SourcePattern: bazelPackagePattern(label), EvidenceSource: evidence, Confidence: confidence}
		}
		if buildDependencies[from] == nil {
			buildDependencies[from] = map[string]bool{}
		}
		buildDependencies[from][to] = true
	}
}

// bazelLabels parses the labels of a query or cquery answer. The result is
// deduplicated and sorted so that discovery is deterministic.
func bazelLabels(output string) []string {
	seen := map[string]bool{}
	labels := make([]string, 0, 16)
	for _, line := range strings.Split(output, "\n") {
		label := bazelConfiguredLabel(strings.TrimSpace(line))
		if label == "" || seen[label] {
			continue
		}
		seen[label] = true
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return labels
}

// bazelConfiguredLabel strips the configuration suffix a configured query may
// print after a label, for example "//pkg:target (0e3f2a1b)".
func bazelConfiguredLabel(line string) string {
	if index := strings.LastIndex(line, " ("); index > 0 && strings.HasSuffix(line, ")") {
		return strings.TrimSpace(line[:index])
	}
	return line
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
	// Every declared report is consumed: a Bazel run can write more than one
	// build-event file (for example one per shard), and reading only the
	// first one would drop the results of the remaining shards.
	return parseBEPSet(data, r.ExitCode)
}
func (a *Adapter) ParseCoverage(ctx context.Context, r adapters.CoverageRequest) (adapters.CoverageResult, error) {
	return a.Base.ParseCoverage(ctx, r)
}

// bepOutcome is the classification of one BEP test attempt. The values are
// the shared adapters outcome vocabulary: "passed", "failed", "skipped" and
// "error". A timed-out test has no dedicated constant in that vocabulary, so
// it is reported as an error, the closest existing category (the shared
// JUnit parser uses it for a test that could not complete).
type bepOutcome string

const (
	bepOutcomePassed  bepOutcome = "passed"
	bepOutcomeFailed  bepOutcome = "failed"
	bepOutcomeSkipped bepOutcome = "skipped"
	bepOutcomeError   bepOutcome = "error"
)

// bepEvent is one build-event record. Bazel reports a test target through
// "testResult" and the aggregate counters through "testSummary".
type bepEvent struct {
	TestResult  *bepTestResult  `json:"testResult"`
	TestSummary *bepTestSummary `json:"testSummary"`
}

// bepTestResult is one test target result. Bazel marks a skipped or a timed
// out target either through the status enum or through the explicit
// "skipped"/"timeout" flags, and it records the individual attempts of a
// flaky target in "runAttempts".
type bepTestResult struct {
	Status             string       `json:"status"`
	Label              string       `json:"label"`
	TestActionExitCode int          `json:"testActionExitCode"`
	Skipped            bool         `json:"skipped"`
	Timeout            bool         `json:"timeout"`
	RunAttempts        []bepAttempt `json:"runAttempts"`
}

// bepAttempt is one attempt of a test target.
type bepAttempt struct {
	Status  string `json:"status"`
	Skipped bool   `json:"skipped"`
	Timeout bool   `json:"timeout"`
}

// bepTestSummary is the aggregate result of one build.
type bepTestSummary struct {
	OverallStatus string `json:"overallStatus"`
	TotalRunCount int    `json:"totalRunCount"`
	Failed        int    `json:"failed"`
	Passed        int    `json:"passed"`
	Skipped       int    `json:"skipped"`
	Flaky         int    `json:"flaky"`
	TotalNumTests int    `json:"totalNumTests"`
}

// bepLabelState collects every attempt of one test target.
type bepLabelState struct {
	outcomes []bepOutcome
	flaky    bool
}

// bepFileResult is the parsed content of one build-event report.
type bepFileResult struct {
	Tests        int
	Passed       int
	Failed       int
	Skipped      int
	Errors       int
	Cases        []adapters.TestCase
	Flaky        map[string]bool
	SummaryFlaky int
}

// parseBEP parses a single build-event report.
func parseBEP(data []byte, exitCode int) (adapters.ParsedResults, error) {
	return parseBEPSet([][]byte{data}, exitCode)
}

// parseBEPSet parses every declared build-event report and merges the
// results. The merged case list is sorted by test id, so the result does not
// depend on the order in which the report paths were declared.
func parseBEPSet(files [][]byte, exitCode int) (adapters.ParsedResults, error) {
	out := adapters.ParsedResults{Complete: true, HasReliableReport: true}
	flaky := map[string]bool{}
	summaryFlaky := 0
	for _, data := range files {
		file, err := parseBEPFile(data)
		if err != nil {
			return adapters.ParsedResults{Complete: false, Reasons: []string{"malformed Bazel BEP JSON"}}, err
		}
		out.Tests += file.Tests
		out.Passed += file.Passed
		out.Failed += file.Failed
		out.Skipped += file.Skipped
		out.Errors += file.Errors
		out.Cases = append(out.Cases, file.Cases...)
		for label := range file.Flaky {
			flaky[label] = true
		}
		summaryFlaky += file.SummaryFlaky
	}
	sort.SliceStable(out.Cases, func(i, j int) bool {
		if out.Cases[i].Name != out.Cases[j].Name {
			return out.Cases[i].Name < out.Cases[j].Name
		}
		return out.Cases[i].Status < out.Cases[j].Status
	})
	// A test target whose attempts disagree is flaky. The shared downstream
	// classification detects the same disagreement in the case list, so the
	// parser keeps both attempts of such a target.
	passed := map[string]bool{}
	failed := map[string]bool{}
	for _, testCase := range out.Cases {
		switch testCase.Status {
		case string(bepOutcomePassed):
			passed[testCase.Name] = true
		case string(bepOutcomeFailed), string(bepOutcomeError):
			failed[testCase.Name] = true
		}
	}
	for label := range passed {
		if failed[label] {
			flaky[label] = true
		}
	}
	for i := range out.Cases {
		if flaky[out.Cases[i].Name] {
			out.Cases[i].Message = "Bazel BEP reported disagreeing attempts for this test target"
		}
	}
	flakyCount := len(flaky)
	if summaryFlaky > flakyCount {
		flakyCount = summaryFlaky
	}
	if flakyCount > 0 {
		out.Reasons = append(out.Reasons, "Bazel BEP reported "+strconv.Itoa(flakyCount)+" flaky test target(s)")
	}
	if out.Tests == 0 {
		out.ZeroTests = true
		out.Reasons = append(out.Reasons, "Bazel BEP reported no tests")
	}
	if exitCode != 0 && out.Failed+out.Errors == 0 {
		out.Errors++
		out.Reasons = append(out.Reasons, "Bazel exited unsuccessfully without a reported test failure")
	}
	return out, nil
}

// parseBEPFile parses one build-event report. Every test target is counted
// once, with the outcome of its final attempt, and every attempt is kept as
// a case so that disagreeing attempts of one target stay visible.
func parseBEPFile(data []byte) (bepFileResult, error) {
	file := bepFileResult{Flaky: map[string]bool{}}
	dec := json.NewDecoder(bytes.NewReader(data))
	states := map[string]*bepLabelState{}
	labels := make([]string, 0, 16)
	var summary *bepSummaryCounts
	for {
		var ev bepEvent
		err := dec.Decode(&ev)
		if err != nil {
			if err == io.EOF {
				break
			}
			return bepFileResult{}, err
		}
		if ev.TestSummary != nil {
			summary = mergeBEPSummary(summary, ev.TestSummary)
			continue
		}
		if ev.TestResult == nil {
			continue
		}
		label := bazelConfiguredLabel(strings.TrimSpace(ev.TestResult.Label))
		if label == "" {
			continue
		}
		state := states[label]
		if state == nil {
			state = &bepLabelState{}
			states[label] = state
			labels = append(labels, label)
		}
		outcomes, flaky := bepResultOutcomes(ev.TestResult)
		state.outcomes = append(state.outcomes, outcomes...)
		if flaky {
			state.flaky = true
		}
	}
	for _, label := range labels {
		state := states[label]
		final := state.outcomes[len(state.outcomes)-1]
		switch final {
		case bepOutcomePassed:
			file.Passed++
		case bepOutcomeFailed:
			file.Failed++
		case bepOutcomeSkipped:
			file.Skipped++
		case bepOutcomeError:
			file.Errors++
		}
		file.Tests++
		if state.flaky || bepOutcomesDisagree(state.outcomes) {
			file.Flaky[label] = true
		}
		for _, outcome := range state.outcomes {
			file.Cases = append(file.Cases, adapters.TestCase{Name: label, Status: string(outcome)})
		}
	}
	if summary != nil && summary.tests > file.Tests {
		// The summary is the authoritative aggregate of the build. It is
		// used when it reports more tests than the observed results, so a
		// truncated result stream never under-reports the executed tests.
		// The summary cannot separate timeouts, so the observed error
		// classification is dropped here instead of counting a test twice.
		file.Tests = summary.tests
		file.Passed = summary.passed
		file.Failed = summary.failed
		file.Skipped = summary.skipped
		file.Errors = 0
	}
	if summary != nil {
		file.SummaryFlaky = summary.flaky
	}
	return file, nil
}

// bepSummaryCounts holds the reconciled counters of the test summaries of
// one report.
type bepSummaryCounts struct {
	tests   int
	passed  int
	failed  int
	skipped int
	flaky   int
}

// mergeBEPSummary folds one test summary into the accumulated summary of a
// report.
func mergeBEPSummary(current *bepSummaryCounts, s *bepTestSummary) *bepSummaryCounts {
	if current == nil {
		current = &bepSummaryCounts{}
	}
	tests := s.TotalNumTests
	if tests == 0 {
		tests = s.TotalRunCount
	}
	failed := s.Failed
	if strings.EqualFold(s.OverallStatus, "FAILED") && failed == 0 {
		failed = 1
	}
	passed := s.Passed
	if passed == 0 && tests > failed+s.Skipped {
		passed = tests - failed - s.Skipped
	}
	current.tests += tests
	current.passed += passed
	current.failed += failed
	current.skipped += s.Skipped
	current.flaky += s.Flaky
	return current
}

// bepResultOutcomes classifies one test result into its attempts.
func bepResultOutcomes(result *bepTestResult) ([]bepOutcome, bool) {
	flaky := strings.EqualFold(strings.TrimSpace(result.Status), "FLAKY")
	if len(result.RunAttempts) == 0 {
		return []bepOutcome{bepAttemptOutcome(result.Status, result.Skipped, result.Timeout)}, flaky
	}
	outcomes := make([]bepOutcome, 0, len(result.RunAttempts))
	for _, attempt := range result.RunAttempts {
		outcomes = append(outcomes, bepAttemptOutcome(attempt.Status, attempt.Skipped || result.Skipped, attempt.Timeout || result.Timeout))
	}
	return outcomes, flaky
}

// bepAttemptOutcome maps one BEP attempt onto the shared outcome vocabulary.
func bepAttemptOutcome(status string, skipped, timeout bool) bepOutcome {
	if skipped {
		return bepOutcomeSkipped
	}
	if timeout {
		return bepOutcomeError
	}
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "SKIPPED":
		return bepOutcomeSkipped
	case "TIMEOUT":
		return bepOutcomeError
	case "PASSED", "CACHED", "REMOTE_CACHE_HIT", "FLAKY":
		return bepOutcomePassed
	default:
		return bepOutcomeFailed
	}
}

// bepOutcomesDisagree reports whether the attempts of one test target
// disagree, which is the shared definition of a flaky target.
func bepOutcomesDisagree(outcomes []bepOutcome) bool {
	passed, failed := false, false
	for _, outcome := range outcomes {
		switch outcome {
		case bepOutcomePassed:
			passed = true
		case bepOutcomeFailed, bepOutcomeError:
			failed = true
		}
	}
	return passed && failed
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
