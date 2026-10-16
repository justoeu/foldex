package folders

import (
	"testing"

	"foldex/internal/pkg/attemptlimit"
	"foldex/internal/pkg/authctx"
)

func TestNewHandlerOwnsADefaultMasterLimiter(t *testing.T) {
	t.Parallel()
	h := NewHandler(nil, []byte("01234567890123456789012345678901"), nil, nil)
	if h.masterLimiter == nil {
		t.Fatal("NewHandler must install a master-password limiter")
	}
	if h.masterLimiter == h.limiter {
		t.Fatal("master and per-folder unlock budgets must start as distinct limiters")
	}
}

func TestWithMasterLimiterReplacesOnlyTheMasterBudget(t *testing.T) {
	t.Parallel()
	h := NewHandler(nil, []byte("01234567890123456789012345678901"), nil, nil)
	unlockBefore := h.limiter
	shared := attemptlimit.New(maxUnlockAttempts, unlockLockout)

	if got := h.WithMasterLimiter(shared); got != h {
		t.Fatal("WithMasterLimiter must return the same handler for fluent wiring")
	}
	if h.masterLimiter != shared {
		t.Fatal("WithMasterLimiter must install the shared limiter as the master budget")
	}
	if h.limiter != unlockBefore {
		t.Fatal("WithMasterLimiter must not disturb the per-folder unlock limiter")
	}

	// Failures against the master key land on the shared budget — the same
	// one the settings handler spends from — not on the unlock limiter.
	key := masterKeyFor(authctx.UserID(9))
	for range maxUnlockAttempts {
		h.masterLimiter.Fail(key)
	}
	if shared.LockedUntil(key).IsZero() {
		t.Fatal("master failures via the handler must lock the shared limiter")
	}
	if !h.limiter.LockedUntil(key).IsZero() {
		t.Fatal("master failures must not spend the per-folder unlock budget")
	}
}
