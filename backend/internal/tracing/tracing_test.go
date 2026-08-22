package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// withRecorder installs a real SDK provider backed by an in-memory span
// recorder, restoring the previous globals afterwards so tests stay isolated.
func withRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
	return rec
}

func attrValue(s sdktrace.ReadOnlySpan, key string) (string, bool) {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.Emit(), true
		}
	}
	return "", false
}

func TestSetupEmptyEndpointDisablesTracing(t *testing.T) {
	shutdown, err := Setup(context.Background(), Config{Endpoint: ""})
	if err != nil {
		t.Fatalf("Setup with empty endpoint must be a silent no-op, got err %v", err)
	}
	if shutdown != nil {
		t.Fatal("Setup with empty endpoint must return a nil shutdown — main uses that nil to keep the middleware unmounted")
	}
}

func TestSetupInstallsGlobalProviderAndShutdownWorks(t *testing.T) {
	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})

	// The gRPC connection is lazy: Setup must succeed with nothing listening.
	shutdown, err := Setup(context.Background(), Config{
		Endpoint:       "http://127.0.0.1:1", // reserved port — never connects
		ServiceName:    "foldex-test",
		ServiceVersion: "0.0.0-test",
	})
	if err != nil {
		t.Fatalf("Setup must not require a live collector: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Setup with an endpoint must return a shutdown func")
	}
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); !ok {
		t.Fatalf("Setup must install the SDK tracer provider globally, got %T", otel.GetTracerProvider())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Shutdown with a dead context must return promptly (error tolerated).
	_ = shutdown(ctx)
}

func TestSetupHTTPSEndpointParses(t *testing.T) {
	prevTP := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(prevTP) })
	shutdown, err := Setup(context.Background(), Config{Endpoint: "https://collector.invalid:4317", ServiceName: "x"})
	if err != nil || shutdown == nil {
		t.Fatalf("https endpoint must parse without error, got shutdown-nil=%v err=%v", shutdown == nil, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = shutdown(ctx)
}

func TestMiddlewareNamesSpanByRoutePattern(t *testing.T) {
	rec := withRecorder(t)

	r := chi.NewRouter()
	r.Use(Middleware)
	r.Get("/api/links/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/links/12345")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected exactly 1 span, got %d", len(spans))
	}
	s := spans[0]
	if s.Name() != "GET /api/links/{id}" {
		t.Fatalf("span must be named by the ROUTE PATTERN (cardinality + IDs never leak), got %q", s.Name())
	}
	if s.SpanKind() != oteltrace.SpanKindServer {
		t.Fatalf("span kind must be SERVER for the service graph, got %v", s.SpanKind())
	}
	if v, ok := attrValue(s, "http.route"); !ok || v != "/api/links/{id}" {
		t.Fatalf("http.route must carry the pattern, got %q (present=%v)", v, ok)
	}
	if v, ok := attrValue(s, "http.response.status_code"); !ok || v != "200" {
		t.Fatalf("status attribute must be 200, got %q (present=%v)", v, ok)
	}
	for _, kv := range s.Attributes() {
		if kv.Value.Emit() == "/api/links/12345" {
			t.Fatalf("raw URL path leaked into span attribute %s — only the route pattern may be recorded", kv.Key)
		}
	}
}

func TestMiddlewareMarks5xxAsError(t *testing.T) {
	rec := withRecorder(t)

	r := chi.NewRouter()
	r.Use(Middleware)
	r.Get("/boom", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Status().Code != codes.Error {
		t.Fatalf("a 500 must set span status Error, got %v", spans[0].Status().Code)
	}

	// 4xx is a client problem, not a service failure — must NOT be Error.
	r2 := chi.NewRouter()
	r2.Use(Middleware)
	r2.Get("/nope", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	r2.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nope", nil))
	all := rec.Ended()
	last := all[len(all)-1]
	if last.Status().Code == codes.Error {
		t.Fatal("a 404 must not mark the span as Error")
	}
}

func TestMiddlewareSkipsProbePaths(t *testing.T) {
	rec := withRecorder(t)

	r := chi.NewRouter()
	r.Use(Middleware)
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	r.Get("/healthz", ok)
	r.Get("/metrics", ok)
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if n := len(rec.Ended()); n != 0 {
		t.Fatalf("probe endpoints must not produce spans, got %d", n)
	}
}

func TestMiddlewareJoinsIncomingTraceContext(t *testing.T) {
	rec := withRecorder(t)

	r := chi.NewRouter()
	r.Use(Middleware)
	r.Get("/api/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	const parentTrace = "0af7651916cd43dd8448eb211c80319c"
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("traceparent", "00-"+parentTrace+"-b7ad6b7169203331-01")
	r.ServeHTTP(httptest.NewRecorder(), req)

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if got := spans[0].SpanContext().TraceID().String(); got != parentTrace {
		t.Fatalf("span must join the caller's trace (W3C traceparent), got trace %s want %s", got, parentTrace)
	}
	if !spans[0].Parent().IsValid() {
		t.Fatal("span must have the remote parent from traceparent")
	}
}

func TestMiddlewareExposesSpanToHandlers(t *testing.T) {
	withRecorder(t)

	r := chi.NewRouter()
	r.Use(Middleware)
	var handlerTraceID string
	r.Get("/api/x", func(w http.ResponseWriter, req *http.Request) {
		handlerTraceID = TraceID(req.Context())
		w.WriteHeader(http.StatusOK)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/x", nil))

	if handlerTraceID == "" {
		t.Fatal("handlers must see the active span in the request context — slogRequest depends on it for trace_id")
	}
}

func TestTraceIDEmptyWithoutSpan(t *testing.T) {
	if got := TraceID(context.Background()); got != "" {
		t.Fatalf("TraceID without a span must be empty, got %q", got)
	}
	// A noop span context is invalid too — still empty.
	ctx := oteltrace.ContextWithSpan(context.Background(), noop.Span{})
	if got := TraceID(ctx); got != "" {
		t.Fatalf("TraceID with a noop span must be empty, got %q", got)
	}
}

func TestMiddlewareDefaultsImplicit200(t *testing.T) {
	rec := withRecorder(t)

	r := chi.NewRouter()
	r.Use(Middleware)
	// Handler writes the body without an explicit WriteHeader.
	r.Get("/implicit", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/implicit", nil))

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if v, ok := attrValue(spans[0], "http.response.status_code"); !ok || v != "200" {
		t.Fatalf("implicit 200 must be recorded as 200, got %q (present=%v)", v, ok)
	}
}
