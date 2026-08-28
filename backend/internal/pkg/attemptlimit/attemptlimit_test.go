package attemptlimit_test

import (
	"fmt"
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

// ─────────────────────────────────────────────────────────────────────
// Set mode: the cap counts DISTINCT MEMBERS, not attempts
// ─────────────────────────────────────────────────────────────────────

// The NAT case, and the whole reason set mode exists (SDD §4.2).
//
// Counting depth answered "is anyone at this address getting passwords wrong?",
// which behind an office NAT is always yes. The bucket has to answer "is this
// origin sweeping many accounts?" instead, and one person mistyping their own
// password twenty times is not that.
func TestSetMode_RepeatedFailuresOnOneMemberNeverLockTheKey(t *testing.T) {
	t.Parallel()
	l := attemptlimit.New(10, time.Hour)

	for i := range 20 {
		_, ok := l.Begin("ip")
		require.True(t, ok, "attempt %d must be admitted: one account is not a spray", i+1)
		distinct, until := l.CommitFailFor("ip", "person@example.com")
		assert.Equal(t, 1, distinct, "twenty failures against one account are still one account")
		assert.True(t, until.IsZero(), "attempt %d must not lock the origin", i+1)
	}
	assert.True(t, l.LockedUntil("ip").IsZero())
}

func TestSetMode_DistinctMembersLockTheKey(t *testing.T) {
	t.Parallel()
	const max = 10
	l := attemptlimit.New(max, time.Hour)

	for i := range max {
		_, ok := l.Begin("ip")
		require.True(t, ok, "member %d must be admitted", i+1)
		distinct, _ := l.CommitFailFor("ip", fmt.Sprintf("victim-%d@example.com", i))
		assert.Equal(t, i+1, distinct)
	}
	assert.False(t, l.LockedUntil("ip").IsZero(),
		"the %dth distinct account failed from one origin is a spray", max)

	_, ok := l.Begin("ip")
	assert.False(t, ok, "a locked-out origin must be refused before the credential is tested")
}

// FailFor is the no-reservation variant, mirroring Fail.
func TestSetMode_FailForCountsWithoutABegin(t *testing.T) {
	t.Parallel()
	l := attemptlimit.New(2, time.Hour)

	distinct, until := l.FailFor("ip", "a@example.com")
	require.Equal(t, 1, distinct)
	require.True(t, until.IsZero())

	distinct, until = l.FailFor("ip", "b@example.com")
	assert.Equal(t, 2, distinct)
	assert.False(t, until.IsZero())
}

// Same argument as the scalar reserve-then-commit test: without Begin holding a
// slot across the slow credential check, N parallel guesses all read the same
// pre-cap count and all proceed.
func TestSetMode_ParallelAttemptsCannotExceedTheCap(t *testing.T) {
	t.Parallel()
	const max = 5
	l := attemptlimit.New(max, time.Hour)

	var mu sync.Mutex
	admitted := 0
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := l.Begin("ip"); ok {
				mu.Lock()
				admitted++
				mu.Unlock()
				time.Sleep(5 * time.Millisecond) // stand in for bcrypt
				l.CommitFailFor("ip", fmt.Sprintf("victim-%d@example.com", i))
			}
		}()
	}
	wg.Wait()
	assert.LessOrEqual(t, admitted, max,
		"%d of 50 parallel attempts were admitted against a cap of %d", admitted, max)
}

// The set is the limiter's own memory, and an unbounded one would make the
// defence the exhaustion vector it exists to close: members come from the
// caller, and on the login path the caller is an unauthenticated stranger.
func TestSetMode_MembersAreCappedAndAFullSetIsLockedOut(t *testing.T) {
	t.Parallel()
	// A ceiling ABOVE the member cap is the misconfiguration this guards: the
	// safe reading of a full set is "locked", never "keep counting".
	l := attemptlimit.New(attemptlimit.MaxMembersPerKey+50, time.Hour)

	var distinct int
	for i := range attemptlimit.MaxMembersPerKey + 200 {
		distinct, _ = l.FailFor("ip", fmt.Sprintf("victim-%d@example.com", i))
	}
	assert.Equal(t, attemptlimit.MaxMembersPerKey, distinct,
		"the set must stop growing at MaxMembersPerKey however many members arrive")
	assert.False(t, l.LockedUntil("ip").IsZero(),
		"a full set must lock the key: a cap that silently stops counting is a cap that stops limiting")
}

func TestSetMode_SweepFreesTheSet(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l := attemptlimit.New(10, time.Hour).WithClock(func() time.Time { return now })

	for i := range 5 {
		l.FailFor("ip", fmt.Sprintf("victim-%d@example.com", i))
	}
	require.Equal(t, 1, l.Len())

	now = now.Add(30 * time.Minute)
	require.Equal(t, 1, l.Sweep(10*time.Minute))
	assert.Zero(t, l.Len(), "the entry — and the set it holds — must be gone")

	distinct, _ := l.FailFor("ip", "victim-0@example.com")
	assert.Equal(t, 1, distinct, "a swept key starts from an empty set, not from the old one")
}

func TestSetMode_SuccessAndResetClearTheSet(t *testing.T) {
	t.Parallel()
	l := attemptlimit.New(10, time.Hour)
	l.FailFor("ip", "a@example.com")
	l.FailFor("ip", "b@example.com")

	l.CommitSuccess("ip")
	distinct, _ := l.FailFor("ip", "c@example.com")
	require.Equal(t, 1, distinct, "a success clears the set, like it clears the scalar count")

	l.Reset("ip")
	distinct, _ = l.FailFor("ip", "d@example.com")
	assert.Equal(t, 1, distinct, "Reset clears the set")
}

func TestSetMode_LockoutExpiryClearsTheSet(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l := attemptlimit.New(2, time.Minute).WithClock(func() time.Time { return now })
	l.FailFor("ip", "a@example.com")
	l.FailFor("ip", "b@example.com")
	require.False(t, l.LockedUntil("ip").IsZero())

	now = now.Add(2 * time.Minute)
	_, ok := l.Begin("ip")
	require.True(t, ok, "the lockout must lift once its window passes")
	distinct, _ := l.CommitFailFor("ip", "a@example.com")
	assert.Equal(t, 1, distinct,
		"a served penalty starts a fresh set, not one member short of locking again")
}

// Release is for requests that never tested a credential; it must not enrol a
// member, or a third party could lock an origin out with malformed requests.
func TestSetMode_ReleaseAddsNoMember(t *testing.T) {
	t.Parallel()
	l := attemptlimit.New(2, time.Hour)
	for range 10 {
		_, ok := l.Begin("ip")
		require.True(t, ok)
		l.Release("ip")
	}
	assert.Zero(t, l.Len(), "a fully released key must leave nothing behind")
	assert.True(t, l.LockedUntil("ip").IsZero())
}

// The other half of Release, and the dangerous one: a released slot leaves the
// key with no failures and no lockout, which is exactly the shape the garbage
// collector deletes. If the set did not count as state, a caller could clear
// its own breadth budget on demand — reach a bucket that refuses (the login
// path releases the IP slot when the per-account bucket says no), and the
// origin walks away with an empty set.
func TestSetMode_ReleaseKeepsTheSetTheKeyAlreadyHolds(t *testing.T) {
	t.Parallel()
	l := attemptlimit.New(10, time.Hour)
	l.FailFor("ip", "a@example.com")
	l.FailFor("ip", "b@example.com")

	_, ok := l.Begin("ip")
	require.True(t, ok)
	l.Release("ip")

	require.Equal(t, 1, l.Len(), "the key must survive a released attempt")
	distinct, _ := l.FailFor("ip", "c@example.com")
	assert.Equal(t, 3, distinct,
		"a request that guessed nothing must not refund the accounts already counted")
}

// ─────────────────────────────────────────────────────────────────────
// Configure: the limits are policy, and policy reloads without a restart
// ─────────────────────────────────────────────────────────────────────

func TestConfigure_RaisesTheCeilingWithoutLosingWhatWasCounted(t *testing.T) {
	t.Parallel()
	l := attemptlimit.New(3, time.Hour)
	l.FailFor("ip", "a@example.com")
	l.FailFor("ip", "b@example.com")

	l.Configure(5, time.Hour)

	_, ok := l.Begin("ip")
	require.True(t, ok, "raising the ceiling must reopen a key that was one member from it")
	distinct, until := l.CommitFailFor("ip", "c@example.com")
	require.Equal(t, 3, distinct, "the two members counted before the change must survive it")
	require.True(t, until.IsZero())

	l.FailFor("ip", "d@example.com")
	_, until = l.FailFor("ip", "e@example.com")
	assert.False(t, until.IsZero(), "the new ceiling of 5 must be what locks the key")
}

func TestConfigure_LowersTheCeilingAgainstStateAlreadyCounted(t *testing.T) {
	t.Parallel()
	l := attemptlimit.New(10, time.Hour)
	l.Fail("k")
	l.Fail("k")

	l.Configure(2, time.Hour)

	_, ok := l.Begin("k")
	assert.False(t, ok, "tightening a limit during an incident must apply to what is already counted")
}

func TestConfigure_ClampsNonsenseAndAppliesTheNewLockout(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l := attemptlimit.New(10, time.Hour).WithClock(func() time.Time { return now })

	l.Configure(0, 30*time.Minute)
	_, ok := l.Begin("k")
	require.True(t, ok, "max<1 must clamp to 1, not to zero — zero would refuse every request")
	_, until := l.CommitFail("k")
	assert.Equal(t, now.Add(30*time.Minute), until, "the new lockout duration must be the one applied")
}

// Configure runs while requests are in flight — an owner tightening a limit
// during an incident is exactly when the login path is busiest. The assertion
// is the race detector.
func TestConfigure_IsSafeConcurrentlyWithAttempts(t *testing.T) {
	t.Parallel()
	l := attemptlimit.New(5, time.Hour)

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := l.Begin("ip"); ok {
				l.CommitFailFor("ip", fmt.Sprintf("victim-%d@example.com", i))
			}
		}()
	}
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Configure(3+i, time.Duration(i+1)*time.Minute)
		}()
	}
	wg.Wait()
	assert.Positive(t, l.Len(), "the key must still exist after the storm")
}
