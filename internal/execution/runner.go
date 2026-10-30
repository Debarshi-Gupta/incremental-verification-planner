// Package execution runs adapter-materialized argv commands inside a local
// repository and classifies the result without upgrading targeted evidence to
// a full-verification claim.
package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/domain"
)

type Status string

const (
	StatusCompleted  Status = "COMPLETED"
	StatusCancelled  Status = "CANCELLED"
	StatusTimedOut   Status = "TIMED_OUT"
	StatusInfraError Status = "INFRA_ERROR"
	StatusStalePlan  Status = "STALE_PLAN"
	StatusEscalated  Status = "ESCALATED"
)

var (
	ErrSelectiveUnsupported = errors.New("selective execution is unsupported on this operating system")
	ErrInvalidCommand       = errors.New("invalid execution command")
)

type RunnerOptions struct {
	MaxConcurrent      int
	MaxOutputBytes     int64
	AllowedEnvironment []string
	TerminationGrace   time.Duration
}

type Runner struct {
	root      string
	options   RunnerOptions
	semaphore chan struct{}
	groupMu   sync.Mutex
	groups    map[string]chan struct{}
}

type Result struct {
	ID              uuid.UUID               `json:"id"`
	Status          Status                  `json:"status"`
	Outcome         domain.ExecutionOutcome `json:"outcome"`
	StartedAt       time.Time               `json:"started_at"`
	CompletedAt     time.Time               `json:"completed_at"`
	Duration        time.Duration           `json:"duration"`
	ExitCode        *int                    `json:"exit_code,omitempty"`
	ErrorClass      string                  `json:"error_class,omitempty"`
	Error           string                  `json:"error,omitempty"`
	StdoutExcerpt   string                  `json:"stdout_excerpt,omitempty"`
	StderrExcerpt   string                  `json:"stderr_excerpt,omitempty"`
	StdoutHash      string                  `json:"stdout_hash,omitempty"`
	StderrHash      string                  `json:"stderr_hash,omitempty"`
	OutputBytes     int64                   `json:"output_bytes"`
	OutputTruncated bool                    `json:"output_truncated"`
	Parsed          *adapters.ParsedResults `json:"parsed,omitempty"`
}

func NewRunner(root string, options RunnerOptions) (*Runner, error) {
	abs, err := adapters.ResolveWithin(root, ".")
	if err != nil {
		return nil, err
	}
	if options.MaxConcurrent <= 0 {
		options.MaxConcurrent = 1
	}
	if options.MaxOutputBytes <= 0 {
		options.MaxOutputBytes = 1 << 20
	}
	if options.TerminationGrace <= 0 {
		options.TerminationGrace = 2 * time.Second
	}
	if options.AllowedEnvironment == nil {
		options.AllowedEnvironment = []string{"PATH", "HOME", "USERPROFILE", "SYSTEMROOT", "WINDIR", "TMP", "TEMP", "TMPDIR", "CI", "LANG", "LC_ALL"}
	}
	sem := make(chan struct{}, options.MaxConcurrent)
	return &Runner{root: abs, options: options, semaphore: sem, groups: map[string]chan struct{}{}}, nil
}

func (r *Runner) Run(ctx context.Context, command adapters.ExecutionCommand) Result {
	result := Result{ID: uuid.New(), Status: StatusInfraError, Outcome: domain.OutcomeInfraFailure, StartedAt: time.Now().UTC()}
	finish := func() {
		result.CompletedAt = time.Now().UTC()
		result.Duration = result.CompletedAt.Sub(result.StartedAt)
	}
	if err := r.validate(command); err != nil {
		result.Status = StatusInfraError
		result.ErrorClass = "INVALID_COMMAND"
		result.Error = err.Error()
		finish()
		return result
	}
	if !command.FullSuite && isWindows() {
		result.Status = StatusEscalated
		result.ErrorClass = "UNSUPPORTED_OS"
		result.Error = ErrSelectiveUnsupported.Error()
		finish()
		return result
	}
	select {
	case r.semaphore <- struct{}{}:
		defer func() { <-r.semaphore }()
	case <-ctx.Done():
		result.Status = StatusCancelled
		result.ErrorClass = "CANCELLED"
		result.Error = ctx.Err().Error()
		finish()
		return result
	}
	if command.ResourceGroup != "" {
		r.groupMu.Lock()
		group := r.groups[command.ResourceGroup]
		if group == nil {
			group = make(chan struct{}, 1)
			r.groups[command.ResourceGroup] = group
		}
		r.groupMu.Unlock()
		select {
		case group <- struct{}{}:
			defer func() { <-group }()
		case <-ctx.Done():
			result.Status = StatusCancelled
			result.ErrorClass = "CANCELLED"
			result.Error = ctx.Err().Error()
			finish()
			return result
		}
	}

	// Resolve bare tools before constructing a child environment that contains
	// only the configured allowlist.
	executable := command.Executable
	if !filepath.IsAbs(executable) && strings.ContainsAny(executable, "/\\") {
		resolved, err := adapters.ResolveWithin(r.root, executable)
		if err != nil {
			result.ErrorClass = "INVALID_EXECUTABLE_PATH"
			result.Error = err.Error()
			finish()
			return result
		}
		executable = resolved
	} else {
		found, err := exec.LookPath(executable)
		if err != nil {
			result.ErrorClass = "COMMAND_NOT_FOUND"
			result.Error = err.Error()
			finish()
			return result
		}
		executable = found
	}
	childEnv, secretValues, err := r.environment(command)
	if err != nil {
		result.ErrorClass = "ENVIRONMENT_REJECTED"
		result.Error = err.Error()
		finish()
		return result
	}
	limit := command.MaxOutputBytes
	if limit <= 0 || limit > r.options.MaxOutputBytes {
		limit = r.options.MaxOutputBytes
	}
	group, err := newCaptureGroup(limit)
	if err != nil {
		result.ErrorClass = "OUTPUT_CAPTURE"
		result.Error = err.Error()
		finish()
		return result
	}
	defer group.Close()
	// Reports must have a workspace-local parent available before the native
	// runner writes them. Report paths themselves are rechecked by the parser.
	if err := r.prepareReportDirs(command.ReportPaths); err != nil {
		result.ErrorClass = "REPORT_PATH"
		result.Error = err.Error()
		finish()
		return result
	}
	workdir, err := adapters.ResolveWithin(r.root, command.WorkingDirectory)
	if err != nil {
		result.ErrorClass = "WORKING_DIRECTORY"
		result.Error = err.Error()
		finish()
		return result
	}
	cmd := exec.Command(executable, command.Args...)
	cmd.Dir = workdir
	cmd.Env = childEnv
	cmd.Stdout = group.Stream("stdout")
	cmd.Stderr = group.Stream("stderr")
	configureProcess(cmd)
	processCtx := ctx
	cancel := func() {}
	if command.Timeout > 0 {
		processCtx, cancel = context.WithTimeout(ctx, command.Timeout)
	}
	defer cancel()
	if err := processCtx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			result.Status = StatusTimedOut
			result.ErrorClass = "TIMEOUT"
		} else {
			result.Status = StatusCancelled
			result.ErrorClass = "CANCELLED"
		}
		result.Error = err.Error()
		finish()
		return result
	}
	if err := cmd.Start(); err != nil {
		result.ErrorClass = classifyStartError(err)
		result.Error = err.Error()
		finish()
		return result
	}
	wait := make(chan error, 1)
	done := make(chan struct{})
	go func() { wait <- cmd.Wait(); close(done) }()
	var waitErr error
	select {
	case waitErr = <-wait:
	case <-processCtx.Done():
		status := StatusCancelled
		errClass := "CANCELLED"
		if errors.Is(processCtx.Err(), context.DeadlineExceeded) {
			status = StatusTimedOut
			errClass = "TIMEOUT"
		}
		terminateProcess(cmd, done, r.options.TerminationGrace)
		waitErr = <-wait
		result.Status = status
		result.ErrorClass = errClass
		result.Error = processCtx.Err().Error()
	}

	outBytes, errOutBytes, truncated, outHash, errHash := group.Summary()
	result.OutputBytes = outBytes + errOutBytes
	result.OutputTruncated = truncated
	stdout, stderr := group.Excerpts()
	result.StdoutExcerpt = sanitize(stdout, secretValues, command.RedactEnv)
	result.StderrExcerpt = sanitize(stderr, secretValues, command.RedactEnv)
	result.StdoutHash = outHash
	result.StderrHash = errHash
	if code := cmd.ProcessState; code != nil {
		n := code.ExitCode()
		result.ExitCode = &n
	}
	if result.Status == StatusInfraError {
		if waitErr == nil {
			result.Status = StatusCompleted
			result.Outcome = domain.OutcomeInfraFailure
		} else {
			result.Status = StatusCompleted
			result.Error = waitErr.Error()
			if result.ExitCode != nil {
				result.ErrorClass = "PROCESS_EXIT"
			} else {
				result.ErrorClass = "PROCESS_FAILURE"
			}
		}
	}
	if result.Status == StatusCompleted && result.ExitCode != nil && *result.ExitCode == 0 {
		result.Outcome = domain.OutcomeInfraFailure
	}
	if result.Status == StatusTimedOut || result.Status == StatusCancelled {
		result.Outcome = domain.OutcomeInfraFailure
	}
	finish()
	return result
}

// RunAndParse turns a process exit plus a reliable native report into a
// canonical outcome. Missing, malformed, or zero-test reports stay infra
// failures, and a targeted pass is always distinct from a full verification.
func (r *Runner) RunAndParse(ctx context.Context, adapter adapters.Adapter, command adapters.ExecutionCommand, req adapters.ParseRequest) Result {
	reportPatterns := append([]string(nil), req.ReportPaths...)
	if len(reportPatterns) == 0 {
		reportPatterns = append(reportPatterns, command.ReportPaths...)
	}
	reportsBefore := FingerprintReports(r.root, reportPatterns)
	result := r.Run(ctx, command)
	if result.Status != StatusCompleted {
		return result
	}
	if result.ExitCode == nil {
		result.Status = StatusInfraError
		result.ErrorClass = "NO_EXIT_STATUS"
		result.Error = "command completed without an exit status"
		return result
	}
	if command.CheckOnly {
		parsed := adapters.ParsedResults{Tests: 1, Complete: true, HasReliableReport: true}
		if *result.ExitCode == 0 {
			parsed.Passed = 1
		} else {
			parsed.Failed = 1
		}
		result.Parsed = &parsed
		if command.FullSuite {
			if parsed.Failed > 0 {
				result.Outcome = domain.OutcomeFullVerificationFail
			} else {
				result.Outcome = domain.OutcomeFullVerificationPass
			}
		} else {
			if parsed.Failed > 0 {
				result.Outcome = domain.OutcomeTargetedFail
			} else {
				result.Outcome = domain.OutcomeTargetedPass
			}
		}
		return result
	}
	req.Root = r.root
	req.Stdout = []byte(result.StdoutExcerpt)
	req.Stderr = []byte(result.StderrExcerpt)
	req.ExitCode = *result.ExitCode
	if len(reportPatterns) > 0 {
		req.ReportPaths = FreshReports(r.root, reportPatterns, reportsBefore)
		if len(req.ReportPaths) == 0 {
			result.Status = StatusInfraError
			result.Outcome = domain.OutcomeInfraFailure
			result.ErrorClass = "REPORT_STALE"
			result.Error = "the command produced no fresh result report"
			return result
		}
	}
	if req.MaxReportBytes <= 0 {
		req.MaxReportBytes = adapters.DefaultReportLimit
	}
	parsed, err := adapter.ParseResults(ctx, req)
	result.Parsed = &parsed
	_, secretValues, _ := r.environment(command)
	for i := range parsed.Cases {
		parsed.Cases[i].Name = sanitize(parsed.Cases[i].Name, secretValues, nil)
		parsed.Cases[i].ClassName = sanitize(parsed.Cases[i].ClassName, secretValues, nil)
		parsed.Cases[i].Message = sanitize(parsed.Cases[i].Message, secretValues, nil)
	}
	result.Parsed = &parsed
	if err != nil || !parsed.Complete || !parsed.HasReliableReport {
		result.Status = StatusInfraError
		result.Outcome = domain.OutcomeInfraFailure
		result.ErrorClass = "REPORT_PARSE"
		if err != nil {
			result.Error = sanitize(err.Error(), secretValues, command.RedactEnv)
		} else {
			result.Error = "adapter result report is incomplete"
		}
		return result
	}
	if parsed.ZeroTests || parsed.Tests == 0 {
		result.Status = StatusInfraError
		result.Outcome = domain.OutcomeInfraFailure
		result.ErrorClass = "ZERO_TESTS"
		result.Error = "the selected command reported zero tests"
		return result
	}
	failed := parsed.Failed+parsed.Errors > 0 || *result.ExitCode != 0
	if command.FullSuite {
		if failed {
			result.Outcome = domain.OutcomeFullVerificationFail
		} else {
			result.Outcome = domain.OutcomeFullVerificationPass
		}
	} else {
		if failed {
			result.Outcome = domain.OutcomeTargetedFail
		} else {
			result.Outcome = domain.OutcomeTargetedPass
		}
	}
	return result
}

// ReportFingerprint captures the identity and modification state of a
// repository-local report before a command starts. Comparing both content and
// modification time recognizes freshly rewritten but byte-identical reports
// while rejecting stale reports left by a prior run.
type ReportFingerprint struct {
	Hash         string
	Size         int64
	ModifiedNanos int64
}

func FingerprintReports(root string, patterns []string) map[string]ReportFingerprint {
	result := make(map[string]ReportFingerprint)
	for _, file := range expandReportPaths(root, patterns) {
		info, err := os.Lstat(file)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > adapters.DefaultReportLimit {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		digest := sha256.Sum256(data)
		result[filepath.Clean(file)] = ReportFingerprint{Hash: hex.EncodeToString(digest[:]), Size: info.Size(), ModifiedNanos: info.ModTime().UnixNano()}
	}
	return result
}

// FreshReports returns only concrete, safe report paths that were created or
// changed by the latest command. Parsers never consume stale files matching a
// configured glob.
func FreshReports(root string, patterns []string, before map[string]ReportFingerprint) []string {
	current := FingerprintReports(root, patterns)
	paths := make([]string, 0, len(current))
	for file, fingerprint := range current {
		previous, existed := before[filepath.Clean(file)]
		if existed && previous == fingerprint {
			continue
		}
		rel, err := filepath.Rel(root, file)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		paths = append(paths, filepath.ToSlash(rel))
	}
	sort.Strings(paths)
	return paths
}

func expandReportPaths(root string, patterns []string) []string {
	seen := make(map[string]bool)
	var files []string
	for _, pattern := range patterns {
		resolved, err := adapters.ResolveWithin(root, pattern)
		if err != nil {
			continue
		}
		matches := []string{resolved}
		if strings.ContainsAny(resolved, "*?[") {
			matches, err = filepath.Glob(resolved)
			if err != nil {
				continue
			}
		}
		for _, match := range matches {
			rel, err := filepath.Rel(root, match)
			if err != nil {
				continue
			}
			file, err := adapters.ResolveWithin(root, rel)
			if err != nil || seen[filepath.Clean(file)] {
				continue
			}
			seen[filepath.Clean(file)] = true
			files = append(files, filepath.Clean(file))
		}
	}
	return files
}

func (r *Runner) validate(c adapters.ExecutionCommand) error {
	if strings.TrimSpace(c.Executable) == "" || strings.ContainsRune(c.Executable, 0) {
		return ErrInvalidCommand
	}
	for _, arg := range c.Args {
		if strings.ContainsRune(arg, 0) {
			return ErrInvalidCommand
		}
	}
	if _, err := adapters.ResolveWithin(r.root, c.WorkingDirectory); err != nil {
		return fmt.Errorf("%w: working directory: %v", ErrInvalidCommand, err)
	}
	if !filepath.IsAbs(c.Executable) && strings.ContainsAny(c.Executable, "/\\") {
		if _, err := adapters.ResolveWithin(r.root, c.Executable); err != nil {
			return fmt.Errorf("%w: executable path escapes repository", ErrInvalidCommand)
		}
	}
	for name, value := range c.Environment {
		if name == "" || strings.ContainsAny(name, "=\x00") || strings.ContainsRune(value, 0) {
			return fmt.Errorf("%w: invalid environment entry", ErrInvalidCommand)
		}
	}
	for _, p := range c.ReportPaths {
		check := p
		if strings.ContainsAny(p, "*?[") {
			check = reportParent(p)
		}
		if _, err := adapters.ResolveWithin(r.root, check); err != nil {
			return fmt.Errorf("%w: report path escapes repository", ErrInvalidCommand)
		}
	}
	return nil
}
func reportParent(p string) string {
	p = filepath.Clean(p)
	if strings.ContainsAny(p, "*?[") {
		for i := 0; i < len(p); i++ {
			if p[i] == '*' || p[i] == '?' || p[i] == '[' {
				prefix := p[:i]
				return filepath.Dir(prefix)
			}
		}
	}
	return filepath.Dir(p)
}
func (r *Runner) prepareReportDirs(paths []string) error {
	for _, p := range paths {
		if strings.ContainsAny(p, "*?[") {
			continue
		}
		abs, err := adapters.ResolveWithin(r.root, filepath.Dir(p))
		if err != nil {
			return err
		}
		if err := os.MkdirAll(abs, 0o700); err != nil {
			return err
		}
	}
	return nil
}
func (r *Runner) environment(c adapters.ExecutionCommand) ([]string, []string, error) {
	allow := map[string]bool{}
	for _, n := range r.options.AllowedEnvironment {
		allow[n] = true
	}
	redact := map[string]bool{}
	for _, n := range c.RedactEnv {
		redact[n] = true
	}
	values := map[string]string{}
	for n := range allow {
		if v, ok := os.LookupEnv(n); ok {
			values[n] = v
		}
	}
	for n, v := range c.Environment {
		if !allow[n] {
			return nil, nil, fmt.Errorf("environment variable %q is not allowlisted", n)
		}
		values[n] = v
	}
	names := make([]string, 0, len(values))
	secrets := make([]string, 0)
	for n, v := range values {
		names = append(names, n+"="+v)
		if (secretName.MatchString(n) || redact[n]) && v != "" {
			secrets = append(secrets, v)
		}
	}
	sort.Strings(names)
	return names, secrets, nil
}

func classifyStartError(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "COMMAND_NOT_FOUND"
	}
	if errors.Is(err, os.ErrPermission) {
		return "PERMISSION_DENIED"
	}
	return "PROCESS_START"
}

var ansiPattern = regexp.MustCompile(`\x1b(\[[0-?]*[ -/]*[@-~]|\][^\x07]*(\x07|\x1b\\))`)
var secretName = regexp.MustCompile(`(?i)(TOKEN|SECRET|PASSWORD|PASSWD|API.?KEY|PRIVATE.?KEY|CREDENTIAL|AUTH)`)
var credentialPattern = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/-]+=*|((api[_-]?key|token|password|secret|authorization)\s*[:=]\s*)[^\s,;]+`)

func sanitize(text string, secrets, configured []string) string {
	for _, value := range secrets {
		if value != "" {
			text = strings.ReplaceAll(text, value, "[REDACTED]")
		}
	}
	_ = configured
	text = ansiPattern.ReplaceAllString(text, "")
	text = credentialPattern.ReplaceAllStringFunc(text, func(s string) string {
		if strings.Contains(strings.ToLower(s), "bearer") {
			return "Bearer [REDACTED]"
		}
		if i := strings.IndexAny(s, "=:"); i >= 0 {
			return s[:i+1] + "[REDACTED]"
		}
		return "[REDACTED]"
	})
	var cleaned strings.Builder
	for _, r := range text {
		if (r == '\n' || r == '\r' || r == '\t') || r >= 0x20 && !isControlRune(r) {
			cleaned.WriteRune(r)
		}
	}
	return cleaned.String()
}
func isControlRune(r rune) bool { return r < 0x20 || r >= 0x7f && r <= 0x9f }

type captureGroup struct {
	mu        sync.Mutex
	limit     int64
	stored    int64
	totalOut  int64
	totalErr  int64
	truncated bool
	outFile   *os.File
	errFile   *os.File
	outHash   [32]byte
	errHash   [32]byte
	outDigest io.Writer
	errDigest io.Writer
	outBytes  []byte
	errBytes  []byte
}
type captureWriter struct {
	g      *captureGroup
	stdout bool
}

func newCaptureGroup(limit int64) (*captureGroup, error) {
	out, e := os.CreateTemp("", "ivp-out-*.log")
	if e != nil {
		return nil, e
	}
	errFile, e := os.CreateTemp("", "ivp-err-*.log")
	if e != nil {
		out.Close()
		os.Remove(out.Name())
		return nil, e
	}
	g := &captureGroup{limit: limit, outFile: out, errFile: errFile, outDigest: sha256.New(), errDigest: sha256.New()}
	return g, nil
}
func (g *captureGroup) Stream(name string) io.Writer {
	return &captureWriter{g: g, stdout: name == "stdout"}
}
func (w *captureWriter) Write(p []byte) (int, error) {
	g := w.g
	g.mu.Lock()
	defer g.mu.Unlock()
	if w.stdout {
		g.totalOut += int64(len(p))
		_, _ = g.outDigest.Write(p)
	} else {
		g.totalErr += int64(len(p))
		_, _ = g.errDigest.Write(p)
	}
	remaining := g.limit - g.stored
	if remaining > 0 {
		n := int64(len(p))
		if n > remaining {
			n = remaining
			g.truncated = true
		}
		file := g.errFile
		if w.stdout {
			file = g.outFile
		}
		if n > 0 {
			if _, err := file.Write(p[:n]); err != nil {
				return 0, err
			}
			g.stored += n
		}
	} else if len(p) > 0 {
		g.truncated = true
	}
	return len(p), nil
}
func (g *captureGroup) Summary() (int64, int64, bool, string, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.totalOut, g.totalErr, g.truncated, hashWriter(g.outDigest), hashWriter(g.errDigest)
}
func hashWriter(w io.Writer) string {
	h, ok := w.(interface{ Sum([]byte) []byte })
	if !ok {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}
func (g *captureGroup) Excerpts() (string, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	out, _ := os.ReadFile(g.outFile.Name())
	err, _ := os.ReadFile(g.errFile.Name())
	return string(out), string(err)
}
func (g *captureGroup) Close() {
	if g == nil {
		return
	}
	if g.outFile != nil {
		name := g.outFile.Name()
		_ = g.outFile.Close()
		_ = os.Remove(name)
	}
	if g.errFile != nil {
		name := g.errFile.Name()
		_ = g.errFile.Close()
		_ = os.Remove(name)
	}
}

type PlanRevalidation struct {
	CurrentWorktreeHash string
	PolicyVersion       int64
	AlgorithmVersion    string
	Now                 time.Time
	KnownTargetRefs     map[string]bool
	MandatoryChecks     []string
}

func ValidatePlan(plan domain.VerificationPlan, v PlanRevalidation) error {
	if v.Now.IsZero() {
		v.Now = time.Now().UTC()
	}
	if plan.Status != domain.PlanReady {
		return fmt.Errorf("plan is not executable: %s", plan.Status)
	}
	if plan.ExpiresAt.IsZero() || !v.Now.Before(plan.ExpiresAt) {
		return errors.New("plan expired or has no expiry")
	}
	if plan.WorktreeHash == "" || v.CurrentWorktreeHash == "" || plan.WorktreeHash != v.CurrentWorktreeHash {
		return errors.New("worktree fingerprint missing or changed")
	}
	if v.PolicyVersion == 0 || plan.PolicyVersion != v.PolicyVersion {
		return errors.New("policy version changed or unavailable")
	}
	if v.AlgorithmVersion == "" || plan.AlgorithmVersion != v.AlgorithmVersion {
		return errors.New("algorithm version changed or unavailable")
	}
	if len(plan.SelectedUnits)+len(plan.MandatoryUnits) > 0 && v.KnownTargetRefs == nil {
		return errors.New("selected target inventory is unavailable")
	}
	selected := map[string]bool{}
	for _, u := range plan.SelectedUnits {
		key := u.TargetRef
		if key == "" {
			key = u.StableKey
		}
		if key == "" {
			return errors.New("selected unit has no trusted target reference")
		}
		selected[key] = true
		if v.KnownTargetRefs != nil && !v.KnownTargetRefs[key] {
			return fmt.Errorf("selected target %q is no longer in the snapshot", key)
		}
	}
	for _, u := range plan.MandatoryUnits {
		key := u.TargetRef
		if key == "" {
			key = u.StableKey
		}
		if key == "" {
			return errors.New("mandatory unit has no trusted target reference")
		}
		selected[key] = true
		if v.KnownTargetRefs != nil && !v.KnownTargetRefs[key] {
			return fmt.Errorf("mandatory target %q is no longer in the snapshot", key)
		}
	}
	for _, required := range v.MandatoryChecks {
		if !selected[required] {
			return fmt.Errorf("mandatory check %q is missing from the plan", required)
		}
	}
	return nil
}
