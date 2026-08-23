//go:build integration

package auth_test

import (
	"context"
	"net/http"
	"testing"

	"foldex/internal/testdb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// probe reads the shared availability answer.
func probe(t *testing.T, c *client, path string) (bool, string) {
	t.Helper()
	rec := c.do(http.MethodGet, path, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decode(t, rec)
	available, _ := body["available"].(bool)
	reason, _ := body["reason"].(string)
	return available, reason
}

// ─────────────────────────────────────────────────────────────────────
// Username availability
// ─────────────────────────────────────────────────────────────────────

func TestUsernameAvailable_AnswersTheThreeOutcomes(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	other := testdb.SeedUserWithPassword(t, h.pool, "other@example.com", "a good password", "editor")
	_, err := h.pool.Exec(context.Background(),
		`UPDATE app_user SET username = 'taken', username_normalized = 'taken' WHERE id = $1`,
		int64(other))
	require.NoError(t, err)

	for _, tc := range []struct {
		name, query string
		available   bool
		reason      string
	}{
		{"free", "valmir", true, ""},
		{"already claimed", "taken", false, "taken"},
		{"claimed in another casing", "TAKEN", false, "taken"},
		// The reserved list and the shape rule produce the SAME error from
		// NormalizeUsername, deliberately — but the form can tell them apart,
		// and someone who typed `admin` deserves better than "wrong characters".
		{"reserved", "admin", false, "reserved"},
		{"too short", "ab", false, "shape"},
		// The `@` refusal is the one that keeps usernames out of everyone's
		// mailbox namespace; a probe must report it as a shape problem rather
		// than looking it up.
		{"address-shaped", "someone@example.com", false, "shape"},
		{"empty", "", false, "empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			available, reason := probe(t, c, "/api/auth/username-available?u="+tc.query)
			assert.Equal(t, tc.available, available)
			assert.Equal(t, tc.reason, reason)
		})
	}
}

// A form that reported "taken" about the name its owner is looking at would be
// answering a question nobody asked — and would block a save that only changed
// the casing.
func TestUsernameAvailable_YourOwnNameIsAvailableToYou(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	require.Equal(t, http.StatusOK, c.do(http.MethodPatch, "/api/auth/profile",
		map[string]string{"username": "valmir"}).Code)

	available, reason := probe(t, c, "/api/auth/username-available?u=valmir")
	assert.True(t, available, "reason was %q", reason)
}

// The whole safety argument for this endpoint is that it is session-only: an
// anonymous caller must not get a cheap way to ask who exists here.
func TestUsernameAvailable_IsClosedToAnonymousCallers(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	rec := h.client(t).do(http.MethodGet, "/api/auth/username-available?u=valmir", nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// An EMPTY probe is free: it looks nothing up and reveals nothing, so charging
// for it would only punish someone clearing the field. Everything else is
// charged, refused shapes included. Skipping them would let a
// script probe for free by appending a character the validator rejects — the
// same reasoning that makes the login bucket increment for addresses that do
// not exist.
func TestUsernameAvailable_MalformedProbesStillCostBudget(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	// The SAME two-character value every time. An earlier version sent `a0`…`a59`
	// believing all sixty were malformed; `usernameShape` requires three
	// characters, so `a10` onward are valid and hit the database — fifty of the
	// sixty probes were exercising the ordinary path, and the mutation was
	// caught only by arithmetic. The limiter does not care that the value
	// repeats; what is under test is that a REFUSED shape still costs budget.
	for i := 0; i < 60; i++ {
		rec := c.do(http.MethodGet, "/api/auth/username-available?u=ab", nil)
		require.Equal(t, http.StatusOK, rec.Code, "probe %d", i)
	}
	rec := c.do(http.MethodGet, "/api/auth/username-available?u=valmir", nil)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code,
		"a malformed probe must consume budget, or the cap is decorative")
}

// ─────────────────────────────────────────────────────────────────────
// E-mail availability (administration only)
// ─────────────────────────────────────────────────────────────────────

func TestEmailAvailable_AnswersForAnAdministrator(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "taken@example.com", "a good password", "editor")

	for _, tc := range []struct {
		name, query string
		available   bool
		reason      string
	}{
		{"free", "new@example.com", true, ""},
		{"registered", "taken@example.com", false, "taken"},
		{"registered in another casing", "TAKEN@Example.COM", false, "taken"},
		{"not an address", "not-an-address", false, "shape"},
		{"empty", "", false, "empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			available, reason := probe(t, c, "/api/admin/users/email-available?email="+tc.query)
			assert.Equal(t, tc.available, available)
			assert.Equal(t, tc.reason, reason)
		})
	}
}

// The unique index guards only the LIVE column, so an address someone is
// already moving to would pass a naive check and then lose the race at their
// own confirmation — after the administrator had been told it was free.
func TestEmailAvailable_APendingChangeCountsAsTaken(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/email/change",
		map[string]string{"new_email": "moving-to@example.com", "password": "a good password"}).Code)

	// Available AND flagged. AdminCreateUser conflicts only on app_user, so
	// reporting "taken" would grey out a Create the server would accept — and
	// the administrator would find no such account in the user list to explain
	// it. The pending row may also expire or be killed by a credential change,
	// freeing the address again.
	available, reason := probe(t, c, "/api/admin/users/email-available?email=moving-to@example.com")
	assert.True(t, available, "a pending change must not refuse a create the server allows")
	assert.Equal(t, "pending", reason)
}

// Same rule the rest of /api/admin follows: a non-admin is told the surface
// does not exist, rather than that they merely lack the role.
func TestEmailAvailable_IsNotFoundForANonAdministrator(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "editor@example.com", "a good password", "editor")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "editor@example.com", "password": "a good password",
	}).Code)

	rec := c.do(http.MethodGet, "/api/admin/users/email-available?email=x@example.com", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// The budget must be keyed per USER. Dropping the id from availabilityKey puts
// every account in one bucket, so one person typing a username 429s the probe
// for the whole instance — a cross-tenant denial of service out of ordinary
// use, from a change no other test would notice.
func TestUsernameAvailable_BudgetIsPerUser(t *testing.T) {
	h := newHarness(t)
	a := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "other@example.com", "a good password", "editor")

	for i := 0; i < 60; i++ {
		require.Equal(t, http.StatusOK,
			a.do(http.MethodGet, "/api/auth/username-available?u=ab", nil).Code, "probe %d", i)
	}
	require.Equal(t, http.StatusTooManyRequests,
		a.do(http.MethodGet, "/api/auth/username-available?u=valmir", nil).Code)

	b := h.client(t)
	require.Equal(t, http.StatusOK, b.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "other@example.com", "password": "a good password",
	}).Code)
	assert.Equal(t, http.StatusOK,
		b.do(http.MethodGet, "/api/auth/username-available?u=valmir", nil).Code,
		"one account exhausting its budget must not spend anyone else's")
}

// The free-empty-probe rule is a real branch, not a comment: moving the empty
// check below the charge would make clearing the field cost budget.
func TestUsernameAvailable_EmptyProbesAreFree(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	for i := 0; i < 60; i++ {
		require.Equal(t, http.StatusOK,
			c.do(http.MethodGet, "/api/auth/username-available?u=", nil).Code, "probe %d", i)
	}
	assert.Equal(t, http.StatusOK,
		c.do(http.MethodGet, "/api/auth/username-available?u=valmir", nil).Code,
		"an empty probe looks nothing up and must not spend budget")
}

// A pending change that can never be confirmed must stop holding the address.
// Both predicates were unlocked: dropping either one leaves an administrator
// told "in use" about an address belonging to no account, forever, with nothing
// in the user list to explain it.
func TestEmailAvailable_DeadPendingChangesDoNotHoldAnAddress(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	ctx := context.Background()

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/email/change",
		map[string]string{"new_email": "moving-to@example.com", "password": "a good password"}).Code)

	for _, tc := range []struct{ name, sql string }{
		{"expired", `UPDATE email_change SET expires_at = now() - interval '1 day'`},
		{"already consumed", `UPDATE email_change SET consumed_at = now()`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.pool.Exec(ctx, `UPDATE email_change SET expires_at = now() + interval '1 hour', consumed_at = NULL`)
			require.NoError(t, err)
			_, err = h.pool.Exec(ctx, tc.sql)
			require.NoError(t, err)

			available, reason := probe(t, c, "/api/admin/users/email-available?email=moving-to@example.com")
			assert.True(t, available, "reason was %q", reason)
			assert.Empty(t, reason)
		})
	}
}
