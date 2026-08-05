package attemptlimit_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/attemptlimit"
)

func TestLocksOutAfterMaxFailures(t *testing.T) {
	t.Parallel()
	l := attemptlimit.New(3, time.Hour)

	for i := 1; i <= 3; i++ {
		_, ok := l.Begin("k")
		require.True(t, ok, "attempt %d must be admitted", i)
		l.CommitFail("k")
	}
	until, ok := l.Begin("k")
	assert.False(t, ok, "the 4th attempt must be refused")
	assert.True(t, until.After(time.Now()), "a refusal must carry a Retry-After expiry")
}

func TestSuccessResetsTheBudget(t *testing.T) {
	t.Parallel()
	l := attemptlimit.New(3, time.Hour)
	l.Begin("k")
	l.CommitFail("k")
	l.Begin("k")
	l.CommitFail("k")

	l.Begin("k")
	l.CommitSuccess("k")

	// The cap is on CONSECUTIVE failures. Without the reset, a user who mistypes
	// twice a day would eventually be locked out by accumulated history.
	for i := 1; i <= 3; i++ {
		_, ok := l.Begin("k")
		assert.True(t, ok, "attempt %d after a success must be admitted", i)
		l.CommitFail("k")
	}
}

// Release must NOT count as a failure: it is the path for requests that never
// tested a credential. Counting them would let a third party lock a victim out
// with malformed requests that guess nothing.
func TestReleaseDoesNotBurnBudget(t *testing.T) {
	t.Parallel()
	l := attemptlimit.New(2, time.Hour)
	for range 10 {
		_, ok := l.Begin("k")
		require.True(t, ok)
		l.Release("k")
	}
	_, ok := l.Begin("k")
	assert.True(t, ok, "released attempts must not consume the budget")
}

func TestLockoutExpires(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l := attemptlimit.New(1, time.Minute).WithClock(func() time.Time { return now })

	l.Begin("k")
	l.CommitFail("k")
	_, ok := l.Begin("k")
	require.False(t, ok)

	now = now.Add(2 * time.Minute)
	_, ok = l.Begin("k")
	assert.True(t, ok, "the lockout must lift once its window passes")
}

func TestKeysAreIndependent(t *testing.T) {
	t.Parallel()
	l := attemptlimit.New(1, time.Hour)
	l.Begin("a")
	l.CommitFail("a")

	_, ok := l.Begin("b")
	assert.True(t, ok, "locking out one key must not affect another")
}

// The concurrency guarantee that justifies the reserve-then-commit API.
//
// With a plain check-then-act (read count → run bcrypt → increment), N parallel
// guesses all observe the same pre-cap count and all proceed, handing an
// attacker N tries for the price of one. Begin reserves under the mutex, so
// in-flight attempts count against the budget while the slow hash runs.
func TestParallelAttemptsCannotExceedTheCap(t *testing.T) {
	t.Parallel()
	const max = 5
	l := attemptlimit.New(max, time.Hour)

	var mu sync.Mutex
	admitted := 0
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := l.Begin("k"); ok {
				mu.Lock()
				admitted++
				mu.Unlock()
				// Stand in for bcrypt: the slot must stay reserved across it.
				time.Sleep(5 * time.Millisecond)
				l.CommitFail("k")
			}
		}()
	}
	wg.Wait()
	assert.LessOrEqual(t, admitted, max,
		"%d of 50 parallel attempts were admitted against a cap of %d", admitted, max)
}

func TestLockedUntil(t *testing.T) {
	t.Parallel()
	l := attemptlimit.New(1, time.Hour)
	assert.True(t, l.LockedUntil("k").IsZero(), "an untouched key is not locked out")

	l.Fail("k")
	assert.False(t, l.LockedUntil("k").IsZero())
}

// Sweep is what keeps the map from being a memory leak: the login limiter is
// keyed by an attacker-supplied e-mail, so without eviction every distinct
// address ever tried leaves a permanent entry.
func TestSweepDropsStaleEntriesButKeepsLiveLockouts(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l := attemptlimit.New(3, time.Hour).WithClock(func() time.Time { return now })

	l.Fail("stale") // 1 failure, no lockout
	for range 3 {   // 3 failures → locked out for an hour
		l.Fail("locked")
	}
	require.Equal(t, 2, l.Len())

	now = now.Add(30 * time.Minute)
	removed := l.Sweep(10 * time.Minute)

	assert.Equal(t, 1, removed)
	assert.Equal(t, 1, l.Len())
	assert.False(t, l.LockedUntil("locked").IsZero(),
		"sweeping must never lift a live lockout — that would hand the attacker a reset")
}

func TestSweepKeepsInFlightEntries(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l := attemptlimit.New(3, time.Hour).WithClock(func() time.Time { return now })

	_, ok := l.Begin("busy")
	require.True(t, ok)

	now = now.Add(time.Hour)
	l.Sweep(time.Minute)

	assert.Equal(t, 1, l.Len(), "an entry with a reserved slot must survive the sweep")
}

func TestResetClearsState(t *testing.T) {
	t.Parallel()
	l := attemptlimit.New(1, time.Hour)
	l.Fail("k")
	require.False(t, l.LockedUntil("k").IsZero())

	l.Reset("k")
	assert.True(t, l.LockedUntil("k").IsZero())
	assert.Equal(t, 0, l.Len())
}

func TestNewClampsNonsenseMax(t *testing.T) {
	t.Parallel()
	l := attemptlimit.New(0, time.Hour)
	_, ok := l.Begin("k")
	require.True(t, ok, "max<1 must clamp to 1, not to zero — zero would refuse every request")
	l.CommitFail("k")
	_, ok = l.Begin("k")
	assert.False(t, ok)
}
