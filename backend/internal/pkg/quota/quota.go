// Package quota caps how many requests one principal may issue inside a
// window.
//
// It answers the gap docs/SDD-ABUSE-DEFENSE.md §5.3 calls the most serious one
// this instance has: an authenticated session is unlimited today, and a hostile
// account — or a legitimate account running a buggy script — can hold the
// sixteen-connection pool against every other tenant. That is the "DDoS" that
// actually exists for a service this size (§2.3), and nothing in the process
// currently refuses it.
//
// # Why this is not internal/pkg/attemptlimit
//
// The repo has an explicit rule against a second parallel implementation of a
// control it already has, so the reason has to be written down rather than
// assumed.
//
// attemptlimit counts CONSECUTIVE FAILURES per key and locks the key out for a
// fixed penalty once it crosses a threshold. Every part of that is the wrong
// shape here:
//
//   - It counts failures. A quota must count SUCCESSES — the requests it is
//     rationing are the ones that work, and the pool is saturated by traffic
//     that succeeds, not by traffic that errors.
//   - CommitSuccess RESETS the counter. One accepted request would erase the
//     whole record, so a caller alternating success and failure would never
//     accumulate anything to be limited on.
//   - It has no notion of refill. A budget that regenerates over a window is
//     what lets a legitimate burst (pasting a folder of links) pass and a
//     sustained loop not, and a lockout-after-N cannot express that: it either
//     admits the burst and never stops the loop, or stops the loop by locking
//     out the person who pasted.
//   - Its reserve-then-commit API (Begin → CommitFail/Release) exists to hold
//     a budget across a SLOW credential check. A quota decision is made before
//     the handler runs and is over in a microsecond; there is nothing to hold.
//
// What the two do share is the lesson INV-155 was written from: an in-memory
// map keyed by something a caller influences is a memory-exhaustion vector
// unless something prunes it. That is not shared code, it is a shared
// requirement, and this package meets it differently — see Sweep.
package quota

import (
	"math"
	"sync"
	"time"
)

// DefaultWindow is used when a caller constructs with a non-positive one.
const DefaultWindow = time.Minute

// defaultMaxKeys bounds the bucket map.
//
// The key space is already bounded by construction — a bucket exists per
// PRINCIPAL, and principals are the accounts on the instance, which the SDD
// puts at "1 to a few dozen". The ceiling is therefore not what stops the
// realistic case; it is what stops the unrealistic one from being unbounded,
// because "the key space is small" is a property of today's key function and
// not of this map. Ten thousand entries is roughly two megabytes and three
// orders of magnitude above any instance this software is for: high enough
// that eviction never touches a real deployment, low enough that a key
// function which someday admits more variety cannot grow without limit.
const defaultMaxKeys = 10_000

// evictionSample bounds the cost of making room.
//
// Picking the single stalest entry would mean scanning the whole map on every
// insert once it is full, which turns an O(1) admission check into O(n) under
// exactly the conditions where n is largest. Sampling a bounded number of
// entries — Go randomises map iteration order, so the sample is a fresh one
// each time — picks a nearly-stalest entry for a fixed cost. Being slightly
// wrong about WHICH idle bucket to drop costs nothing: an evicted bucket comes
// back full, and a bucket that is idle is already nearly full.
const evictionSample = 64

// evictionBatch is how many entries the pressure path frees at once. Freeing
// one leaves the next insert at the ceiling, so the sample-and-evict scan
// repeats per admission exactly while the map is full.
const evictionBatch = 32

// Decision is the answer to one admission check.
type Decision struct {
	// Allowed reports whether the request may proceed.
	Allowed bool
	// RetryAfter is how long until one token is available again. Zero when
	// Allowed. It is the real refill time rather than a constant, because a
	// client told to wait a minute for a token that arrives in a second is
	// being throttled far harder than the configured limit says.
	RetryAfter time.Duration
}

// Limiter is a token bucket per key, refilling continuously over one window.
//
// A token bucket rather than a fixed-window counter for one reason that
// matters at these magnitudes: a fixed window lets a caller spend the whole
// budget in the last instant of one window and the whole budget again in the
// first instant of the next, so a limit of 120/min admits a burst of 240
// against the pool. The bucket's continuous refill has no such boundary.
//
// The limit is a PARAMETER of Allow rather than construction state. The numbers
// live in an owner-editable policy (internal/abusepolicy) that must take effect
// without a restart — the screen that sets them is on the same instance being
// defended, and an operator tightening a limit during an incident cannot be
// told to redeploy first.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket

	window  time.Duration
	maxKeys int

	// lastSweep drives the self-sweep in Allow. See Sweep for why the pruning
	// lives here rather than on somebody else's ticker.
	lastSweep time.Time

	now func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New returns a limiter whose buckets refill one full budget per window.
func New(window time.Duration, maxKeys int) *Limiter {
	if window <= 0 {
		window = DefaultWindow
	}
	if maxKeys <= 0 {
		maxKeys = defaultMaxKeys
	}
	l := &Limiter{
		buckets: make(map[string]*bucket),
		window:  window,
		maxKeys: maxKeys,
		now:     time.Now,
	}
	l.lastSweep = l.now()
	return l
}

// WithClock overrides the time source. Tests use it to cross a window without
// sleeping through one — the expensive bucket's window is an hour. Production
// never calls it; call it before the limiter is shared with any goroutine.
func (l *Limiter) WithClock(now func() time.Time) *Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.now = now
	l.lastSweep = now()
	return l
}

// Window reports the refill window this limiter was built with.
func (l *Limiter) Window() time.Duration { return l.window }

// Allow charges one request against key, given the limit in force right now.
//
// The whole read-modify-write happens under one mutex. A check-then-act
// version — read the count, decide, increment — passes every sequential test
// and fails under load, because N parallel requests all read the same
// pre-cap value and all proceed.
func (l *Limiter) Allow(key string, limit int) Decision {
	if limit < 1 {
		// Only reachable from a bug upstream: abusepolicy's bounds refuse a
		// non-positive limit at both the read and the write path. Enforcing a
		// zero would refuse every mutation on the instance, which is a far
		// worse failure than admitting one request per window.
		limit = 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.maybeSweepLocked(now)

	capacity := float64(limit)
	rate := capacity / l.window.Seconds()

	b := l.buckets[key]
	if b == nil {
		l.makeRoomLocked()
		b = &bucket{tokens: capacity, last: now}
		l.buckets[key] = b
	} else {
		b.tokens = math.Min(capacity, b.tokens+now.Sub(b.last).Seconds()*rate)
		b.last = now
	}

	if b.tokens >= 1 {
		b.tokens--
		return Decision{Allowed: true}
	}
	return Decision{RetryAfter: roundUpMillis(time.Duration((1 - b.tokens) / rate * float64(time.Second)))}
}

// Refund returns one token to key.
//
// It exists so a request that is REFUSED costs nothing. A caller that charges
// two buckets — an expensive route pays both its own hourly budget and the
// ordinary per-minute one — would otherwise burn the first bucket's token on a
// request the second bucket rejected, and that error compounds in exactly the
// wrong direction: a user who hits the expensive ceiling would also drain their
// ordinary write budget while getting nothing done. SDD §8 calls a limiter that
// locks out legitimate users worse than a loose one.
func (l *Limiter) Refund(key string, limit int) {
	if limit < 1 {
		limit = 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.buckets[key]
	if b == nil {
		// Deliberately does not allocate. A refund for a key that was never
		// charged is a bug, and allocating here would let one turn into an
		// unbounded map through a path with no ceiling check.
		return
	}
	now := l.now()
	capacity := float64(limit)
	b.tokens = math.Min(capacity, b.tokens+now.Sub(b.last).Seconds()*(capacity/l.window.Seconds())+1)
	b.last = now
}

// Sweep drops buckets untouched for at least olderThan, and reports how many.
//
// olderThan is raised to the window when a caller asks for less, and that clamp
// is the correctness of the whole thing: a bucket dropped while it still owes
// tokens comes back FULL, which hands the caller back exactly the budget they
// were supposed to be out of. At one full window of idleness a bucket has
// refilled to capacity on its own, so deleting it loses no state at all.
//
// Allow calls this on its own once per window, so nothing outside this package
// has to remember to. That is deliberate: INV-155 exists because a limiter left
// off somebody else's sweep list grows forever and nothing says so. Here there
// is no external list to be left off. The method stays exported anyway so an
// operator-visible sweeper can report the eviction count alongside its own.
func (l *Limiter) Sweep(olderThan time.Duration) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sweepLocked(l.now(), olderThan)
}

// Len reports how many keys are currently tracked — for tests asserting the
// map does not grow without bound, and for a sweeper's log line.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

func (l *Limiter) maybeSweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < l.window {
		return
	}
	l.sweepLocked(now, l.window)
}

func (l *Limiter) sweepLocked(now time.Time, olderThan time.Duration) int {
	if olderThan < l.window {
		olderThan = l.window
	}
	l.lastSweep = now
	cutoff := now.Add(-olderThan)
	removed := 0
	for k, b := range l.buckets {
		if b.last.After(cutoff) {
			continue
		}
		delete(l.buckets, k)
		removed++
	}
	return removed
}

// makeRoomLocked guarantees the map stays at or under its ceiling.
//
// Refusing the new key instead would be worse than useless: the ceiling would
// become a way for whoever fills the map first to make every later principal
// unaccounted (fail-open) or refused (fail-closed, a denial of service built
// out of the defence). Evicting the stalest entry keeps the limiter working for
// everyone and costs the evicted key at most one extra request's worth of
// budget.
func (l *Limiter) makeRoomLocked() {
	if len(l.buckets) < l.maxKeys {
		return
	}
	// maybeSweepLocked, not sweepLocked: the sweep is a full scan of the map,
	// and at the ceiling this path runs on EVERY insert. Calling it
	// unconditionally meant one O(n) scan per admission, serialized under the
	// same mutex — measured at 63.6 µs/op against 49 ns/op normally, a 1,300×
	// cliff that arrives precisely when the instance is busiest. The
	// window-scoped guard was already here for exactly this, in the coalescer;
	// this one was missing it.
	l.maybeSweepLocked(l.now())
	if len(l.buckets) < l.maxKeys {
		return
	}
	// Evict a BATCH. Freeing one slot leaves the next insert at the ceiling, so
	// the sample-and-evict scan repeats per insert under pressure — the same
	// shape the sweep guard above removes, one level down.
	for range evictionBatch {
		if len(l.buckets) < l.maxKeys {
			return
		}
		l.evictStalestLocked()
	}
}

func (l *Limiter) evictStalestLocked() {
	var (
		stalestKey string
		stalest    time.Time
		seen       int
	)
	for k, b := range l.buckets {
		if seen == 0 || b.last.Before(stalest) {
			stalestKey, stalest = k, b.last
		}
		seen++
		if seen >= evictionSample {
			break
		}
	}
	if seen > 0 {
		delete(l.buckets, stalestKey)
	}
}

// roundUpMillis keeps the retry hint honest in the direction that matters: a
// client that waits exactly as long as it was told must find a token there. A
// hint truncated below the real refill time turns a well-behaved client into a
// loop of 429s.
func roundUpMillis(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Millisecond
	}
	if r := d % time.Millisecond; r != 0 {
		d += time.Millisecond - r
	}
	return d
}
