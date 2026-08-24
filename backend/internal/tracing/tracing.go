// Package tracing wires OpenTelemetry distributed tracing for the backend.
//
// Traces are OPT-IN: without OTEL_EXPORTER_OTLP_ENDPOINT the tracer provider
// is never installed, the global provider stays the OTel no-op and the
// middleware adds two context lookups per request — no exporter goroutine, no
// buffer, no network. This mirrors how metrics are opt-in via METRICS_TOKEN.
//
// Span naming follows the OTel HTTP semantic conventions: `{method} {route}`
// with the CHI ROUTE PATTERN (`GET /api/links/{id}`), never the raw URL path.
// That is deliberate and load-bearing: raw paths carry numeric IDs and public
// slugs (the same reason logsafe.HTTPPath exists for logs), and unbounded
// span names would also blow up the Tempo metrics-generator cardinality
// budget that feeds the APM dashboards.
//
// Incoming trace context is DISCARDED, not joined: this service is the edge
// (nothing upstream traces), so a client-supplied traceparent could only
// choose our trace ids and sampling flags — letting an attacker exclude their
// own requests from telemetry (sampled=0) or graft spans into a victim's
// trace. Every request re-originates its trace here. Revisit if a trusted
// tracing gateway ever fronts this service.
package tracing

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"foldex/internal/pkg/authctx"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "foldex/internal/tracing"

// authViaKey records which credential authenticated the request — a session
// cookie or an API token. There is no semantic convention for it, so it is
// namespaced under the service rather than squatting on a reserved key that a
// future semconv release could define differently.
const authViaKey = attribute.Key("foldex.auth.via")

// Config carries what Setup needs. Endpoint accepts the OTLP gRPC endpoint in
// the standard OTEL_EXPORTER_OTLP_ENDPOINT shapes: "host:4317",
// "http://host:4317" (plaintext) or "https://host:4317" (TLS). A trailing
// slash or path (an OTLP/HTTP habit) is stripped — gRPC targets take none.
type Config struct {
	Endpoint       string
	ServiceName    string
	ServiceVersion string
}

// rootServerSampler samples root spans only when they are SERVER spans, and
// lets children inherit their parent's decision (via ParentBased). Without
// it, every pool query OUTSIDE a request — the healthz pool.Ping probed by
// compose every few seconds, the preview/mail/sweeper workers — becomes a
// one-span orphan trace, forever. Probe noise is skipped at the HTTP layer;
// this is the same rule at the trace layer.
type rootServerSampler struct{}

func (rootServerSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	if p.Kind == trace.SpanKindServer {
		return sdktrace.AlwaysSample().ShouldSample(p)
	}
	return sdktrace.NeverSample().ShouldSample(p)
}

func (rootServerSampler) Description() string { return "RootServerOnly" }

// Setup installs the global OTel tracer provider exporting OTLP/gRPC and
// returns its shutdown function. An empty endpoint returns (nil, nil): tracing
// stays disabled and the caller skips the middleware entirely.
//
// The gRPC connection is lazy — Setup succeeds even when the collector is
// down, and the batch exporter retries in the background. A telemetry outage
// must never take the application down with it.
func Setup(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	if cfg.Endpoint == "" {
		return nil, nil
	}

	endpoint := cfg.Endpoint
	insecure := true
	switch {
	case strings.HasPrefix(endpoint, "https://"):
		endpoint = strings.TrimPrefix(endpoint, "https://")
		insecure = false
	case strings.HasPrefix(endpoint, "http://"):
		endpoint = strings.TrimPrefix(endpoint, "http://")
	}
	// "host:4317/" or "host:4317/v1/traces" (an OTLP/HTTP habit) would make
	// the lazy gRPC dial fail forever with no warning anywhere — strip it.
	if i := strings.IndexByte(endpoint, '/'); i >= 0 {
		endpoint = endpoint[:i]
	}

	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(endpoint)}
	if insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("otlp exporter: %w", err)
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(cfg.ServiceName),
		semconv.ServiceVersion(cfg.ServiceVersion),
	))
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(rootServerSampler{})),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// normMethod collapses any method outside the standard set into "_OTHER"
// (semconv's normalization) — r.Method is a client-controlled token and the
// unmatched-route fallback span name would otherwise mint one span name per
// invented method. Same rule, same reason as metrics.metricMethod.
func normMethod(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions,
		http.MethodTrace, http.MethodConnect:
		return m
	}
	return "_OTHER"
}

// Middleware creates one SERVER span per request, named by the chi route
// pattern resolved AFTER the handler ran (the pattern is only known once
// routing happened). Client-supplied trace context is deliberately ignored —
// see the package comment.
//
// /healthz and /metrics are skipped for the same reason metrics.Instrument
// skips them: probe noise with no diagnostic value.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}

		method := normMethod(r.Method)
		ctx, span := otel.GetTracerProvider().Tracer(tracerName).Start(
			r.Context(), "HTTP "+method,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(semconv.HTTPRequestMethodKey.String(method)),
		)
		defer span.End()

		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r.WithContext(ctx))

		if rctx := chi.RouteContext(ctx); rctx != nil {
			if pattern := rctx.RoutePattern(); pattern != "" {
				span.SetName(method + " " + pattern)
				span.SetAttributes(semconv.HTTPRoute(pattern))
			}
		}
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		span.SetAttributes(semconv.HTTPResponseStatusCode(status))
		if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(status))
		}
	})
}

// AnnotatePrincipal stamps the identity in ctx onto the SERVER span that
// Middleware opened. It reads the principal from ctx rather than taking one,
// so it cannot be called with an identity the request is not actually running
// as.
//
// It is a FUNCTION called from the three places that establish a principal —
// auth.Middleware.Authenticate, auth.Middleware.Optional and the
// AUTH_ENABLED=0 bootstrap — and deliberately NOT a middleware of its own. A
// mounted middleware annotates only the group it was mounted on, and the first
// draft of this feature proved how that fails: mounted on the main /api group,
// it missed the whole authenticated half of /api/auth (sessions, password
// change, 2FA, API tokens) — the credential-management surface an operator
// most wants attributed — and nothing failed. No build error, no panic, an
// identical response. Hanging it off principal creation instead means a
// request that HAS an identity has it on its span, and a future route group
// cannot forget to opt in.
//
// The span was opened several middlewares further out and its End is deferred
// there, so it is still mutable at every one of those call sites.
//
// Only the opaque numeric id travels. The address and the display name are
// deliberately absent: a trace store is a different retention domain from the
// database, with different access control and a copy in every backend that
// scrapes it, and an id is worthless to anyone who cannot already read
// app_user. It is the same reasoning that keeps raw URL paths out of span
// names. See INV-170 for the cardinality rule this implies for Tempo's
// metrics-generator.
func AnnotatePrincipal(ctx context.Context) {
	// IsRecording first: it is what makes this free when tracing is off or the
	// span was not sampled, and everything below allocates.
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	p, ok := authctx.FromContext(ctx)
	if !ok {
		return
	}
	attrs := make([]attribute.KeyValue, 0, 3)
	attrs = append(attrs, semconv.UserID(strconv.FormatInt(int64(p.UserID), 10)))
	if p.Role != "" {
		// semconv types user.roles as an array; a single-element one is still
		// the conventional shape, and inventing a scalar key beside it would
		// give Tempo two answers to the same question.
		attrs = append(attrs, semconv.UserRoles(string(p.Role)))
	}
	if p.Via != "" {
		attrs = append(attrs, authViaKey.String(p.Via))
	}
	span.SetAttributes(attrs...)
}

// TraceID returns the hex trace id of the span in ctx, or "" when no recording
// span is there. Used by the request logger so every access log line carries
// the id Grafana's Loki→Tempo derived field links on.
func TraceID(ctx context.Context) string {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		return sc.TraceID().String()
	}
	return ""
}
