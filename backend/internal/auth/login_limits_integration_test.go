//go:build integration

package auth_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/abusepolicy"
	"foldex/internal/testdb"
)

// The NAT case, end to end (docs/SDD-ABUSE-DEFENSE.md §4.2).
//
// Thirty-two failed attempts from ONE address, and the per-origin bucket must
// not react: eight colleagues mistyping their own passwords four times each is
// not a spray, it is a Monday. Under the previous per-IP cap — twenty
// CONSECUTIVE failures, whoever they belonged to — the twenty-first request
// would have been refused and the whole office locked out of its own instance.
func TestLogin_ManyPeopleBehindOneAddressDoNotLockEachOtherOut(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)

	const colleagues = 8 // under the ceiling of 10 distinct accounts
	const mistypes = 4   // under the per-account cap of 5
	for i := range colleagues {
		email := fmt.Sprintf("colleague-%d@example.com", i)
		testdb.SeedUserWithPassword(t, h.pool, email, "a good password", "editor")
		for attempt := range mistypes {
			rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
				"email": email, "password": "wrong",
			})
			require.Equal(t, http.StatusUnauthorized, rec.Code,
				"colleague %d attempt %d must be judged on its own account, not on the address it shares",
				i, attempt+1)
		}
	}

	testdb.SeedUserWithPassword(t, h.pool, "ninth@example.com", "a good password", "editor")
	rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "ninth@example.com", "password": "a good password",
	})
	assert.Equal(t, http.StatusOK, rec.Code,
		"the next person through the same door must still be able to sign in: %s", rec.Body.String())
}

// The other half of the same change: breadth is what the origin is judged on,
// and an address that was never registered counts exactly like one that was.
// A spray walking a leaked credential list is the adversary this bucket exists
// for, and it burns the budget in ten requests instead of twenty.
func TestLogin_ASprayAcrossManyAccountsLocksTheOrigin(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	n := abusepolicy.Default().LoginDistinctAccountsPerIP

	for i := range n {
		rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
			"email": fmt.Sprintf("victim-%d@example.com", i), "password": "wrong",
		})
		require.Equal(t, http.StatusUnauthorized, rec.Code, "account %d of the sweep", i+1)
	}

	rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "victim-fresh@example.com", "password": "wrong",
	})
	assert.Equal(t, http.StatusTooManyRequests, rec.Code,
		"the %dth distinct account probed from one origin must be refused", n+1)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"), "a 429 must tell the client when to retry")
}

// The lockout has to leave a ROW, and this is the test that says so.
//
// `auth.rate_limited` shipped declared, classified, ordered and READ — the
// anomaly panel's third rule (internal/auth/anomaly.go) queries for it — with
// nothing in production writing it. Every unit test passed, the vocabulary
// guard passed, and the "already throttled" signal could never fire on a real
// instance. Two parallel workstreams each owned one half and neither owned the
// meeting point.
//
// It is written ONCE, at the transition into lockout, and not per refused
// request: Begin refuses a locked key before the handler reaches the failure
// branch, so a non-zero expiry here IS the edge. Writing per refusal would make
// the trail the amplifier the limiter exists to remove — the attacker would
// choose how many rows to insert.
func TestLogin_EnteringLockoutLeavesAnAuditRow(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	n := abusepolicy.Default().LoginDistinctAccountsPerIP

	for i := range n {
		rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
			"email": fmt.Sprintf("swept-%d@example.com", i), "password": "wrong",
		})
		require.Equal(t, http.StatusUnauthorized, rec.Code, "account %d of the sweep", i+1)
	}

	var rows int
	require.NoError(t, h.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM audit_log WHERE action = 'auth.rate_limited'`).Scan(&rows))
	assert.Equal(t, 1, rows,
		"crossing the origin's ceiling must record exactly one auth.rate_limited row — "+
			"the anomaly panel's throttle rule reads this action and nothing else writes it")

	// And the row has to carry the address, because the panel groups by it.
	var ip *string
	require.NoError(t, h.pool.QueryRow(t.Context(),
		`SELECT host(ip) FROM audit_log WHERE action = 'auth.rate_limited'`).Scan(&ip))
	require.NotNil(t, ip, "a throttle row with no origin is invisible to the panel that groups by origin")
	assert.NotEmpty(t, *ip)

	// Further refused requests must NOT add rows. The attacker controls how
	// many of those there are.
	for range 5 {
		rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
			"email": "swept-again@example.com", "password": "wrong",
		})
		require.Equal(t, http.StatusTooManyRequests, rec.Code)
	}
	require.NoError(t, h.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM audit_log WHERE action = 'auth.rate_limited'`).Scan(&rows))
	assert.Equal(t, 1, rows,
		"refused requests must not each add a row — the trail would become the amplifier")
}

// The bypass, end to end: an origin must not buy its breadth budget back with
// one legitimate sign-in.
//
// This is adversary A3 — an account that legitimately exists on a multi-user
// instance, sweeping its neighbours. Under the old CommitSuccess, nine probes
// plus one own-account login was a loop that never tripped the origin bucket.
func TestLogin_ASuccessDoesNotBuyBackTheOriginsBudget(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	n := abusepolicy.Default().LoginDistinctAccountsPerIP

	testdb.SeedUserWithPassword(t, h.pool, "insider@example.com", "a good password", "editor")

	// One short of the ceiling.
	for i := range n - 1 {
		rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
			"email": fmt.Sprintf("neighbour-%d@example.com", i), "password": "wrong",
		})
		require.Equal(t, http.StatusUnauthorized, rec.Code, "probe %d", i+1)
	}

	// The insider signs in to their OWN account, successfully.
	rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "insider@example.com", "password": "a good password",
	})
	require.Equal(t, http.StatusOK, rec.Code, "the insider's own login must still work: %s", rec.Body.String())

	// The nth distinct account crosses the ceiling. That request itself is
	// still judged on its credential (401); the refusal lands on the next one,
	// which is what Begin checks.
	rec = c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "neighbour-last@example.com", "password": "wrong",
	})
	require.Equal(t, http.StatusUnauthorized, rec.Code, "the %dth probe is still answered on its credential", n)

	rec = c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "neighbour-fresh@example.com", "password": "wrong",
	})
	assert.Equal(t, http.StatusTooManyRequests, rec.Code,
		"a successful sign-in must not forgive the %d accounts this origin already swept — "+
			"with the old CommitSuccess this loop never tripped the origin bucket at all", n-1)
}

// Two identifiers that name the same account must cost the same as two that
// name nothing.
//
// The breadth bucket keys on the identifier the caller SUBMITTED. Keying on the
// resolved account instead made the set size depend on whether two identifiers
// collapse — and that difference is readable from outside: with ten probes
// budgeted, a pair that collapsed leaves one probe of headroom and a pair that
// did not does not, so 429-versus-401 answers "does this username belong to
// this mailbox?" for an anonymous caller. The only username probe this
// instance offers is authenticated by design (INV-013).
func TestLogin_AliasesOfOneAccountCostTheSameAsStrangers(t *testing.T) {
	n := abusepolicy.Default().LoginDistinctAccountsPerIP

	// A real account with BOTH a username and an e-mail, probed by each.
	withAccount := func(t *testing.T) int {
		h := newHarness(t)
		c := h.client(t)
		testdb.SeedUserWithPassword(t, h.pool, "alias@example.com", "a good password", "editor")
		tag, err := h.pool.Exec(t.Context(),
			`UPDATE app_user SET username = 'aliasuser', username_normalized = 'aliasuser' WHERE email = 'alias@example.com'`)
		require.NoError(t, err)
		// A zero-row UPDATE would make BOTH arms measure a pair that names
		// nothing, and the test would agree with itself while the oracle was
		// live. The fixture has to prove it built the case.
		require.EqualValues(t, 1, tag.RowsAffected(), "the alias fixture did not take")
		return probesUntilRefused(t, c, n+2, []string{"aliasuser", "alias@example.com"})
	}
	// The same two shapes, naming nothing at all.
	withoutAccount := func(t *testing.T) int {
		h := newHarness(t)
		c := h.client(t)
		return probesUntilRefused(t, c, n+2, []string{"ghostuser", "ghost@example.com"})
	}

	// Anchored absolutely as well as against each other: two arms that agree on
	// the WRONG number would pass a comparison-only assertion.
	strangers := withoutAccount(t)
	require.Equal(t, n+1, strangers,
		"two identifiers that name nothing must each cost one member, so the ceiling falls on probe %d", n+1)
	assert.Equal(t, strangers, withAccount(t),
		"the origin must reach its ceiling after the same number of probes whether or not "+
			"two submitted identifiers happen to name one account — otherwise 429-vs-401 is a "+
			"username-to-mailbox oracle for an anonymous caller")
}

// probesUntilRefused submits the given identifiers first, then filler, and
// returns how many requests it took to be answered 429.
func probesUntilRefused(t *testing.T, c *client, max int, first []string) int {
	t.Helper()
	for i := range max {
		who := fmt.Sprintf("filler-%d@example.com", i)
		if i < len(first) {
			who = first[i]
		}
		rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
			"email": who, "password": "wrong",
		})
		if rec.Code == http.StatusTooManyRequests {
			return i + 1
		}
		require.Equal(t, http.StatusUnauthorized, rec.Code, "probe %d (%s)", i+1, who)
	}
	t.Fatalf("never refused after %d probes", max)
	return 0
}

// The per-account lockout writes its own row, labelled `account`.
//
// The two constants exist "so a typo would be a category nobody can search
// for", and until this test nothing read either of them back: the sweep test
// used a distinct address per probe, so only the origin bucket ever tripped and
// swapping the two labels was green.
func TestLogin_AnAccountLockoutIsLabelledAsSuch(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	testdb.SeedUserWithPassword(t, h.pool, "hammered@example.com", "a good password", "editor")

	// The SAME account every time, so the account bucket is what trips and the
	// origin's breadth stays at one.
	for i := range abusepolicy.Default().LoginFailuresPerAccount {
		rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
			"email": "hammered@example.com", "password": "wrong",
		})
		require.Equal(t, http.StatusUnauthorized, rec.Code, "attempt %d", i+1)
	}

	var detail *string
	require.NoError(t, h.pool.QueryRow(t.Context(),
		`SELECT detail FROM audit_log WHERE action = 'auth.rate_limited'`).Scan(&detail))
	require.NotNil(t, detail)
	assert.Equal(t, "account", *detail,
		"a lockout earned by hammering ONE account must not be filed as a sweep of many")
}
