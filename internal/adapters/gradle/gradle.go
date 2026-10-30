package gradle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/adapters/native"
)

type Adapter struct{ native.Base }

func New() *Adapter {
	return &Adapter{native.Base{Name: "gradle", Binary: "gradle", Caps: adapters.Capabilities{SelectableGranularity: "GRADLE_TASK", DependencyPrecision: "TASK_GRAPH", CoveragePrecision: "CLASS_OR_FILE", DiscoveryReliable: true, ResultFormats: []string{"junit-xml"}, SupportsCoverage: true, SupportsFiltering: false}}}
}
func (a *Adapter) ID() string { return "gradle" }
func (a *Adapter) tool(root string) string {
	if p := native.FindFirst(root, "gradlew", "gradlew.bat"); p != "" {
		return p
	}
	return "gradle"
}
func (a *Adapter) Detect(_ context.Context, root string) (adapters.Detection, error) {
	d, e := native.DetectFiles(root, "settings.gradle", "settings.gradle.kts", "build.gradle", "build.gradle.kts")
	if e == nil && d.Detected {
		d.Metadata = map[string]string{"wrapper": native.Relative(d.Root, a.tool(d.Root))}
	}
	return d, e
}
func (a *Adapter) Version(ctx context.Context, root string) (string, error) {
	stdout, stderr, e := adapters.RunCapture(ctx, root, a.tool(root), []string{"--version"}, 64<<10)
	if e != nil {
		return "", e
	}
	s := strings.TrimSpace(string(stdout))
	if s == "" {
		s = strings.TrimSpace(string(stderr))
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s, nil
}

var projectLine = regexp.MustCompile(`Project '([^']+)'`)
var taskLine = regexp.MustCompile(`^\s+([A-Za-z][A-Za-z0-9_-]*)\s+-`)

func parseProjectNames(output []byte) []string {
	var projects []string
	seen := make(map[string]struct{})
	for _, match := range projectLine.FindAllStringSubmatch(string(output), -1) {
		name := strings.TrimSpace(match[1])
		if name == "root project" || strings.HasPrefix(name, "root project ") || !strings.HasPrefix(name, ":") {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		projects = append(projects, name)
	}
	return projects
}

func parseTestTaskNames(output []byte) []string {
	var tasks []string
	seen := make(map[string]struct{})
	for _, line := range strings.Split(string(output), "\n") {
		match := taskLine.FindStringSubmatch(line)
		if len(match) != 2 || !strings.Contains(strings.ToLower(match[1]), "test") {
			continue
		}
		if _, ok := seen[match[1]]; ok {
			continue
		}
		seen[match[1]] = struct{}{}
		tasks = append(tasks, match[1])
	}
	return tasks
}

func (a *Adapter) Discover(ctx context.Context, req adapters.DiscoverRequest) (adapters.DiscoveryResult, error) {
	if e := a.ValidateConfig(req.Config); e != nil {
		return adapters.DiscoveryResult{Complete: false, Reasons: []string{e.Error()}}, e
	}
	tool := a.tool(req.Root)
	out, _, err := adapters.RunCapture(ctx, req.Root, tool, []string{"projects", "--console=plain"}, req.MaxOutputBytes)
	if err != nil {
		return adapters.DiscoveryResult{Complete: false, Reasons: []string{"Gradle project discovery failed"}}, err
	}
	projects := []string{""}
	seen := map[string]bool{"": true}
	for _, name := range parseProjectNames(out) {
		if !seen[name] {
			seen[name] = true
			projects = append(projects, name)
		}
	}
	result := adapters.DiscoveryResult{Complete: true}
	taskSeen := map[string]bool{}
	for _, project := range projects {
		if project != "" && !validTaskKey(project+":test") {
			result.Complete = false
			result.Reasons = append(result.Reasons, "Gradle returned an invalid project path")
			continue
		}
		args := []string{"tasks", "--all", "--console=plain"}
		if project != "" {
			args = append([]string{project + ":tasks"}, args[1:]...)
		}
		data, _, runErr := adapters.RunCapture(ctx, req.Root, tool, args, req.MaxOutputBytes)
		if runErr != nil {
			result.Complete = false
			result.Reasons = append(result.Reasons, "Gradle task discovery failed for "+project)
			continue
		}
		for _, name := range parseTestTaskNames(data) {
			key := project + ":" + name
			if taskSeen[key] {
				continue
			}
			taskSeen[key] = true
			modulePath := strings.TrimPrefix(project, ":")
			modulePath = strings.ReplaceAll(modulePath, ":", "/")
			if modulePath != "" {
				if _, pathErr := adapters.ResolveWithin(req.Root, modulePath); pathErr != nil {
					result.Complete = false
					result.Reasons = append(result.Reasons, "Gradle project path escapes repository")
					continue
				}
			}
			sourcePaths := []string{modulePath}
			if modulePath == "" {
				sourcePaths = []string{"."}
			}
			rel := strings.Trim(project+"/build/test-results/"+name, "/")
			result.Units = append(result.Units, adapters.TestUnit{Key: key, DisplayName: key, Kind: "BUILD_TARGET", Module: project, Paths: sourcePaths, SelectableGranularity: "GRADLE_TASK", Filterable: false, Timeout: 30 * time.Minute, ReportPaths: []string{filepath.ToSlash(filepath.Join(rel, "TEST-*.xml"))}})
		}
	}
	if len(result.Units) == 0 {
		result.Complete = false
		result.Reasons = append(result.Reasons, "no Gradle test tasks discovered")
	}
	version, _ := a.Version(ctx, req.Root)
	result.Version = version
	return result, nil
}
func (a *Adapter) Materialize(_ context.Context, req adapters.MaterializeRequest) (adapters.ExecutionCommand, error) {
	if e := a.ValidateConfig(req.Config); e != nil {
		return adapters.ExecutionCommand{}, e
	}
	if req.FullSuite && len(req.Config.FullSuiteArgv) > 0 {
		return configuredFull(req)
	}
	if req.FullSuite {
		return adapters.ExecutionCommand{}, errors.New("Gradle full-suite command must be configured")
	}
	if e := adapters.ValidateTargetKeys(req.TargetKeys, req.KnownTargets, nil); e != nil {
		return adapters.ExecutionCommand{}, e
	}
	if len(req.TargetKeys) != 1 {
		return adapters.ExecutionCommand{}, errors.New("Gradle materializes one task per command")
	}
	key := req.TargetKeys[0]
	if !validTaskKey(key) {
		return adapters.ExecutionCommand{}, fmt.Errorf("invalid Gradle task key %q", key)
	}
	tool := a.tool(req.Root)
	cwd, e := adapters.ResolveWithin(req.Root, ".")
	if e != nil {
		return adapters.ExecutionCommand{}, e
	}
	args := append([]string(nil), req.Config.Options...)
	args = append(args, key, "--console=plain")
	out := req.MaxOutputBytes
	if out <= 0 {
		out = 1 << 20
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	rel := strings.TrimPrefix(key, ":")
	parts := strings.Split(rel, ":")
	task := parts[len(parts)-1]
	mod := strings.Join(parts[:len(parts)-1], "/")
	report := filepath.ToSlash(filepath.Join(mod, "build", "test-results", task, "TEST-*.xml"))
	return adapters.ExecutionCommand{Executable: tool, Args: args, WorkingDirectory: cwd, Environment: map[string]string{}, Timeout: timeout, RedactEnv: append([]string(nil), req.Config.RedactEnv...), MaxOutputBytes: out, TargetKey: key, ReportPaths: []string{report}, Filtered: false}, nil
}
func validTaskKey(k string) bool {
	if !strings.HasPrefix(k, ":") || strings.Contains(k, "..") || strings.ContainsAny(k, " \t\r\n\\") {
		return false
	}
	for _, p := range strings.Split(strings.TrimPrefix(k, ":"), ":") {
		if p == "" {
			return false
		}
		for _, r := range p {
			if !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
				return false
			}
		}
	}
	return true
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
	return adapters.ExecutionCommand{Executable: exe, Args: args, WorkingDirectory: cwd, Environment: map[string]string{}, Timeout: max(req.Timeout, 30*time.Minute), RedactEnv: append([]string(nil), req.Config.RedactEnv...), MaxOutputBytes: max(req.MaxOutputBytes, int64(1<<20)), FullSuite: true}, nil
}
func (a *Adapter) ValidateConfig(c adapters.AdapterConfig) error { return a.Base.ValidateConfig(c) }
func (a *Adapter) ParseResults(ctx context.Context, r adapters.ParseRequest) (adapters.ParsedResults, error) {
	return a.Base.ParseResults(ctx, r)
}
func (a *Adapter) ParseCoverage(ctx context.Context, r adapters.CoverageRequest) (adapters.CoverageResult, error) {
	return a.Base.ParseCoverage(ctx, r)
}

var _ adapters.Adapter = (*Adapter)(nil)
