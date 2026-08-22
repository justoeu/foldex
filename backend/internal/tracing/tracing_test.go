package tracing

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
)

// withRecorder installs a real SDK provider backed by an in-memory span
// recorder, restoring the previous global afterwards so tests stay isolated.
func withRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
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

// fakeCollector is a real OTLP/gRPC trace receiver on a loopback port — the
// only way to prove what Setup actually EXPORTS (endpoint parsing, TLS mode,
// resource identity, sampler decisions, shutdown flush). The mutation sweep
// showed every one of those survives tests that stop at "err == nil".
type fakeCollector struct {
	collectorpb.UnimplementedTraceServiceServer
	mu    sync.Mutex
	spans []*tracepb.ResourceSpans
}

func (f *fakeCollector) Export(_ context.Context, req *collectorpb.ExportTraceServiceRequest) (*collectorpb.ExportTraceServiceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spans = append(f.spans, req.GetResourceSpans()...)
	return &collectorpb.ExportTraceServiceResponse{}, nil
}

func (f *fakeCollector) resourceSpans() []*tracepb.ResourceSpans {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*tracepb.ResourceSpans(nil), f.spans...)
}

func (f *fakeCollector) spanNames() []string {
	var names []string
	for _, rs := range f.resourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, s := range ss.GetSpans() {
				names = append(names, s.GetName())
			}
		}
	}
	return names
}

func startFakeCollector(t *testing.T) (*fakeCollector, string) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeCollector{}
	srv := grpc.NewServer()
	collectorpb.RegisterTraceServiceServer(srv, fc)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return fc, lis.Addr().String()
}

// setupAgainst runs Setup with the given endpoint, restores globals on
// cleanup, emits one SERVER span and returns the shutdown error — the full
// export path, end to end.
func setupAgainst(t *testing.T, endpoint, spanName string) error {
	t.Helper()
	prev := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	shutdown, err := Setup(context.Background(), Config{
		Endpoint:       endpoint,
		ServiceName:    "foldex-test",
		ServiceVersion: "0.0.0-test",
	})
	if err != nil {
		t.Fatalf("Setup(%q): %v", endpoint, err)
	}
	if shutdown == nil {
		t.Fatalf("Setup(%q) returned nil shutdown", endpoint)
	}
	_, span := otel.GetTracerProvider().Tracer(tracerName).Start(
		context.Background(), spanName, oteltrace.WithSpanKind(oteltrace.SpanKindServer))
	span.End()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return shutdown(ctx)
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

// One test, three endpoint shapes, one real receiver: bare host:port,
// http:// scheme, and the OTLP/HTTP habit of a trailing path — all must
// reach the collector in plaintext, and shutdown must FLUSH the span
// (a no-op shutdown mutant leaves the batcher undrained and this fails).
func TestSetupExportsOverEveryPlaintextEndpointForm(t *testing.T) {
	for _, form := range []struct{ name, prefix, suffix string }{
		{"bare-host-port", "", ""},
		{"http-scheme", "http://", ""},
		{"http-scheme-with-path", "http://", "/v1/traces"},
	} {
		t.Run(form.name, func(t *testing.T) {
			fc, addr := startFakeCollector(t)
			if err := setupAgainst(t, form.prefix+addr+form.suffix, "probe-"+form.name); err != nil {
				t.Fatalf("shutdown: %v", err)
			}
			names := fc.spanNames()
			if len(names) != 1 || names[0] != "probe-"+form.name {
				t.Fatalf("collector must receive exactly the emitted span, got %v", names)
			}
		})
	}
}

func TestSetupHTTPSEndpointDoesNotSpeakPlaintext(t *testing.T) {
	fc, addr := startFakeCollector(t)
	// TLS handshake against a plaintext listener can never deliver spans; a
	// mutant that drops the https case (leaving insecure=true) DOES deliver
	// and fails here.
	_ = setupAgainst(t, "https://"+addr, "must-not-arrive")
	if names := fc.spanNames(); len(names) != 0 {
		t.Fatalf("https endpoint exported over plaintext — TLS mode was ignored: %v", names)
	}
}

func TestSetupExportsResourceIdentity(t *testing.T) {
	fc, addr := startFakeCollector(t)
	if err := setupAgainst(t, addr, "identity"); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	rss := fc.resourceSpans()
	if len(rss) == 0 {
		t.Fatal("no resource spans received")
	}
	got := map[string]string{}
	for _, kv := range rss[0].GetResource().GetAttributes() {
		got[kv.GetKey()] = kv.GetValue().GetStringValue()
	}
	if got["service.name"] != "foldex-test" {
		t.Fatalf("service.name must identify this service in the Tempo service graph, got %q", got["service.name"])
	}
	if got["service.version"] != "0.0.0-test" {
		t.Fatalf("service.version must carry Config.ServiceVersion, got %q", got["service.version"])
	}
}

// The sampler contract: root CLIENT spans (healthz pool.Ping, background
// workers via otelpgx) are dropped; SERVER roots and their CLIENT children
// are kept. Without this every compose healthcheck mints an orphan trace.
func TestSamplerDropsRootClientSpansKeepsServerTraces(t *testing.T) {
	fc, addr := startFakeCollector(t)
	prev := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	shutdown, err := Setup(context.Background(), Config{Endpoint: addr, ServiceName: "s"})
	if err != nil || shutdown == nil {
		t.Fatalf("Setup: shutdown-nil=%v err=%v", shutdown == nil, err)
	}
	tr := otel.GetTracerProvider().Tracer(tracerName)

	// Orphan CLIENT root — the healthz Ping shape.
	_, orphan := tr.Start(context.Background(), "orphan-client", oteltrace.WithSpanKind(oteltrace.SpanKindClient))
	orphan.End()

	// SERVER root with a CLIENT child — the request+query shape.
	ctx, server := tr.Start(context.Background(), "server-root", oteltrace.WithSpanKind(oteltrace.SpanKindServer))
	_, child := tr.Start(ctx, "client-child", oteltrace.WithSpanKind(oteltrace.SpanKindClient))
	child.End()
	server.End()

	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(sctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	names := fc.spanNames()
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	if got["orphan-client"] {
		t.Fatal("root CLIENT span was exported — healthz/background queries would mint orphan traces forever")
	}
	if !got["server-root"] || !got["client-child"] {
		t.Fatalf("SERVER root and its CLIENT child must both be exported, got %v", names)
	}
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

// The unmatched-route fallback is where raw-path leaks would happen: a
// scanner's 404 or a mistyped public slug must produce the constant name
// "HTTP GET", never anything containing the path.
func TestMiddlewareNeverNamesSpanByRawPathWhenRouteUnmatched(t *testing.T) {
	rec := withRecorder(t)

	r := chi.NewRouter()
	r.Use(Middleware)
	r.Get("/registered", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/go/secret-public-slug-abc123", nil))

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	s := spans[0]
	if s.Name() != "HTTP GET" {
		t.Fatalf("unmatched route must keep the constant fallback name, got %q", s.Name())
	}
	if strings.Contains(s.Name(), "slug") {
		t.Fatalf("raw path leaked into span name: %q", s.Name())
	}
	for _, kv := range s.Attributes() {
		if strings.Contains(kv.Value.Emit(), "secret-public-slug") {
			t.Fatalf("raw path leaked into attribute %s", kv.Key)
		}
	}
	if _, ok := attrValue(s, "http.route"); ok {
		t.Fatal("http.route must be absent when no route matched")
	}
}

// r.Method is a client-controlled token; without normalization every invented
// method mints a new span name ("HTTP FOOBAR123") — same series-explosion
// vector metrics.metricMethod already collapses.
func TestMiddlewareNormalizesUnknownMethod(t *testing.T) {
	rec := withRecorder(t)

	r := chi.NewRouter()
	r.Use(Middleware)
	r.Get("/api/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest("FOOBAR123", "/api/x", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Name() != "HTTP _OTHER" {
		t.Fatalf("unknown method must collapse to _OTHER, got %q", spans[0].Name())
	}
	if v, _ := attrValue(spans[0], "http.request.method"); v != "_OTHER" {
		t.Fatalf("method attribute must be normalized to _OTHER, got %q", v)
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

// Client-supplied trace context must be DISCARDED (see package comment): a
// hostile traceparent may neither choose our trace id, nor attach us as a
// child, nor opt itself out of telemetry via sampled=0.
func TestMiddlewareIgnoresClientTraceparent(t *testing.T) {
	rec := withRecorder(t)

	r := chi.NewRouter()
	r.Use(Middleware)
	r.Get("/api/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	const hostileTrace = "0af7651916cd43dd8448eb211c80319c"
	// sampled=0: the classic telemetry-evasion flag.
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("traceparent", "00-"+hostileTrace+"-b7ad6b7169203331-00")
	r.ServeHTTP(httptest.NewRecorder(), req)

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("the request must still be traced (no sampled=0 evasion), got %d spans", len(spans))
	}
	if got := spans[0].SpanContext().TraceID().String(); got == hostileTrace {
		t.Fatal("client-chosen trace id was honoured — trace context must be re-originated at this edge")
	}
	if spans[0].Parent().IsValid() {
		t.Fatal("span must be a root — a hostile traceparent must not become our parent")
	}
}

func TestMiddlewareExposesSpanToHandlers(t *testing.T) {
	rec := withRecorder(t)

	r := chi.NewRouter()
	r.Use(Middleware)
	var handlerTraceID string
	r.Get("/api/x", func(w http.ResponseWriter, req *http.Request) {
		handlerTraceID = TraceID(req.Context())
		w.WriteHeader(http.StatusOK)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/x", nil))

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	want := spans[0].SpanContext().TraceID().String()
	if handlerTraceID != want {
		t.Fatalf("TraceID must return the ACTIVE span's trace id (%s), got %q — a span id or constant here breaks every Loki→Tempo link", want, handlerTraceID)
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

// A handler that writes NOTHING (hijack, empty response, disconnected client)
// leaves ww.Status() at 0 — the fallback must record 200, not a meaningless
// 0 that breaks the APM error-rate queries. (A handler that only Writes gets
// its 200 from the wrapper itself, which would mask this fallback.)
func TestMiddlewareRecordsImplicit200WhenHandlerWritesNothing(t *testing.T) {
	rec := withRecorder(t)

	r := chi.NewRouter()
	r.Use(Middleware)
	r.Get("/silent", func(http.ResponseWriter, *http.Request) {})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/silent", nil))

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if v, ok := attrValue(spans[0], "http.response.status_code"); !ok || v != "200" {
		t.Fatalf("silent handler must record status 200 via the fallback, got %q (present=%v)", v, ok)
	}
}
