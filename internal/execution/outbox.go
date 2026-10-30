package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/domain"
)

const DefaultOutboxBytes int64 = 32 << 20

type LocalReport struct {
	ExecutionID    uuid.UUID               `json:"execution_id"`
	IdempotencyKey string                  `json:"idempotency_key"`
	PlanID         uuid.UUID               `json:"plan_id"`
	Outcome        domain.ExecutionOutcome `json:"outcome"`
	Status         Status                  `json:"status"`
	StartedAt      time.Time               `json:"started_at"`
	CompletedAt    time.Time               `json:"completed_at"`
	ExitCode       *int                    `json:"exit_code,omitempty"`
	ErrorClass     string                  `json:"error_class,omitempty"`
	OutputHashes   []string                `json:"output_hashes,omitempty"`
	StdoutExcerpt  string                  `json:"stdout_excerpt,omitempty"`
	StderrExcerpt  string                  `json:"stderr_excerpt,omitempty"`
	Parsed         *adapters.ParsedResults `json:"parsed,omitempty"`
	CreatedAt      time.Time               `json:"created_at"`
}

type Outbox struct {
	mu         sync.Mutex
	root       string
	maxBytes   int64
	maxRecords int
}

func NewOutbox(root string, maxBytes int64, maxRecords int) (*Outbox, error) {
	abs, e := adapters.ResolveWithin(root, ".")
	if e != nil {
		return nil, e
	}
	if maxBytes <= 0 {
		maxBytes = DefaultOutboxBytes
	}
	if maxRecords <= 0 {
		maxRecords = 500
	}
	return &Outbox{root: abs, maxBytes: maxBytes, maxRecords: maxRecords}, nil
}

func (o *Outbox) Save(planID uuid.UUID, result Result) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if result.ID == uuid.Nil {
		result.ID = uuid.New()
	}
	report := LocalReport{ExecutionID: result.ID, IdempotencyKey: result.ID.String(), PlanID: planID, Outcome: result.Outcome, Status: result.Status, StartedAt: result.StartedAt, CompletedAt: result.CompletedAt, ExitCode: result.ExitCode, ErrorClass: result.ErrorClass, OutputHashes: nonempty(result.StdoutHash, result.StderrHash), StdoutExcerpt: result.StdoutExcerpt, StderrExcerpt: result.StderrExcerpt, Parsed: result.Parsed, CreatedAt: time.Now().UTC()}
	data, e := json.Marshal(report)
	if e != nil {
		return "", e
	}
	if int64(len(data)) > o.maxBytes {
		return "", errors.New("execution report exceeds outbox size limit")
	}
	dir := filepath.Join(o.root, ".ivp", "outbox")
	if _, e = adapters.ResolveWithin(o.root, filepath.Join(".ivp", "outbox")); e != nil {
		return "", e
	}
	if e = os.MkdirAll(dir, 0o700); e != nil {
		return "", e
	}
	if _, e = adapters.ResolveWithin(o.root, filepath.Join(".ivp", "outbox")); e != nil {
		return "", e
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return "", e
	}
	var used int64
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		count++
		info, er := entry.Info()
		if er == nil {
			used += info.Size()
		}
	}
	if count >= o.maxRecords || used+int64(len(data)) > o.maxBytes {
		return "", errors.New("execution outbox capacity reached")
	}
	name := filepath.Join(dir, result.ID.String()+".json")
	tmp, e := os.CreateTemp(dir, ".pending-*.tmp")
	if e != nil {
		return "", e
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, e = tmp.Write(data); e != nil {
		tmp.Close()
		return "", e
	}
	if e = tmp.Sync(); e != nil {
		tmp.Close()
		return "", e
	}
	if e = tmp.Close(); e != nil {
		return "", e
	}
	// Keep rename-based commit on Unix. Windows sandboxed workspaces can deny
	// rename even within one directory, so use an exclusive bounded write there.
	if runtime.GOOS != "windows" {
		if e = os.Rename(tmpName, name); e != nil {
			return "", e
		}
		return name, nil
	}
	dst, e := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if e != nil {
		return "", e
	}
	remaining := data
	for len(remaining) > 0 {
		written, writeErr := dst.Write(remaining)
		if writeErr != nil {
			_ = dst.Close()
			_ = os.Remove(name)
			return "", writeErr
		}
		if written == 0 {
			_ = dst.Close()
			_ = os.Remove(name)
			return "", io.ErrShortWrite
		}
		remaining = remaining[written:]
	}
	if e = dst.Sync(); e != nil {
		_ = dst.Close()
		_ = os.Remove(name)
		return "", e
	}
	if e = dst.Close(); e != nil {
		_ = os.Remove(name)
		return "", e
	}
	return name, nil
}

type UploadFunc func(context.Context, LocalReport) error

// RetryUploads only retries saved reports. It never starts the associated test
// command, and the stable idempotency key is preserved for every attempt.
func (o *Outbox) RetryUploads(ctx context.Context, upload UploadFunc) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if upload == nil {
		return 0, errors.New("upload function is required")
	}
	dir := filepath.Join(o.root, ".ivp", "outbox")
	entries, e := os.ReadDir(dir)
	if os.IsNotExist(e) {
		return 0, nil
	}
	if e != nil {
		return 0, e
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	sent := 0
	var used int64
	for _, name := range names {
		if e := ctx.Err(); e != nil {
			return sent, e
		}
		abs, e := adapters.ResolveWithin(o.root, filepath.Join(".ivp", "outbox", name))
		if e != nil {
			return sent, e
		}
		info, e := os.Stat(abs)
		if e != nil {
			return sent, e
		}
		used += info.Size()
		if used > o.maxBytes {
			return sent, errors.New("outbox retry exceeds configured byte limit")
		}
		data, e := os.ReadFile(abs)
		if e != nil {
			return sent, e
		}
		if int64(len(data)) > o.maxBytes {
			return sent, errors.New("outbox record exceeds configured byte limit")
		}
		var report LocalReport
		if e = json.Unmarshal(data, &report); e != nil {
			return sent, fmt.Errorf("invalid outbox record %s: %w", name, e)
		}
		if report.ExecutionID == uuid.Nil || report.IdempotencyKey != report.ExecutionID.String() {
			return sent, fmt.Errorf("invalid idempotency key in outbox record %s", name)
		}
		if e = upload(ctx, report); e != nil {
			return sent, e
		}
		if e = os.Remove(abs); e != nil {
			return sent, e
		}
		sent++
	}
	return sent, nil
}

func (o *Outbox) PersistThenUpload(ctx context.Context, planID uuid.UUID, result Result, upload UploadFunc) (string, error) {
	if result.ID == uuid.Nil {
		result.ID = uuid.New()
	}
	path, e := o.Save(planID, result)
	if e != nil {
		return "", e
	}
	if upload == nil {
		return path, nil
	}
	report := LocalReport{ExecutionID: result.ID, IdempotencyKey: result.ID.String(), PlanID: planID, Outcome: result.Outcome, Status: result.Status, StartedAt: result.StartedAt, CompletedAt: result.CompletedAt, ExitCode: result.ExitCode, ErrorClass: result.ErrorClass, OutputHashes: nonempty(result.StdoutHash, result.StderrHash), StdoutExcerpt: result.StdoutExcerpt, StderrExcerpt: result.StderrExcerpt, Parsed: result.Parsed, CreatedAt: time.Now().UTC()}
	if e = upload(ctx, report); e != nil {
		return path, e
	}
	if e = os.Remove(path); e != nil {
		return path, e
	}
	return "", nil
}

func ToDomainExecution(planID uuid.UUID, result Result) domain.Execution {
	var exit *int
	if result.ExitCode != nil {
		n := *result.ExitCode
		exit = &n
	}
	return domain.Execution{ID: result.ID, PlanID: planID, Outcome: result.Outcome, StartedAt: result.StartedAt, CompletedAt: result.CompletedAt, ExitStatus: exit, EnvironmentFingerprint: "", EscalationPerformed: result.Status == StatusEscalated, OutputHashes: nonempty(result.StdoutHash, result.StderrHash), SanitizedExcerptRefs: nil}
}
func nonempty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

type OfflinePolicy struct {
	Trusted                  bool
	ExpiresAt                time.Time
	AlgorithmVersion         string
	ExpectedAlgorithmVersion string
}

func ValidateOfflinePolicy(p OfflinePolicy, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if !p.Trusted {
		return errors.New("offline policy snapshot is not trusted")
	}
	if p.ExpiresAt.IsZero() || !now.Before(p.ExpiresAt) {
		return errors.New("offline policy snapshot is expired")
	}
	if p.AlgorithmVersion == "" || p.AlgorithmVersion != p.ExpectedAlgorithmVersion {
		return errors.New("offline policy algorithm version is incompatible")
	}
	return nil
}
func RunOfflineFallback(ctx context.Context, runner *Runner, adapter adapters.Adapter, command adapters.ExecutionCommand, parse adapters.ParseRequest) (Result, error) {
	if runner == nil {
		return Result{}, errors.New("runner is required")
	}
	if !command.FullSuite {
		return Result{Status: StatusEscalated, Outcome: domain.OutcomeInfraFailure, ErrorClass: "INVALID_FALLBACK", Error: "offline fallback command is not marked as a full suite"}, errors.New("offline fallback must be a configured full-suite command")
	}
	return runner.RunAndParse(ctx, adapter, command, parse), nil
}
