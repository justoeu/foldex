package backupagent

import (
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics is the agent's own registry, served on BACKUP_METRICS_ADDR.
// Prometheus PULL is the primary observation channel by design: absent() over
// a scrape detects the agent that never came up — a push stream that stops is
// indistinguishable from "nothing to report" (SDD-OPS-BACKUP §9.1).
type Metrics struct {
	registry *prometheus.Registry

	lastSuccess  *prometheus.GaugeVec
	consecFails  *prometheus.GaugeVec
	lastDuration *prometheus.GaugeVec
	artifactSize *prometheus.GaugeVec
	runs         *prometheus.CounterVec
}

func NewMetrics() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		lastSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "foldex_backup_last_success_timestamp_seconds",
			Help: "Unix time of the last succeeded run, per job.",
		}, []string{"job"}),
		consecFails: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "foldex_backup_consecutive_failures",
			Help: "Failed runs since the last success, per job.",
		}, []string{"job"}),
		lastDuration: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "foldex_backup_last_run_duration_seconds",
			Help: "Duration of the last finished run, per job.",
		}, []string{"job"}),
		artifactSize: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "foldex_backup_artifact_bytes",
			Help: "Size of the last shipped artifact (ciphertext), per job.",
		}, []string{"job"}),
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "foldex_backup_runs_total",
			Help: "Finished runs by job and status.",
		}, []string{"job", "status"}),
	}
	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.lastSuccess, m.consecFails, m.lastDuration, m.artifactSize, m.runs,
	)
	return m
}

// ObserveSuccess records a finished successful run.
func (m *Metrics) ObserveSuccess(job string, finished time.Time, duration time.Duration, artifactBytes int64) {
	m.lastSuccess.WithLabelValues(job).Set(float64(finished.Unix()))
	m.consecFails.WithLabelValues(job).Set(0)
	m.lastDuration.WithLabelValues(job).Set(duration.Seconds())
	if artifactBytes > 0 {
		m.artifactSize.WithLabelValues(job).Set(float64(artifactBytes))
	}
	m.runs.WithLabelValues(job, "succeeded").Inc()
}

// ObserveFailure records a finished failed run with the consecutive count.
func (m *Metrics) ObserveFailure(job string, duration time.Duration, consecutive int) {
	m.consecFails.WithLabelValues(job).Set(float64(consecutive))
	m.lastDuration.WithLabelValues(job).Set(duration.Seconds())
	m.runs.WithLabelValues(job, "failed").Inc()
}

// SeedLastSuccess primes the gauge from backup_run at boot, so a restart does
// not zero the staleness signal the alert rules watch.
func (m *Metrics) SeedLastSuccess(job string, at time.Time) {
	if !at.IsZero() {
		m.lastSuccess.WithLabelValues(job).Set(float64(at.Unix()))
	}
}

// Handler serves /metrics behind the same bearer token contract the backend
// uses: empty token ⇒ 503, no anonymous mode (INV-mirroring internal/metrics).
func (m *Metrics) Handler(token string) http.Handler {
	inner := promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		MaxRequestsInFlight: 1,
		Timeout:             10 * time.Second,
	})
	// The guard mirrors internal/metrics.Handler byte for byte (same bodies,
	// same WWW-Authenticate) — one operator, one scrape contract; two subtly
	// different copies of a security check age badly.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			http.Error(w, "metrics disabled", http.StatusServiceUnavailable)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	})
}
