package quota

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clock is a manually advanced time source. Every test here is about a window,
// and sleeping through a real minute (let alone a real hour, which is the
// expensive bucket's window) is how a suite becomes something nobody runs.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Unix(1_700_000_000, 0)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestAllow_SpendsTheBudgetThenRefuses(t *testing.T) {
	t.Parallel()
	l := New(time.Minute, 100)

	for i := 0; i < 5; i++ {
		d := l.Allow("u:1", 5)
		require.True(t, d.Allowed, "request %d must fit inside a budget of 5", i+1)
		assert.Zero(t, d.RetryAfter, "an accepted request carries no retry hint")
	}

	d := l.Allow("u:1", 5)
	assert.False(t, d.Allowed, "the sixth request must not fit a budget of 5")
	assert.Positive(t, d.RetryAfter, "a refusal must say how long to back off")
	assert.LessOrEqual(t, d.RetryAfter, time.Minute,
		"backing off longer than the window would be a lie: the bucket refills within one")
}

// The retry hint is what makes 429 recoverable rather than a wall. It has to be
// the real refill time, not a constant: a client told to wait a minute when a
// token arrives in a second is being throttled far harder than the limit says.
func TestAllow_RetryAfterIsTheRealRefillTime(t *testing.T) {
	t.Parallel()
	c := newClock()
	l := New(time.Minute, 100).WithClock(c.now)

	for i := 0; i < 60; i++ {
		require.True(t, l.Allow("u:1", 60).Allowed)
	}
	d := l.Allow("u:1", 60)
	require.False(t, d.Allowed)
	// 60 per minute is one per second.
	assert.InDelta(t, float64(time.Second), float64(d.RetryAfter), float64(50*time.Millisecond))

	// And waiting exactly that long is enough — the hint must be honest in the
	// direction that matters, or a well-behaved client loops on 429 forever.
	c.advance(d.RetryAfter)
	assert.True(t, l.Allow("u:1", 60).Allowed, "the caller waited exactly as told and is still refused")
}

func TestAllow_RefillsOverTheWindow(t *testing.T) {
	t.Parallel()
	c := newClock()
	l := New(time.Minute, 100).WithClock(c.now)

	for i := 0; i < 10; i++ {
		require.True(t, l.Allow("u:1", 10).Allowed)
	}
	require.False(t, l.Allow("u:1", 10).Allowed)

	c.advance(time.Minute)
	for i := 0; i < 10; i++ {
		assert.True(t, l.Allow("u:1", 10).Allowed, "a full window must restore the whole budget")
	}
	assert.False(t, l.Allow("u:1", 10).Allowed, "and no more than the whole budget")
}

// Two principals must not share a budget. This is the failure mode SDD §8
// names as a revert criterion: a limiter that locks out a user who did nothing
// denies service for free.
func TestAllow_BudgetsAreIndependentPerKey(t *testing.T) {
	t.Parallel()
	l := New(time.Minute, 100)

	for i := 0; i < 3; i++ {
		require.True(t, l.Allow("u:1", 3).Allowed)
	}
	require.False(t, l.Allow("u:1", 3).Allowed, "u:1 exhausted its own budget")

	for i := 0; i < 3; i++ {
		assert.True(t, l.Allow("u:2", 3).Allowed, "u:2 must not pay for u:1's traffic")
	}
}

// The limit is a per-call argument, not construction state, because the policy
// behind it is editable at runtime: an owner tightening a limit during an
// incident cannot be told to redeploy first.
func TestAllow_ANewLimitTakesEffectWithoutRebuilding(t *testing.T) {
	t.Parallel()
	c := newClock()
	l := New(time.Minute, 100)
	l.WithClock(c.now)

	for i := 0; i < 10; i++ {
		require.True(t, l.Allow("u:1", 10).Allowed)
	}
	c.advance(time.Minute) // refill to whatever the capacity now is

	// Tightened to 2: the bucket must be clamped down to the NEW capacity, not
	// keep the ten tokens it refilled to under the old one.
	assert.True(t, l.Allow("u:1", 2).Allowed)
	assert.True(t, l.Allow("u:1", 2).Allowed)
	assert.False(t, l.Allow("u:1", 2).Allowed, "a tightened limit must bind immediately")
}

// A limit of zero or less can only come from a bug upstream, and the wrong
// answer is to enforce it: it would refuse every write on the instance. The
// bounds in abusepolicy already make this unreachable through configuration;
// this is what keeps a future caller from turning a mistake into an outage.
func TestAllow_ANonPositiveLimitIsTreatedAsOne(t *testing.T) {
	t.Parallel()
	l := New(time.Minute, 100)
	assert.True(t, l.Allow("u:1", 0).Allowed, "the first request must still pass")
	assert.False(t, l.Allow("u:1", 0).Allowed)
}

func TestRefund_ReturnsATokenWithoutExceedingCapacity(t *testing.T) {
	t.Parallel()
	l := New(time.Minute, 100)

	require.True(t, l.Allow("u:1", 2).Allowed)
	require.True(t, l.Allow("u:1", 2).Allowed)
	require.False(t, l.Allow("u:1", 2).Allowed)

	l.Refund("u:1", 2)
	assert.True(t, l.Allow("u:1", 2).Allowed, "a refunded token must be spendable again")
	assert.False(t, l.Allow("u:1", 2).Allowed)

	// Refunding more than was spent must not manufacture budget.
	l.Refund("u:1", 2)
	l.Refund("u:1", 2)
	l.Refund("u:1", 2)
	assert.True(t, l.Allow("u:1", 2).Allowed)
	assert.True(t, l.Allow("u:1", 2).Allowed)
	assert.False(t, l.Allow("u:1", 2).Allowed, "refunds must clamp at the capacity")
}

func TestRefund_OnAnUnknownKeyDoesNotCreateABucket(t *testing.T) {
	t.Parallel()
	l := New(time.Minute, 100)
	l.Refund("never-seen", 10)
	assert.Zero(t, l.Len(), "refunding a key nobody spent must not allocate one")
}

// The ceiling is the whole reason this is a package and not a map: without it
// the limiter is the next memory-exhaustion vector, reachable by whoever can
// mint distinct keys.
func TestAllow_TheMapNeverExceedsItsCeiling(t *testing.T) {
	t.Parallel()
	l := New(time.Minute, 32)
	for i := 0; i < 5000; i++ {
		l.Allow(fmt.Sprintf("u:%d", i), 10)
	}
	assert.LessOrEqual(t, l.Len(), 32, "the bucket map grew past its ceiling")
}

// Eviction under pressure must not hand out free budget to the key doing the
// pressing: the entry being spent right now is the freshest one, so it is the
// last thing an eviction should pick.
func TestAllow_EvictionKeepsTheActiveKeyAccounted(t *testing.T) {
	t.Parallel()
	l := New(time.Minute, 8)

	require.True(t, l.Allow("attacker", 2).Allowed)
	require.True(t, l.Allow("attacker", 2).Allowed)
	for i := 0; i < 200; i++ {
		l.Allow(fmt.Sprintf("filler:%d", i), 10)
		assert.False(t, l.Allow("attacker", 2).Allowed,
			"filling the map must not reset the offender's own bucket")
	}
}

func TestSweep_DropsIdleBucketsAndKeepsIndebtedOnes(t *testing.T) {
	t.Parallel()
	c := newClock()
	l := New(time.Minute, 100).WithClock(c.now)

	require.True(t, l.Allow("idle", 10).Allowed)
	require.True(t, l.Allow("busy", 10).Allowed)
	require.Equal(t, 2, l.Len())

	c.advance(30 * time.Second)
	l.Allow("busy", 10) // touched inside the window

	removed := l.Sweep(time.Minute)
	assert.Equal(t, 0, removed, "nothing is idle for a full window yet")
	assert.Equal(t, 2, l.Len())

	c.advance(time.Minute)
	removed = l.Sweep(time.Minute)
	assert.Equal(t, 2, removed)
	assert.Zero(t, l.Len(), "a bucket idle for a whole window has refilled — it holds no state")
}

// Sweeping sooner than the window would forgive debt: a bucket dropped while it
// still owes tokens comes back full, which is exactly the budget the caller was
// supposed to be out of.
func TestSweep_NeverForgivesDebtByShorteningTheWindow(t *testing.T) {
	t.Parallel()
	c := newClock()
	l := New(time.Minute, 100).WithClock(c.now)

	for i := 0; i < 5; i++ {
		require.True(t, l.Allow("u:1", 5).Allowed)
	}
	c.advance(2 * time.Second)
	l.Sweep(time.Second) // caller asks for an unsafely short retention

	assert.False(t, l.Allow("u:1", 5).Allowed, "the sweep handed the budget back")
}

// Nothing outside this package has to remember to run the sweep. INV-155 exists
// because a bucket left out of somebody else's sweep list only ever grows; the
// answer here is that there is no outside list to be left out of.
func TestAllow_SweepsItselfWithoutAnExternalTicker(t *testing.T) {
	t.Parallel()
	c := newClock()
	l := New(time.Minute, 100_000).WithClock(c.now)

	for i := 0; i < 500; i++ {
		l.Allow(fmt.Sprintf("burst:%d", i), 10)
	}
	require.Equal(t, 500, l.Len())

	c.advance(2 * time.Minute)
	l.Allow("someone", 10)

	assert.Equal(t, 1, l.Len(), "the idle burst should have been reclaimed by the limiter itself")
}

// The cap must hold when the requests arrive at once, which is the only way
// they arrive in production. A check-then-act limiter passes every sequential
// test in this file and fails this one.
func TestAllow_ConcurrentCallersCannotExceedTheCap(t *testing.T) {
	t.Parallel()
	const (
		limit    = 50
		requests = 400
	)
	l := New(time.Minute, 100)

	var granted atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if l.Allow("u:1", limit).Allowed {
				granted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.EqualValues(t, limit, granted.Load(),
		"%d parallel callers were granted %d of a budget of %d", requests, granted.Load(), limit)
}

func TestAllow_ConcurrentDistinctKeysStayWithinTheCeiling(t *testing.T) {
	t.Parallel()
	l := New(time.Minute, 64)

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				l.Allow(fmt.Sprintf("k:%d:%d", worker, j), 10)
			}
		}(i)
	}
	wg.Wait()
	assert.LessOrEqual(t, l.Len(), 64)
}

func TestNew_RejectsUnusableConstruction(t *testing.T) {
	t.Parallel()
	l := New(0, 0)
	assert.True(t, l.Allow("u:1", 1).Allowed)
	assert.Positive(t, l.Window(), "a non-positive window must fall back to something usable")
}
