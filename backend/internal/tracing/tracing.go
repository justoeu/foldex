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
package tracing

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "foldex/internal/tracing"

// Config carries what Setup needs. Endpoint accepts the OTLP gRPC endpoint in
// the standard OTEL_EXPORTER_OTLP_ENDPOINT shapes: "host:4317",
// "http://host:4317" (plaintext) or "https://host:4317" (TLS).
type Config struct {
	Endpoint       string
	ServiceName    string
	ServiceVersion string
}

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
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	return tp.Shutdown, nil
}

// Middleware creates one SERVER span per request, named by the chi route
// pattern resolved AFTER the handler ran (the pattern is only known once
// routing happened). Incoming W3C traceparent/baggage headers are honoured so
// spans join the caller's trace.
//
// /healthz and /metrics are skipped for the same reason metrics.Instrument
// skips them: probe noise with no diagnostic value.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}

		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := otel.GetTracerProvider().Tracer(tracerName).Start(
			ctx, "HTTP "+r.Method,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(semconv.HTTPRequestMethodKey.String(r.Method)),
		)
		defer span.End()

		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r.WithContext(ctx))

		if rctx := chi.RouteContext(ctx); rctx != nil {
			if pattern := rctx.RoutePattern(); pattern != "" {
				span.SetName(r.Method + " " + pattern)
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

// TraceID returns the hex trace id of the span in ctx, or "" when no recording
// span is there. Used by the request logger so every access log line carries
// the id Grafana's Loki→Tempo derived field links on.
func TraceID(ctx context.Context) string {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		return sc.TraceID().String()
	}
	return ""
}
