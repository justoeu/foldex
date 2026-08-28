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

// MaxMembersPerKey bounds the set a key remembers in set mode.
//
// Without a bound the limiter becomes the exhaustion vector it exists to close:
// members are caller strings, and on the login path the caller is an
// unauthenticated stranger who can mint a new one per request. The correction
// cannot open the hole it closes.
//
// The value is chosen ABOVE the largest ceiling any caller may configure —
// abusepolicy.MaxLoginDistinctAccountsPerIP is 100 — and not at the 64 the SDD
// sketched, because the two numbers meet: a cap BELOW the configurable ceiling
// would silently enforce a limit the operator did not choose and could not see,
// which is the INV-169 failure (a knob that reverts must say so, and this one
// could not). Sitting above every legal ceiling, the cap only ever binds on a
// caller that configured something illegal, and there its answer is "locked"
// (see commitFailForLocked) rather than "keep counting" — a full set is the one
// state where the limiter has lost the ability to tell breadth from noise, and
// the safe reading of that is refusal.
//
// internal/auth owns the guard that keeps the two in step; attemptlimit does
// not import abusepolicy, because a generic limiter that knows the login policy
// is no longer generic.
const MaxMembersPerKey = 128

// Limiter caps failures per key, counting either ATTEMPTS or DISTINCT MEMBERS.
//
// The reserve-then-commit API (Begin → CommitFail / CommitSuccess / Release)
// is what makes the cap hold under concurrency. A naive check-then-act — read
// the fail count, run bcrypt, increment — lets N parallel guesses all read the
// same pre-cap count and all proceed, so an attacker gets N tries for the price
// of one. Begin reserves a slot under the same mutex that reads the count, so
// in-flight attempts count against the budget while the slow hash runs.
//
// # The two modes, and why they are one type
//
// A key written with CommitFail counts DEPTH: consecutive failures, the shape
// every anti-brute-force bucket needs. A key written with CommitFailFor counts
// BREADTH: how many distinct members failed under it. docs/SDD-ABUSE-DEFENSE.md
// §4.2 argues why the login-by-IP bucket needs the second: counting depth there
// answered "is anyone at this address getting a password wrong?", which behind
// an office NAT is always yes, instead of "is this origin sweeping accounts?".
//
// They share one type because they share every guarantee that is hard to get
// right — the reservation, the lockout arithmetic, the sweep. A second
// structure would have to reimplement all three and would drift from this one,
// which is the reason this package exists in the first place.
type Limiter struct {
	mu      sync.Mutex
	entries map[string]*attempt
	max     int
	lockout time.Duration
	now     func() time.Time
}

type attempt struct {
	fails    int
	inFlight int
	// members is nil for a scalar key and allocated on the first CommitFailFor.
	// Its LENGTH is the count that key is judged by.
	members     map[string]struct{}
	lockedUntil time.Time
	lastFail    time.Time
}

// count is what the cap is measured against.
//
// Writing one key in both modes is a caller mistake, and the larger of the two
// counts is the only safe reading of it: taking the smaller would let a stray
// scalar Fail on a set key hand back budget that was already spent.
func (e *attempt) count() int {
	return max(e.fails, len(e.members))
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
	// In-flight attempts count against the budget in BOTH modes. In set mode
	// that is deliberately conservative — the requests in flight may all name
	// the member already counted — because the alternative is admitting them
	// all and learning afterwards, which is the check-then-act this API exists
	// to remove. The over-refusal it can cause is one request deep and lifts as
	// soon as the reservations settle.
	if e.count()+e.inFlight >= l.max {
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
	if e.count() >= l.max {
		e.lockedUntil = now.Add(l.lockout)
	}
	return e.fails, e.lockedUntil
}

// CommitFailFor records a failure ATTRIBUTED to a member, for a reserved slot,
// and returns how many distinct members the key now holds plus the lockout
// expiry (zero when not yet locked out).
//
// The member is stored, so a caller whose member strings are attacker-supplied
// must bound their length before passing them: MaxMembersPerKey bounds how MANY
// are kept, not how big each one is.
func (l *Limiter) CommitFailFor(key, member string) (distinct int, lockedUntil time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.commitFailForLocked(key, member, true)
}

// FailFor records a member failure without a prior Begin, mirroring Fail.
func (l *Limiter) FailFor(key, member string) (distinct int, lockedUntil time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.commitFailForLocked(key, member, false)
}

func (l *Limiter) commitFailForLocked(key, member string, releaseSlot bool) (int, time.Time) {
	now := l.now()
	e := l.entryLocked(key)
	if releaseSlot && e.inFlight > 0 {
		e.inFlight--
	}
	l.clearExpiredLocked(e, now)
	if e.members == nil {
		e.members = make(map[string]struct{})
	}
	if _, seen := e.members[member]; !seen && len(e.members) < MaxMembersPerKey {
		e.members[member] = struct{}{}
	}
	e.lastFail = now
	n := len(e.members)
	// A full set locks the key even when the configured ceiling is higher: at
	// that point the limiter can no longer tell a new account from one it has
	// already seen, so counting further would report a number it is not
	// measuring.
	if n >= l.max || n >= MaxMembersPerKey {
		e.lockedUntil = now.Add(l.lockout)
	}
	return n, e.lockedUntil
}

// Configure replaces the ceiling and the lockout duration in place, so an owner
// tightening a limit during an incident does not have to redeploy to be obeyed.
//
// It changes only the limits: what a key has already accumulated stays, because
// the alternative — resetting the counters on every policy reload — would hand
// anyone who can save the screen a way to clear every live lockout.
func (l *Limiter) Configure(max int, lockout time.Duration) {
	if max < 1 {
		max = 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.max = max
	l.lockout = lockout
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
	if e.count() >= l.max {
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
		// A served penalty starts a fresh set. Keeping the members would leave
		// the key one failure from locking again forever, which is a permanent
		// lockout wearing a lockout's clothes.
		e.members = nil
		e.lockedUntil = time.Time{}
	}
}

func (l *Limiter) gcLocked(key string, e *attempt) {
	if e.count() == 0 && e.inFlight == 0 && e.lockedUntil.IsZero() {
		delete(l.entries, key)
	}
}
