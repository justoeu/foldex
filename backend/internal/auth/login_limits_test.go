package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/abusepolicy"
	"foldex/internal/pkg/attemptlimit"
)

// ─────────────────────────────────────────────────────────────────────
// The two login buckets answer different questions (SDD §4.2)
// ─────────────────────────────────────────────────────────────────────

// The NAT case. One person mistyping their own password all morning is noise;
// the per-account bucket already holds it, and it must not spend the budget of
// everyone sharing the office's address.
func TestLoginFailure_TheIPBucketCountsAccounts_NotAttempts(t *testing.T) {
	t.Parallel()
	h := newLimiterOnlyHandler(t)
	h.configureLoginLimits(t.Context())

	const ipKey = "login:ip:203.0.113.7"
	for i := range 20 {
		_, ok := h.loginByIP.Begin(ipKey)
		require.True(t, ok, "attempt %d from the office must be admitted", i+1)
		h.recordLoginFailure(ipKey, "login:em:person@example.com", "person@example.com")
	}

	assert.True(t, h.loginByIP.LockedUntil(ipKey).IsZero(),
		"twenty failures against ONE account must never lock the origin — behind a NAT that origin is a building")
}

func TestLoginFailure_DistinctAccountsFromOneOriginLockIt(t *testing.T) {
	t.Parallel()
	h := newLimiterOnlyHandler(t)
	h.configureLoginLimits(t.Context())
	n := abusepolicy.Default().LoginDistinctAccountsPerIP

	const ipKey = "login:ip:198.51.100.9"
	for i := range n {
		bucket := fmt.Sprintf("victim-%d@example.com", i)
		_, ok := h.loginByIP.Begin(ipKey)
		require.True(t, ok, "account %d must be admitted", i+1)
		h.recordLoginFailure(ipKey, "login:em:"+bucket, bucket)
	}

	assert.False(t, h.loginByIP.LockedUntil(ipKey).IsZero(),
		"%d distinct accounts failed from one origin is a spray and must lock it", n)
}

// Not counting an unknown address is itself an oracle — the attacker learns
// which addresses are lockable, and therefore which exist. Set mode makes that
// easier to get wrong than the scalar bucket did, because a member the limiter
// declines to enrol is a member the origin never pays for.
func TestLoginFailure_UnknownAddressesCostTheSameAsRealOnes(t *testing.T) {
	t.Parallel()
	n := abusepolicy.Default().LoginDistinctAccountsPerIP

	// Two origins, same script, one sweeping real accounts and one sweeping
	// addresses that were never registered. The bucket must not be able to tell
	// them apart, because the handler cannot tell the caller apart either.
	spray := func(t *testing.T, addr func(int) string) time.Time {
		t.Helper()
		h := newLimiterOnlyHandler(t)
		h.configureLoginLimits(t.Context())
		ipKey := "login:ip:" + addr(0)
		for i := range n {
			bucket := addr(i)
			if _, ok := h.loginByIP.Begin(ipKey); !ok {
				break
			}
			h.recordLoginFailure(ipKey, "login:em:"+bucket, bucket)
		}
		return h.loginByIP.LockedUntil(ipKey)
	}

	real := spray(t, func(i int) string { return fmt.Sprintf("real-%d@example.com", i) })
	ghost := spray(t, func(i int) string { return fmt.Sprintf("ghost-%d@example.com", i) })

	assert.False(t, real.IsZero(), "fixture precondition: a full spray locks the origin")
	assert.Equal(t, real.IsZero(), ghost.IsZero(),
		"a sweep of addresses that do not exist must cost exactly what a sweep of real ones costs")
}

// The member is attacker-supplied and the set holds up to MaxMembersPerKey of
// them per origin. Login deliberately does NOT validate the address — it must
// answer identically for garbage and for a real account — so without a bound
// here one origin could park MaxMembersPerKey × 64 KiB of strings in memory.
func TestLoginFailure_TheMemberIsTruncatedLikeTheAuditRow(t *testing.T) {
	t.Parallel()
	h := newLimiterOnlyHandler(t)
	h.configureLoginLimits(t.Context())

	const ipKey = "login:ip:192.0.2.1"
	huge := string(make([]byte, 64*1024))
	for i := range 3 {
		// Two oversized addresses sharing their first maxAuditEmail bytes must
		// collapse into ONE member, which is what proves the truncation ran.
		h.recordLoginFailure(ipKey, "login:em:x", huge+fmt.Sprint(i))
	}
	distinct, _ := h.loginByIP.FailFor(ipKey, truncateTo(huge, maxAuditEmail))
	assert.Equal(t, 1, distinct,
		"oversized addresses must be truncated before they enter the set, or one origin parks megabytes in it")
}

// ─────────────────────────────────────────────────────────────────────
// The limits are policy, and policy is live
// ─────────────────────────────────────────────────────────────────────

type staticPolicy struct {
	p   abusepolicy.Policy
	err error
}

func (s staticPolicy) Get(context.Context) (abusepolicy.Policy, error) { return s.p, s.err }

func TestConfigureLoginLimits_AppliesTheLivePolicy(t *testing.T) {
	t.Parallel()
	h := newLimiterOnlyHandler(t)
	p := abusepolicy.Default()
	p.LoginDistinctAccountsPerIP = 3
	p.LoginFailuresPerAccount = 4
	p.LoginWindowMinutes = 7
	h.abuse = abusepolicy.NewCache(staticPolicy{p: p}, time.Minute, nil)

	h.configureLoginLimits(t.Context())

	const ipKey = "login:ip:203.0.113.20"
	for i := range 3 {
		h.loginByIP.FailFor(ipKey, fmt.Sprintf("victim-%d@example.com", i))
	}
	until := h.loginByIP.LockedUntil(ipKey)
	require.False(t, until.IsZero(), "the configured ceiling of 3 distinct accounts must be the one enforced")
	assert.WithinDuration(t, time.Now().Add(7*time.Minute), until, time.Minute,
		"the configured window must be the lockout the origin serves")

	for range 4 {
		h.loginByEmail.Fail("login:em:target@example.com")
	}
	assert.False(t, h.loginByEmail.LockedUntil("login:em:target@example.com").IsZero(),
		"the configured per-account ceiling of 4 must be the one enforced")
}

// Most tests build a Handler directly and never wire a policy cache; so does an
// instance whose store is unreachable. Both must run the compiled defaults
// rather than panic or fall to zero, which would refuse every login.
func TestConfigureLoginLimits_WithoutACacheRunsTheCompiledDefaults(t *testing.T) {
	t.Parallel()
	d := abusepolicy.Default()

	wirings := map[string]func(*Handler){
		"never wired": func(*Handler) {},
		"store is down": func(h *Handler) {
			h.abuse = abusepolicy.NewCache(staticPolicy{err: errors.New("boom")}, time.Minute,
				slog.New(slog.NewJSONHandler(io.Discard, nil)))
		},
	}
	for name, wire := range wirings {
		h := newLimiterOnlyHandler(t)
		wire(h)
		h.configureLoginLimits(t.Context())

		const ipKey = "login:ip:198.51.100.44"
		for i := range d.LoginDistinctAccountsPerIP - 1 {
			h.loginByIP.FailFor(ipKey, fmt.Sprintf("victim-%d@example.com", i))
		}
		require.True(t, h.loginByIP.LockedUntil(ipKey).IsZero(), "%s: locked one account early", name)
		h.loginByIP.FailFor(ipKey, "victim-last@example.com")
		assert.False(t, h.loginByIP.LockedUntil(ipKey).IsZero(),
			"%s: the compiled default of %d must be what applies", name, d.LoginDistinctAccountsPerIP)
	}
}

// The set cap is the limiter's memory bound; the policy ceiling is the
// operator's. If the second could exceed the first, an owner could type a
// number the limiter silently refuses to honour — the INV-169 failure, where a
// knob reverts and the screen still shows what was typed.
func TestLoginLimits_TheConfigurableCeilingFitsInsideTheLimiterSet(t *testing.T) {
	t.Parallel()
	assert.LessOrEqual(t, abusepolicy.MaxLoginDistinctAccountsPerIP, attemptlimit.MaxMembersPerKey,
		"an origin may be configured to spend %d distinct accounts but the set only remembers %d: "+
			"raise attemptlimit.MaxMembersPerKey or lower abusepolicy.MaxLoginDistinctAccountsPerIP — "+
			"never leave a ceiling the limiter cannot reach",
		abusepolicy.MaxLoginDistinctAccountsPerIP, attemptlimit.MaxMembersPerKey)
}

// ─────────────────────────────────────────────────────────────────────
// Regression: every OTHER bucket still counts depth, at its old ceiling
// ─────────────────────────────────────────────────────────────────────

// Set mode is additive or it is a regression. These ten buckets are unchanged
// by the SDD, and a shared implementation is exactly where "unchanged" stops
// being obvious.
func TestScalarBuckets_KeepTheirDocumentedCaps(t *testing.T) {
	t.Parallel()
	h := newLimiterOnlyHandler(t)
	h.configureLoginLimits(t.Context())

	cases := []struct {
		name string
		l    *attemptlimit.Limiter
		cap  int
	}{
		{"loginByEmail", h.loginByEmail, abusepolicy.Default().LoginFailuresPerAccount},
		{"bootstrapIP", h.bootstrapIP, 5},
		{"inviteIP", h.inviteIP, 20},
		{"pwResetIP", h.pwResetIP, 10},
		{"pwResetEmail", h.pwResetEmail, 3},
		{"stepUpUser", h.stepUpUser, 5},
		{"stepUpPasswordUser", h.stepUpPasswordUser, 5},
		{"availabilityUser", h.availabilityUser, 60},
		{"oauthIP", h.oauthIP, 30},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for i := 1; i < c.cap; i++ {
				fails, until := c.l.Fail("k")
				require.Equal(t, i, fails, "the scalar bucket must count ATTEMPTS")
				require.True(t, until.IsZero(), "%s locked after %d of %d", c.name, i, c.cap)
			}
			_, until := c.l.Fail("k")
			assert.False(t, until.IsZero(), "%s must lock on failure %d", c.name, c.cap)
		})
	}
}
