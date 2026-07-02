package folders

import (
	"testing"
	"time"
)

func TestUnlockLimiter_LocksAfterMaxAttempts(t *testing.T) {
	base := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	l := newUnlockLimiter()
	l.now = func() time.Time { return base }

	// First maxUnlockAttempts-1 fails do not lock.
	for i := 1; i < maxUnlockAttempts; i++ {
		fails, until := l.fail(1)
		if fails != i {
			t.Fatalf("fail %d: got count %d", i, fails)
		}
		if !until.IsZero() {
			t.Fatalf("fail %d: unexpected lockout", i)
		}
		if !l.lockedUntil(1).IsZero() {
			t.Fatalf("fail %d: should not be locked yet", i)
		}
	}

	// The maxUnlockAttempts-th fail locks for unlockLockout.
	fails, until := l.fail(1)
	if fails != maxUnlockAttempts || until.IsZero() {
		t.Fatalf("final fail: count=%d locked=%v", fails, !until.IsZero())
	}
	if got := l.lockedUntil(1); got != base.Add(unlockLockout) {
		t.Fatalf("lockedUntil = %v, want %v", got, base.Add(unlockLockout))
	}
}

func TestUnlockLimiter_ResetClears(t *testing.T) {
	l := newUnlockLimiter()
	l.fail(2)
	l.fail(2)
	l.reset(2)
	if fails, _ := l.fail(2); fails != 1 {
		t.Fatalf("after reset, next fail should be count 1, got %d", fails)
	}
}

func TestUnlockLimiter_LockoutExpires(t *testing.T) {
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	l := newUnlockLimiter()
	l.now = func() time.Time { return now }
	for i := 0; i < maxUnlockAttempts; i++ {
		l.fail(3)
	}
	if l.lockedUntil(3).IsZero() {
		t.Fatal("should be locked")
	}
	// Advance past the lockout — it clears, and the counter restarts.
	now = now.Add(unlockLockout + time.Second)
	if !l.lockedUntil(3).IsZero() {
		t.Fatal("lockout should have expired")
	}
	if fails, until := l.fail(3); fails != 1 || !until.IsZero() {
		t.Fatalf("post-expiry fail should restart at 1 with no lock, got fails=%d locked=%v", fails, !until.IsZero())
	}
}

func TestUnlockLimiter_IsolatesFolders(t *testing.T) {
	l := newUnlockLimiter()
	for i := 0; i < maxUnlockAttempts; i++ {
		l.fail(10)
	}
	if l.lockedUntil(10).IsZero() {
		t.Fatal("folder 10 should be locked")
	}
	if !l.lockedUntil(11).IsZero() {
		t.Fatal("folder 11 must be unaffected")
	}
}
