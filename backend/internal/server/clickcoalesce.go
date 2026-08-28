package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"net/http"
	"sort"
	"sync"
	"time"

	"foldex/internal/abusepolicy"
	"foldex/internal/pkg/clickctx"
)

// clickCoalesceMaxKeys bounds the dedup map.
//
// The key is (entity kind, entity id, hashed visitor). A visitor cannot mint
// addresses freely, but they CAN walk every slug they know, so the entity half
// is bounded by the library and the visitor half by the addresses that actually
// reach the process. Fifty thousand entries is roughly two megabytes and far
// above any instance this software is for, while still being a number rather
// than a hope: without a ceiling the coalescer is simply the next thing to
// fill, and an attacker who cannot grow click_log any more grows this instead.
const clickCoalesceMaxKeys = 50_000

// clickCoalesceEvictionSample bounds the cost of making room, for the same
// reason quota.evictionSample does: scanning the whole map on every insert
// would be O(n) exactly when n is largest.
const clickCoalesceEvictionSample = 64

// clickCoalesceEvictionBatch is how many entries one eviction frees.
//
// More than one, because freeing a single slot leaves the very next insert at
// the ceiling: the sampling scan would then run on every insert for as long as
// the pressure lasts, which is precisely the period when it can least be
// afforded. A batch amortises that scan over the whole batch.
const clickCoalesceEvictionBatch = 32

// visitorKey is the opaque per-process identity of one visitor.
//
// A keyed digest, never the address. This map lives in shared process memory
// that no privacy decision covers, and "should a visitor's IP be retained
// against the things they read?" is a question this change does not need to
// answer to remove a write amplifier — so it does not answer it. The key is
// random per process, so the digests are not a table anyone can precompute or
// correlate across restarts, and 128 bits is far past collision risk for a map
// this size.
type visitorKey [16]byte

type coalesceKey struct {
	kind    string
	id      int64
	visitor visitorKey
}

// clickCoalescer suppresses a repeat click row from the same visitor on the
// same entity inside a short window — SDD §5.4.
//
// /go/{slug} and /n/{slug} take no session and each hit writes a row to
// click_log, so a loop over one known slug is unbounded database writing by an
// anonymous caller. The state here is deliberately EPHEMERAL and in memory: a
// click count is a product metric, not accounting, and losing the second hit of
// one visitor inside ten seconds changes nothing anyone reads. Persisting the
// dedup instead would mean a new column holding who visited what, which is a
// much larger decision than the one being made here.
type clickCoalescer struct {
	pol policyReader

	mu   sync.Mutex
	seen map[coalesceKey]time.Time
	// lastSweep drives the periodic reclaim in allow. Nothing outside this
	// file knows the coalescer exists, so it prunes itself rather than waiting
	// on a sweeper somebody has to remember to register it with. It starts at
	// the zero time so the first call sweeps an empty map, rather than being
	// seeded from a clock a test may be about to replace.
	lastSweep time.Time

	maxKeys int
	key     []byte
	now     func() time.Time
}

func newClickCoalescer(pol policyReader) *clickCoalescer {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		// crypto/rand cannot fail on any platform this runs on, and a process
		// that cannot produce randomness has worse problems than click counting
		// — every session token comes from the same source.
		panic("server: cannot seed the click coalescer key: " + err.Error())
	}
	return &clickCoalescer{
		pol:     pol,
		seen:    make(map[coalesceKey]time.Time),
		maxKeys: clickCoalesceMaxKeys,
		key:     key,
		now:     time.Now,
	}
}

// middleware installs the gate the repositories consult.
//
// It suppresses the WRITE and never the resolution. That distinction is the
// whole safety of the feature: a mistake that suppressed the redirect would
// break every shared link on the instance for every visitor, which is far worse
// than an over-counted click — so the gate is a boolean the repository asks
// about a row it has already resolved, and there is no path from here to the
// destination lookup at all.
func (c *clickCoalescer) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		window := c.window(r.Context())
		if window <= 0 {
			// Off is a supported configuration: the operator is buying back an
			// exact click counter, with nginx's limit_req still covering the
			// surface. No gate at all, so clickctx.Allow answers its default.
			next.ServeHTTP(w, r)
			return
		}
		// clientIP, not RemoteAddr: trustedProxyRealIP has already resolved the
		// real address when — and only when — a configured proxy vouched for
		// it, and NormalizeIP is what stops "::ffff:1.2.3.4" being a second
		// visitor. Without that, the IPv4-mapped form would be a free way to
		// double every counter.
		ip := clientIP(r)
		if ip == "" {
			// NormalizeIP answers "" for a RemoteAddr it cannot parse. Hashing
			// that would put every such caller under ONE key, and one of them
			// would then suppress the clicks of all the others — a coalescer
			// that loses other people's rows is worse than no coalescer, since
			// the counter it protects is the thing it just corrupted. No gate,
			// so these record.
			next.ServeHTTP(w, r)
			return
		}
		v := c.visitor(ip)
		gate := func(kind string, id int64) bool { return c.allow(kind, id, v, window) }
		next.ServeHTTP(w, r.WithContext(clickctx.WithGate(r.Context(), gate)))
	})
}

func (c *clickCoalescer) window(ctx context.Context) time.Duration {
	p := abusepolicy.Default()
	if c.pol != nil {
		p = c.pol.Current(ctx)
	}
	return time.Duration(p.ClickCoalesceSeconds()) * time.Second
}

func (c *clickCoalescer) visitor(ip string) visitorKey {
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write([]byte(ip))
	var out visitorKey
	copy(out[:], mac.Sum(nil))
	return out
}

// allow is the test-and-set. It has to be one critical section: a
// check-then-act version lets every goroutine in a concurrent burst read "not
// seen" and all of them write, which is the exact burst this exists to absorb.
func (c *clickCoalescer) allow(kind string, id int64, v visitorKey, window time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	c.maybeSweepLocked(now, window)
	k := coalesceKey{kind: kind, id: id, visitor: v}
	if seenAt, ok := c.seen[k]; ok && now.Sub(seenAt) < window {
		return false
	}
	c.makeRoomLocked(now, window)
	c.seen[k] = now
	return true
}

// maybeSweepLocked drops expired entries once per window.
//
// Without it the map only shrinks under ceiling pressure, so an instance that
// never reaches the ceiling would hold every (entity, visitor) pair it has ever
// seen for as long as the process lives — bounded, but bounded by a number
// nobody chose. Past the window an entry decides nothing, so dropping it loses
// no state at all.
func (c *clickCoalescer) maybeSweepLocked(now time.Time, window time.Duration) {
	if now.Sub(c.lastSweep) < window {
		return
	}
	c.lastSweep = now
	c.dropExpiredLocked(now, window)
}

func (c *clickCoalescer) dropExpiredLocked(now time.Time, window time.Duration) {
	for k, seenAt := range c.seen {
		if now.Sub(seenAt) >= window {
			delete(c.seen, k)
		}
	}
}

// makeRoomLocked keeps the map at or under its ceiling.
//
// Expired entries go first — they hold nothing, since past the window the
// visitor counts again anyway. Only if that is not enough does it evict a live
// one, and an evicted entry costs exactly one extra click row. The alternative
// designs are both worse: refusing to insert (and recording every click) hands
// whoever fills the map a way to switch the coalescer off, and refusing to
// record hands them a way to zero everyone's counters.
func (c *clickCoalescer) makeRoomLocked(now time.Time, window time.Duration) {
	if len(c.seen) < c.maxKeys {
		return
	}
	// Only when the periodic sweep did not just run: at the ceiling, repeating
	// a scan that has already been done this window is a full O(n) pass under
	// the lock for zero deletions, on every insert — and being at the ceiling
	// IS the attack this defends against, so that is exactly the wrong moment
	// to serialise every visitor behind a scan.
	if now.Sub(c.lastSweep) >= window {
		c.lastSweep = now
		c.dropExpiredLocked(now, window)
		if len(c.seen) < c.maxKeys {
			return
		}
	}
	// A BATCH, not one entry. Freeing a single slot leaves the next insert at
	// the ceiling again, so the eviction scan would run once per insert forever.
	c.evictBatchLocked(clickCoalesceEvictionBatch)
}

// evictBatchLocked drops up to n of the stalest entries it finds in a bounded
// sample. Go randomises map iteration order, so the sample is a fresh one each
// time; being slightly wrong about WHICH entry to drop costs the evicted
// visitor at most one extra click row.
func (c *clickCoalescer) evictBatchLocked(n int) {
	type victim struct {
		key    coalesceKey
		seenAt time.Time
	}
	sample := make([]victim, 0, clickCoalesceEvictionSample)
	for k, seenAt := range c.seen {
		sample = append(sample, victim{key: k, seenAt: seenAt})
		if len(sample) >= clickCoalesceEvictionSample {
			break
		}
	}
	sort.Slice(sample, func(i, j int) bool { return sample[i].seenAt.Before(sample[j].seenAt) })
	if n > len(sample) {
		n = len(sample)
	}
	for _, v := range sample[:n] {
		delete(c.seen, v.key)
	}
}

func (c *clickCoalescer) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.seen)
}
