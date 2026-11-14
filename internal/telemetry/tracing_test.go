package telemetry

import (
	"context"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// LLD 2.2 observability: a span must be exportable, and the OTLP exporter
// must refuse an incomplete configuration.
func TestTracingExportsSpansAndTheOTLPExporterIsValidated(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tracing, err := NewTracing("ivp-test", exporter)
	if err != nil {
		t.Fatal(err)
	}
	_, span := tracing.Tracer.Start(context.Background(), "ivp.test.span")
	span.SetAttributes(SafeAttributes(map[string]string{"ivp.operation": "test"})...)
	span.End()
	if err := tracing.Provider.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "ivp.test.span" {
		t.Fatalf("the exported spans = %+v, want one named ivp.test.span", spans)
	}

	// The OTLP exporter requires a service name and an endpoint.
	if _, err := NewOTLPTracing(context.Background(), "", "localhost:4318", true); err == nil {
		t.Error("the OTLP exporter accepted an empty service name")
	}
	if _, err := NewOTLPTracing(context.Background(), "ivp-test", "  ", true); err == nil {
		t.Error("the OTLP exporter accepted an empty endpoint")
	}
	exported, err := NewOTLPTracing(context.Background(), "ivp-test", "localhost:4318", true)
	if err != nil {
		t.Fatalf("the OTLP exporter could not be created: %v", err)
	}
	if exported.Tracer == nil || exported.Provider == nil {
		t.Fatal("the OTLP tracing provider is incomplete")
	}
	_ = exported.Provider.Shutdown(context.Background())
}

// The bounded attribute vocabulary must never accept an unbounded value: a
// trace label is a metric label too.
func TestSafeAttributesRejectsUnknownValues(t *testing.T) {
	attributes := SafeAttributes(map[string]string{
		"ivp.operation": "execution.command",
		"ivp.adapter":   "gradle",
		"secret.value":  "super-secret",
	})
	for _, attribute := range attributes {
		if strings.Contains(attribute.Value.Emit(), "super-secret") {
			t.Fatalf("an unknown attribute value reached the span: %+v", attributes)
		}
	}
	var _ sdktrace.SpanExporter = tracetest.NewInMemoryExporter()
}
