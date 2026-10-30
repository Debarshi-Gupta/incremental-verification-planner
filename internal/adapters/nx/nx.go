package nx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/adapters/native"
)

type Adapter struct{ native.Base }

func New() *Adapter {
	return &Adapter{native.Base{Name: "nx", Binary: "nx", Caps: adapters.Capabilities{SelectableGranularity: "NX_PROJECT_TARGET", DependencyPrecision: "NX_PROJECT_GRAPH", CoveragePrecision: "RUNNER_DECLARED", DiscoveryReliable: true, ResultFormats: []string{"junit-xml", "jest-json"}, SupportsCoverage: true, SupportsFiltering: false}}}
}
func (a *Adapter) ID() string { return "nx" }
func (a *Adapter) Detect(_ context.Context, root string) (adapters.Detection, error) {
	return native.DetectFiles(root, "nx.json")
}
func (a *Adapter) prefix(root string) (string, []string) {
	if p := native.FindFirst(root, "node_modules/.bin/nx"); p != "" {
		return p, nil
	}
	return "npx", []string{"--no-install", "nx"}
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
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s, nil
}

func parseProjectNames(data []byte) ([]string, error) {
	var projects []string
	if err := json.Unmarshal(data, &projects); err != nil {
		var wrapped struct {
			Projects []string `json:"projects"`
		}
		if wrappedErr := json.Unmarshal(data, &wrapped); wrappedErr != nil {
			return nil, err
		}
		projects = wrapped.Projects
	}
	sort.Strings(projects)
	unique := projects[:0]
	for _, name := range projects {
		if len(unique) == 0 || unique[len(unique)-1] != name {
			unique = append(unique, name)
		}
	}
	return unique, nil
}

type projectTarget struct {
	Executor       string                     `json:"executor"`
	Outputs        []string                   `json:"outputs"`
	Configurations map[string]json.RawMessage `json:"configurations"`
	Options        map[string]any             `json:"options"`
}
type projectJSON struct {
	Name                 string                   `json:"name"`
	Root                 string                   `json:"root"`
	SourceRoot           string                   `json:"sourceRoot"`
	ImplicitDependencies []string                 `json:"implicitDependencies"`
	Targets              map[string]projectTarget `json:"targets"`
}

func parseProject(data []byte) (projectJSON, error) {
	var project projectJSON
	err := json.Unmarshal(data, &project)
	return project, err
}

func (a *Adapter) Discover(ctx context.Context, req adapters.DiscoverRequest) (adapters.DiscoveryResult, error) {
	if e := a.ValidateConfig(req.Config); e != nil {
		return adapters.DiscoveryResult{Complete: false, Reasons: []string{e.Error()}}, e
	}
	out := adapters.DiscoveryResult{Complete: true}
	data, _, e := a.run(ctx, req.Root, []string{"show", "projects", "--json"}, req.MaxOutputBytes)
	if e != nil {
		out.Complete = false
		out.Reasons = append(out.Reasons, "Nx project discovery failed")
		return out, e
	}
	projects, e := parseProjectNames(data)
	if e != nil {
		return adapters.DiscoveryResult{Complete: false, Reasons: []string{"Nx returned malformed project JSON"}}, e
	}
	projectBuildTargets := make(map[string]adapters.BuildTarget, len(projects))
	var affectedProjects []string
	for _, name := range projects {
		if name == "" || strings.ContainsAny(name, "\r\n\x00") {
			out.Complete = false
			out.Reasons = append(out.Reasons, "Nx returned an invalid project name")
			continue
		}
		pdata, _, pe := a.run(ctx, req.Root, []string{"show", "project", name, "--json"}, req.MaxOutputBytes)
		if pe != nil {
			out.Complete = false
			out.Reasons = append(out.Reasons, "Nx target discovery failed for project "+name)
			continue
		}
		project, parseErr := parseProject(pdata)
		if parseErr != nil {
			out.Complete = false
			out.Reasons = append(out.Reasons, "Nx project metadata was malformed for "+name)
			continue
		}
		if project.Name != "" {
			name = project.Name
		}
		projectRoot := filepath.ToSlash(strings.TrimSpace(project.Root))
		if projectRoot == "" {
			projectRoot = filepath.ToSlash(strings.TrimSpace(project.SourceRoot))
		}
		sourcePattern := "**"
		if projectRoot != "" {
			sourcePattern = strings.TrimSuffix(projectRoot, "/") + "/**"
		}
		dependencies := make([]string, 0, len(project.ImplicitDependencies))
		for _, dependency := range project.ImplicitDependencies {
			dependency = strings.TrimSpace(dependency)
			if dependency != "" && !strings.HasPrefix(dependency, "!") {
				dependencies = append(dependencies, dependency)
			}
		}
		sort.Strings(dependencies)
		projectBuildTargets[name] = adapters.BuildTarget{Key: name, SourcePattern: sourcePattern, Dependencies: dependencies, EvidenceSource: "nx:project-metadata", Confidence: 0.70}
		targetNames := make([]string, 0, len(project.Targets))
		for target := range project.Targets {
			targetNames = append(targetNames, target)
		}
		sort.Strings(targetNames)
		for _, target := range targetNames {
			details := project.Targets[target]
			if !interestingTarget(target, details.Executor) {
				continue
			}
			key := name + ":" + target
			units := []string{key}
			var configs []string
			for conf := range details.Configurations {
				configs = append(configs, conf)
			}
			sort.Strings(configs)
			for _, conf := range configs {
				units = append(units, key+":"+conf)
			}
			report := reportFromOptions(details.Options)
			if report == "" && strings.Contains(strings.ToLower(details.Executor), "jest") {
				report = filepath.ToSlash(filepath.Join(".ivp", "reports", "nx-"+shortHash(key)+".json"))
			}
			mappedPaths, pathErr := projectPaths(req.Root, project.SourceRoot, project.Root)
			if pathErr != nil {
				out.Complete = false
				out.Reasons = append(out.Reasons, "Nx project source path escapes repository for "+name)
				continue
			}
			for _, unitKey := range units {
				unitReport := report
				if unitReport != "" && unitKey != key {
					unitReport = filepath.ToSlash(filepath.Join(".ivp", "reports", "nx-"+shortHash(unitKey)+".json"))
				}
				paths := []string(nil)
				if unitReport != "" {
					paths = []string{unitReport}
				}
				kind := "BUILD_TARGET"
				if isCheckTarget(target) {
					kind = "CHECK"
				}
				out.Units = append(out.Units, adapters.TestUnit{Key: unitKey, DisplayName: unitKey, Kind: kind, Runner: details.Executor, Module: name, Paths: mappedPaths, BuildTargets: []string{name}, SelectableGranularity: "NX_PROJECT_TARGET", Filterable: true, Timeout: 30 * time.Minute, ReportPaths: paths})
			}
		}
	}
	projectNames := make([]string, 0, len(projectBuildTargets))
	for project := range projectBuildTargets {
		projectNames = append(projectNames, project)
	}
	sort.Strings(projectNames)
	for _, project := range projectNames {
		out.BuildTargets = append(out.BuildTargets, projectBuildTargets[project])
	}
	// The native affected query is useful metadata but does not replace the full
	// inventory needed for safe plans. If it fails, preserve the inventory while
	// marking discovery incomplete so the planner will escalate.
	if req.BaseSHA != "" && req.HeadSHA != "" {
		aout, _, ae := a.run(ctx, req.Root, []string{"show", "projects", "--affected", "--base=" + req.BaseSHA, "--head=" + req.HeadSHA, "--json"}, req.MaxOutputBytes)
		if ae != nil {
			out.Complete = false
			out.Reasons = append(out.Reasons, "Nx affected-project calculation failed")
		} else {
			var affected []string
			if ae = json.Unmarshal(aout, &affected); ae != nil {
				out.Complete = false
				out.Reasons = append(out.Reasons, "Nx affected-project output was malformed")
			} else {
				affectedProjects = affected
			}
		}
	}
	targetsByProject := make(map[string][]string, len(projects))
	for _, unit := range out.Units {
		targetsByProject[unit.Module] = append(targetsByProject[unit.Module], unit.Key)
	}
	for _, project := range affectedProjects {
		keys := targetsByProject[project]
		if len(keys) == 0 {
			out.Complete = false
			out.Reasons = append(out.Reasons, "Nx affected project has no discovered test/check target: "+project)
			continue
		}
		out.AffectedTargets = append(out.AffectedTargets, keys...)
	}
	sort.Strings(out.AffectedTargets)
	out.AffectedTargets = unique(out.AffectedTargets)
	if len(out.Units) == 0 {
		out.Complete = false
		out.Reasons = append(out.Reasons, "no Nx test/check targets discovered")
	}
	out.Version, _ = a.Version(ctx, req.Root)
	return out, nil
}

func unique(values []string) []string {
	if len(values) == 0 {
		return values
	}
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}

func interestingTarget(name, executor string) bool {
	n := strings.ToLower(name)
	e := strings.ToLower(executor)
	return strings.Contains(n, "test") || strings.Contains(n, "e2e") || strings.Contains(n, "lint") || strings.Contains(n, "typecheck") || strings.Contains(e, "jest") || strings.Contains(e, "test") || strings.Contains(e, "lint")
}
func (a *Adapter) Materialize(_ context.Context, req adapters.MaterializeRequest) (adapters.ExecutionCommand, error) {
	if e := a.ValidateConfig(req.Config); e != nil {
		return adapters.ExecutionCommand{}, e
	}
	if req.FullSuite {
		if len(req.Config.FullSuiteArgv) == 0 {
			return adapters.ExecutionCommand{}, errors.New("Nx full CI target set must be configured")
		}
		return configuredFull(req)
	}
	if e := adapters.ValidateTargetKeys(req.TargetKeys, req.KnownTargets, nil); e != nil {
		return adapters.ExecutionCommand{}, e
	}
	if len(req.TargetKeys) != 1 {
		return adapters.ExecutionCommand{}, errors.New("Nx materializes one project target per command")
	}
	key := req.TargetKeys[0]
	parts := strings.Split(key, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return adapters.ExecutionCommand{}, fmt.Errorf("invalid Nx project target key %q", key)
	}
	for _, p := range parts {
		if p == "" || strings.ContainsAny(p, "/\\ \t\r\n\x00") {
			return adapters.ExecutionCommand{}, fmt.Errorf("invalid Nx project target key %q", key)
		}
	}
	exe, prefix := a.prefix(req.Root)
	args := append([]string(nil), prefix...)
	args = append(args, "run", parts[0]+":"+parts[1])
	if len(parts) == 3 {
		args = append(args, "--configuration="+parts[2])
	}
	for _, opt := range req.Config.Options {
		if strings.ContainsRune(opt, 0) {
			return adapters.ExecutionCommand{}, adapters.ErrUnsafeCommand
		}
		args = append(args, opt)
	}
	var known adapters.TestUnit
	found := false
	for _, u := range req.KnownUnits {
		if u.Key == key {
			known = u
			found = true
			break
		}
	}
	if !found {
		return adapters.ExecutionCommand{}, fmt.Errorf("Nx target %q lacks trusted snapshot metadata", key)
	}
	checkOnly := known.Kind == "CHECK" || isCheckTarget(parts[1])
	reports := append([]string(nil), known.ReportPaths...)
	coverage := append(append([]string(nil), known.CoveragePaths...), req.Config.CoveragePaths...)
	if strings.Contains(strings.ToLower(known.Runner), "jest") && len(reports) == 0 {
		report := filepath.ToSlash(filepath.Join(".ivp", "reports", "nx-"+shortHash(key)+".json"))
		reports = []string{report}
	}
	if strings.Contains(strings.ToLower(known.Runner), "jest") && len(reports) > 0 && strings.HasPrefix(reports[0], ".ivp/reports/nx-") {
		args = append(args, "--json", "--outputFile="+reports[0])
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
	return adapters.ExecutionCommand{Executable: exe, Args: args, WorkingDirectory: cwd, Environment: map[string]string{}, Timeout: timeout, RedactEnv: append([]string(nil), req.Config.RedactEnv...), MaxOutputBytes: maxOut, TargetKey: key, ReportPaths: reports, CoveragePaths: coverage, CheckOnly: checkOnly}, nil
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
		timeout = 45 * time.Minute
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

var _ adapters.Adapter = (*Adapter)(nil)

func isCheckTarget(target string) bool {
	target = strings.ToLower(target)
	return strings.Contains(target, "lint") || strings.Contains(target, "typecheck") || strings.Contains(target, "format") || strings.Contains(target, "check") || strings.Contains(target, "scan")
}
func reportFromOptions(options map[string]any) string {
	for _, key := range []string{"outputFile", "junitFile", "junitReport", "reportFile"} {
		if value, ok := options[key].(string); ok && value != "" {
			return filepath.ToSlash(value)
		}
	}
	return ""
}
func projectPaths(repoRoot, sourceRoot, projectRoot string) ([]string, error) {
	path := sourceRoot
	if path == "" {
		path = projectRoot
	}
	if path == "" {
		return nil, nil
	}
	absRoot, err := adapters.ResolveWithin(repoRoot, ".")
	if err != nil {
		return nil, err
	}
	absPath, err := adapters.ResolveWithin(absRoot, path)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return nil, err
	}
	return []string{filepath.ToSlash(rel)}, nil
}
func shortHash(value string) string {
	var sum uint64 = 1469598103934665603
	for i := 0; i < len(value); i++ {
		sum ^= uint64(value[i])
		sum *= 1099511628211
	}
	return strconv.FormatUint(sum, 16)
}
