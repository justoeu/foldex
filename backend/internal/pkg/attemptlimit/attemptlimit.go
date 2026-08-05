// Package attemptlimit is the shared in-memory brute-force throttle.
//
// It generalizes the per-folder unlock limiter ADR-28 introduced (which keyed
// on int64 folder ids) to arbitrary string keys, so login, bootstrap and the
// folder unlock all share one implementation and one set of concurrency
// guarantees instead of three copies drifting apart.
//
// State is in-memory only. A restart lifts every lockout early, which is
// acceptable for the surfaces that use it because each one has a SECOND,
// durable control behind it: the folder unlock has bcrypt's cost per attempt,
// and login has both the per-e-mail bucket and (from PR3) the challenge attempt
// counter, which lives in the database precisely because a restart must not
// reset a second-factor budget.
package attemptlimit

import (
	"sync"
	"time"
)

// Limiter caps consecutive failures per key.
//
// The reserve-then-commit API (Begin → CommitFail / CommitSuccess / Release)
// is what makes the cap hold under concurrency. A naive check-then-act — read
// the fail count, run bcrypt, increment — lets N parallel guesses all read the
// same pre-cap count and all proceed, so an attacker gets N tries for the price
// of one. Begin reserves a slot under the same mutex that reads the count, so
// in-flight attempts count against the budget while the slow hash runs.
type Limiter struct {
	mu      sync.Mutex
	entries map[string]*attempt
	max     int
	lockout time.Duration
	now     func() time.Time
}

type attempt struct {
	fails       int
	inFlight    int
	lockedUntil time.Time
	lastFail    time.Time
}

// New returns a limiter allowing max consecutive failures per key before
// locking that key out for lockout.
func New(max int, lockout time.Duration) *Limiter {
	if max < 1 {
		max = 1
	}
	return &Limiter{
		entries: make(map[string]*attempt),
		max:     max,
		lockout: lockout,
		now:     time.Now,
	}
}

// WithClock overrides the time source. Tests use it to advance past a lockout
// without sleeping; production never calls it.
func (l *Limiter) WithClock(now func() time.Time) *Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.now = now
	return l
}

// LockedUntil reports the active lockout expiry for key, or the zero time when
// it is not currently locked out.
func (l *Limiter) LockedUntil(key string) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e == nil {
		return time.Time{}
	}
	if e.lockedUntil.After(l.now()) {
		return e.lockedUntil
	}
	return time.Time{}
}

// Begin reserves one attempt slot. ok=false means the key is locked out (or the
// in-flight cap is exhausted) and lockedUntil carries the Retry-After expiry.
//
// Every successful Begin MUST be paired with exactly one of CommitFail,
// CommitSuccess or Release, or the slot leaks and the key drifts toward a
// permanent lockout.
func (l *Limiter) Begin(key string) (lockedUntil time.Time, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e := l.entryLocked(key)
	l.clearExpiredLocked(e, now)
	if e.lockedUntil.After(now) {
		return e.lockedUntil, false
	}
	if e.fails+e.inFlight >= l.max {
		e.lockedUntil = now.Add(l.lockout)
		return e.lockedUntil, false
	}
	e.inFlight++
	return time.Time{}, true
}

// Release drops a reserved slot WITHOUT counting a failure. Use it when the
// request never got as far as testing the credential — malformed JSON, missing
// row, a surface that turned out not to be protected. Counting those would let
// a third party lock a victim out with requests that never guess anything.
func (l *Limiter) Release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e == nil || e.inFlight == 0 {
		return
	}
	e.inFlight--
	l.gcLocked(key, e)
}

// CommitFail records a failed attempt for a reserved slot and returns the
// running fail count plus the lockout expiry (zero when not yet locked out).
func (l *Limiter) CommitFail(key string) (fails int, lockedUntil time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e := l.entryLocked(key)
	if e.inFlight > 0 {
		e.inFlight--
	}
	l.clearExpiredLocked(e, now)
	e.fails++
	e.lastFail = now
	if e.fails >= l.max {
		e.lockedUntil = now.Add(l.lockout)
	}
	return e.fails, e.lockedUntil
}

// CommitSuccess clears all attempt state for key. A correct credential resets
// the budget — the cap is on consecutive failures, not lifetime attempts.
func (l *Limiter) CommitSuccess(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// Fail records a failure without a prior Begin, for call sites that do not need
// the concurrency reservation (and for tests).
func (l *Limiter) Fail(key string) (fails int, lockedUntil time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e := l.entryLocked(key)
	l.clearExpiredLocked(e, now)
	e.fails++
	e.lastFail = now
	if e.fails >= l.max {
		e.lockedUntil = now.Add(l.lockout)
	}
	return e.fails, e.lockedUntil
}

// Reset clears attempt state for key.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// Sweep drops entries that are neither locked out nor mid-attempt and whose
// failures are stale.
//
// Without this the map is unbounded: every distinct key an attacker sends —
// and the login limiter is keyed by e-mail, which is attacker-supplied — leaves
// a permanent entry. A few million requests turn a throttle into a memory leak.
// The caller runs this on a ticker.
func (l *Limiter) Sweep(olderThan time.Duration) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-olderThan)
	removed := 0
	for k, e := range l.entries {
		if e.inFlight > 0 {
			continue
		}
		// A live lockout must survive: dropping it would lift the penalty.
		if e.lockedUntil.After(now) {
			continue
		}
		if e.lastFail.After(cutoff) {
			continue
		}
		delete(l.entries, k)
		removed++
	}
	return removed
}

// Len reports how many keys are currently tracked. Used by the sweeper's log
// line and by tests asserting the map does not grow without bound.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

func (l *Limiter) entryLocked(key string) *attempt {
	e := l.entries[key]
	if e == nil {
		e = &attempt{}
		l.entries[key] = e
	}
	return e
}

func (l *Limiter) clearExpiredLocked(e *attempt, now time.Time) {
	if !e.lockedUntil.IsZero() && !e.lockedUntil.After(now) {
		e.fails = 0
		e.lockedUntil = time.Time{}
	}
}

func (l *Limiter) gcLocked(key string, e *attempt) {
	if e.fails == 0 && e.inFlight == 0 && e.lockedUntil.IsZero() {
		delete(l.entries, key)
	}
}
