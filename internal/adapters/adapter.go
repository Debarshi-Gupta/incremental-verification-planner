// Package adapters defines the shared contract between repository analyzers,
// the planner, and the local execution engine. Adapter inputs are untrusted.
package adapters

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultReportLimit int64 = 16 << 20

type Adapter interface {
	ID() string
	Detect(ctx context.Context, root string) (Detection, error)
	Discover(ctx context.Context, req DiscoverRequest) (DiscoveryResult, error)
	Materialize(ctx context.Context, req MaterializeRequest) (ExecutionCommand, error)
	ParseResults(ctx context.Context, req ParseRequest) (ParsedResults, error)
	ParseCoverage(ctx context.Context, req CoverageRequest) (CoverageResult, error)
	Capabilities() Capabilities
	Version(ctx context.Context, root string) (string, error)
	ValidateConfig(config AdapterConfig) error
}

type Capabilities struct {
	SelectableGranularity string   `json:"selectable_granularity"`
	DependencyPrecision   string   `json:"dependency_precision"`
	CoveragePrecision     string   `json:"coverage_precision"`
	DiscoveryReliable     bool     `json:"discovery_reliable"`
	ResultFormats         []string `json:"result_formats"`
	SupportsCoverage      bool     `json:"supports_coverage"`
	SupportsFiltering     bool     `json:"supports_filtering"`
}

type Detection struct {
	Detected bool              `json:"detected"`
	Root     string            `json:"root,omitempty"`
	Version  string            `json:"version,omitempty"`
	Evidence []string          `json:"evidence,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type DiscoverRequest struct {
	Root           string
	Config         AdapterConfig
	ChangedFiles   []string
	BaseSHA        string
	HeadSHA        string
	Full           bool
	KnownTargets   []string // target keys already trusted by the immutable snapshot
	MaxOutputBytes int64
}

type DiscoveryResult struct {
	Units           []TestUnit `json:"units"`
	Complete        bool       `json:"complete"`
	Reasons         []string   `json:"reasons,omitempty"`
	Version         string     `json:"version,omitempty"`
	AffectedTargets []string   `json:"affected_targets,omitempty"`
}

type TestUnit struct {
	Key                   string        `json:"key"`
	DisplayName           string        `json:"display_name,omitempty"`
	Kind                  string        `json:"kind"`
	Runner                string        `json:"runner,omitempty"`
	Module                string        `json:"module,omitempty"`
	Paths                 []string      `json:"paths,omitempty"`
	Dependencies          []string      `json:"dependencies,omitempty"`
	RiskLabels            []string      `json:"risk_labels,omitempty"`
	Mandatory             bool          `json:"mandatory,omitempty"`
	SelectableGranularity string        `json:"selectable_granularity,omitempty"`
	Filterable            bool          `json:"filterable,omitempty"`
	Timeout               time.Duration `json:"timeout,omitempty"`
	EstimatedDuration     time.Duration `json:"estimated_duration,omitempty"`
	ResourceGroup         string        `json:"resource_group,omitempty"`
	ReportPaths           []string      `json:"report_paths,omitempty"`
	CoveragePaths         []string      `json:"coverage_paths,omitempty"`
	TrustedConfig         bool          `json:"trusted_config,omitempty"`
}

// AdapterConfig is intentionally a narrow, adapter-owned configuration view.
// Application config loaders may decode into these values after strict schema
// validation rather than allowing adapters to inspect arbitrary YAML.
type AdapterConfig struct {
	Enabled            bool         `json:"enabled" yaml:"enabled"`
	Targets            []TargetSpec `json:"targets,omitempty" yaml:"targets,omitempty"`
	AllowedExecutables []string     `json:"allowed_executables,omitempty" yaml:"allowed_executables,omitempty"`
	RedactEnv          []string     `json:"redact_env,omitempty" yaml:"redact_env,omitempty"`
	FullSuiteArgv      []string     `json:"full_suite_argv,omitempty" yaml:"full_suite_argv,omitempty"`
	Options            []string     `json:"options,omitempty" yaml:"options,omitempty"`
	MaxOutputBytes     int64        `json:"max_output_bytes,omitempty" yaml:"max_output_bytes,omitempty"`
}

type TargetSpec struct {
	ID            string        `json:"id" yaml:"id"`
	Kind          string        `json:"kind,omitempty" yaml:"kind"`
	DisplayName   string        `json:"display_name,omitempty" yaml:"display_name,omitempty"`
	Argv          []string      `json:"argv" yaml:"argv"`
	CWD           string        `json:"cwd,omitempty" yaml:"cwd,omitempty"`
	MapsPaths     []string      `json:"maps_paths,omitempty" yaml:"maps_paths,omitempty"`
	DependsOn     []string      `json:"depends_on,omitempty" yaml:"depends_on,omitempty"`
	RiskLabels    []string      `json:"risk_labels,omitempty" yaml:"risk_labels,omitempty"`
	Mandatory     bool          `json:"mandatory,omitempty" yaml:"mandatory,omitempty"`
	Timeout       time.Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	ReportPaths   []string      `json:"report_paths,omitempty" yaml:"report_paths,omitempty"`
	CoveragePaths []string      `json:"coverage_paths,omitempty" yaml:"coverage_paths,omitempty"`
	ResourceGroup string        `json:"resource_group,omitempty" yaml:"resource_group,omitempty"`
	RedactEnv     []string      `json:"redact_env,omitempty" yaml:"redact_env,omitempty"`
}

type MaterializeRequest struct {
	Root           string
	Config         AdapterConfig
	TargetKeys     []string
	KnownTargets   []string   // snapshot target keys; unknown keys are always rejected
	KnownUnits     []TestUnit // immutable snapshot metadata for materialization
	ChangedFiles   []string
	SelectedTests  []string
	BaseSHA        string
	HeadSHA        string
	FullSuite      bool
	Timeout        time.Duration
	MaxOutputBytes int64
}

type ExecutionCommand struct {
	Executable       string            `json:"executable"`
	Args             []string          `json:"args"`
	WorkingDirectory string            `json:"working_directory"`
	Environment      map[string]string `json:"environment,omitempty"`
	Timeout          time.Duration     `json:"timeout"`
	ResourceGroup    string            `json:"resource_group,omitempty"`
	RedactEnv        []string          `json:"redact_env,omitempty"`
	MaxOutputBytes   int64             `json:"max_output_bytes,omitempty"`
	TargetKey        string            `json:"target_key,omitempty"`
	ReportPaths      []string          `json:"report_paths,omitempty"`
	CoveragePaths    []string          `json:"coverage_paths,omitempty"`
	Filtered         bool              `json:"filtered,omitempty"`
	FullSuite        bool              `json:"full_suite,omitempty"`
	CheckOnly        bool              `json:"check_only,omitempty"`
}

type ParseRequest struct {
	Root           string
	ReportPaths    []string
	Stdout         []byte
	Stderr         []byte
	ExitCode       int
	SelectedTests  []string
	MaxReportBytes int64
}

type ParsedResults struct {
	Tests             int           `json:"tests"`
	Passed            int           `json:"passed"`
	Failed            int           `json:"failed"`
	Skipped           int           `json:"skipped"`
	Errors            int           `json:"errors"`
	Duration          time.Duration `json:"duration"`
	Cases             []TestCase    `json:"cases,omitempty"`
	Complete          bool          `json:"complete"`
	HasReliableReport bool          `json:"has_reliable_report"`
	ZeroTests         bool          `json:"zero_tests"`
	Reasons           []string      `json:"reasons,omitempty"`
}

type TestCase struct {
	Name      string        `json:"name"`
	ClassName string        `json:"class_name,omitempty"`
	Status    string        `json:"status"`
	Duration  time.Duration `json:"duration,omitempty"`
	Message   string        `json:"message,omitempty"`
}

type CoverageRequest struct {
	Root           string
	ReportPaths    []string
	Format         string
	MaxReportBytes int64
}

type CoverageResult struct {
	Format       string                  `json:"format"`
	Complete     bool                    `json:"complete"`
	LineCoverage float64                 `json:"line_coverage,omitempty"`
	LinesFound   int64                   `json:"lines_found,omitempty"`
	LinesHit     int64                   `json:"lines_hit,omitempty"`
	Files        map[string]FileCoverage `json:"files,omitempty"`
	Reasons      []string                `json:"reasons,omitempty"`
}

type FileCoverage struct {
	LinesFound int64 `json:"lines_found"`
	LinesHit   int64 `json:"lines_hit"`
}

var (
	ErrInvalidPath   = errors.New("path is outside repository root or invalid")
	ErrUnknownTarget = errors.New("target is not present in trusted snapshot or adapter config")
	ErrUnsafeCommand = errors.New("unsafe command configuration")
)

// ResolveWithin returns a canonical absolute path beneath root. Existing
// symlinks are resolved to prevent a repository-relative symlink escaping the
// workspace. Missing final files are permitted for prospective report paths.
func ResolveWithin(root, name string) (string, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(name) == "" || strings.ContainsRune(name, 0) {
		return "", ErrInvalidPath
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absRoot, err = filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(name) {
		// Absolute paths are accepted only if they resolve under the repository.
	} else {
		for _, segment := range strings.FieldsFunc(strings.ReplaceAll(name, "\\", "/"), func(r rune) bool { return r == '/' }) {
			if segment == ".." {
				return "", ErrInvalidPath
			}
		}
		name = filepath.Clean(name)
		if name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return "", ErrInvalidPath
		}
	}
	resolved := name
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(absRoot, resolved)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	parent := resolved
	for {
		if _, statErr := os.Lstat(parent); statErr == nil {
			parent, err = filepath.EvalSymlinks(parent)
			if err != nil {
				return "", err
			}
			break
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", ErrInvalidPath
		}
		parent = next
	}
	if !within(absRoot, parent) {
		return "", ErrInvalidPath
	}
	if _, statErr := os.Lstat(resolved); statErr == nil {
		resolved, err = filepath.EvalSymlinks(resolved)
		if err != nil {
			return "", err
		}
	}
	if !within(absRoot, resolved) {
		return "", ErrInvalidPath
	}
	return resolved, nil
}

func within(root, candidate string) bool {
	r, err1 := filepath.Rel(root, candidate)
	if err1 != nil {
		return false
	}
	return r == "." || (r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r))
}

func ValidateTargetKeys(requested, snapshot []string, config []TargetSpec) error {
	allowed := make(map[string]struct{}, len(snapshot)+len(config))
	for _, k := range snapshot {
		allowed[k] = struct{}{}
	}
	for _, t := range config {
		allowed[t.ID] = struct{}{}
	}
	for _, k := range requested {
		if _, ok := allowed[k]; !ok {
			return fmt.Errorf("%w: %q", ErrUnknownTarget, k)
		}
	}
	return nil
}
