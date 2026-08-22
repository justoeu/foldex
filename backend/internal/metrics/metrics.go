// Package metrics exposes Prometheus-format metrics for scraping by the
// observability stack (app-deployments/observability). The /metrics endpoint
// is bearer-token-protected: metrics reveal route shapes, traffic volume and
// pool sizing, which is reconnaissance material on a multi-tenant instance.
package metrics

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics groups the application collectors and the Prometheus registry.
type Metrics struct {
	registry    *prometheus.Registry
	reqTotal    *prometheus.CounterVec
	reqDuration *prometheus.HistogramVec
	inFlight    prometheus.Gauge
}

// New builds the registry with the default collectors (Go runtime + process)
// plus the application's HTTP collectors. A non-nil pool also registers the
// pgxpool statistics collector.
func New(pool *pgxpool.Pool) *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	m := &Metrics{
		registry: reg,
		reqTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "foldex_http_requests_total",
			Help: "Total HTTP requests by method, route and status.",
		}, []string{"method", "route", "status"}),
		reqDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "foldex_http_request_duration_seconds",
			Help:    "HTTP request duration in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "foldex_http_requests_in_flight",
			Help: "HTTP requests currently being served.",
		}),
	}
	reg.MustRegister(m.reqTotal, m.reqDuration, m.inFlight)

	if pool != nil {
		reg.MustRegister(newPoolCollector(pool))
	}
	return m
}

// Registerer returns the shared Prometheus registry so other packages can add
// collectors to the same /metrics endpoint.
func (m *Metrics) Registerer() prometheus.Registerer { return m.registry }

// statusRecorder captures the status code for the request metrics.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Instrument measures every request. The route label is chi's registered
// pattern (e.g. /api/links/{id}), never the raw URL — raw paths carry slugs
// and ids, which would be both a cardinality leak and a data leak.
func (m *Metrics) Instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" || isProbePath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		m.inFlight.Inc()
		defer m.inFlight.Dec()

		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		route := "unmatched"
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			if pattern := rctx.RoutePattern(); pattern != "" {
				route = pattern
			}
		}
		method := metricMethod(r.Method)
		m.reqTotal.WithLabelValues(method, route, strconv.Itoa(rec.status)).Inc()
		m.reqDuration.WithLabelValues(method, route).Observe(time.Since(start).Seconds())
	})
}

// metricMethod collapses methods outside the standard set into "other" before
// they become a label. r.Method is client-controlled and Go's net/http accepts
// any valid token — without this, every distinct method becomes a permanent
// series in the registry (client_golang never prunes series), reachable
// without auth because Instrument runs before the session middleware.
// Unbounded cardinality = slow leak per uptime and a trivial memory DoS.
func metricMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodHead, http.MethodOptions,
		http.MethodConnect, http.MethodTrace:
		return method
	default:
		return "other"
	}
}

// Handler serves /metrics behind a bearer token. Empty token → 503 (endpoint
// disabled). Wrong token → 401. MaxRequestsInFlight caps concurrent scrapes
// at 1: a scrape walks every collector, and an unauthenticated attacker who
// guessed the token could otherwise stack Gather calls.
func (m *Metrics) Handler(token string) http.Handler {
	promHandler := promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{MaxRequestsInFlight: 1})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			http.Error(w, "metrics disabled", http.StatusServiceUnavailable)
			return
		}
		// Constant-time compare: no timing oracle over the metrics token.
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		promHandler.ServeHTTP(w, r)
	})
}

// poolCollector exposes pgxpool statistics as gauges/counters. The extra
// acquire-wait series distinguish pool starvation from a goroutine leak:
// without them, saturation shows up only as go_goroutines climbing with flat
// RSS, and the investigation starts at pprof — where there is nothing.
type poolCollector struct {
	pool                *pgxpool.Pool
	total               *prometheus.Desc
	acquired            *prometheus.Desc
	idle                *prometheus.Desc
	maxConns            *prometheus.Desc
	acquireCount        *prometheus.Desc
	emptyAcquire        *prometheus.Desc
	emptyAcquireWait    *prometheus.Desc
	canceledAcquire     *prometheus.Desc
	maxLifetimeDestroys *prometheus.Desc
}

func newPoolCollector(pool *pgxpool.Pool) *poolCollector {
	return &poolCollector{
		pool:         pool,
		total:        prometheus.NewDesc("foldex_db_pool_total_conns", "Total connections in the pool.", nil, nil),
		acquired:     prometheus.NewDesc("foldex_db_pool_acquired_conns", "Connections currently in use.", nil, nil),
		idle:         prometheus.NewDesc("foldex_db_pool_idle_conns", "Idle connections.", nil, nil),
		maxConns:     prometheus.NewDesc("foldex_db_pool_max_conns", "Pool max connections.", nil, nil),
		acquireCount: prometheus.NewDesc("foldex_db_pool_acquire_count", "Total connection acquisitions.", nil, nil),
		emptyAcquire: prometheus.NewDesc("foldex_db_pool_empty_acquire_count",
			"Acquisitions that had to wait because the pool was empty.", nil, nil),
		emptyAcquireWait: prometheus.NewDesc("foldex_db_pool_empty_acquire_wait_seconds",
			"Total time spent waiting for a connection with the pool empty.", nil, nil),
		canceledAcquire: prometheus.NewDesc("foldex_db_pool_canceled_acquire_count",
			"Acquisitions aborted by context cancellation/timeout.", nil, nil),
		maxLifetimeDestroys: prometheus.NewDesc("foldex_db_pool_max_lifetime_destroy_count",
			"Connections recycled for reaching MaxConnLifetime.", nil, nil),
	}
}

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.total
	ch <- c.acquired
	ch <- c.idle
	ch <- c.maxConns
	ch <- c.acquireCount
	ch <- c.emptyAcquire
	ch <- c.emptyAcquireWait
	ch <- c.canceledAcquire
	ch <- c.maxLifetimeDestroys
}

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.pool.Stat()
	ch <- prometheus.MustNewConstMetric(c.total, prometheus.GaugeValue, float64(s.TotalConns()))
	ch <- prometheus.MustNewConstMetric(c.acquired, prometheus.GaugeValue, float64(s.AcquiredConns()))
	ch <- prometheus.MustNewConstMetric(c.idle, prometheus.GaugeValue, float64(s.IdleConns()))
	ch <- prometheus.MustNewConstMetric(c.maxConns, prometheus.GaugeValue, float64(s.MaxConns()))
	ch <- prometheus.MustNewConstMetric(c.acquireCount, prometheus.CounterValue, float64(s.AcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.emptyAcquire, prometheus.CounterValue, float64(s.EmptyAcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.emptyAcquireWait, prometheus.CounterValue, s.EmptyAcquireWaitTime().Seconds())
	ch <- prometheus.MustNewConstMetric(c.canceledAcquire, prometheus.CounterValue, float64(s.CanceledAcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.maxLifetimeDestroys, prometheus.CounterValue, float64(s.MaxLifetimeDestroyCount()))
}

// isProbePath keeps the health probe out of the request metrics — it is
// polled by orchestrators and would dominate every rate() otherwise.
func isProbePath(path string) bool {
	return path == "/healthz"
}
