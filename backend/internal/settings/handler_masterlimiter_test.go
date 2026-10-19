package settings

import (
	"testing"
	"time"

	"foldex/internal/pkg/attemptlimit"
	"foldex/internal/pkg/authctx"
)

func TestNewHandlerOwnsADefaultLimiter(t *testing.T) {
	t.Parallel()
	a := NewHandler(NewRepository(nil))
	b := NewHandler(NewRepository(nil))
	if a.limiter == nil || b.limiter == nil {
		t.Fatal("NewHandler must install a master-password limiter")
	}
	if a.limiter == b.limiter {
		t.Fatal("handlers constructed separately must not silently share one limiter")
	}
}

func TestWithMasterLimiterReplacesTheBudgetAndReturnsSameHandler(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewRepository(nil))
	shared := attemptlimit.New(maxMasterAttempts, masterLockout)

	if got := h.WithMasterLimiter(shared); got != h {
		t.Fatal("WithMasterLimiter must return the same handler for fluent wiring")
	}
	if h.limiter != shared {
		t.Fatal("WithMasterLimiter must install the shared limiter")
	}

	// Spending through the handler's limiter is visible on the shared one:
	// the master secret is one, so its guessing budget must be one too.
	key := masterKeyFor(authctx.UserID(7))
	for range maxMasterAttempts {
		h.limiter.Fail(key)
	}
	if shared.LockedUntil(key).IsZero() {
		t.Fatal("failures recorded via the handler must lock the shared limiter")
	}
}

func TestSweepLimitersPrunesStaleKeysAndPreservesActiveLockouts(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	shared := attemptlimit.New(maxMasterAttempts, masterLockout).
		WithClock(func() time.Time { return now })
	h := NewHandler(NewRepository(nil)).WithMasterLimiter(shared)

	stale := masterKeyFor(authctx.UserID(1))
	shared.Fail(stale)

	now = now.Add(masterLockout + time.Nanosecond)
	locked := masterKeyFor(authctx.UserID(2))
	for range maxMasterAttempts {
		shared.Fail(locked)
	}

	if removed := h.SweepLimiters(masterLockout); removed != 1 {
		t.Fatalf("removed keys = %d, want 1 (only the stale entry)", removed)
	}
	if got := shared.Len(); got != 1 {
		t.Fatalf("tracked keys = %d, want only the active lockout", got)
	}
	if shared.LockedUntil(locked).IsZero() {
		t.Fatal("sweep lifted an active master-password lockout")
	}
}

func TestSweepLimitersOnFreshHandlerRemovesNothing(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewRepository(nil))
	if removed := h.SweepLimiters(time.Hour); removed != 0 {
		t.Fatalf("removed keys = %d, want 0", removed)
	}
}
