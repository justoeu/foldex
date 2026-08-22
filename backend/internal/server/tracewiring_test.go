package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"foldex/internal/config"
	"foldex/internal/tracing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// The middleware only mounts when Deps.Trace is set — the zero-value Deps of
// every other router test keeps tracing off, so no test needs a provider.
func TestTraceMiddlewareMountedOnlyWhenSet(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	logger := slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil))

	// Without Deps.Trace: no spans, whatever the global provider is.
	off := New(Deps{Logger: logger, Config: config.Config{BindAddr: "127.0.0.1"}})
	off.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if n := len(rec.Ended()); n != 0 {
		t.Fatalf("router without Deps.Trace produced %d spans, want 0", n)
	}

	// With Deps.Trace: one SERVER span per API request, and the request log
	// line carries the trace_id Grafana's Loki→Tempo derived field links on.
	var logBuf bytes.Buffer
	jl := slog.New(slog.NewJSONHandler(&logBuf, nil))
	on := New(Deps{Logger: jl, Trace: tracing.Middleware, Config: config.Config{BindAddr: "127.0.0.1"}})
	on.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/does-not-exist", nil))

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("router with Deps.Trace produced %d spans, want 1", len(spans))
	}
	wantID := spans[0].SpanContext().TraceID().String()
	if !strings.Contains(logBuf.String(), `"trace_id":"`+wantID+`"`) {
		t.Fatalf("request log must carry trace_id %s for Loki→Tempo correlation, got: %s", wantID, logBuf.String())
	}
}

// Tracing off must not add trace_id noise to the logs — the derived field
// regex in Grafana would otherwise match empty/garbage values.
func TestRequestLogHasNoTraceIDWhenTracingOff(t *testing.T) {
	t.Parallel()
	var logBuf bytes.Buffer
	jl := slog.New(slog.NewJSONHandler(&logBuf, nil))
	router := New(Deps{Logger: jl, Config: config.Config{BindAddr: "127.0.0.1"}})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/does-not-exist", nil))

	if strings.Contains(logBuf.String(), "trace_id") {
		t.Fatalf("request log must omit trace_id when tracing is off, got: %s", logBuf.String())
	}
}
