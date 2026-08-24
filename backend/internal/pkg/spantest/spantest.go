// Package spantest is test scaffolding for asserting on the spans the tracing
// middleware produced.
//
// It is a non-test package so packages outside internal/tracing can import it
// from their own _test files, and it is excluded from coverage measurement for
// the same reason testdb and authctxtest are: instrumenting scaffolding
// reports on the harness rather than on the code under test.
//
// Its whole reason to exist is that the recorder-swap and the two filters were
// copied into four files across three packages before anyone noticed. The
// filters are not incidental — a recorder installed globally also collects the
// CLIENT spans otelpgx emits for every query on the request path, so a test
// that counts "all spans" is structurally flaky.
package spantest

import (
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// scope is the instrumentation scope internal/tracing names its tracer with.
// Filtering on it is what separates our SERVER spans from otelpgx's.
const scope = "foldex/internal/tracing"

// Recorder installs a real SDK provider backed by an in-memory recorder as the
// global one, restoring the previous provider on cleanup so tests stay
// isolated. The provider must be global because the middleware resolves it per
// request rather than holding a reference.
func Recorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return rec
}

// ServerSpans returns only the SERVER spans produced by internal/tracing, in
// the order they ended.
func ServerSpans(rec *tracetest.SpanRecorder) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.InstrumentationScope().Name == scope && s.SpanKind() == oteltrace.SpanKindServer {
			out = append(out, s)
		}
	}
	return out
}

// Last returns the most recently ended SERVER span, failing when there is
// none. Use it when a test performs several requests and only the last one
// carries the assertion.
func Last(t *testing.T, rec *tracetest.SpanRecorder) sdktrace.ReadOnlySpan {
	t.Helper()
	spans := ServerSpans(rec)
	if len(spans) == 0 {
		t.Fatal("spantest: no SERVER span was recorded")
	}
	return spans[len(spans)-1]
}

// Attr returns a span attribute's rendered value and whether it was present.
func Attr(s sdktrace.ReadOnlySpan, key string) (string, bool) {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.Emit(), true
		}
	}
	return "", false
}
