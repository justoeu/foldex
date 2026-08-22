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
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// tracingSpans filters the recorder down to spans produced by OUR middleware
// (scope + SERVER kind): the pool's otelpgx tracer shares the same global
// provider, so an exact count over everything would be structurally flaky.
func tracingSpans(rec *tracetest.SpanRecorder) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.InstrumentationScope().Name == "foldex/internal/tracing" && s.SpanKind() == oteltrace.SpanKindServer {
			out = append(out, s)
		}
	}
	return out
}

func withGlobalRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return rec
}

// The middleware only mounts when Deps.Trace is set — the zero-value Deps of
// every other router test keeps tracing off, so no test needs a provider.
func TestTraceMiddlewareMountedOnlyWhenSet(t *testing.T) {
	rec := withGlobalRecorder(t)

	logger := slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil))

	// Without Deps.Trace: no spans, whatever the global provider is.
	off := New(Deps{Logger: logger, Config: config.Config{BindAddr: "127.0.0.1"}})
	off.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if n := len(tracingSpans(rec)); n != 0 {
		t.Fatalf("router without Deps.Trace produced %d spans, want 0", n)
	}

	// With Deps.Trace: one SERVER span per API request, and the request log
	// line carries the trace_id Grafana's Loki→Tempo derived field links on.
	var logBuf bytes.Buffer
	jl := slog.New(slog.NewJSONHandler(&logBuf, nil))
	on := New(Deps{Logger: jl, Trace: tracing.Middleware, Config: config.Config{BindAddr: "127.0.0.1"}})
	on.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/does-not-exist", nil))

	spans := tracingSpans(rec)
	if len(spans) != 1 {
		t.Fatalf("router with Deps.Trace produced %d spans, want 1", len(spans))
	}
	wantID := spans[0].SpanContext().TraceID().String()
	if !strings.Contains(logBuf.String(), `"trace_id":"`+wantID+`"`) {
		t.Fatalf("request log must carry trace_id %s for Loki→Tempo correlation, got: %s", wantID, logBuf.String())
	}
}

// Locks the middleware ORDER (Trace outside Recoverer) and the deferred
// span.End: a panic — the 500 that tracing exists to diagnose — must still
// produce a finished span named by its route, marked 500/Error. With Trace
// mounted after Recoverer the span loses route, status and Error; with a
// non-deferred End it never finishes at all. Both mutants survived the first
// sweep; this is their tombstone.
func TestPanicIsRecordedAsErrorSpanWithRoute(t *testing.T) {
	rec := withGlobalRecorder(t)

	var logBuf bytes.Buffer
	jl := slog.New(slog.NewJSONHandler(&logBuf, nil))
	router := New(Deps{Logger: jl, Trace: tracing.Middleware, Config: config.Config{BindAddr: "127.0.0.1"}})

	// A REAL panic through New()'s mounted chain: with a nil Pool, the
	// bootstrap-principal middleware dereferences the pool on the first /api
	// request — the same trigger TestRecoveredPanicIsCountedAsInternalServerError
	// uses for the metrics tripwire.
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/links", nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("nil-pool panic must surface as 500 through the Recoverer, got %d", rr.Code)
	}
	spans := tracingSpans(rec)
	if len(spans) != 1 {
		t.Fatalf("panic must still END exactly one span (deferred End), got %d", len(spans))
	}
	s := spans[0]
	var statusAttr string
	for _, kv := range s.Attributes() {
		if string(kv.Key) == "http.response.status_code" {
			statusAttr = kv.Value.Emit()
		}
	}
	if statusAttr != "500" {
		t.Fatalf("panic span must record status 500, got %q", statusAttr)
	}
	if s.Status().Code != codes.Error {
		t.Fatalf("panic span must have status Error, got %v", s.Status().Code)
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
