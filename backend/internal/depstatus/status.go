// Package depstatus reports whether optional runtime dependencies are
// reachable. Postgres is not in this set: if the database is down the
// process is down, and /healthz already answers that for probes.
//
// The JSON never carries a host, URL, or probe error — those leak
// addressing and, for AMQP, the broker password in the URL.
package depstatus

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	// ObjectStore is the S3-compatible bucket (RustFS).
	ObjectStore = "object_store"
	// MailBroker is the AMQP transport (RabbitMQ) when MAIL_TRANSPORT=amqp.
	MailBroker = "mail_broker"

	StateOK          = "ok"
	StateUnreachable = "unreachable"
	defaultTTL       = 30 * time.Second
	defaultProbeWait = 2 * time.Second
)

// ErrUnreachable is the sentinel AlwaysUnreachable returns so a process that
// failed to wire a client at boot does not dial on every status refresh.
var ErrUnreachable = errors.New("unreachable")

// AlwaysUnreachable is a Probe for a dependency the process configured but
// did not connect to at boot (object store down → screenshot routes 503).
func AlwaysUnreachable(context.Context) error { return ErrUnreachable }

// Probe is a bounded reachability check. The error is discarded after the
// boolean is recorded; it must never be written to a response.
type Probe func(ctx context.Context) error

// Resource is one named dependency. State is "ok" or "unreachable".
type Resource struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// Snapshot is the payload GET /api/status returns.
type Snapshot struct {
	Resources []Resource `json:"resources"`
}

type namedProbe struct {
	id string
	fn Probe
}

// Checker caches probe results so a hung store cannot turn every footer
// poll into a multi-second wait, and so /metrics scrapes never dial.
type Checker struct {
	probes  []namedProbe
	ttl     time.Duration
	timeout time.Duration
	logger  *slog.Logger

	mu         sync.Mutex
	cached     Snapshot
	cachedAt   time.Time
	ready      bool
	last       map[string]string
	refreshing chan struct{}
}

// Option configures a Checker.
type Option func(*Checker)

// WithTTL is how long a snapshot is reused. Zero keeps the default.
func WithTTL(d time.Duration) Option {
	return func(c *Checker) {
		if d > 0 {
			c.ttl = d
		}
	}
}

// WithTimeout bounds each probe. Zero keeps the default (2s, same as /healthz).
func WithTimeout(d time.Duration) Option {
	return func(c *Checker) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithLogger records ok↔unreachable transitions. Probe errors are not logged:
// they routinely contain the endpoint.
func WithLogger(l *slog.Logger) Option {
	return func(c *Checker) { c.logger = l }
}

// New builds an empty checker. Add probes before the first Snapshot.
func New(opts ...Option) *Checker {
	c := &Checker{
		ttl:     defaultTTL,
		timeout: defaultProbeWait,
		last:    map[string]string{},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Add registers a named probe. Duplicate ids replace the previous probe.
// Unconfigured dependencies are simply never added, so they do not appear
// in the snapshot or as a Prometheus series.
func (c *Checker) Add(id string, probe Probe) {
	if c == nil || probe == nil || id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, p := range c.probes {
		if p.id == id {
			c.probes[i].fn = probe
			return
		}
	}
	c.probes = append(c.probes, namedProbe{id: id, fn: probe})
}

// Snapshot returns the cached view, refreshing when it is stale. The request
// context is not used for the dial: a cancelled poll must not abort a probe
// that other callers (and the collector) will read.
//
// The mutex is not held across probes. Collect (/metrics) clones the last
// snapshot and must not inherit a store's dial timeout.
func (c *Checker) Snapshot(_ context.Context) Snapshot {
	if c == nil {
		return Snapshot{Resources: []Resource{}}
	}
	c.mu.Lock()
	if c.ready && time.Since(c.cachedAt) < c.ttl {
		snap := clone(c.cached)
		c.mu.Unlock()
		return snap
	}
	if wait := c.refreshing; wait != nil {
		c.mu.Unlock()
		<-wait
		c.mu.Lock()
		snap := clone(c.cached)
		c.mu.Unlock()
		return snap
	}
	done := make(chan struct{})
	c.refreshing = done
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.refreshing = nil
		close(done)
		c.mu.Unlock()
	}()

	snap := c.refresh()

	c.mu.Lock()
	c.cached = snap
	c.cachedAt = time.Now()
	c.ready = true
	out := clone(c.cached)
	c.mu.Unlock()
	return out
}

// Start refreshes on ttl so Prometheus sees a live gauge even when nobody
// is signed in to poll /api/status. Cancel ctx to stop.
func (c *Checker) Start(ctx context.Context) {
	if c == nil {
		return
	}
	go func() {
		_ = c.Snapshot(ctx)
		ticker := time.NewTicker(c.ttl)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = c.Snapshot(ctx)
			}
		}
	}()
}

func (c *Checker) refresh() Snapshot {
	c.mu.Lock()
	probes := append([]namedProbe(nil), c.probes...)
	timeout := c.timeout
	logger := c.logger
	c.mu.Unlock()

	results := make([]Resource, len(probes))
	var wg sync.WaitGroup
	for i, p := range probes {
		wg.Add(1)
		go func(i int, p namedProbe) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			err := p.fn(ctx)
			cancel()
			state := StateOK
			if err != nil {
				state = StateUnreachable
			}
			results[i] = Resource{ID: p.id, State: state}
		}(i, p)
	}
	wg.Wait()

	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range results {
		if prev, ok := c.last[r.ID]; !ok || prev != r.State {
			c.last[r.ID] = r.State
			if logger != nil {
				if r.State == StateUnreachable {
					logger.Warn("dependency unreachable", "id", r.ID)
				} else {
					logger.Info("dependency recovered", "id", r.ID)
				}
			}
		}
	}
	return Snapshot{Resources: results}
}

func clone(s Snapshot) Snapshot {
	if len(s.Resources) == 0 {
		return Snapshot{Resources: []Resource{}}
	}
	cp := make([]Resource, len(s.Resources))
	copy(cp, s.Resources)
	return Snapshot{Resources: cp}
}

var depUpDesc = prometheus.NewDesc(
	"foldex_dependency_up",
	"1 if the named optional dependency is reachable from this process, 0 if it is configured and unreachable.",
	[]string{"name"},
	nil,
)

// Describe implements prometheus.Collector.
func (c *Checker) Describe(ch chan<- *prometheus.Desc) {
	ch <- depUpDesc
}

// Collect implements prometheus.Collector. It never probes: a scrape must
// not inherit a store's dial timeout.
func (c *Checker) Collect(ch chan<- prometheus.Metric) {
	if c == nil {
		return
	}
	c.mu.Lock()
	snap := clone(c.cached)
	c.mu.Unlock()
	for _, r := range snap.Resources {
		v := 0.0
		if r.State == StateOK {
			v = 1
		}
		ch <- prometheus.MustNewConstMetric(depUpDesc, prometheus.GaugeValue, v, r.ID)
	}
}
