package folders

import (
	"sync"
	"time"
)

// Per-folder brute-force throttle for the unlock endpoint (ADR-28): after
// maxUnlockAttempts consecutive wrong passwords a folder is locked out for
// unlockLockout before another attempt is accepted. State is in-memory only —
// single-user/local threat model, so a backend restart clearing the counters
// (and thus lifting a lockout early) is acceptable; the bcrypt cost per attempt
// is the real floor. A correct password resets the counter.
//
// Concurrency: beginAttempt reserves a slot under one Lock before bcrypt runs
// so N parallel wrong-password requests cannot all bypass the 5-attempt cap
// (check-then-act on lockedUntil+fail alone would admit every racer).
const (
	maxUnlockAttempts = 5
	unlockLockout     = time.Hour
)

type unlockAttempt struct {
	fails       int
	inFlight    int
	lockedUntil time.Time
}

type unlockLimiter struct {
	mu      sync.Mutex
	entries map[int64]*unlockAttempt
	now     func() time.Time // injectable for tests
}

func newUnlockLimiter() *unlockLimiter {
	return &unlockLimiter{entries: make(map[int64]*unlockAttempt), now: time.Now}
}

// lockedUntil reports the active lockout expiry for a folder, or the zero time
// when it is not currently locked out.
func (l *unlockLimiter) lockedUntil(id int64) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[id]
	if e == nil {
		return time.Time{}
	}
	if e.lockedUntil.After(l.now()) {
		return e.lockedUntil
	}
	return time.Time{}
}

func (l *unlockLimiter) entry(id int64) *unlockAttempt {
	e := l.entries[id]
	if e == nil {
		e = &unlockAttempt{}
		l.entries[id] = e
	}
	return e
}

func (l *unlockLimiter) clearExpiredLocked(e *unlockAttempt, now time.Time) {
	if !e.lockedUntil.IsZero() && !e.lockedUntil.After(now) {
		e.fails = 0
		e.lockedUntil = time.Time{}
	}
}

// beginAttempt reserves one attempt slot under the mutex. ok=false means the
// folder is locked out (or the concurrent cap is exhausted); lockedUntil is
// the Retry-After expiry. Callers MUST pair a successful begin with exactly
// one of releaseAttempt / commitFail / commitSuccess.
func (l *unlockLimiter) beginAttempt(id int64) (lockedUntil time.Time, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e := l.entry(id)
	l.clearExpiredLocked(e, now)
	if e.lockedUntil.After(now) {
		return e.lockedUntil, false
	}
	if e.fails+e.inFlight >= maxUnlockAttempts {
		e.lockedUntil = now.Add(unlockLockout)
		return e.lockedUntil, false
	}
	e.inFlight++
	return time.Time{}, true
}

// releaseAttempt drops a reserved slot without counting a failure (bad JSON,
// folder not found, not protected, etc.).
func (l *unlockLimiter) releaseAttempt(id int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[id]
	if e == nil || e.inFlight == 0 {
		return
	}
	e.inFlight--
	if e.fails == 0 && e.inFlight == 0 && e.lockedUntil.IsZero() {
		delete(l.entries, id)
	}
}

// commitFail records a wrong-password attempt for a previously reserved slot
// and returns the running fail count plus lockout expiry (zero when not yet
// locked).
func (l *unlockLimiter) commitFail(id int64) (fails int, lockedUntil time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e := l.entry(id)
	if e.inFlight > 0 {
		e.inFlight--
	}
	l.clearExpiredLocked(e, now)
	e.fails++
	if e.fails >= maxUnlockAttempts {
		e.lockedUntil = now.Add(unlockLockout)
	}
	return e.fails, e.lockedUntil
}

// commitSuccess clears attempt state after a correct password (reserved slot
// included).
func (l *unlockLimiter) commitSuccess(id int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, id)
}

// fail records a wrong-password attempt without a prior beginAttempt. Kept for
// tests and any non-handler call sites; production unlock uses begin/commit.
func (l *unlockLimiter) fail(id int64) (fails int, lockedUntil time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e := l.entry(id)
	l.clearExpiredLocked(e, now)
	e.fails++
	if e.fails >= maxUnlockAttempts {
		e.lockedUntil = now.Add(unlockLockout)
	}
	return e.fails, e.lockedUntil
}

// reset clears a folder's attempt state — called on a successful unlock when
// not using begin/commitSuccess.
func (l *unlockLimiter) reset(id int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, id)
}
