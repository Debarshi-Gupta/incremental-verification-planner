package audit

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
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
}

func (s Service) Process(ctx context.Context, in Input) (Result, error) {
	if s.Store == nil {
		return Result{}, errors.New("audit recorder is required")
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
	ClearEligibleQuarantine(context.Context, uuid.UUID, uuid.UUID, string, time.Time) error
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

func ClearAfterCleanAudit(ctx context.Context, store QuarantineStore, tenantID, repositoryID uuid.UUID, scope string, now time.Time) error {
	if store == nil {
		return errors.New("quarantine store is required")
	}
	return store.ClearEligibleQuarantine(ctx, tenantID, repositoryID, scope, now)
}

var _ = domain.AuditRecord{}
