package maven

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/adapters/native"
)

type Adapter struct{ native.Base }

func New() *Adapter {
	return &Adapter{native.Base{Name: "maven", Binary: "mvn", Caps: adapters.Capabilities{SelectableGranularity: "MAVEN_MODULE_GOAL", DependencyPrecision: "REACTOR_MODULE", CoveragePrecision: "CLASS_OR_FILE", DiscoveryReliable: true, ResultFormats: []string{"surefire-xml", "failsafe-xml"}, SupportsCoverage: true, SupportsFiltering: false}}}
}
func (a *Adapter) ID() string { return "maven" }
func (a *Adapter) tool(root string) string {
	if p := native.FindFirst(root, "mvnw", "mvnw.cmd"); p != "" {
		return p
	}
	return "mvn"
}
func (a *Adapter) Detect(_ context.Context, root string) (adapters.Detection, error) {
	d, e := native.DetectFiles(root, "pom.xml")
	if e == nil && d.Detected {
		d.Metadata = map[string]string{"wrapper": native.Relative(d.Root, a.tool(d.Root))}
	}
	return d, e
}
func (a *Adapter) Version(ctx context.Context, root string) (string, error) {
	out, stderr, e := adapters.RunCapture(ctx, root, a.tool(root), []string{"--version"}, 64<<10)
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

type pom struct {
	XMLName xml.Name
	Modules struct {
		Items []string `xml:"module"`
	} `xml:"modules"`
	Build struct {
		Plugins struct {
			Items []struct {
				Artifact string `xml:"artifactId"`
			} `xml:"plugin"`
		} `xml:"plugins"`
		PluginManagement struct {
			Plugins struct {
				Items []struct {
					Artifact string `xml:"artifactId"`
				} `xml:"plugin"`
			} `xml:"plugins"`
		} `xml:"pluginManagement"`
	} `xml:"build"`
}

func readPOM(root, rel string) (pom, error) {
	var p pom
	abs, e := adapters.ResolveWithin(root, filepath.Join(rel, "pom.xml"))
	if e != nil {
		return p, e
	}
	data, e := os.ReadFile(abs)
	if e != nil {
		return p, e
	}
	if len(data) > 8<<20 {
		return p, errors.New("POM exceeds 8 MiB")
	}
	e = xml.Unmarshal(data, &p)
	return p, e
}
func hasFailsafe(p pom) bool {
	for _, x := range p.Build.Plugins.Items {
		if x.Artifact == "maven-failsafe-plugin" {
			return true
		}
	}
	for _, x := range p.Build.PluginManagement.Plugins.Items {
		if x.Artifact == "maven-failsafe-plugin" {
			return true
		}
	}
	return false
}
func (a *Adapter) Discover(ctx context.Context, req adapters.DiscoverRequest) (adapters.DiscoveryResult, error) {
	if e := a.ValidateConfig(req.Config); e != nil {
		return adapters.DiscoveryResult{Complete: false, Reasons: []string{e.Error()}}, e
	}
	result := adapters.DiscoveryResult{Complete: true}
	queue := []string{"."}
	seen := map[string]bool{}
	for len(queue) > 0 {
		if e := ctx.Err(); e != nil {
			return result, e
		}
		module := queue[0]
		queue = queue[1:]
		module = filepath.Clean(module)
		if seen[module] {
			continue
		}
		seen[module] = true
		p, e := readPOM(req.Root, module)
		if e != nil {
			if module == "." {
				result.Complete = false
				result.Reasons = append(result.Reasons, "root Maven POM could not be parsed")
				return result, e
			}
			result.Complete = false
			result.Reasons = append(result.Reasons, "Maven reactor module POM could not be parsed: "+filepath.ToSlash(module))
			continue
		}
		keyPath := filepath.ToSlash(module)
		if keyPath == "." {
			keyPath = ""
		}
		modulePaths := []string{keyPath}
		if keyPath == "" {
			modulePaths = []string{"."}
		}
		base := keyPath + "#test"
		result.Units = append(result.Units, adapters.TestUnit{Key: base, DisplayName: "Maven unit tests " + keyPath, Kind: "PROJECT_SUITE", Module: keyPath, Paths: modulePaths, SelectableGranularity: "MAVEN_MODULE_GOAL", Filterable: false, Timeout: 30 * time.Minute, ReportPaths: []string{filepath.ToSlash(filepath.Join(keyPath, "target", "surefire-reports", "TEST-*.xml"))}})
		if hasFailsafe(p) {
			key := keyPath + "#verify"
			result.Units = append(result.Units, adapters.TestUnit{Key: key, DisplayName: "Maven integration tests " + keyPath, Kind: "PROJECT_SUITE", Module: keyPath, Paths: modulePaths, SelectableGranularity: "MAVEN_MODULE_GOAL", Filterable: false, Timeout: 45 * time.Minute, ReportPaths: []string{filepath.ToSlash(filepath.Join(keyPath, "target", "failsafe-reports", "TEST-*.xml"))}})
		}
		for _, child := range p.Modules.Items {
			child = strings.TrimSpace(child)
			if child == "" || filepath.IsAbs(child) || strings.ContainsAny(child, "\\\x00") {
				result.Complete = false
				result.Reasons = append(result.Reasons, "invalid Maven module path")
				continue
			}
			joined := filepath.Clean(filepath.Join(module, child))
			if joined == ".." || strings.HasPrefix(joined, ".."+string(filepath.Separator)) {
				result.Complete = false
				result.Reasons = append(result.Reasons, "Maven module escapes repository root")
				continue
			}
			queue = append(queue, joined)
		}
	}
	if len(result.Units) == 0 {
		result.Complete = false
		result.Reasons = append(result.Reasons, "no Maven modules discovered")
	}
	version, _ := a.Version(ctx, req.Root)
	result.Version = version
	return result, nil
}
func (a *Adapter) Materialize(_ context.Context, req adapters.MaterializeRequest) (adapters.ExecutionCommand, error) {
	if e := a.ValidateConfig(req.Config); e != nil {
		return adapters.ExecutionCommand{}, e
	}
	tool := a.tool(req.Root)
	if req.FullSuite {
		if len(req.Config.FullSuiteArgv) == 0 {
			return adapters.ExecutionCommand{}, errors.New("Maven full-suite command must be configured")
		}
		return configuredFull(req)
	}
	if e := adapters.ValidateTargetKeys(req.TargetKeys, req.KnownTargets, nil); e != nil {
		return adapters.ExecutionCommand{}, e
	}
	if len(req.TargetKeys) != 1 {
		return adapters.ExecutionCommand{}, errors.New("Maven materializes one module goal per command")
	}
	key := req.TargetKeys[0]
	ix := strings.LastIndexByte(key, '#')
	if ix < 0 {
		return adapters.ExecutionCommand{}, fmt.Errorf("invalid Maven target key %q", key)
	}
	module, goal := key[:ix], key[ix+1:]
	if goal != "test" && goal != "verify" {
		return adapters.ExecutionCommand{}, fmt.Errorf("unsupported Maven goal %q", goal)
	}
	if module != "" {
		if _, e := adapters.ResolveWithin(req.Root, module); e != nil {
			return adapters.ExecutionCommand{}, e
		}
	}
	args := append([]string(nil), req.Config.Options...)
	if module != "" {
		args = append(args, "-pl", module, "-am")
	}
	args = append(args, goal)
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
	reportDir := filepath.Join(module, "target")
	reportName := "surefire-reports"
	if goal == "verify" {
		reportName = "failsafe-reports"
	}
	report := filepath.ToSlash(filepath.Join(reportDir, reportName, "TEST-*.xml"))
	return adapters.ExecutionCommand{Executable: tool, Args: args, WorkingDirectory: cwd, Environment: map[string]string{}, Timeout: timeout, RedactEnv: append([]string(nil), req.Config.RedactEnv...), MaxOutputBytes: maxOut, TargetKey: key, ReportPaths: []string{report}}, nil
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
