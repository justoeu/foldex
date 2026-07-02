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
const (
	maxUnlockAttempts = 5
	unlockLockout     = time.Hour
)

type unlockAttempt struct {
	fails       int
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

// fail records a wrong-password attempt and returns the running fail count plus
// the lockout expiry (zero when not yet locked). An expired lockout resets the
// counter before this attempt is tallied, so the window is rolling.
func (l *unlockLimiter) fail(id int64) (fails int, lockedUntil time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e := l.entries[id]
	if e == nil {
		e = &unlockAttempt{}
		l.entries[id] = e
	}
	if !e.lockedUntil.IsZero() && !e.lockedUntil.After(now) {
		e.fails = 0
		e.lockedUntil = time.Time{}
	}
	e.fails++
	if e.fails >= maxUnlockAttempts {
		e.lockedUntil = now.Add(unlockLockout)
	}
	return e.fails, e.lockedUntil
}

// reset clears a folder's attempt state — called on a successful unlock.
func (l *unlockLimiter) reset(id int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, id)
}
