package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SweepTouch is what makes the last_seen_at throttle map bounded.
//
// forgetTouch alone was NOT enough, and relying on it was the bug: it only
// fires on the two paths that revoke a single named session. Every bulk
// revocation — logout-everywhere, an admin disabling a user, a password change
// dropping other devices — and every grace-window sibling left an entry behind
// for the life of the process.
func TestSweepTouchDropsIdleEntriesAndKeepsFreshOnes(t *testing.T) {
	t.Parallel()
	m := &Middleware{lastTouch: map[int64]time.Time{
		1: time.Now().Add(-2 * time.Hour),
		2: time.Now().Add(-90 * time.Minute),
		3: time.Now(),
	}}

	dropped := m.SweepTouch(time.Hour)

	assert.Equal(t, 2, dropped)
	assert.Len(t, m.lastTouch, 1)
	_, fresh := m.lastTouch[3]
	assert.True(t, fresh, "an entry seen just now must survive")
}

func TestSweepTouchOnAnEmptyMap(t *testing.T) {
	t.Parallel()
	m := &Middleware{lastTouch: map[int64]time.Time{}}
	assert.Zero(t, m.SweepTouch(time.Hour))
}

// Dropping a live session's entry is harmless — the next request just pays one
// extra UPDATE and re-seeds it — so the sweep is allowed to be aggressive.
func TestSweepTouchIsSafeToOverPrune(t *testing.T) {
	t.Parallel()
	m := &Middleware{lastTouch: map[int64]time.Time{7: time.Now()}}
	assert.Equal(t, 1, m.SweepTouch(0))
	assert.Empty(t, m.lastTouch)
}

func TestForgetTouchRemovesOneEntry(t *testing.T) {
	t.Parallel()
	m := &Middleware{lastTouch: map[int64]time.Time{1: time.Now(), 2: time.Now()}}
	m.forgetTouch(1)
	assert.Len(t, m.lastTouch, 1)
	m.forgetTouch(999) // absent id must be a no-op, not a panic
	assert.Len(t, m.lastTouch, 1)
}

func TestIsSafeMethod(t *testing.T) {
	t.Parallel()
	for _, m := range []string{"GET", "get", "HEAD", "OPTIONS"} {
		assert.True(t, isSafeMethod(m), "%s carries no CSRF risk", m)
	}
	for _, m := range []string{"POST", "put", "PATCH", "DELETE"} {
		assert.False(t, isSafeMethod(m), "%s must require the CSRF header", m)
	}
}

// SweepLimiters is the fix for a real leak: the buckets are keyed by
// attacker-supplied e-mail on an UNAUTHENTICATED endpoint, so without eviction
// every address ever tried leaves a permanent entry. The method existing is not
// enough — main.go has to hang it off the sweeper's ticker, which the
// integration suite asserts separately.
func TestSweepLimitersEvictsEveryBucket(t *testing.T) {
	t.Parallel()
	h := newLimiterOnlyHandler(t)

	// Burn a failure in each bucket so all four maps hold a key.
	h.loginByIP.Fail("login:ip:198.51.100.1")
	h.loginByEmail.Fail("login:em:ghost@example.com")
	h.bootstrapIP.Fail("bootstrap:198.51.100.1")
	h.inviteIP.Fail("invite:198.51.100.1")

	// Nothing is stale yet.
	assert.Zero(t, h.SweepLimiters(time.Hour))

	// Everything is stale now — and none of these is locked out (one failure
	// each, against caps of 5/20), so all four are eligible.
	assert.Equal(t, 4, h.SweepLimiters(0))
	assert.Zero(t, h.SweepLimiters(0), "a second sweep has nothing left to drop")
}

// A live lockout must survive the sweep, or eviction would hand an attacker a
// free reset of their own penalty.
func TestSweepLimitersKeepsLiveLockouts(t *testing.T) {
	t.Parallel()
	h := newLimiterOnlyHandler(t)
	for range 5 { // the per-e-mail cap
		h.loginByEmail.Fail("login:em:target@example.com")
	}
	require.False(t, h.loginByEmail.LockedUntil("login:em:target@example.com").IsZero(),
		"fixture precondition: the key is locked out")

	h.SweepLimiters(0)

	assert.False(t, h.loginByEmail.LockedUntil("login:em:target@example.com").IsZero(),
		"sweeping must not lift an active lockout")
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "abc", truncate("abc", 5))
	assert.Equal(t, "abc", truncate("abc", 3))
	assert.Equal(t, "ab", truncate("abc", 2))
	assert.Equal(t, "", truncate("abc", 0))
	assert.Equal(t, "", truncate("", 5))
}

// A blank IP must become SQL NULL, not the empty string: `ip` is INET, and ”
// is not a valid INET value.
func TestNullIP(t *testing.T) {
	t.Parallel()
	assert.Nil(t, nullIP(""))
	got := nullIP("192.0.2.1")
	require.NotNil(t, got)
	assert.Equal(t, "192.0.2.1", *got)
}
