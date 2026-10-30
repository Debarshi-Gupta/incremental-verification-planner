package generic

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ivp/ivp/internal/adapters"
)

type Adapter struct{}

func New() *Adapter         { return &Adapter{} }
func (*Adapter) ID() string { return "generic" }
func (*Adapter) Capabilities() adapters.Capabilities {
	return adapters.Capabilities{SelectableGranularity: "DECLARED_UNIT", DependencyPrecision: "EXPLICIT", CoveragePrecision: "EXPLICIT_PATH_MAPPING", DiscoveryReliable: true, ResultFormats: []string{"junit-xml", "jest-json"}, SupportsCoverage: true, SupportsFiltering: true}
}
func (*Adapter) Detect(_ context.Context, root string) (adapters.Detection, error) {
	for _, p := range []string{".ivp/config.yaml", ".ivp/config.yml", "ivp.yaml", "ivp.yml"} {
		if abs, e := adapters.ResolveWithin(root, p); e == nil {
			if st, e := os.Stat(abs); e == nil && !st.IsDir() {
				return adapters.Detection{Detected: true, Root: root, Evidence: []string{p}}, nil
			}
		}
	}
	return adapters.Detection{Detected: false, Root: root}, nil
}
func (*Adapter) ValidateConfig(c adapters.AdapterConfig) error {
	seen := map[string]bool{}
	for _, t := range c.Targets {
		if t.ID == "" || seen[t.ID] {
			return fmt.Errorf("generic target IDs must be nonempty and unique: %q", t.ID)
		}
		seen[t.ID] = true
		if t.Kind != "" && t.Kind != "TEST" && t.Kind != "CHECK" {
			return fmt.Errorf("generic target %q kind must be TEST or CHECK", t.ID)
		}
		if len(t.Argv) == 0 || strings.TrimSpace(t.Argv[0]) == "" {
			return fmt.Errorf("generic target %q requires argv", t.ID)
		}
		if t.Timeout < 0 {
			return fmt.Errorf("generic target %q has negative timeout", t.ID)
		}
		for i, a := range t.Argv {
			if strings.ContainsRune(a, 0) {
				return adapters.ErrUnsafeCommand
			}
			if strings.ContainsAny(a, "\r\n") {
				return fmt.Errorf("generic target %q argv[%d] contains a line break", t.ID, i)
			}
			if looksLikeShellInjection(a) {
				return fmt.Errorf("generic target %q argv[%d] contains shell control syntax", t.ID, i)
			}
			if strings.Contains(a, "{") || strings.Contains(a, "}") {
				if !placeholder(a) {
					return fmt.Errorf("generic target %q uses a placeholder as part of an argv element", t.ID)
				}
			}
		}
		for _, p := range append(append([]string(nil), t.MapsPaths...), t.CoveragePaths...) {
			if _, err := compileGlob(p); err != nil {
				return fmt.Errorf("generic target %q: %w", t.ID, err)
			}
		}
		if _, err := safeExecutableStatic(t.Argv[0], c.AllowedExecutables); err != nil {
			return fmt.Errorf("generic target %q: %w", t.ID, err)
		}
	}
	for _, t := range c.Targets {
		for _, dep := range t.DependsOn {
			if !seen[dep] {
				return fmt.Errorf("generic target %q depends on unknown target %q", t.ID, dep)
			}
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(key string) error {
		if visiting[key] {
			return fmt.Errorf("generic target dependency cycle at %q", key)
		}
		if visited[key] {
			return nil
		}
		visiting[key] = true
		var t *adapters.TargetSpec
		for i := range c.Targets {
			if c.Targets[i].ID == key {
				t = &c.Targets[i]
				break
			}
		}
		if t != nil {
			for _, d := range t.DependsOn {
				if err := visit(d); err != nil {
					return err
				}
			}
		}
		delete(visiting, key)
		visited[key] = true
		return nil
	}
	for _, t := range c.Targets {
		if err := visit(t.ID); err != nil {
			return err
		}
	}
	for _, arg := range c.FullSuiteArgv {
		if strings.ContainsRune(arg, 0) || strings.ContainsAny(arg, "\r\n") || !placeholder(arg) && strings.ContainsAny(arg, "{}") {
			return adapters.ErrUnsafeCommand
		}
	}
	if len(c.FullSuiteArgv) > 0 {
		if _, err := safeExecutableStatic(c.FullSuiteArgv[0], c.AllowedExecutables); err != nil {
			return err
		}
	}
	return nil
}
func placeholder(s string) bool {
	return s == "{changed_files}" || s == "{selected_tests}" || s == "{base_sha}" || s == "{head_sha}"
}
func looksLikeShellInjection(s string) bool {
	return strings.ContainsAny(s, "`|;<>") || strings.Contains(s, "$(") || strings.Contains(s, "&&") || strings.Contains(s, "||")
}
func (*Adapter) Discover(_ context.Context, req adapters.DiscoverRequest) (adapters.DiscoveryResult, error) {
	if err := (*Adapter)(nil).ValidateConfig(req.Config); err != nil {
		return adapters.DiscoveryResult{Complete: false, Reasons: []string{err.Error()}}, err
	}
	if !req.Config.Enabled {
		return adapters.DiscoveryResult{Complete: true}, nil
	}
	out := adapters.DiscoveryResult{Complete: true}
	for _, t := range req.Config.Targets {
		kind := t.Kind
		if kind == "" {
			kind = "TEST"
		}
		u := adapters.TestUnit{Key: t.ID, DisplayName: t.DisplayName, Kind: kind, Paths: append([]string(nil), t.MapsPaths...), Dependencies: append([]string(nil), t.DependsOn...), RiskLabels: append([]string(nil), t.RiskLabels...), Mandatory: t.Mandatory, SelectableGranularity: "DECLARED_UNIT", Filterable: true, Timeout: t.Timeout, EstimatedDuration: t.Timeout, ResourceGroup: t.ResourceGroup, ReportPaths: append([]string(nil), t.ReportPaths...), CoveragePaths: append([]string(nil), t.CoveragePaths...), TrustedConfig: true}
		if u.DisplayName == "" {
			u.DisplayName = t.ID
		}
		out.Units = append(out.Units, u)
	}
	sort.Slice(out.Units, func(i, j int) bool { return out.Units[i].Key < out.Units[j].Key })
	return out, nil
}
func (a *Adapter) Materialize(_ context.Context, req adapters.MaterializeRequest) (adapters.ExecutionCommand, error) {
	if err := a.ValidateConfig(req.Config); err != nil {
		return adapters.ExecutionCommand{}, err
	}
	if err := adapters.ValidateTargetKeys(req.TargetKeys, req.KnownTargets, req.Config.Targets); err != nil && !req.FullSuite {
		return adapters.ExecutionCommand{}, err
	}
	var argv []string
	var cwd string
	var key, resource string
	var timeout time.Duration
	var reports, covers, redact []string
	checkOnly := false
	if req.FullSuite {
		argv = append([]string(nil), req.Config.FullSuiteArgv...)
		if len(argv) == 0 {
			return adapters.ExecutionCommand{}, errors.New("generic full-suite argv is not configured")
		}
		for _, t := range req.Config.Targets {
			if t.Mandatory {
				continue
			}
			if len(t.Argv) > 0 { /* full command is explicitly configured and remains a single unit */
			}
		}
	} else {
		if len(req.TargetKeys) != 1 {
			return adapters.ExecutionCommand{}, errors.New("generic adapter materializes exactly one declared unit per command")
		}
		var target *adapters.TargetSpec
		for i := range req.Config.Targets {
			if req.Config.Targets[i].ID == req.TargetKeys[0] {
				target = &req.Config.Targets[i]
				break
			}
		}
		if target == nil {
			return adapters.ExecutionCommand{}, adapters.ErrUnknownTarget
		}
		argv = append([]string(nil), target.Argv...)
		key = target.ID
		resource = target.ResourceGroup
		timeout = target.Timeout
		reports = append(reports, target.ReportPaths...)
		covers = append(covers, target.CoveragePaths...)
		cwd = target.CWD
		checkOnly = target.Kind == "CHECK"
		redact = append(append([]string(nil), req.Config.RedactEnv...), target.RedactEnv...)
	}
	if cwd == "" {
		cwd = "."
	}
	workdir, err := adapters.ResolveWithin(req.Root, cwd)
	if err != nil {
		return adapters.ExecutionCommand{}, err
	}
	changed, err := canonicalChangedPaths(req.Root, req.ChangedFiles)
	if err != nil {
		return adapters.ExecutionCommand{}, err
	}
	selected := append([]string(nil), req.SelectedTests...)
	for _, test := range selected {
		if strings.ContainsRune(test, 0) {
			return adapters.ExecutionCommand{}, adapters.ErrUnsafeCommand
		}
	}
	expanded := make([]string, 0, len(argv))
	for _, arg := range argv {
		switch arg {
		case "{changed_files}":
			expanded = append(expanded, changed...)
		case "{selected_tests}":
			expanded = append(expanded, selected...)
		case "{base_sha}":
			expanded = append(expanded, req.BaseSHA)
		case "{head_sha}":
			expanded = append(expanded, req.HeadSHA)
		default:
			if strings.ContainsAny(arg, "{}") || strings.ContainsRune(arg, 0) {
				return adapters.ExecutionCommand{}, adapters.ErrUnsafeCommand
			}
			expanded = append(expanded, arg)
		}
	}
	if len(expanded) == 0 {
		return adapters.ExecutionCommand{}, adapters.ErrUnsafeCommand
	}
	executable, err := resolveExecutable(req.Root, expanded[0], req.Config.AllowedExecutables)
	if err != nil {
		return adapters.ExecutionCommand{}, err
	}
	maxOut := req.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = req.Config.MaxOutputBytes
	}
	if maxOut <= 0 {
		maxOut = 1 << 20
	}
	if timeout <= 0 {
		timeout = req.Timeout
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	if req.FullSuite {
		redact = append([]string(nil), req.Config.RedactEnv...)
	}
	return adapters.ExecutionCommand{Executable: executable, Args: expanded[1:], WorkingDirectory: workdir, Environment: map[string]string{}, Timeout: timeout, ResourceGroup: resource, RedactEnv: redact, MaxOutputBytes: maxOut, TargetKey: key, ReportPaths: reports, CoveragePaths: covers, Filtered: !req.FullSuite, FullSuite: req.FullSuite, CheckOnly: checkOnly}, nil
}
func (*Adapter) ParseResults(ctx context.Context, req adapters.ParseRequest) (adapters.ParsedResults, error) {
	return adapters.ParseReports(ctx, req)
}
func (*Adapter) ParseCoverage(_ context.Context, req adapters.CoverageRequest) (adapters.CoverageResult, error) {
	if len(req.ReportPaths) == 0 {
		return adapters.CoverageResult{Format: req.Format, Complete: false, Reasons: []string{"no coverage report"}}, errors.New("no coverage report")
	}
	return adapters.ParseCoverageReports(req.Root, req.ReportPaths, req.Format, req.MaxReportBytes)
}
func (*Adapter) Version(context.Context, string) (string, error) { return "generic-config-v1", nil }

func safeExecutableStatic(value string, allow []string) (bool, error) {
	if value == "" || strings.ContainsRune(value, 0) {
		return false, adapters.ErrUnsafeCommand
	}
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(value)), ".exe")
	switch base {
	case "sh", "bash", "dash", "zsh", "fish", "cmd", "powershell", "pwsh":
		return false, fmt.Errorf("shell executable %q is not permitted by the generic adapter", value)
	}
	if filepath.IsAbs(value) || strings.ContainsAny(value, "/\\") || value == "." || value == ".." {
		return true, nil
	}
	for _, x := range allow {
		if value == x {
			return false, nil
		}
	}
	return false, fmt.Errorf("bare executable %q is not allowlisted", value)
}
func resolveExecutable(root, value string, allow []string) (string, error) {
	inside, bareErr := safeExecutableStatic(value, allow)
	if bareErr != nil {
		return "", bareErr
	}
	if !inside {
		return value, nil
	}
	p, err := adapters.ResolveWithin(root, value)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", errors.New("generic executable points to a directory")
	}
	return p, nil
}
func canonicalChangedPaths(root string, paths []string) ([]string, error) {
	absRoot, e := adapters.ResolveWithin(root, ".")
	if e != nil {
		return nil, e
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		abs, e := adapters.ResolveWithin(absRoot, p)
		if e != nil {
			return nil, e
		}
		rel, e := filepath.Rel(absRoot, abs)
		if e != nil {
			return nil, e
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out, nil
}

var globMeta = regexp.MustCompile(`([.+^$(){}|])`)

func compileGlob(pattern string) (*regexp.Regexp, error) {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	if pattern == "" || strings.HasPrefix(pattern, "/") || filepath.IsAbs(pattern) || regexp.MustCompile(`^[A-Za-z]:`).MatchString(pattern) || strings.Contains(pattern, "\\") {
		return nil, errors.New("path glob must be nonempty and repository-relative")
	}
	for _, part := range strings.Split(pattern, "/") {
		if part == ".." {
			return nil, adapters.ErrInvalidPath
		}
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(globMeta.ReplaceAllString(string(c), `\$1`))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
