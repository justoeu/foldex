package abusepolicy

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Reader is the storage seam. The enforcement sites depend on this, not on a
// concrete repository, which is what lets the login path and the quota
// middleware be built and tested without a database.
type Reader interface {
	Get(ctx context.Context) (Policy, error)
}

// DefaultTTL is how stale an enforcement decision may be.
//
// Thirty seconds is chosen against the ONE workflow that notices: an owner
// changes a limit and immediately tries to observe the effect. Half a minute is
// short enough that the screen does not feel broken and long enough that the
// login path is not issuing a query per attempt — which would put a database
// round-trip in front of every unauthenticated request, i.e. hand an attacker a
// cheaper way to load the pool than the thing being rate limited.
const DefaultTTL = 30 * time.Second

// loadTimeout bounds one refresh. It is short because a slow answer here delays
// a login, and the fallback (serve the previous value) is correct.
const loadTimeout = 3 * time.Second

// Cache is the live policy every enforcement site reads.
//
// It exists because these numbers must take effect WITHOUT a restart: the
// screen that sets them is on the same instance being defended, and an operator
// tightening a limit during an incident cannot be told to redeploy first. That
// is the whole of what "dynamic" means here — the values reload, they do not
// tune themselves. Nothing in this package changes a limit on its own, and
// docs/ARCHITECTURE.md ADR-47 records why: an automatic limiter hands the
// attacker the input to a control that can lock the operator out, which is the
// same objection INV-178 makes to automatic IP blocking.
//
// # Failure posture
//
// Current NEVER returns an error and never blocks on a failed load. A database
// hiccup must not decide how many login attempts an origin gets: it serves the
// last known good policy, and the compiled defaults before the first successful
// load. Both are safe values — this is fail-STATIC, not fail-open, because the
// limits stay enforced at whatever they last were.
type Cache struct {
	reader Reader
	ttl    time.Duration
	log    *slog.Logger

	// exp is read on the hot path without taking mu. Every request that
	// enforces a limit passes through here; a mutex per request would put the
	// defence itself on the contended path.
	exp atomic.Int64

	mu      sync.Mutex
	current Policy

	// now is set at construction and by WithClock BEFORE first use, and read
	// without the mutex thereafter. It is not guarded because guarding it would
	// deadlock: the refresh path already holds mu when it needs the time, and
	// sync.Mutex is not reentrant.
	now func() time.Time
}

// NewCache returns a cache seeded with the compiled defaults, so it is usable
// before the first load and during any outage of the store.
func NewCache(r Reader, ttl time.Duration, log *slog.Logger) *Cache {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if log == nil {
		log = slog.Default()
	}
	return &Cache{reader: r, ttl: ttl, log: log, current: Default(), now: time.Now}
}

// WithClock overrides the time source for tests; production never calls it.
// Call it before the cache is shared with any goroutine.
func (c *Cache) WithClock(now func() time.Time) *Cache {
	c.now = now
	return c
}

// Current returns the policy to enforce right now.
func (c *Cache) Current(ctx context.Context) Policy {
	if c == nil || c.reader == nil {
		return Default()
	}
	now := c.clock()
	if c.exp.Load() > now.UnixNano() {
		c.mu.Lock()
		p := c.current
		c.mu.Unlock()
		return p
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	// Double-check: another goroutine may have refreshed while we waited.
	if c.exp.Load() > c.clock().UnixNano() {
		return c.current
	}

	// Detached context. The caller's request may be cancelled a millisecond
	// from now — a browser navigating away, a client timing out — and letting
	// that cancel the refresh would mean a busy instance never updates its own
	// policy while looking like it is trying to.
	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loadTimeout)
	defer cancel()

	p, err := c.reader.Get(loadCtx)
	if err != nil {
		// Back off for the full TTL rather than retrying on the next request:
		// a store that is down would otherwise get one failing query per
		// login attempt, which is exactly the load amplification the limiter
		// exists to prevent, arriving from inside.
		c.exp.Store(c.clock().Add(c.ttl).UnixNano())
		c.log.Warn("abuse policy load failed; keeping the previous values", "error", err)
		return c.current
	}
	c.current = p.Sanitize()
	c.exp.Store(c.clock().Add(c.ttl).UnixNano())
	return c.current
}

// Invalidate forces the next Current to reload. The write handler calls it so
// an owner who just saved sees the new limits take effect immediately instead
// of within the TTL — the one case where waiting would read as a bug.
func (c *Cache) Invalidate() {
	if c == nil {
		return
	}
	c.exp.Store(0)
}

func (c *Cache) clock() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}
