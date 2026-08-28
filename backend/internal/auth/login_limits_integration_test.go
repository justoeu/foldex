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
