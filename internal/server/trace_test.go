package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// TestProxyPropagatesTraceparent proves the forward carries the W3C trace
// context: the receiving pod must see a traceparent header bearing the same
// trace id as the sender's active span, so both server spans join one trace.
// Needs a real (SDK) tracer provider — the default noop yields an invalid
// span context, which the propagator rightly refuses to inject.
func TestProxyPropagatesTraceparent(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })
	oldTP, oldProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(oldTP)
		otel.SetTextMapPropagator(oldProp)
	})

	var got string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("traceparent")
	}))
	t.Cleanup(target.Close)

	srv := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	req := httptest.NewRequest(http.MethodPost, "/api/games/g1/moves", strings.NewReader("{}"))
	ctx, span := otel.Tracer("test").Start(req.Context(), "incoming request")
	defer span.End()
	req = req.WithContext(ctx)

	srv.proxyTo(httptest.NewRecorder(), req, "g1", "owner", strings.TrimPrefix(target.URL, "http://"),
		func() { t.Fatal("proxy fell back locally") })

	if got == "" {
		t.Fatal("forwarded request carries no traceparent header")
	}
	wantTraceID := span.SpanContext().TraceID().String()
	if !strings.Contains(got, wantTraceID) {
		t.Fatalf("traceparent %q does not carry the parent trace id %s", got, wantTraceID)
	}
}
