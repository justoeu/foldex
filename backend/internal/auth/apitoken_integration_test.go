//go:build integration

package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/auth"
)

// mint creates a token and returns its plaintext.
func (h *harness) mintToken(t *testing.T, c *client, name string) string {
	t.Helper()
	rec := c.do(http.MethodPost, "/api/auth/tokens", map[string]any{"name": name})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	tok, _ := decode(t, rec)["token"].(string)
	require.NotEmpty(t, tok)
	return tok
}

// bearerHeader is the whole credential an extension or a script presents: no
// cookie jar, no CSRF header, one Authorization line.
func bearerHeader(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func TestAPIToken_AuthenticatesTheContentSurfaceWithNoCookies(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	token := h.mintToken(t, admin, "extension")

	rec := h.client(t).doRaw(http.MethodGet, "/api/links", nil, bearerHeader(token))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"uid":`)
}

// The plaintext exists in exactly one response. The database keeps sha256, so
// this is not a convenience that was skipped — it cannot be shown again.
func TestAPIToken_PlaintextIsNeverStoredNorListed(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	token := h.mintToken(t, admin, "extension")

	rec := admin.do(http.MethodGet, "/api/auth/tokens", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), token)

	var stored []byte
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT token_hash FROM api_token`).Scan(&stored))
	assert.NotContains(t, string(stored), token)
}

func TestAPIToken_CarriesTheGreppablePrefix(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	assert.True(t, strings.HasPrefix(h.mintToken(t, admin, "extension"), auth.APITokenPrefix),
		"the prefix is what makes a leaked token findable by a secret scanner")
}

// A CSRF header is meaningless for a bearer credential — there is no ambient
// cookie for a cross-site request to ride on — so an unsafe verb must work
// without one. Requiring it would break every script.
func TestAPIToken_UnsafeVerbNeedsNoCSRFHeader(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	token := h.mintToken(t, admin, "extension")

	rec := h.client(t).doRaw(http.MethodPost, "/api/links", map[string]string{}, bearerHeader(token))
	assert.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
}

// THE scope test. A token pasted into an extension's configuration must not be
// able to change the password, mint an invite or download a backup — otherwise
// it is not a content credential, it is the account.
func TestAPIToken_IsRefusedOnTheIdentityAndAdminSurfaces(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	token := h.mintToken(t, admin, "extension")
	hdr := bearerHeader(token)

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/auth/password/change",
			map[string]string{"current_password": "correct horse battery", "new_password": "a new one"}},
		{http.MethodPost, "/api/auth/tokens", map[string]string{"name": "another"}},
		{http.MethodGet, "/api/auth/sessions", nil},
		{http.MethodPost, "/api/auth/logout-all", nil},
		{http.MethodPost, "/api/auth/2fa/totp/disable", map[string]string{}},
	} {
		rec := h.client(t).doRaw(tc.method, tc.path, tc.body, hdr)
		assert.Equal(t, http.StatusForbidden, rec.Code, "%s %s", tc.method, tc.path)
		assert.Equal(t, "token_scope", errCode(t, rec), "%s %s", tc.method, tc.path)
	}

	// The admin surface is gated too. It answers 403 HERE because the token
	// belongs to an admin; a non-admin's token gets the 404 RequireAdmin gives
	// everyone else, which is what stops a token holder from learning the
	// surface exists at all.
	rec := h.client(t).doRaw(http.MethodGet, "/api/admin/users", nil, hdr)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "token_scope", errCode(t, rec))
}

func TestAPIToken_RevokedTokenStopsWorkingImmediately(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	token := h.mintToken(t, admin, "extension")
	hdr := bearerHeader(token)

	rec := admin.do(http.MethodGet, "/api/auth/tokens", nil)
	list := decode(t, rec)["tokens"].([]any)
	require.Len(t, list, 1)
	id := int64(list[0].(map[string]any)["id"].(float64))

	require.Equal(t, http.StatusNoContent,
		admin.do(http.MethodDelete, "/api/auth/tokens/"+itoa(id), nil).Code)

	rec = h.client(t).doRaw(http.MethodGet, "/api/links", nil, hdr)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// Disabling an account must stop its tokens at the same instant it stops its
// sessions — otherwise a ban only takes effect when a cookie expires.
func TestAPIToken_DiesWithADisabledAccount(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	uid := h.inviteAndAccept(t, admin, "user@example.com", "another good password")

	user := h.login(t, "user@example.com", "another good password")
	token := h.mintToken(t, user, "extension")
	hdr := bearerHeader(token)
	require.Equal(t, http.StatusOK, h.client(t).doRaw(http.MethodGet, "/api/links", nil, hdr).Code)

	require.Equal(t, http.StatusOK, admin.do(http.MethodPatch,
		"/api/admin/users/"+itoa(int64(uid)), map[string]any{"status": "disabled"}).Code)

	assert.Equal(t, http.StatusUnauthorized,
		h.client(t).doRaw(http.MethodGet, "/api/links", nil, hdr).Code)
}

// Every malformed shape gets the same 401. Distinguishing "no such id" from
// "wrong secret" would let a caller enumerate which token ids exist.
func TestAPIToken_EveryBadShapeIsTheSame401(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	real := h.mintToken(t, admin, "extension")

	for _, bad := range []string{
		"", "garbage", "fx_", "fx_notanumber_secret", "fx_999999_secret",
		"fx_0_secret", "fx_-1_secret",
		// The right id with the wrong secret.
		real[:strings.LastIndex(real, "_")+1] + "wrong",
	} {
		rec := h.client(t).doRaw(http.MethodGet, "/api/links", nil,
			bearerHeader(bad))
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "token %q", bad)
	}
}

// A session cookie and a bearer header on the same request: the cookie wins.
// Silently preferring the token would hand a developer testing with
// curl-copied headers a principal with no admin routes and no CSRF, and the
// failure would surface far from its cause.
func TestAPIToken_SessionCookieTakesPrecedence(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	token := h.mintToken(t, admin, "extension")

	// The cookie jar is the admin's; the header is a token. /api/auth/sessions
	// is refused for tokens and allowed for sessions, so the answer says which
	// credential resolved.
	rec := admin.doRaw(http.MethodGet, "/api/auth/sessions", nil, bearerHeader(token))
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// A non-admin's token gets 404, not 403 — the role gate runs first, so the
// answer is identical to a route that does not exist.
func TestAPIToken_NonAdminTokenGets404FromTheAdminSurface(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	h.inviteAndAccept(t, admin, "user@example.com", "another good password")

	user := h.login(t, "user@example.com", "another good password")
	token := h.mintToken(t, user, "extension")

	rec := h.client(t).doRaw(http.MethodGet, "/api/admin/users", nil, bearerHeader(token))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAPIToken_CapsHowManyOneAccountMayHold(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	for i := range 20 {
		rec := admin.do(http.MethodPost, "/api/auth/tokens",
			map[string]any{"name": "token " + itoa(int64(i))})
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	}
	rec := admin.do(http.MethodPost, "/api/auth/tokens", map[string]any{"name": "one too many"})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "too_many_tokens", errCode(t, rec))
}

func TestConcurrentAPITokenCreationNeverExceedsCap(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	for i := range 19 {
		h.mintToken(t, admin, "existing "+itoa(int64(i)))
	}

	ctx := context.Background()
	blocker, err := h.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(ctx) }()
	var uid int64
	require.NoError(t, blocker.QueryRow(ctx,
		`SELECT id FROM app_user WHERE email = 'admin@example.com' FOR UPDATE`).Scan(&uid))

	clients := []*client{clientOnHarness(t, h, admin), clientOnHarness(t, h, admin)}
	results := make(chan *httptest.ResponseRecorder, len(clients))
	for i, c := range clients {
		go func() {
			results <- c.do(http.MethodPost, "/api/auth/tokens",
				map[string]any{"name": "concurrent " + itoa(int64(i))})
		}()
	}
	waitForBlockedSQLCount(t, h.pool, "SELECT id FROM app_user WHERE id = $1 FOR NO KEY UPDATE", len(clients))
	require.NoError(t, blocker.Commit(ctx))

	var created, refused int
	for range clients {
		rec := <-results
		switch rec.Code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			refused++
			assert.Equal(t, "too_many_tokens", errCode(t, rec))
		default:
			require.Failf(t, "unexpected create response", "status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	assert.Equal(t, 1, created)
	assert.Equal(t, 1, refused)
	var live int
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT count(*) FROM api_token WHERE user_id = $1 AND revoked_at IS NULL`, uid).Scan(&live))
	assert.Equal(t, 20, live)
}

// The sweeper is what bounds api_token and oauth_state over time. Neither is a
// detector — unlike session_used_token, whose rows ARE the reuse memory — so
// dead rows there have no forensic value and are deleted rather than retained.
func TestSweep_DropsDeadTokensAndSpentOAuthStates(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	live := h.mintToken(t, admin, "still in use")

	ctx := context.Background()
	// One revoked long ago, one expired long ago. Both are past any window in
	// which knowing they existed helps anybody.
	_, err := h.pool.Exec(ctx, `
		INSERT INTO api_token (user_id, name, token_hash, revoked_at)
		VALUES (1, 'revoked', '\x01', now() - interval '90 days')`)
	require.NoError(t, err)
	_, err = h.pool.Exec(ctx, `
		INSERT INTO api_token (user_id, name, token_hash, expires_at)
		VALUES (1, 'expired', '\x02', now() - interval '90 days')`)
	require.NoError(t, err)
	_, err = h.pool.Exec(ctx, `
		INSERT INTO oauth_state (state_hash, code_verifier, provider, purpose, expires_at)
		VALUES ('\x03', 'v', 'google', 'login', now() - interval '90 days')`)
	require.NoError(t, err)

	tokens, err := h.repo.SweepAPITokens(ctx, 7*24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(2), tokens)

	states, err := h.repo.SweepOAuthState(ctx, 7*24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(1), states)

	// The live token is untouched and still authenticates.
	assert.Equal(t, http.StatusOK,
		h.client(t).doRaw(http.MethodGet, "/api/links", nil, bearerHeader(live)).Code)
}

// A token revoked a minute ago must NOT be swept: the row is what makes the
// owner's list show what they revoked, and deleting it immediately would make
// "I revoked that yesterday" unverifiable.
func TestSweep_KeepsRecentlyRevokedTokens(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	h.mintToken(t, admin, "just revoked")

	ctx := context.Background()
	_, err := h.pool.Exec(ctx, `UPDATE api_token SET revoked_at = now()`)
	require.NoError(t, err)

	n, err := h.repo.SweepAPITokens(ctx, 7*24*time.Hour)
	require.NoError(t, err)
	assert.Zero(t, n)
}

// An expiring token stops working the moment it expires — the resolution query
// filters on expires_at, so this does not wait for the sweeper.
func TestAPIToken_ExpiredTokenStopsWorkingBeforeItIsSwept(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	token := h.mintToken(t, admin, "short lived")

	_, err := h.pool.Exec(context.Background(),
		`UPDATE api_token SET expires_at = now() - interval '1 minute'`)
	require.NoError(t, err)

	assert.Equal(t, http.StatusUnauthorized,
		h.client(t).doRaw(http.MethodGet, "/api/links", nil, bearerHeader(token)).Code)
}

// An expiry is optional, and asking for one has to actually produce one —
// otherwise "expires in 30 days" in the UI would be decoration.
func TestAPIToken_HonoursTheRequestedExpiry(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	rec := admin.do(http.MethodPost, "/api/auth/tokens",
		map[string]any{"name": "short lived", "expires_in_days": 30})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.NotEmpty(t, decode(t, rec)["expires_at"])

	rec = admin.do(http.MethodPost, "/api/auth/tokens", map[string]any{"name": "forever"})
	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Nil(t, decode(t, rec)["expires_at"])
}

func TestAPIToken_RejectsAnEmptyOrOversizedName(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	for _, name := range []string{"", "   ", strings.Repeat("x", 101)} {
		rec := admin.do(http.MethodPost, "/api/auth/tokens", map[string]any{"name": name})
		assert.Equal(t, http.StatusBadRequest, rec.Code, "name %q", name)
		assert.Equal(t, "invalid_name", errCode(t, rec))
	}
}

func TestAPIToken_RejectsANegativeExpiry(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	rec := admin.do(http.MethodPost, "/api/auth/tokens",
		map[string]any{"name": "backwards", "expires_in_days": -1})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_expiry", errCode(t, rec))
}

// Someone else's token is 404, never 403 — the same rule content rows follow.
// A 403 would confirm the id exists and turn a dense BIGSERIAL into an
// enumeration oracle across accounts.
func TestAPIToken_RevokingAnotherUsersTokenIsNotFound(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	h.inviteAndAccept(t, admin, "user@example.com", "another good password")

	user := h.login(t, "user@example.com", "another good password")
	h.mintToken(t, user, "theirs")

	rec := admin.do(http.MethodGet, "/api/auth/tokens", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, decode(t, rec)["tokens"], "another account's token must not be listed")

	// id 1 is the other user's, and the admin must not be able to kill it.
	assert.Equal(t, http.StatusNotFound,
		admin.do(http.MethodDelete, "/api/auth/tokens/1", nil).Code)

	var alive int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM api_token WHERE revoked_at IS NULL`).Scan(&alive))
	assert.Equal(t, 1, alive)
}

// The list is the owner's audit surface, so the order it comes back in is part
// of what makes it readable: newest first, so a token minted a minute ago by
// someone who should not have it is at the top rather than buried.
func TestAPIToken_ListsNewestFirst(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	for _, name := range []string{"oldest", "middle", "newest"} {
		h.mintToken(t, admin, name)
		// created_at has sub-second resolution, but two inserts inside the same
		// tick would make the order arbitrary and the test flaky.
		_, err := h.pool.Exec(context.Background(),
			`UPDATE api_token SET created_at = now() WHERE name = $1`, name)
		require.NoError(t, err)
		time.Sleep(2 * time.Millisecond)
	}

	rec := admin.do(http.MethodGet, "/api/auth/tokens", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	list := decode(t, rec)["tokens"].([]any)
	require.Len(t, list, 3)
	assert.Equal(t, "newest", list[0].(map[string]any)["name"])
	assert.Equal(t, "oldest", list[2].(map[string]any)["name"])
}

// A revoked token disappears from the list. Leaving it visible would make the
// count meaningless and the audit surface noisy with things that no longer work.
func TestAPIToken_RevokedTokensLeaveTheList(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	h.mintToken(t, admin, "doomed")
	h.mintToken(t, admin, "survivor")

	rec := admin.do(http.MethodGet, "/api/auth/tokens", nil)
	list := decode(t, rec)["tokens"].([]any)
	require.Len(t, list, 2)

	var doomed int64
	for _, row := range list {
		m := row.(map[string]any)
		if m["name"] == "doomed" {
			doomed = int64(m["id"].(float64))
		}
	}
	require.NotZero(t, doomed)
	require.Equal(t, http.StatusNoContent,
		admin.do(http.MethodDelete, "/api/auth/tokens/"+itoa(doomed), nil).Code)

	rec = admin.do(http.MethodGet, "/api/auth/tokens", nil)
	list = decode(t, rec)["tokens"].([]any)
	require.Len(t, list, 1)
	assert.Equal(t, "survivor", list[0].(map[string]any)["name"])

	// Revoking twice is a 404, not a silent success: the second call did not do
	// what the caller asked, and saying otherwise would hide a stale UI.
	assert.Equal(t, http.StatusNotFound,
		admin.do(http.MethodDelete, "/api/auth/tokens/"+itoa(doomed), nil).Code)
}
