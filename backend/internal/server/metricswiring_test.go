package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"foldex/internal/config"
	"foldex/internal/metrics"
)

func metricsRouter(t *testing.T, m *metrics.Metrics, token string) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(Deps{
		Logger:  logger,
		Metrics: m,
		Config:  config.Config{BindAddr: "127.0.0.1", MetricsToken: token},
	})
}

// The route only exists when Deps.Metrics is set — the zero-value Deps used
// by the other router tests must keep answering 404 there.
func TestMetricsRouteAbsentWithoutCollectors(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := New(Deps{Logger: logger, Config: config.Config{BindAddr: "127.0.0.1"}})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("/metrics without collectors = %d, want 404", recorder.Code)
	}
}

func TestMetricsRouteIsTokenProtectedAndInstrumented(t *testing.T) {
	t.Parallel()
	m := metrics.New(nil)
	router := metricsRouter(t, m, "scrape-secret")

	// Wrong token → 401. This is what stands between the internet and the
	// instance's route/traffic shape, so the wiring (not just the handler
	// unit) is what gets asserted.
	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("no-token scrape = %d, want 401", unauthorized.Code)
	}

	// Traffic through the real middleware stack lands in the counters with
	// chi's pattern as the route label.
	probe := httptest.NewRecorder()
	router.ServeHTTP(probe, httptest.NewRequest(http.MethodGet, "/api/does-not-exist", nil))
	if probe.Code == 0 {
		t.Fatal("request did not reach the router")
	}

	authorized := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer scrape-secret")
	router.ServeHTTP(authorized, req)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized scrape = %d body=%s", authorized.Code, authorized.Body.String())
	}
	if body := authorized.Body.String(); !strings.Contains(body, "foldex_http_requests_total") {
		t.Fatalf("instrumentation middleware is not mounted — no request series: %s", body)
	}
}

// Empty METRICS_TOKEN keeps the endpoint mounted but disabled: 503, never an
// open scrape.
func TestMetricsRouteDisabledWithEmptyToken(t *testing.T) {
	t.Parallel()
	router := metricsRouter(t, metrics.New(nil), "")

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("empty-token scrape = %d, want 503", recorder.Code)
	}
}
