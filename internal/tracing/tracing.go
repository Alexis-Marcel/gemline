// Package tracing wires the global OTel tracer provider with an OTLP HTTP
// exporter. Setup is a no-op when OTEL_EXPORTER_OTLP_ENDPOINT is unset, so
// the same binary boots cleanly in dev (no Alloy / Tempo) and in production.
package tracing

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Setup installs the global tracer provider + W3C TraceContext propagator and
// returns a shutdown function the caller defers. When OTEL_EXPORTER_OTLP_ENDPOINT
// is empty (dev or any environment without a collector), Setup returns a no-op
// shutdown — the global tracer stays at the SDK's noop default, so the
// otelhttp / otelsql wrappers compile and emit nothing.
//
// The OTLP exporter reads OTEL_EXPORTER_OTLP_ENDPOINT itself (and the other
// OTEL_* env vars) — no hard-coded address here. In our cluster that's
// http://alloy.monitoring.svc.cluster.local:4318.
func Setup(ctx context.Context, serviceName, serviceVersion string) (func(context.Context) error, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}

	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("otlp exporter: %w", err)
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	// W3C TraceContext + Baggage are what every contrib instrumentation expects.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp.Shutdown, nil
}
