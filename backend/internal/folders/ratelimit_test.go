package folders

import (
	"testing"
	"time"
)

// The limiter MECHANICS (cap under concurrency, lockout expiry, key isolation,
// release-does-not-burn-budget, sweep) are locked by
// internal/pkg/attemptlimit's own suite — this package used to carry a second
// copy of both the implementation and those tests. What is folder-specific,
// and therefore still worth asserting here, is the POLICY wired into it and
// the key shape. The end-to-end lockout behaviour is covered by
// TestHandler_Unlock_LocksOutAfterFiveWrongAttempts.

func TestUnlockLimiterAppliesTheFolderPolicy(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	l := newUnlockLimiter().WithClock(func() time.Time { return base })

	key := unlockKeyFor(1)
	for i := 1; i < maxUnlockAttempts; i++ {
		if fails, until := l.Fail(key); fails != i || !until.IsZero() {
			t.Fatalf("fail %d: count=%d locked=%v, want count=%d unlocked", i, fails, !until.IsZero(), i)
		}
	}

	fails, until := l.Fail(key)
	if fails != maxUnlockAttempts || until.IsZero() {
		t.Fatalf("attempt %d should lock: count=%d locked=%v", maxUnlockAttempts, fails, !until.IsZero())
	}
	if got := l.LockedUntil(key); got != base.Add(unlockLockout) {
		t.Fatalf("LockedUntil = %v, want %v", got, base.Add(unlockLockout))
	}
}

// Two folders must not share a budget — the key has to carry the id, not be a
// constant. A limiter keyed by anything folder-invariant would let one wrong
// password anywhere lock every protected folder at once.
func TestUnlockKeyForIsPerFolder(t *testing.T) {
	t.Parallel()
	if unlockKeyFor(10) == unlockKeyFor(11) {
		t.Fatal("distinct folders must produce distinct limiter keys")
	}

	l := newUnlockLimiter()
	for i := 0; i < maxUnlockAttempts; i++ {
		l.Fail(unlockKeyFor(10))
	}
	if l.LockedUntil(unlockKeyFor(10)).IsZero() {
		t.Fatal("folder 10 should be locked")
	}
	if !l.LockedUntil(unlockKeyFor(11)).IsZero() {
		t.Fatal("folder 11 must be unaffected")
	}
}

func TestSweepLimitersPrunesStaleKeysAndPreservesActiveLockouts(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	h := &Handler{limiter: newUnlockLimiter().WithClock(func() time.Time { return now })}
	h.limiter.Fail(unlockKeyFor(10))

	now = now.Add(unlockAttemptRetention + time.Nanosecond)
	for range maxUnlockAttempts {
		h.limiter.Fail(unlockKeyFor(11))
	}

	if removed := h.SweepLimiters(unlockAttemptRetention); removed != 1 {
		t.Fatalf("removed keys = %d, want 1", removed)
	}
	if got := h.limiter.Len(); got != 1 {
		t.Fatalf("tracked keys = %d, want only the active lockout", got)
	}
	if h.limiter.LockedUntil(unlockKeyFor(11)).IsZero() {
		t.Fatal("production sweep lifted an active folder lockout")
	}
}

func TestForgetDeletedUnlockAttemptsRemovesOnlyDeletedFolders(t *testing.T) {
	t.Parallel()
	h := &Handler{limiter: newUnlockLimiter()}
	h.limiter.Fail(unlockKeyFor(10))
	for range maxUnlockAttempts {
		h.limiter.Fail(unlockKeyFor(11))
	}

	h.forgetDeletedUnlockAttempts([]int64{10})

	if got := h.limiter.Len(); got != 1 {
		t.Fatalf("tracked keys = %d, want 1", got)
	}
	if !h.limiter.LockedUntil(unlockKeyFor(10)).IsZero() {
		t.Fatal("deleted folder key must be forgotten")
	}
	if h.limiter.LockedUntil(unlockKeyFor(11)).IsZero() {
		t.Fatal("an unrelated folder key must remain locked")
	}
}
