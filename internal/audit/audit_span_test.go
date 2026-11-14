package audit

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// tracetestProvider builds a span provider that exports into the in-memory
// exporter.
func tracetestProvider(exporter sdktrace.SpanExporter) *sdktrace.TracerProvider {
	return sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
}

type spanRecorder struct {
	recorded bool
}

func (r *spanRecorder) RecordAuditAndQuarantine(context.Context, Input, Result) error {
	r.recorded = true
	return nil
}

// LLD 2.2: the audit path must record a span, and the span must carry only
// the bounded attributes.
func TestAuditProcessRecordsASpan(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := tracetestProvider(exporter)
	defer func() { _ = provider.Shutdown(context.Background()) }()

	recorder := &spanRecorder{}
	service := Service{Store: recorder, Tracer: provider.Tracer("github.com/ivp/ivp")}
	result, err := service.Process(context.Background(), Input{
		TenantID: uuid.New(), RepositoryID: uuid.New(), PlanID: uuid.New(),
		SelectedExecutionID: uuid.New(), FullExecutionID: uuid.New(),
		Adapter: "pytest", SelectedOutcome: "TARGETED_PASS", FullOutcome: "FULL_VERIFICATION_PASS",
		SelectedKeys: []string{"test:a"}, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !recorder.recorded {
		t.Fatal("the audit was not recorded")
	}
	if result.Disposition == "" {
		t.Fatalf("the audit produced no disposition: %+v", result)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "ivp.audit.process" {
		t.Fatalf("the exported spans = %+v, want one named ivp.audit.process", spans)
	}
	for _, attribute := range spans[0].Attributes {
		if attribute.Key == "ivp.test_unit_key" {
			t.Fatalf("a test key reached the trace: %+v", attribute)
		}
	}
}
