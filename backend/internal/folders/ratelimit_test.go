package folders

import (
	"sync"
	"sync/atomic"
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

// TestUnlockLimiter_ConcurrentBurstRespectsMax locks RACE-HER-004: N parallel
// beginAttempt calls must admit at most maxUnlockAttempts slots; the rest see
// lockout without ever reaching bcrypt (commitFail).
func TestUnlockLimiter_ConcurrentBurstRespectsMax(t *testing.T) {
	l := newUnlockLimiter()
	const n = 100
	var admitted atomic.Int64
	var rejected atomic.Int64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if _, ok := l.beginAttempt(42); !ok {
				rejected.Add(1)
				return
			}
			admitted.Add(1)
			l.commitFail(42)
		}()
	}
	wg.Wait()
	if got := admitted.Load(); got > int64(maxUnlockAttempts) {
		t.Fatalf("admitted %d concurrent attempts, want ≤%d", got, maxUnlockAttempts)
	}
	if rejected.Load() == 0 {
		t.Fatal("expected some rejections under burst")
	}
	if l.lockedUntil(42).IsZero() {
		t.Fatal("folder should be locked after concurrent fails")
	}
}

func TestUnlockLimiter_BeginReleaseDoesNotCountFail(t *testing.T) {
	l := newUnlockLimiter()
	if _, ok := l.beginAttempt(7); !ok {
		t.Fatal("first begin should succeed")
	}
	l.releaseAttempt(7)
	if fails, until := l.fail(7); fails != 1 || !until.IsZero() {
		t.Fatalf("after release, next fail should be count 1 unlocked, got fails=%d locked=%v", fails, !until.IsZero())
	}
}

func TestUnlockLimiter_CommitSuccessClears(t *testing.T) {
	l := newUnlockLimiter()
	if _, ok := l.beginAttempt(8); !ok {
		t.Fatal("begin")
	}
	l.commitFail(8)
	if _, ok := l.beginAttempt(8); !ok {
		t.Fatal("second begin")
	}
	l.commitSuccess(8)
	if fails, _ := l.fail(8); fails != 1 {
		t.Fatalf("after success, counter must restart at 1, got %d", fails)
	}
}
