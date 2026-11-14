package audit

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/telemetry"
	"go.opentelemetry.io/otel/trace"
)

type Recorder interface {
	RecordAuditAndQuarantine(context.Context, Input, Result) error
}
type Metrics interface {
	AuditRun(outcome, adapter string)
	AuditMiss(severity, adapter string)
	Quarantine(reason string)
}
type Service struct {
	Store              Recorder
	Metrics            Metrics
	QuarantineDuration time.Duration
	// Tracer records one span per processed audit when it is set. The span
	// carries the bounded attributes only (never a test key or a secret).
	Tracer trace.Tracer
}

func (s Service) Process(ctx context.Context, in Input) (Result, error) {
	if s.Store == nil {
		return Result{}, errors.New("audit recorder is required")
	}
	// One span per processed audit: the disposition and the adapter only, so
	// no test key or environment value can reach the trace (LLD 2.2, 17.5).
	if s.Tracer != nil {
		var span trace.Span
		ctx, span = s.Tracer.Start(ctx, "ivp.audit.process")
		defer span.End()
		defer func() {
			span.SetAttributes(telemetry.SafeAttributes(map[string]string{
				"ivp.operation": "audit.process",
				"ivp.adapter":   in.Adapter,
				"ivp.outcome":   string(Classify(in).Disposition),
			})...)
		}()
	}
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	}
	if in.QuarantineFor <= 0 {
		if s.QuarantineDuration > 0 {
			in.QuarantineFor = s.QuarantineDuration
		} else {
			in.QuarantineFor = 7 * 24 * time.Hour
		}
	}
	result := Classify(in)
	if result.Record.ID == uuid.Nil {
		return result, errors.New("audit input is incomplete")
	}
	// Record and quarantine share one database transaction so no miss can exist
	// without immediately disabling selective execution for its scope.
	if err := s.Store.RecordAuditAndQuarantine(ctx, in, result); err != nil {
		return result, err
	}
	if s.Metrics != nil {
		s.Metrics.AuditRun(string(result.Disposition), in.Adapter)
		if result.Quarantine {
			s.Metrics.AuditMiss(string(result.Disposition), in.Adapter)
			s.Metrics.Quarantine("AUDIT_MISS")
		}
	}
	return result, nil
}

type QuarantineStore interface {
	AcknowledgeQuarantine(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, string) error
	ClearEligibleQuarantine(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, string, time.Time) error
}

func Acknowledge(ctx context.Context, store QuarantineStore, tenantID, actorID, repositoryID uuid.UUID, scope, reason string) error {
	if store == nil {
		return errors.New("quarantine store is required")
	}
	if scope == "" || reason == "" {
		return errors.New("scope and remediation reason are required")
	}
	return store.AcknowledgeQuarantine(ctx, tenantID, actorID, repositoryID, scope, reason)
}

func ClearAfterCleanAudit(ctx context.Context, store QuarantineStore, tenantID, actorID, repositoryID uuid.UUID, scope, reason string, now time.Time) error {
	if store == nil {
		return errors.New("quarantine store is required")
	}
	if scope == "" || reason == "" {
		return errors.New("scope and clearing reason are required")
	}
	return store.ClearEligibleQuarantine(ctx, tenantID, actorID, repositoryID, scope, reason, now)
}

var _ = domain.AuditRecord{}
