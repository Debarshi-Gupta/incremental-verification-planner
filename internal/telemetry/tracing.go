package telemetry

import (
	"context"
	"errors"
	"strings"

	"github.com/ivp/ivp/internal/security"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

type Tracing struct {
	Provider *sdktrace.TracerProvider
	Tracer   trace.Tracer
}

func NewTracing(serviceName string, exporter sdktrace.SpanExporter) (*Tracing, error) {
	if strings.TrimSpace(serviceName) == "" {
		return nil, errors.New("service name required")
	}
	r, err := resource.New(context.Background(), resource.WithAttributes(attribute.String("service.name", serviceName)))
	if err != nil {
		return nil, err
	}
	options := []sdktrace.TracerProviderOption{sdktrace.WithResource(r), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(1.0)))}
	if exporter != nil {
		options = append(options, sdktrace.WithBatcher(exporter))
	}
	p := sdktrace.NewTracerProvider(options...)
	return &Tracing{Provider: p, Tracer: p.Tracer("github.com/ivp/ivp")}, nil
}

func (t *Tracing) Shutdown(ctx context.Context) error {
	if t == nil || t.Provider == nil {
		return nil
	}
	return t.Provider.Shutdown(ctx)
}

func SafeAttributes(values map[string]string) []attribute.KeyValue {
	allowed := map[string]bool{"ivp.operation": true, "ivp.adapter": true, "ivp.mode": true, "ivp.outcome": true, "ivp.reason_code": true, "ivp.unit_type": true, "http.route": true, "http.request.method": true, "http.response.status_code": true, "error.type": true, "ivp.graph.complete": true}
	out := make([]attribute.KeyValue, 0, len(values))
	for key, value := range values {
		if !allowed[key] {
			continue
		}
		safe := security.Redact(value, 256)
		out = append(out, attribute.String(key, safe))
	}
	return out
}

// Propagator returns W3C trace-context and baggage propagation. Baggage values
// should not carry repository data, paths, source, or credentials.
func Propagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
}
