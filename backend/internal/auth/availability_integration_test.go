//go:build integration

package auth_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"foldex/internal/testdb"

	"github.com/go-chi/chi/v5"

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

// The placement of the e-mail probe is the whole safety argument, and until
// this test it existed only in comments.
//
// The two probes answer the same shape of question and their arguments are NOT
// the same. A username exists only on this instance, so confirming one is taken
// says "somebody here uses that handle". An address is also a mailbox and
// exists outside foldex, so a free "does this have an account here?" is exactly
// the oracle login spends an always-run bcrypt, one 401 body and a 250 ms floor
// to deny — which is why the e-mail counterpart is allowed only past
// RequireAdmin, where the caller can already list every account with its
// address and therefore learns nothing new.
//
// Mirroring it onto /api/auth "for symmetry with the username row" is the most
// likely regression this feature has. Before this walked the tree, nothing
// would have failed.
func TestAvailability_EmailProbeIsMountedOnlyUnderAdmin(t *testing.T) {
	h := newHarness(t)

	routes, ok := h.router.(chi.Routes)
	require.True(t, ok, "the harness router must be walkable, or this guard sees nothing")

	var email, username []string
	require.NoError(t, chi.Walk(routes,
		func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			switch {
			case strings.Contains(route, "email-available"):
				email = append(email, method+" "+route)
			case strings.Contains(route, "username-available"):
				username = append(username, method+" "+route)
			}
			return nil
		}))

	// Without these the guard passes on a tree where the routes were renamed
	// and no longer match — green, and measuring nothing.
	require.NotEmpty(t, email, "the e-mail probe is gone or renamed; re-scope this guard")
	require.NotEmpty(t, username, "the username probe is gone or renamed; re-scope this guard")

	for _, r := range email {
		_, path, _ := strings.Cut(r, " ")
		assert.True(t, strings.HasPrefix(path, "/api/admin/"),
			"%s puts an e-mail existence oracle outside the administration surface", r)
	}
	for _, r := range username {
		_, path, _ := strings.Cut(r, " ")
		assert.True(t, strings.HasPrefix(path, "/api/auth/"),
			"%s is not where the session-authenticated probe belongs", r)
	}
}

// Both probes must answer 500 — never a cheerful `available: true` — when the
// lookup itself fails.
//
// This is the branch a form is most likely to misread: the client treats an
// error as "could not check, you may still save", which is right, while an
// `available: true` produced by a broken query would be a green check the
// database never agreed to. The failure is forced by renaming the column each
// query reads, which is the only honest way to make pgx fail here.
func TestAvailability_ReportsAServerErrorRatherThanAnAnswer(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	ctx := context.Background()

	for _, tc := range []struct {
		name, breaks, restores, path string
	}{
		{
			"username",
			`ALTER TABLE app_user RENAME COLUMN username_normalized TO username_normalized_x`,
			`ALTER TABLE app_user RENAME COLUMN username_normalized_x TO username_normalized`,
			"/api/auth/username-available?u=valmir",
		},
		{
			"e-mail",
			`ALTER TABLE email_change RENAME COLUMN new_email_normalized TO new_email_normalized_x`,
			`ALTER TABLE email_change RENAME COLUMN new_email_normalized_x TO new_email_normalized`,
			"/api/admin/users/email-available?email=new@example.com",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.pool.Exec(ctx, tc.breaks)
			require.NoError(t, err)
			t.Cleanup(func() {
				_, err := h.pool.Exec(ctx, tc.restores)
				require.NoError(t, err, "the shared container must be left usable")
			})

			rec := c.do(http.MethodGet, tc.path, nil)
			assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
			// The raw pgx text must not reach the caller.
			assert.NotContains(t, rec.Body.String(), "_x")
		})
	}
}
