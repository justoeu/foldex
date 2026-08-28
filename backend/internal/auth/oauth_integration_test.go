//go:build integration

package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/auth"
	"foldex/internal/mailer"
	"foldex/internal/oauthgoogle"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/secrets"
	"foldex/internal/testdb"
)

// ─────────────────────────────────────────────────────────────────────
// A Google that does what the test says
// ─────────────────────────────────────────────────────────────────────

// fakeGoogle stands in for the provider.
//
// The double lives at the INTERFACE, not at an HTTP stub, because what these
// tests are about is foldex's policy — which account a subject resolves to,
// what a matching e-mail is allowed to unlock, whether 2FA still applies. The
// wire protocol is oauthgoogle's own concern and is covered by its unit tests
// against a real httptest server.
type fakeGoogle struct {
	mu       sync.Mutex
	enabled  bool
	profile  oauthgoogle.UserInfo
	exchErr  error
	infoErr  error
	lastCode string
	// lastVerifier records what was sent, so a test can prove the PKCE verifier
	// travelled server-side rather than through the browser.
	lastVerifier string
	// authURL overrides what AuthCodeURL returns, so a test can stand up a
	// provider that hands back a target the handler must refuse.
	authURL string
}

func (f *fakeGoogle) Enabled() bool { return f.enabled }

func (f *fakeGoogle) AuthCodeURL(state, challenge string) (string, error) {
	if !f.enabled {
		return "", oauthgoogle.ErrDisabled
	}
	if f.authURL != "" {
		return f.authURL, nil
	}
	return "https://accounts.google.test/o/oauth2/v2/auth?state=" + url.QueryEscape(state) +
		"&code_challenge=" + url.QueryEscape(challenge), nil
}

func (f *fakeGoogle) Exchange(_ context.Context, code, verifier string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastCode, f.lastVerifier = code, verifier
	if f.exchErr != nil {
		return "", f.exchErr
	}
	return "access-token", nil
}

func (f *fakeGoogle) UserInfo(_ context.Context, _ string) (oauthgoogle.UserInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.infoErr != nil {
		return oauthgoogle.UserInfo{}, f.infoErr
	}
	return f.profile, nil
}

func (f *fakeGoogle) as(sub, email string, verified bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.profile = oauthgoogle.UserInfo{
		Subject: sub, Email: email, EmailVerified: verified, Name: "Google User",
	}
}

func (f *fakeGoogle) verifier() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastVerifier
}

// ─────────────────────────────────────────────────────────────────────
// Flow helpers
// ─────────────────────────────────────────────────────────────────────

// startOAuth follows /start and returns the state it minted.
func (c *client) startOAuth(t *testing.T, query string) string {
	t.Helper()
	rec := c.do(http.MethodGet, "/api/auth/oauth/google/start?"+query, nil)
	require.Equal(t, http.StatusFound, rec.Code, "start: %s", rec.Body.String())

	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	state := loc.Query().Get("state")
	require.NotEmpty(t, state, "the redirect must carry a state")
	require.Equal(t, state, c.cookies[auth.CookieOAuth],
		"the cookie and the redirect must carry the SAME state")
	return state
}

func (c *client) startOAuthLink(t *testing.T, password, code string) string {
	t.Helper()
	rec := c.do(http.MethodPost, "/api/auth/oauth/google/start", map[string]string{
		"current_password": password,
		"code":             code,
	})
	require.Equal(t, http.StatusOK, rec.Code, "link start: %s", rec.Body.String())

	target, ok := decode(t, rec)["redirect_url"].(string)
	require.True(t, ok)
	loc, err := url.Parse(target)
	require.NoError(t, err)
	state := loc.Query().Get("state")
	require.NotEmpty(t, state, "the redirect must carry a state")
	require.Equal(t, state, c.cookies[auth.CookieOAuth],
		"the cookie and the redirect must carry the SAME state")
	return state
}

func (c *client) startOAuthInvite(t *testing.T, invite string) string {
	t.Helper()
	rec := c.do(http.MethodPost, "/api/auth/oauth/google/invite/start",
		map[string]string{"invite": invite})
	require.Equal(t, http.StatusOK, rec.Code, "invite start: %s", rec.Body.String())

	target, ok := decode(t, rec)["redirect_url"].(string)
	require.True(t, ok)
	loc, err := url.Parse(target)
	require.NoError(t, err)
	state := loc.Query().Get("state")
	require.NotEmpty(t, state, "the redirect must carry a state")
	require.Equal(t, state, c.cookies[auth.CookieOAuth])
	return state
}

// callback replays Google's redirect back to us and returns the outcome markers.
func (c *client) callback(t *testing.T, state, code string) (outcome, failure string) {
	t.Helper()
	rec := c.do(http.MethodGet,
		"/api/auth/oauth/google/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code), nil)
	require.Equal(t, http.StatusFound, rec.Code, "callback: %s", rec.Body.String())

	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	return loc.Query().Get("oauth"), loc.Query().Get("oauth_error")
}

// googleRoundTrip runs start → callback in one step, the common case.
func (c *client) googleRoundTrip(t *testing.T, purpose string) (outcome, failure string) {
	t.Helper()
	state := c.startOAuth(t, "purpose="+purpose)
	return c.callback(t, state, "auth-code")
}

func (c *client) googleLinkRoundTrip(t *testing.T, password, code string) (outcome, failure string) {
	t.Helper()
	return c.callback(t, c.startOAuthLink(t, password, code), "auth-code")
}

func newGoogleHarness(t *testing.T, opts harnessOpts) (*harness, *fakeGoogle) {
	t.Helper()
	g := &fakeGoogle{enabled: true}
	opts.Google = g
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(context.Background(), pool))
	return newHarnessWith(t, pool, opts), g
}

// ─────────────────────────────────────────────────────────────────────
// Start
// ─────────────────────────────────────────────────────────────────────

func TestOAuthStart_RedirectsWithAStateBoundToACookie(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	c := h.client(t)

	state := c.startOAuth(t, "purpose=login")

	ck := c.cookies[auth.CookieOAuth]
	assert.Equal(t, state, ck)

	var stored int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM oauth_state WHERE consumed_at IS NULL`).Scan(&stored))
	assert.Equal(t, 1, stored, "the verifier must be stored server-side")
}

// The PKCE verifier is the one value that must never travel through the
// browser: an attacker who can read the redirect URL still cannot complete an
// exchange without it.
func TestOAuthStart_NeverPutsTheVerifierInTheRedirect(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	c := h.client(t)

	rec := c.do(http.MethodGet, "/api/auth/oauth/google/start?purpose=login", nil)
	location := rec.Header().Get("Location")

	var verifier string
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT code_verifier FROM oauth_state ORDER BY id DESC LIMIT 1`).Scan(&verifier))
	require.NotEmpty(t, verifier)
	assert.NotContains(t, location, verifier)
	// And it is the value actually used at exchange time.
	state := c.cookies[auth.CookieOAuth]
	c.callback(t, state, "code")
	assert.Equal(t, verifier, g.verifier())
}

func TestOAuthStart_LinkCannotUseTheProoflessGETFlow(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	rec := admin.do(http.MethodGet, "/api/auth/oauth/google/start?purpose=link", nil)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Empty(t, rec.Header().Get("Location"))
	assert.Empty(t, admin.cookies[auth.CookieOAuth])
}

func TestOAuthInviteStart_AcceptsTheTokenOnlyInAPOSTBody(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	token := h.createInvite(t, admin, "invited@example.com")

	rec := h.client(t).do(http.MethodGet,
		"/api/auth/oauth/google/start?purpose=accept_invite&invite="+url.QueryEscape(token), nil)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Empty(t, rec.Header().Get("Location"))

	c := h.client(t)
	assert.NotEmpty(t, c.startOAuthInvite(t, token))
}

func TestOAuthInviteStart_OptionalSessionStillEnforcesCSRF(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	token := h.createInvite(t, admin, "invited@example.com")

	rec := admin.doRaw(http.MethodPost, "/api/auth/oauth/google/invite/start",
		map[string]string{"invite": token}, nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "csrf_failed", errCode(t, rec))
}

func TestOAuthInviteStart_RejectsDisabledProviderAndUnknownInput(t *testing.T) {
	t.Run("provider disabled", func(t *testing.T) {
		h, g := newGoogleHarness(t, harnessOpts{})
		g.enabled = false

		rec := h.client(t).do(http.MethodPost, "/api/auth/oauth/google/invite/start",
			map[string]string{"invite": "not-reached"})
		assert.Equal(t, http.StatusNotImplemented, rec.Code)
		assert.Equal(t, "oauth_disabled", errCode(t, rec))
	})

	t.Run("unknown JSON field", func(t *testing.T) {
		h, _ := newGoogleHarness(t, harnessOpts{})

		rec := h.client(t).do(http.MethodPost, "/api/auth/oauth/google/invite/start",
			map[string]string{"invite": "not-reached", "subject": "must-not-be-accepted"})
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Equal(t, "invalid_json", errCode(t, rec))
	})
}

func TestOAuthInviteStart_RefusesANonHTTPSTarget(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	token := h.createInvite(t, admin, "invited@example.com")
	g.authURL = "http://accounts.google.test/o/oauth2/v2/auth"

	rec := h.client(t).do(http.MethodPost, "/api/auth/oauth/google/invite/start",
		map[string]string{"invite": token})
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.Nil(t, cookieByName(rec, auth.CookieOAuth))
}

func TestOAuthLinkStart_RequiresASessionAndCSRF(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	body := map[string]string{"current_password": "correct horse battery"}

	rec := h.client(t).do(http.MethodPost, "/api/auth/oauth/google/start", body)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = admin.doRaw(http.MethodPost, "/api/auth/oauth/google/start", body, nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "csrf_failed", errCode(t, rec))
}

func TestOAuthLinkStart_RequiresTheCurrentPassword(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	rec := admin.do(http.MethodPost, "/api/auth/oauth/google/start", map[string]string{
		"current_password": "wrong password",
	})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "invalid_credentials", errCode(t, rec))
	assert.Empty(t, admin.cookies[auth.CookieOAuth])

	var states int
	require.NoError(t, h.pool.QueryRow(context.Background(), `SELECT count(*) FROM oauth_state`).Scan(&states))
	assert.Zero(t, states, "a failed proof must not mint OAuth state")
}

func TestOAuthLinkStart_RejectsDisabledProviderAndUnknownInput(t *testing.T) {
	t.Run("provider disabled", func(t *testing.T) {
		h, g := newGoogleHarness(t, harnessOpts{})
		admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
		g.enabled = false

		rec := admin.do(http.MethodPost, "/api/auth/oauth/google/start", map[string]string{
			"current_password": "correct horse battery",
		})
		assert.Equal(t, http.StatusNotImplemented, rec.Code)
		assert.Equal(t, "oauth_disabled", errCode(t, rec))
	})

	t.Run("unknown JSON field", func(t *testing.T) {
		h, _ := newGoogleHarness(t, harnessOpts{})
		admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

		rec := admin.do(http.MethodPost, "/api/auth/oauth/google/start", map[string]string{
			"current_password": "correct horse battery",
			"subject":          "must-not-come-from-the-browser",
		})
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Equal(t, "invalid_json", errCode(t, rec))
	})
}

func TestOAuthLinkStart_ValidProofReturnsARedirectWithoutCredentials(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	rec := admin.do(http.MethodPost, "/api/auth/oauth/google/start", map[string]string{
		"current_password": "correct horse battery",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	target := decode(t, rec)["redirect_url"].(string)
	assert.NotContains(t, target, "correct horse battery")
	assert.NotContains(t, target, "current_password")
	assert.NotContains(t, target, "code=")

	var uid, sessionID int64
	var tokenVersion int
	var proofAt time.Time
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT user_id, session_id, token_version, proof_at
		FROM oauth_state WHERE consumed_at IS NULL`).
		Scan(&uid, &sessionID, &tokenVersion, &proofAt))
	assert.Equal(t, int64(1), uid)
	assert.Positive(t, sessionID)
	assert.Zero(t, tokenVersion)
	assert.WithinDuration(t, time.Now(), proofAt, 5*time.Second)
}

func TestOAuthLinkStart_RequiresCurrentTOTPOrRecoveryWhenEnabled(t *testing.T) {
	t.Run("wrong TOTP is refused", func(t *testing.T) {
		h, _ := newGoogleHarness(t, harnessOpts{TwoFactor: true})
		h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
		e := enrolUser(t, h, "admin@example.com", "correct horse battery")

		rec := e.client.do(http.MethodPost, "/api/auth/oauth/google/start", map[string]string{
			"current_password": "correct horse battery", "code": "000000",
		})
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Equal(t, "invalid_code", errCode(t, rec))
		assert.Empty(t, e.client.cookies[auth.CookieOAuth])
	})

	t.Run("current TOTP starts the flow", func(t *testing.T) {
		h, _ := newGoogleHarness(t, harnessOpts{TwoFactor: true})
		h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
		e := enrolUser(t, h, "admin@example.com", "correct horse battery")

		assert.NotEmpty(t, e.client.startOAuthLink(t,
			"correct horse battery", codeNextStep(t, e.secret)))
	})

	t.Run("recovery code starts the flow once", func(t *testing.T) {
		h, _ := newGoogleHarness(t, harnessOpts{TwoFactor: true})
		h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
		e := enrolUser(t, h, "admin@example.com", "correct horse battery")

		assert.NotEmpty(t, e.client.startOAuthLink(t, "correct horse battery", e.codes[0]))
		var used bool
		require.NoError(t, h.pool.QueryRow(context.Background(), `
			SELECT used_at IS NOT NULL FROM recovery_code ORDER BY id LIMIT 1`).Scan(&used))
		assert.True(t, used, "a recovery credential must remain single-use")
	})
}

func TestOAuthLinkStart_RateLimitsPasswordAndSecondFactorGuesses(t *testing.T) {
	t.Run("password", func(t *testing.T) {
		h, _ := newGoogleHarness(t, harnessOpts{})
		admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
		for range 5 {
			rec := admin.do(http.MethodPost, "/api/auth/oauth/google/start",
				map[string]string{"current_password": "wrong password"})
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
		}
		rec := admin.do(http.MethodPost, "/api/auth/oauth/google/start",
			map[string]string{"current_password": "wrong password"})
		assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	})

	t.Run("second factor", func(t *testing.T) {
		h, _ := newGoogleHarness(t, harnessOpts{TwoFactor: true})
		h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
		e := enrolUser(t, h, "admin@example.com", "correct horse battery")
		for range 5 {
			rec := e.client.do(http.MethodPost, "/api/auth/oauth/google/start", map[string]string{
				"current_password": "correct horse battery", "code": "000000",
			})
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
		}
		rec := e.client.do(http.MethodPost, "/api/auth/oauth/google/start", map[string]string{
			"current_password": "correct horse battery", "code": "000000",
		})
		assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	})
}

func TestOAuthStart_RejectsAnUnknownPurpose(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	rec := h.client(t).do(http.MethodGet, "/api/auth/oauth/google/start?purpose=whatever", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_purpose", errCode(t, rec))
}

// The redirect target is built by the provider from a package constant, so no
// request input reaches it — Semgrep flags the http.Redirect anyway, since its
// taint analysis cannot see through AuthCodeURL. The guard is real regardless:
// it bounds what a provider bug (or a future configurable endpoint) can do at
// the one moment the browser is handed a URL carrying the state token.
//
// Each case would fail differently and silently without it: a plain http target
// puts the state on the wire in cleartext; `//evil.test` is protocol-relative
// and sends the browser off-origin; a relative path resolves against foldex
// itself and dies far from the cause.
func TestOAuthStart_RefusesANonHTTPSTarget(t *testing.T) {
	for _, target := range []string{
		"http://accounts.google.test/o/oauth2/v2/auth",
		"//evil.test/o/oauth2/v2/auth",
		"/o/oauth2/v2/auth",
		"javascript:alert(1)",
	} {
		t.Run(target, func(t *testing.T) {
			h, g := newGoogleHarness(t, harnessOpts{})
			g.authURL = target

			rec := h.client(t).do(http.MethodGet, "/api/auth/oauth/google/start?purpose=login", nil)
			require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
			assert.Empty(t, rec.Header().Get("Location"), "no redirect may be issued")
			// The state cookie is set immediately before the redirect, so a
			// guard placed after it would leave the browser holding a state for
			// a flow that never started.
			assert.Nil(t, cookieByName(rec, auth.CookieOAuth),
				"a refused start must not leave a state cookie behind")
		})
	}
}

func TestOAuth_DisabledProviderAnswersReadablyRatherThan404(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	g.enabled = false

	rec := h.client(t).do(http.MethodGet, "/api/auth/oauth/google/start?purpose=login", nil)
	assert.Equal(t, http.StatusNotImplemented, rec.Code)
	assert.Equal(t, "oauth_disabled", errCode(t, rec))
}

// ─────────────────────────────────────────────────────────────────────
// State — the login-CSRF defence
// ─────────────────────────────────────────────────────────────────────

// The core of the login-CSRF defence: an attacker who obtains a valid state in
// THEIR browser and feeds the callback URL to a victim must get nowhere,
// because the victim's browser holds no matching cookie.
func TestOAuthCallback_StateWithoutTheMatchingCookieIsRefused(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	g.as("sub-1", "a@b.test", true)

	attacker := h.client(t)
	state := attacker.startOAuth(t, "purpose=login")

	victim := h.client(t) // a fresh jar: no fx_oauth
	_, failure := victim.callback(t, state, "code")
	assert.Equal(t, "state_invalid", failure)
}

// A state is single-use by conditional UPDATE, so a replay of the honest
// callback cannot re-run the flow.
func TestOAuthCallback_StateIsSpentOnFirstUse(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	g.as("sub-1", "nobody@example.test", true)
	c := h.client(t)

	state := c.startOAuth(t, "purpose=login")
	c.callback(t, state, "code")

	// Put the cookie back — the callback cleared it — so the ONLY thing that
	// can refuse the replay is the spent row.
	c.cookies[auth.CookieOAuth] = state
	_, failure := c.callback(t, state, "code")
	assert.Equal(t, "state_invalid", failure)
}

func TestOAuthLinkCallback_RejectsADeadOrChangedProof(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, h *harness, c *client)
	}{
		{
			name: "logout",
			mutate: func(t *testing.T, _ *harness, c *client) {
				require.Equal(t, http.StatusNoContent,
					c.do(http.MethodPost, "/api/auth/logout", nil).Code)
			},
		},
		{
			name: "password reset",
			mutate: func(t *testing.T, h *harness, c *client) {
				token, err := h.repo.CreatePasswordReset(context.Background(), authctx.UserID(1), time.Minute, "192.0.2.10", auth.MailDraft{})
				require.NoError(t, err)
				rec := c.do(http.MethodPost, "/api/auth/password/reset", map[string]string{
					"token": token, "password": "new correct horse battery",
				})
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			},
		},
		{
			name: "session revoke",
			mutate: func(t *testing.T, h *harness, _ *client) {
				var sid int64
				require.NoError(t, h.pool.QueryRow(context.Background(), `
					SELECT id FROM session WHERE user_id = 1 AND revoked_at IS NULL`).Scan(&sid))
				require.NoError(t, h.repo.RevokeSession(context.Background(), authctx.UserID(1), sid, auth.ReasonLogout))
			},
		},
		{
			name: "password change bumps the credential epoch",
			mutate: func(t *testing.T, _ *harness, c *client) {
				rec := c.do(http.MethodPost, "/api/auth/password/change", map[string]string{
					"current_password": "correct horse battery",
					"new_password":     "new correct horse battery",
				})
				require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, g := newGoogleHarness(t, harnessOpts{})
			c := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
			g.as("google-sub", "personal@gmail.test", true)
			state := c.startOAuthLink(t, "correct horse battery", "")

			tc.mutate(t, h, c)
			c.cookies[auth.CookieOAuth] = state
			_, failure := c.callback(t, state, "code")
			assert.Equal(t, "state_invalid", failure)

			var identities int
			require.NoError(t, h.pool.QueryRow(context.Background(),
				`SELECT count(*) FROM user_identity`).Scan(&identities))
			assert.Zero(t, identities, "an invalidated proof attached an identity")
		})
	}
}

func TestOAuthLinkCallback_StateCannotBeReplayed(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	c := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	g.as("google-sub", "personal@gmail.test", true)
	state := c.startOAuthLink(t, "correct horse battery", "")

	outcome, failure := c.callback(t, state, "code")
	require.Equal(t, "linked", outcome, "failure: %s", failure)
	c.cookies[auth.CookieOAuth] = state
	_, failure = c.callback(t, state, "code")
	assert.Equal(t, "state_invalid", failure)

	var identities int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM user_identity`).Scan(&identities))
	assert.Equal(t, 1, identities)
}

func TestOAuthLinkCallback_RequiresTheExactSessionAndFreshProof(t *testing.T) {
	t.Run("another live session for the same user is not enough", func(t *testing.T) {
		h, g := newGoogleHarness(t, harnessOpts{})
		starter := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
		other := h.login(t, "admin@example.com", "correct horse battery")
		g.as("google-sub", "personal@gmail.test", true)
		state := starter.startOAuthLink(t, "correct horse battery", "")

		other.cookies[auth.CookieOAuth] = state
		_, failure := other.callback(t, state, "code")
		assert.Equal(t, "state_invalid", failure)
		assert.Empty(t, g.verifier(), "an invalid principal reached the provider exchange")
	})

	t.Run("the step-up proof has a short lifetime", func(t *testing.T) {
		h, g := newGoogleHarness(t, harnessOpts{})
		c := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
		g.as("google-sub", "personal@gmail.test", true)
		state := c.startOAuthLink(t, "correct horse battery", "")
		_, err := h.pool.Exec(context.Background(), `
			UPDATE oauth_state SET proof_at = now() - interval '6 minutes'
			WHERE purpose = 'link'`)
		require.NoError(t, err)

		_, failure := c.callback(t, state, "code")
		assert.Equal(t, "state_invalid", failure)
		assert.Empty(t, g.verifier(), "an expired proof reached the provider exchange")
	})
}

func TestOAuthCallback_ProviderRefusalIsReportedAsCancelled(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	c := h.client(t)
	c.startOAuth(t, "purpose=login")

	rec := c.do(http.MethodGet, "/api/auth/oauth/google/callback?error=access_denied", nil)
	require.Equal(t, http.StatusFound, rec.Code)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	assert.Equal(t, "cancelled", loc.Query().Get("oauth_error"))
}

func TestOAuthCallback_ExchangeFailureIsOpaque(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	g.exchErr = errors.New("boom")

	_, failure := h.client(t).googleRoundTrip(t, "login")
	assert.Equal(t, "provider_error", failure)
}

// ─────────────────────────────────────────────────────────────────────
// Login — the anti-takeover rules
// ─────────────────────────────────────────────────────────────────────

// THE takeover test.
//
// A Google account whose e-mail matches an existing foldex account must NOT
// produce a session. The address is not a secret and anyone can put one in a
// Google profile; if a match alone signed you in, "sign in with Google" would
// be an account-takeover button. It stops at the conversion prompt, and that
// prompt still demands the account's current password.
func TestOAuth_EmailMatchAloneNeverIssuesASession(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "victim@example.com", "the real password")
	g.as("attacker-sub", "victim@example.com", true)

	c := h.client(t)
	outcome, failure := c.googleRoundTrip(t, "login")

	assert.Equal(t, "convert", outcome)
	assert.Empty(t, failure)
	assert.Empty(t, c.cookies[auth.CookieAccess], "no session cookie may be issued")
	assert.Empty(t, c.cookies[auth.CookieRefresh])

	// And /me reports the conversion prompt rather than an authenticated user.
	body := decode(t, c.do(http.MethodGet, "/api/auth/me", nil))
	assert.Equal(t, "convert_password_account", body["status"])
}

// No auto-provisioning: an instance is invite-only, and anyone with a Google
// account being able to create one would bypass that policy silently.
func TestOAuth_UnknownEmailIsNotLinkedAndNeverAutoProvisions(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	g.as("stranger-sub", "stranger@elsewhere.test", true)

	_, failure := h.client(t).googleRoundTrip(t, "login")
	assert.Equal(t, "not_linked", failure)

	var n int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM app_user`).Scan(&n))
	assert.Equal(t, 1, n, "no account may be created")
}

// An unverified Google address is a string somebody typed into a profile.
// Honouring it would let anyone who claims a@b.test walk up to the conversion
// prompt for the real a@b.test.
//
// It answers `not_linked` — the SAME thing an unknown address gets — and that
// sameness is the point. Reporting `email_unverified` only when the address
// matched would make this endpoint an existence oracle: register an unverified
// Google account claiming the victim's address, and the difference between the
// two answers tells you whether they have an account here.
func TestOAuth_UnverifiedGoogleAddressIsIndistinguishableFromUnknown(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "victim@example.com", "the real password")

	g.as("attacker-sub", "victim@example.com", false)
	_, matching := h.client(t).googleRoundTrip(t, "login")

	g.as("other-sub", "nobody@nowhere.test", false)
	_, unknown := h.client(t).googleRoundTrip(t, "login")

	assert.Equal(t, unknown, matching, "the two must be indistinguishable")
	assert.Equal(t, "not_linked", matching)

	// And no challenge may have been opened for the victim.
	var n int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM auth_challenge`).Scan(&n))
	assert.Zero(t, n)
}

// A disabled account answers exactly what a NON-EXISTENT one answers. A
// distinct code would confirm the address has an account here.
func TestOAuth_DisabledAccountAnswersTheSameAsUnknown(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	invited := h.inviteAndAccept(t, admin, "user@example.com", "another good password")

	rec := admin.do(http.MethodPatch, "/api/admin/users/"+itoa(int64(invited)),
		map[string]any{"status": "disabled"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	g.as("some-sub", "user@example.com", true)
	_, disabled := h.client(t).googleRoundTrip(t, "login")

	g.as("other-sub", "nobody@nowhere.test", true)
	_, unknown := h.client(t).googleRoundTrip(t, "login")

	assert.Equal(t, unknown, disabled, "the two must be indistinguishable")
	assert.Equal(t, "not_linked", disabled)
}

// ─────────────────────────────────────────────────────────────────────
// Conversion
// ─────────────────────────────────────────────────────────────────────

func TestOAuth_ConvertRequiresTheCurrentPassword(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "victim@example.com", "the real password")
	g.as("attacker-sub", "victim@example.com", true)

	c := h.client(t)
	outcome, _ := c.googleRoundTrip(t, "login")
	require.Equal(t, "convert", outcome)

	rec := c.do(http.MethodPost, "/api/auth/oauth/google/convert",
		map[string]string{"password": "not the password"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "invalid_credentials", errCode(t, rec))
	assert.Empty(t, c.cookies[auth.CookieAccess])

	// The identity must NOT have been attached.
	var n int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM user_identity`).Scan(&n))
	assert.Zero(t, n)
}

// The budget lives on the challenge row, so a wrong guess costs the attacker a
// real attempt rather than being retryable for free.
func TestOAuth_ConvertWrongPasswordCountsAgainstTheChallengeCap(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "victim@example.com", "the real password")
	g.as("attacker-sub", "victim@example.com", true)

	c := h.client(t)
	requireRoundTrip(t, c, "login", "convert")

	// Five guesses are SPENT, then the sixth is refused. The cap is on the
	// budget the challenge carries, not on how many 401s the endpoint emits, so
	// the exhaustion shows up on the attempt AFTER the last one charged.
	for i := range 5 {
		rec := c.do(http.MethodPost, "/api/auth/oauth/google/convert",
			map[string]string{"password": "wrong"})
		require.Equal(t, http.StatusUnauthorized, rec.Code, "guess %d", i+1)
	}
	rec := c.do(http.MethodPost, "/api/auth/oauth/google/convert",
		map[string]string{"password": "wrong"})
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	// And the CORRECT password no longer helps: the budget is spent, so a
	// restart-proof cap really is a cap rather than a delay.
	rec = c.do(http.MethodPost, "/api/auth/oauth/google/convert",
		map[string]string{"password": "the real password"})
	assert.NotEqual(t, http.StatusOK, rec.Code)

	var linked int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM user_identity`).Scan(&linked))
	assert.Zero(t, linked)
}

func TestOAuth_ConvertRetiresThePasswordAndMakesTheAccountGoogleOnly(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "victim@example.com", "the real password")
	g.as("google-sub", "victim@example.com", true)

	c := h.client(t)
	requireRoundTrip(t, c, "login", "convert")

	rec := c.do(http.MethodPost, "/api/auth/oauth/google/convert",
		map[string]string{"password": "the real password"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := decode(t, rec)
	assert.Equal(t, "authenticated", body["status"])
	assert.Equal(t, false, body["user"].(map[string]any)["has_password"])

	ctx := context.Background()
	var hasPassword bool
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT password_hash IS NOT NULL FROM app_user WHERE email_normalized = 'victim@example.com'`).
		Scan(&hasPassword))
	assert.False(t, hasPassword, "the password must be gone")

	var subject string
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT subject FROM user_identity WHERE provider = 'google'`).Scan(&subject))
	assert.Equal(t, "google-sub", subject)

	// The old password no longer signs in.
	fresh := h.client(t)
	rec = fresh.do(http.MethodPost, "/api/auth/login",
		map[string]string{"email": "victim@example.com", "password": "the real password"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestOAuth_ConversionChallengeCannotSurvivePasswordReset(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "victim@example.com", "the real password")
	g.as("google-sub", "victim@example.com", true)

	c := h.client(t)
	requireRoundTrip(t, c, "login", "convert")
	user, err := h.repo.UserByEmail(context.Background(), "victim@example.com")
	require.NoError(t, err)
	reset, err := h.repo.CreatePasswordReset(context.Background(), user.ID, time.Minute, "", auth.MailDraft{})
	require.NoError(t, err)

	ctx := context.Background()
	blocker, err := h.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(ctx) }()
	var locked int64
	require.NoError(t, blocker.QueryRow(ctx,
		`SELECT id FROM app_user WHERE id = $1 FOR UPDATE`, int64(user.ID)).Scan(&locked))

	resetResult := make(chan error, 1)
	go func() {
		_, err := h.repo.ConsumePasswordReset(ctx, reset, "the reset password")
		resetResult <- err
	}()
	waitForBlockedSQL(t, h.pool, "SELECT status, token_version FROM app_user WHERE id = $1 FOR NO KEY UPDATE")

	convertResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		convertResult <- c.do(http.MethodPost, "/api/auth/oauth/google/convert",
			map[string]string{"password": "the real password"})
	}()
	require.NoError(t, blocker.Commit(ctx))
	require.NoError(t, <-resetResult)
	rec := <-convertResult
	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, "challenge_invalid", errCode(t, rec))

	var identities int
	var hasPassword bool
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM user_identity), password_hash IS NOT NULL
		FROM app_user WHERE id = $1`, int64(user.ID)).Scan(&identities, &hasPassword))
	assert.Zero(t, identities, "a pre-reset callback linked its identity")
	assert.True(t, hasPassword, "a pre-reset callback removed the reset password")
}

func TestOAuth_ReplacedConversionChallengeCannotMutateCredentials(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "victim@example.com", "the real password")

	g.as("first-sub", "victim@example.com", true)
	first := h.client(t)
	requireRoundTrip(t, first, "login", "convert")
	var oldChallengeID, uid int64
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT id, user_id FROM auth_challenge WHERE consumed_at IS NULL`).Scan(&oldChallengeID, &uid))

	g.as("replacement-sub", "victim@example.com", true)
	replacement := h.client(t)
	state := replacement.startOAuth(t, "purpose=login")

	ctx := context.Background()
	blocker, err := h.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(ctx) }()
	var locked int64
	require.NoError(t, blocker.QueryRow(ctx,
		`SELECT id FROM app_user WHERE id = $1 FOR UPDATE`, uid).Scan(&locked))

	replacementResult := make(chan [2]string, 1)
	go func() {
		outcome, failure := replacement.callback(t, state, "auth-code")
		replacementResult <- [2]string{outcome, failure}
	}()
	waitForBlockedSQL(t, h.pool, "SELECT id, token_version FROM app_user")

	convertResult := make(chan error, 1)
	go func() {
		_, _, err := h.repo.ConvertToProvider(ctx, oldChallengeID, "the real password")
		convertResult <- err
	}()
	require.NoError(t, blocker.Commit(ctx))
	replaced := <-replacementResult
	require.Equal(t, "convert", replaced[0], "replacement callback failed: %s", replaced[1])
	err = <-convertResult
	assert.ErrorIs(t, err, auth.ErrChallengeInvalid)

	var identities int
	var hasPassword bool
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM user_identity), password_hash IS NOT NULL
		FROM app_user WHERE id = $1`, uid).Scan(&identities, &hasPassword))
	assert.Zero(t, identities, "the replaced callback linked its identity")
	assert.True(t, hasPassword, "the replaced callback removed the password")
}

func TestConvertToProviderRejectsAbsentOrMalformedChallengeWithoutMutation(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "victim@example.com", "the real password")
	ctx := context.Background()
	user, err := h.repo.UserByEmail(ctx, "victim@example.com")
	require.NoError(t, err)

	_, _, err = h.repo.ConvertToProvider(ctx, 999_999, "the real password")
	assert.ErrorIs(t, err, auth.ErrChallengeInvalid)

	_, malformedID, err := h.repo.CreateChallenge(ctx, auth.NewChallenge{
		UserID: user.ID, Purpose: auth.PurposeConvertGoogle,
		TokenVersion: user.TokenVersion, TTL: time.Minute,
	})
	require.NoError(t, err)
	_, _, err = h.repo.ConvertToProvider(ctx, malformedID, "the real password")
	assert.ErrorIs(t, err, auth.ErrChallengeInvalid)

	var identities int
	var hasPassword bool
	var challengeLive bool
	require.NoError(t, h.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM user_identity), u.password_hash IS NOT NULL,
		       c.consumed_at IS NULL
		FROM app_user u JOIN auth_challenge c ON c.user_id = u.id
		WHERE u.id = $1 AND c.id = $2`, int64(user.ID), malformedID).
		Scan(&identities, &hasPassword, &challengeLive))
	assert.Zero(t, identities)
	assert.True(t, hasPassword)
	assert.True(t, challengeLive, "a refused malformed challenge was consumed outside the rolled-back mutation")
}

func TestOAuth_ConversionChallengeCannotSurviveStatusChange(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "owner@example.com", "the owner password")
	uid := testdb.SeedUserWithPassword(t, h.pool, "victim@example.com", "the victim password", "editor")
	g.as("victim-sub", "victim@example.com", true)

	c := h.client(t)
	requireRoundTrip(t, c, "login", "convert")
	status := auth.StatusDisabled
	_, err := h.repo.UpdateUser(context.Background(), uid, nil, nil, &status)
	require.NoError(t, err)

	rec := c.do(http.MethodPost, "/api/auth/oauth/google/convert",
		map[string]string{"password": "the victim password"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, "challenge_invalid", errCode(t, rec))

	var identities int
	var hasPassword bool
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM user_identity), password_hash IS NOT NULL
		FROM app_user WHERE id = $1`, int64(uid)).Scan(&identities, &hasPassword))
	assert.Zero(t, identities)
	assert.True(t, hasPassword)
}

// The conversion changes the credential set, so every session minted against
// the old password has to die.
func TestOAuth_ConvertRevokesEveryOtherSession(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	old := h.bootstrapAdmin(t, "victim@example.com", "the real password")
	require.Equal(t, http.StatusOK, old.do(http.MethodGet, "/api/links", nil).Code)

	g.as("google-sub", "victim@example.com", true)
	c := h.client(t)
	requireRoundTrip(t, c, "login", "convert")
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/oauth/google/convert",
		map[string]string{"password": "the real password"}).Code)

	assert.Equal(t, http.StatusUnauthorized, old.do(http.MethodGet, "/api/links", nil).Code,
		"the session that existed before the conversion must be dead")
}

// THE 2FA-bypass test.
//
// Conversion proves ONE factor — the password. With TOTP mandatory for admins,
// a conversion that issued a session directly would make "sign in with Google"
// strictly weaker than the password it replaces.
func TestOAuth_ConvertStillRequiresTheSecondFactor(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{TwoFactor: true, Require2FAForAdmins: true})
	h.bootstrapAdmin(t, "victim@example.com", "the real password")
	g.as("google-sub", "victim@example.com", true)

	c := h.client(t)
	requireRoundTrip(t, c, "login", "convert")

	rec := c.do(http.MethodPost, "/api/auth/oauth/google/convert",
		map[string]string{"password": "the real password"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := decode(t, rec)
	assert.NotEqual(t, "authenticated", body["status"],
		"a converted admin must not walk straight into a session")
	assert.Empty(t, c.cookies[auth.CookieAccess])
}

// The subject is pinned onto the challenge row server-side. If the convert
// request could name one, an attacker who proved a password would be able to
// attach a DIFFERENT Google account than the one that was authenticated.
//
// Two things hold that line, and the test checks both: the DTO refuses a body
// carrying a `subject` field at all (DecodeJSON disallows unknown fields), and
// the conversion reads the subject from the challenge row regardless.
func TestOAuth_ConvertUsesTheSubjectGoogleAuthenticated(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "victim@example.com", "the real password")
	g.as("the-authenticated-sub", "victim@example.com", true)

	c := h.client(t)
	requireRoundTrip(t, c, "login", "convert")

	rec := c.do(http.MethodPost, "/api/auth/oauth/google/convert", map[string]string{
		"password": "the real password",
		"subject":  "an-attacker-chosen-sub",
	})
	require.Equal(t, http.StatusBadRequest, rec.Code,
		"a body naming a subject must not even parse")
	assert.Equal(t, "invalid_json", errCode(t, rec))

	rec = c.do(http.MethodPost, "/api/auth/oauth/google/convert",
		map[string]string{"password": "the real password"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var subject string
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT subject FROM user_identity WHERE provider = 'google'`).Scan(&subject))
	assert.Equal(t, "the-authenticated-sub", subject)
}

// ─────────────────────────────────────────────────────────────────────
// Linked login
// ─────────────────────────────────────────────────────────────────────

func TestOAuth_LinkedAccountSignsInWithoutAPassword(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	g.as("google-sub", "admin@example.com", true)

	outcome, failure := admin.googleLinkRoundTrip(t, "correct horse battery", "")
	require.Equal(t, "linked", outcome, "failure: %s", failure)

	fresh := h.client(t)
	outcome, failure = fresh.googleRoundTrip(t, "login")
	assert.Equal(t, "signed_in", outcome, "failure: %s", failure)
	assert.NotEmpty(t, fresh.cookies[auth.CookieAccess])
}

// Same rule as conversion, on the ordinary login path.
func TestOAuth_LinkedLoginDoesNotBypassTOTP(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{TwoFactor: true})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	g.as("google-sub", "admin@example.com", true)
	requireLinkRoundTrip(t, admin, "correct horse battery", "", "linked")

	enrolUser(t, h, "admin@example.com", "correct horse battery")

	fresh := h.client(t)
	outcome, _ := fresh.googleRoundTrip(t, "login")
	assert.Equal(t, "two_factor", outcome)
	assert.Empty(t, fresh.cookies[auth.CookieAccess])
}

// One provider account maps to at most one foldex user. Without this, two
// accounts could both claim the same Google identity and the login lookup
// would be ambiguous.
func TestOAuth_SubjectAlreadyBoundToAnotherUserIsRefused(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	other := h.inviteAndAccept(t, admin, "other@example.com", "another good password")
	_ = other

	g.as("shared-sub", "admin@example.com", true)
	requireLinkRoundTrip(t, admin, "correct horse battery", "", "linked")

	second := h.login(t, "other@example.com", "another good password")
	_, failure := second.googleLinkRoundTrip(t, "another good password", "")
	assert.Equal(t, "already_linked", failure)
}

// Linking does NOT require the addresses to match: a personal Gmail on a work
// account is legitimate, precisely because the session already proved
// possession. That is also why linking without a session can never be allowed.
func TestOAuth_LinkAllowsADifferentAddress(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "work@example.com", "correct horse battery")
	g.as("personal-sub", "personal@gmail.test", true)

	outcome, failure := admin.googleLinkRoundTrip(t, "correct horse battery", "")
	assert.Equal(t, "linked", outcome, "failure: %s", failure)
}

func TestLinkIdentityRefusesInvalidUserAndSessionProofs(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	c := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	user, err := h.repo.UserByEmail(context.Background(), "admin@example.com")
	require.NoError(t, err)
	ctx := context.Background()
	proofAt := time.Now()
	version := user.TokenVersion

	missingUser := authctx.UserID(999_999)
	missingSession := int64(999_999)
	state := auth.OAuthState{
		UserID: &missingUser, SessionID: &missingSession,
		TokenVersion: &version, ProofAt: &proofAt,
	}
	assert.ErrorIs(t, h.repo.LinkIdentity(ctx, state, c.cookies[auth.CookieAccess],
		auth.ProviderGoogle, "sub", "google@example.com", time.Minute), auth.ErrOAuthLinkInvalid)

	state.UserID = &user.ID
	assert.ErrorIs(t, h.repo.LinkIdentity(ctx, state, c.cookies[auth.CookieAccess],
		auth.ProviderGoogle, "sub", "google@example.com", time.Minute), auth.ErrOAuthLinkInvalid)

	state.SessionID = nil
	assert.ErrorIs(t, h.repo.LinkIdentity(ctx, state, c.cookies[auth.CookieAccess],
		auth.ProviderGoogle, "sub", "google@example.com", time.Minute), auth.ErrOAuthLinkInvalid)
}

func TestValidateOAuthLinkProofRefusesInvalidAndStaleProofs(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	c := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	user, err := h.repo.UserByEmail(context.Background(), "admin@example.com")
	require.NoError(t, err)
	ctx := context.Background()
	var sessionID int64
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT id FROM session WHERE access_token_hash = $1`, secrets.Hash(c.cookies[auth.CookieAccess])).
		Scan(&sessionID))
	proofAt := time.Now()
	version := user.TokenVersion
	state := auth.OAuthState{
		UserID: &user.ID, SessionID: &sessionID,
		TokenVersion: &version, ProofAt: &proofAt,
	}

	require.NoError(t, h.repo.ValidateOAuthLinkProof(ctx, state,
		c.cookies[auth.CookieAccess], time.Minute))
	assert.ErrorIs(t, h.repo.ValidateOAuthLinkProof(ctx, state, "wrong access token", time.Minute),
		auth.ErrOAuthLinkInvalid)
	state.ProofAt = nil
	assert.ErrorIs(t, h.repo.ValidateOAuthLinkProof(ctx, state,
		c.cookies[auth.CookieAccess], time.Minute), auth.ErrOAuthLinkInvalid)
}

func TestLinkIdentityDoesNotDeadlockWithSessionRotation(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	c := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	user, err := h.repo.UserByEmail(context.Background(), "admin@example.com")
	require.NoError(t, err)
	ctx := context.Background()
	var sessionID int64
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT id FROM session WHERE access_token_hash = $1`, secrets.Hash(c.cookies[auth.CookieAccess])).
		Scan(&sessionID))

	rotation, err := h.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = rotation.Rollback(ctx) }()
	var lockedSession int64
	require.NoError(t, rotation.QueryRow(ctx,
		`SELECT id FROM session WHERE id = $1 FOR UPDATE`, sessionID).Scan(&lockedSession))

	proofAt := time.Now()
	version := user.TokenVersion
	state := auth.OAuthState{
		UserID: &user.ID, SessionID: &sessionID,
		TokenVersion: &version, ProofAt: &proofAt,
	}
	linkResult := make(chan error, 1)
	go func() {
		linkResult <- h.repo.LinkIdentity(ctx, state, c.cookies[auth.CookieAccess],
			auth.ProviderGoogle, "google-sub", "google@example.com", time.Minute)
	}()
	waitForBlockedSQL(t, h.pool, "SELECT id FROM session")

	var siblingID int64
	require.NoError(t, rotation.QueryRow(ctx, `
		INSERT INTO session (
			user_id, family_id, access_token_hash, access_expires_at,
			refresh_token_hash, refresh_expires_at, csrf_token_hash
		)
		SELECT user_id, family_id, $2, now() + interval '1 minute',
		       $3, now() + interval '1 minute', $4
		FROM session WHERE id = $1
		RETURNING id`, sessionID, []byte("sibling-access"), []byte("sibling-refresh"), []byte("sibling-csrf")).
		Scan(&siblingID), "the session FK check deadlocked with the OAuth user lock")
	require.NoError(t, rotation.Commit(ctx))
	require.NoError(t, <-linkResult)
	assert.Positive(t, siblingID)
}

func TestOAuth_LinkRefusesASecondGoogleIdentityForTheSameAccount(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	g.as("first-sub", "first@gmail.test", true)
	requireLinkRoundTrip(t, admin, "correct horse battery", "", "linked")

	g.as("second-sub", "second@gmail.test", true)
	_, failure := admin.googleLinkRoundTrip(t, "correct horse battery", "")
	assert.Equal(t, "already_linked", failure)

	var identities int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM user_identity WHERE user_id = 1`).Scan(&identities))
	assert.Equal(t, 1, identities)
}

// ─────────────────────────────────────────────────────────────────────
// Unlink and the lockout exits
// ─────────────────────────────────────────────────────────────────────

// The whole point of ADR-31's second exit: a Google-only account must acquire a
// password BEFORE it can unlink, or it would be left with no way in at all.
func TestOAuth_GoogleOnlyAccountCannotUnlinkUntilItSetsAPassword(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "victim@example.com", "the real password")
	g.as("google-sub", "victim@example.com", true)

	c := h.client(t)
	requireRoundTrip(t, c, "login", "convert")
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/oauth/google/convert",
		map[string]string{"password": "the real password"}).Code)

	// Google-only now: unlinking would strip the last credential.
	rec := c.do(http.MethodDelete, "/api/auth/oauth/google", map[string]string{"password": ""})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "password_required", errCode(t, rec))

	// Set one, and the unlink becomes possible.
	rec = c.do(http.MethodPost, "/api/auth/password/set",
		map[string]string{"password": "a brand new password"})
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	rec = c.do(http.MethodDelete, "/api/auth/oauth/google",
		map[string]string{"password": "a brand new password"})
	assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	var n int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM user_identity`).Scan(&n))
	assert.Zero(t, n)
}

// The database is the real guarantee, not the handler: migration 000021's
// deferred constraint trigger refuses the end state regardless of which code
// path produced it.
func TestOAuth_DatabaseRefusesToLeaveAnActiveAccountWithNoCredential(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	_, err := h.pool.Exec(context.Background(),
		`UPDATE app_user SET password_hash = NULL WHERE email_normalized = 'admin@example.com'`)
	require.Error(t, err, "nulling the only credential must be refused by the database")
	assert.Contains(t, strings.ToLower(err.Error()), "no way to sign in")
}

// SetPassword is not an alias for password/change. Overwriting an existing
// password without proving the current one would turn a stolen session into
// permanent account takeover.
func TestSetPassword_RefusesWhenAPasswordAlreadyExists(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	c := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	rec := c.do(http.MethodPost, "/api/auth/password/set",
		map[string]string{"password": "a different password"})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "password_exists", errCode(t, rec))
}

func TestSetPassword_RepositoryRefusalsAreTyped(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{TwoFactor: true})
	c := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	user, err := h.repo.GetUser(context.Background(), authctx.UserID(1))
	require.NoError(t, err)
	var sid int64
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT id FROM session WHERE access_token_hash = $1`,
		secrets.Hash(c.cookies[auth.CookieAccess])).Scan(&sid))

	err = h.repo.SetPassword(context.Background(), user.ID, sid, user.TokenVersion,
		"a different password", auth.SecondFactorProof{})
	assert.ErrorIs(t, err, auth.ErrPasswordExists)
	err = h.repo.SetPassword(context.Background(), authctx.UserID(999_999), sid, 0,
		"a different password", auth.SecondFactorProof{})
	assert.ErrorIs(t, err, auth.ErrNoUser)

	enrolUser(t, h, "admin@example.com", "correct horse battery")
	testdb.ConvertToGoogleOnly(t, h.pool, user.ID, "admin@example.com", "google-sub")
	current, err := h.repo.GetUser(context.Background(), user.ID)
	require.NoError(t, err)
	err = h.repo.SetPassword(context.Background(), user.ID, sid, current.TokenVersion,
		"a different password", auth.SecondFactorProof{})
	assert.ErrorIs(t, err, auth.ErrTOTPReplay)
}

// ─────────────────────────────────────────────────────────────────────
// Invite acceptance through Google
// ─────────────────────────────────────────────────────────────────────

// An invitation is issued TO a mailbox. Letting a leaked link be claimed by any
// Google account would silently hand someone else the role it carried.
func TestOAuth_InviteRefusesADifferentGoogleAddress(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	token := h.createInvite(t, admin, "invited@example.com")

	g.as("interloper-sub", "someone.else@gmail.test", true)
	c := h.client(t)
	state := c.startOAuthInvite(t, token)
	_, failure := c.callback(t, state, "code")

	assert.Equal(t, "invite_email_mismatch", failure)

	var n int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM app_user`).Scan(&n))
	assert.Equal(t, 1, n, "no account may be created")
}

func TestOAuth_InviteAcceptedWithTheMatchingGoogleAccount(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	token := h.createInvite(t, admin, "invited@example.com")

	g.as("invited-sub", "invited@example.com", true)
	c := h.client(t)
	state := c.startOAuthInvite(t, token)
	outcome, failure := c.callback(t, state, "code")

	require.Equal(t, "signed_in", outcome, "failure: %s", failure)
	assert.NotEmpty(t, c.cookies[auth.CookieAccess])

	// Provider-only from birth: there is no password to guess.
	var hasPassword bool
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT password_hash IS NOT NULL FROM app_user WHERE email_normalized = 'invited@example.com'`).
		Scan(&hasPassword))
	assert.False(t, hasPassword)
}

func TestOAuth_InviteRefusesAnUnverifiedGoogleAddress(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	token := h.createInvite(t, admin, "invited@example.com")

	g.as("invited-sub", "invited@example.com", false)
	c := h.client(t)
	state := c.startOAuthInvite(t, token)
	_, failure := c.callback(t, state, "code")
	assert.Equal(t, "email_unverified", failure)
}

// ─────────────────────────────────────────────────────────────────────
// Forgot password on a converted account
// ─────────────────────────────────────────────────────────────────────

// The third lockout exit, and the reason it carries NO link: a reset link here
// would let control of the mailbox alone resurrect a password credential —
// exactly what requiring the current password during conversion refused.
func TestForgotPassword_GoogleOnlyAccountGetsAMessageButNoResetLink(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{SMTP: true})
	h.bootstrapAdmin(t, "victim@example.com", "the real password")
	g.as("google-sub", "victim@example.com", true)

	c := h.client(t)
	requireRoundTrip(t, c, "login", "convert")
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/oauth/google/convert",
		map[string]string{"password": "the real password"}).Code)
	h.mail.reset()

	rec := h.client(t).do(http.MethodPost, "/api/auth/password/forgot",
		map[string]string{"email": "victim@example.com"})
	require.Equal(t, http.StatusAccepted, rec.Code)

	msg := h.mail.waitFor(t, "victim@example.com")
	assert.Contains(t, strings.ToLower(msg.Text), "google")
	assert.NotContains(t, msg.Text, "?reset=", "a reset link would undo the conversion's guarantee")

	var n int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM password_reset`).Scan(&n))
	assert.Zero(t, n, "no reset token may even be minted")
}

// ─────────────────────────────────────────────────────────────────────
// Admin force-password-reset — the first lockout exit
// ─────────────────────────────────────────────────────────────────────

func TestAdmin_ForcePasswordResetRestoresAccessToAGoogleOnlyAccount(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{SMTP: true})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	h.inviteAndAccept(t, admin, "user@example.com", "the user's password")

	// Convert the invited account to Google-only.
	g.as("google-sub", "user@example.com", true)
	victim := h.client(t)
	requireRoundTrip(t, victim, "login", "convert")
	require.Equal(t, http.StatusOK, victim.do(http.MethodPost, "/api/auth/oauth/google/convert",
		map[string]string{"password": "the user's password"}).Code)

	var uid int64
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT id FROM app_user WHERE email_normalized = 'user@example.com'`).Scan(&uid))
	h.mail.reset()

	rec := admin.do(http.MethodPost, "/api/admin/users/"+itoa(uid)+"/force-password-reset", nil)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	assert.Empty(t, strings.TrimSpace(rec.Body.String()))
	token := resetTokenFrom(t, h.mail.waitFor(t, "user@example.com").Text)

	reset := h.client(t).do(http.MethodPost, "/api/auth/password/reset",
		map[string]string{"token": token, "password": "a password chosen by the user"})
	require.Equal(t, http.StatusOK, reset.Code, reset.Body.String())
	assert.Equal(t, http.StatusOK, h.client(t).do(http.MethodPost, "/api/auth/login",
		map[string]string{"email": "user@example.com", "password": "a password chosen by the user"}).Code)
}

// An admin who has forgotten their OWN password is in the one situation this
// cannot solve; offering it would only sign them out of the session they still
// have.
func TestAdmin_ForcePasswordResetRefusesOnYourOwnAccount(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	rec := admin.do(http.MethodGet, "/api/auth/me", nil)
	uid := int64(decode(t, rec)["user"].(map[string]any)["id"].(float64))

	rec = admin.do(http.MethodPost, "/api/admin/users/"+itoa(uid)+"/force-password-reset", nil)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "self_target", errCode(t, rec))
}

func TestAdmin_ForcePasswordResetDoesNotInstallOrReturnACredential(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{SMTP: true})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	uid := h.inviteAndAccept(t, admin, "user@example.com", "the user's password")
	h.mail.reset()

	var hashBefore string
	var epochBefore, sessionsBefore int
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT password_hash, token_version FROM app_user WHERE id = $1`, int64(uid)).
		Scan(&hashBefore, &epochBefore))
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM session WHERE user_id = $1 AND revoked_at IS NULL`, int64(uid)).
		Scan(&sessionsBefore))

	rec := admin.do(http.MethodPost, "/api/admin/users/"+itoa(int64(uid))+"/force-password-reset", nil)
	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.Empty(t, strings.TrimSpace(rec.Body.String()), "the admin response exposed recovery material")
	assert.NotContains(t, rec.Body.String(), "temporary_password")
	assert.NotContains(t, rec.Body.String(), "token")

	var hashAfter string
	var epochAfter, sessionsAfter int
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT password_hash, token_version FROM app_user WHERE id = $1`, int64(uid)).
		Scan(&hashAfter, &epochAfter))
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM session WHERE user_id = $1 AND revoked_at IS NULL`, int64(uid)).
		Scan(&sessionsAfter))
	assert.Equal(t, hashBefore, hashAfter, "the admin action installed a password before the target acted")
	assert.Equal(t, epochBefore, epochAfter)
	assert.Equal(t, sessionsBefore, sessionsAfter)
	var resetEpoch int
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT token_version FROM password_reset WHERE user_id = $1 AND consumed_at IS NULL`, int64(uid)).
		Scan(&resetEpoch))
	assert.Equal(t, epochBefore, resetEpoch, "administrator recovery did not capture the live credential epoch")
	assert.Equal(t, http.StatusOK, h.client(t).do(http.MethodPost, "/api/auth/login",
		map[string]string{"email": "user@example.com", "password": "the user's password"}).Code)

	var sessionsAtConsumption int
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM session WHERE user_id = $1 AND revoked_at IS NULL`, int64(uid)).
		Scan(&sessionsAtConsumption))
	token := resetTokenFrom(t, h.mail.waitFor(t, "user@example.com").Text)
	require.Equal(t, http.StatusOK, h.client(t).do(http.MethodPost, "/api/auth/password/reset",
		map[string]string{"token": token, "password": "the target's chosen password"}).Code)

	var consumedEpoch, liveSessions, revokedSessions int
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT token_version FROM app_user WHERE id = $1`, int64(uid)).Scan(&consumedEpoch))
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT count(*) FILTER (WHERE revoked_at IS NULL),
		       count(*) FILTER (WHERE revoked_reason = 'password_changed')
		FROM session WHERE user_id = $1`, int64(uid)).Scan(&liveSessions, &revokedSessions))
	assert.Equal(t, epochBefore+1, consumedEpoch)
	assert.Equal(t, 1, liveSessions, "only the post-recovery session should be live")
	assert.GreaterOrEqual(t, revokedSessions, sessionsAtConsumption,
		"pre-recovery sessions survived token consumption")
}

func TestAdmin_ForcePasswordResetLinkDiesAfterSessionRevocation(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{SMTP: true})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	uid := h.inviteAndAccept(t, admin, "user@example.com", "the user's password")
	h.mail.reset()

	rec := admin.do(http.MethodPost, "/api/admin/users/"+itoa(int64(uid))+"/force-password-reset", nil)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	token := resetTokenFrom(t, h.mail.waitFor(t, "user@example.com").Text)

	rec = admin.do(http.MethodPost, "/api/admin/users/"+itoa(int64(uid))+"/sessions/revoke", nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	rec = h.client(t).do(http.MethodPost, "/api/auth/password/reset", map[string]string{
		"token": token, "password": "a stale recovery password",
	})
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, "reset_invalid", errCode(t, rec))
	assert.Equal(t, http.StatusOK, h.client(t).do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "user@example.com", "password": "the user's password",
	}).Code)
}

func TestAdmin_ForcePasswordResetPreservesTheTargetsSecondFactor(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{SMTP: true, TwoFactor: true})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	uid := h.inviteAndAccept(t, admin, "user@example.com", "the user's password")
	enrolled := enrolUser(t, h, "user@example.com", "the user's password")
	testdb.ConvertToGoogleOnly(t, h.pool, uid, "user@example.com", "google-only-with-totp")
	h.mail.reset()

	rec := admin.do(http.MethodPost, "/api/admin/users/"+itoa(int64(uid))+"/force-password-reset", nil)
	require.Equal(t, http.StatusAccepted, rec.Code)
	token := resetTokenFrom(t, h.mail.waitFor(t, "user@example.com").Text)

	target := h.client(t)
	rec = target.do(http.MethodPost, "/api/auth/password/reset", map[string]string{
		"token": token, "password": "the target's chosen password",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "two_factor_required", decode(t, rec)["status"])
	assert.Empty(t, target.cookies[auth.CookieAccess])

	rec = target.do(http.MethodPost, "/api/auth/2fa/verify",
		map[string]string{"code": codeNextStep(t, enrolled.secret)})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotEmpty(t, target.cookies[auth.CookieAccess])
}

func TestAdmin_ForcePasswordResetTokenRemainsSingleUseAndExpires(t *testing.T) {
	t.Run("single use", func(t *testing.T) {
		h, _ := newGoogleHarness(t, harnessOpts{SMTP: true})
		admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
		uid := h.inviteAndAccept(t, admin, "user@example.com", "the user's password")
		h.mail.reset()

		rec := admin.do(http.MethodPost, "/api/admin/users/"+itoa(int64(uid))+"/force-password-reset", nil)
		require.Equal(t, http.StatusAccepted, rec.Code)
		token := resetTokenFrom(t, h.mail.waitFor(t, "user@example.com").Text)
		require.Equal(t, http.StatusOK, h.client(t).do(http.MethodPost, "/api/auth/password/reset",
			map[string]string{"token": token, "password": "first target password"}).Code)
		rec = h.client(t).do(http.MethodPost, "/api/auth/password/reset",
			map[string]string{"token": token, "password": "second target password"})
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Equal(t, "reset_invalid", errCode(t, rec))
	})

	t.Run("expiry", func(t *testing.T) {
		h, _ := newGoogleHarness(t, harnessOpts{SMTP: true})
		admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
		uid := h.inviteAndAccept(t, admin, "user@example.com", "the user's password")
		h.mail.reset()

		rec := admin.do(http.MethodPost, "/api/admin/users/"+itoa(int64(uid))+"/force-password-reset", nil)
		require.Equal(t, http.StatusAccepted, rec.Code)
		token := resetTokenFrom(t, h.mail.waitFor(t, "user@example.com").Text)
		_, err := h.pool.Exec(context.Background(), `UPDATE password_reset SET expires_at = now() - interval '1 minute'`)
		require.NoError(t, err)

		rec = h.client(t).do(http.MethodPost, "/api/auth/password/reset",
			map[string]string{"token": token, "password": "a target password"})
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Equal(t, http.StatusOK, h.client(t).do(http.MethodPost, "/api/auth/login",
			map[string]string{"email": "user@example.com", "password": "the user's password"}).Code)
	})
}

func TestAdminRecoverySupersedingAConcurrentResetCannotApplyTheOldToken(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{SMTP: true})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	uid := h.inviteAndAccept(t, admin, "user@example.com", "the user's password")
	oldToken, err := h.repo.CreatePasswordReset(context.Background(), uid, time.Minute, "", auth.MailDraft{})
	require.NoError(t, err)

	deliveryStarted := make(chan struct{})
	releaseDelivery := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseDelivery) }) }
	t.Cleanup(release)
	recoveryErr := make(chan error, 1)
	go func() {
		recoveryErr <- h.repo.CreateAdminPasswordRecovery(context.Background(), uid, time.Minute,
			// draftFor runs INSIDE the transaction, right before the enqueue and
			// commit, which makes it the pause point this test needs now that the
			// send is no longer synchronous. What is being proven is unchanged:
			// the app_user row stays locked for the whole recovery, so a
			// concurrent ConsumePasswordReset cannot apply the superseded token.
			func(string, string) auth.MailDraft {
				close(deliveryStarted)
				<-releaseDelivery
				return auth.MailDraft{}
			})
	}()
	<-deliveryStarted

	resetErr := make(chan error, 1)
	go func() {
		_, err := h.repo.ConsumePasswordReset(context.Background(), oldToken, "an attacker password")
		resetErr <- err
	}()
	waitForBlockedSQL(t, h.pool, "SELECT status, token_version FROM app_user WHERE id = $1 FOR NO KEY UPDATE")
	release()
	require.NoError(t, <-recoveryErr)
	assert.ErrorIs(t, <-resetErr, auth.ErrResetInvalid)

	var epoch int
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT token_version FROM app_user WHERE id = $1`, int64(uid)).Scan(&epoch))
	assert.Zero(t, epoch, "the superseded reset changed the credential epoch")
	assert.Equal(t, http.StatusOK, h.client(t).do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "user@example.com", "password": "the user's password",
	}).Code)
	assert.Equal(t, http.StatusUnauthorized, h.client(t).do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "user@example.com", "password": "an attacker password",
	}).Code)
}

func TestAdminRecoveryTokenCannotMutateAfterTargetIsDisabled(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{SMTP: true})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	uid := h.inviteAndAccept(t, admin, "user@example.com", "the user's password")
	h.mail.reset()

	rec := admin.do(http.MethodPost, "/api/admin/users/"+itoa(int64(uid))+"/force-password-reset", nil)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	token := resetTokenFrom(t, h.mail.waitFor(t, "user@example.com").Text)
	status := auth.StatusDisabled
	_, err := h.repo.UpdateUser(context.Background(), uid, nil, nil, &status)
	require.NoError(t, err)

	_, err = h.repo.ConsumePasswordReset(context.Background(), token, "an attacker password")
	assert.ErrorIs(t, err, auth.ErrResetInvalid)
	var epoch int
	var originalWorks bool
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT token_version, password_hash IS NOT NULL FROM app_user WHERE id = $1`, int64(uid)).
		Scan(&epoch, &originalWorks))
	assert.Equal(t, 1, epoch, "the refused reset bumped the status-change epoch")
	assert.True(t, originalWorks, "the refused reset removed the existing password")
}

// A transport outage no longer costs the administrator the operation.
//
// This used to assert 503 mail_unavailable and that NOTHING was written: the
// send was synchronous inside the transaction, so SMTP refusing rolled the
// token back. ADR-36 §12.1 changed that deliberately — token and message commit
// together and the message is retried, so a blip stops discarding a recovery
// the administrator is entitled to start.
//
// What must STILL hold, and is what this now proves: the target's password,
// sessions and credential epoch are untouched until they consume the link
// themselves. An administrator starts a recovery; they never install one.
func TestAdmin_ForcePasswordResetSurvivesATransportOutage(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{SMTP: true})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	uid := h.inviteAndAccept(t, admin, "user@example.com", "the user's password")
	h.mail.reset()
	h.mail.fail(errors.New("smtp unavailable"))

	var hashBefore string
	var epochBefore, sessionsBefore int
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT password_hash, token_version FROM app_user WHERE id = $1`, int64(uid)).
		Scan(&hashBefore, &epochBefore))
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM session WHERE user_id = $1 AND revoked_at IS NULL`, int64(uid)).Scan(&sessionsBefore))

	rec := admin.do(http.MethodPost, "/api/admin/users/"+itoa(int64(uid))+"/force-password-reset", nil)
	assert.Equal(t, http.StatusAccepted, rec.Code, "a transport outage must no longer deny the operation")

	// The credential exists and so does its message — the whole point of one
	// transaction — and the message is still queued for a retry.
	var resets, queued int
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM password_reset WHERE user_id = $1 AND consumed_at IS NULL`,
		int64(uid)).Scan(&resets))
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM mail_outbox WHERE template = $1`, mailer.TemplateAdminRecovery).Scan(&queued))
	assert.Equal(t, 1, resets)
	assert.Equal(t, 1, queued, "the token was created without a message to carry it")

	var hashAfter string
	var epochAfter, sessionsAfter int
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT password_hash, token_version FROM app_user WHERE id = $1`, int64(uid)).
		Scan(&hashAfter, &epochAfter))
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM session WHERE user_id = $1 AND revoked_at IS NULL`, int64(uid)).Scan(&sessionsAfter))
	assert.Equal(t, hashBefore, hashAfter, "the admin changed the target's password")
	assert.Equal(t, epochBefore, epochAfter, "the admin moved the target's credential epoch")
	assert.Equal(t, sessionsBefore, sessionsAfter, "the admin revoked the target's sessions")
}

func TestAdmin_ForcePasswordResetRequiresSMTPAndAVerifiedTarget(t *testing.T) {
	t.Run("log driver", func(t *testing.T) {
		h, _ := newGoogleHarness(t, harnessOpts{})
		admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
		uid := h.inviteAndAccept(t, admin, "user@example.com", "the user's password")
		h.mail.reset()

		rec := admin.do(http.MethodPost, "/api/admin/users/"+itoa(int64(uid))+"/force-password-reset", nil)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Equal(t, "smtp_required", errCode(t, rec))
		assert.Empty(t, h.mail.all(), "a recovery credential was written to the log mailer")
	})

	t.Run("unverified target", func(t *testing.T) {
		h, _ := newGoogleHarness(t, harnessOpts{SMTP: true})
		admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
		uid := h.inviteAndAccept(t, admin, "user@example.com", "the user's password")
		h.mail.reset()
		_, err := h.pool.Exec(context.Background(), `UPDATE app_user SET email_verified_at = NULL WHERE id = $1`, int64(uid))
		require.NoError(t, err)

		rec := admin.do(http.MethodPost, "/api/admin/users/"+itoa(int64(uid))+"/force-password-reset", nil)
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, "recovery_unavailable", errCode(t, rec))
		assert.Empty(t, h.mail.all())
	})
}

// ─────────────────────────────────────────────────────────────────────
// Helpers used above
// ─────────────────────────────────────────────────────────────────────

// requireRoundTrip runs a full round-trip and asserts where it ended, printing
// the FAILURE marker when it did not.
//
// The obvious helper — one that returns only the outcome — throws away the one
// value that says why: a failed round-trip reports an empty outcome and a
// populated oauth_error, so discarding the second leaves every failure reading
// `expected "convert", actual ""`.
func requireRoundTrip(t *testing.T, c *client, purpose, want string) {
	t.Helper()
	outcome, failure := c.googleRoundTrip(t, purpose)
	require.Equal(t, want, outcome, "callback failed with oauth_error=%q", failure)
}

func requireLinkRoundTrip(t *testing.T, c *client, password, code, want string) {
	t.Helper()
	got, failure := c.googleLinkRoundTrip(t, password, code)
	require.Equal(t, want, got, "oauth failure: %s", failure)
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// createInvite issues an invitation and returns the raw token from its URL.
func (h *harness) createInvite(t *testing.T, admin *client, email string) string {
	t.Helper()
	rec := admin.do(http.MethodPost, "/api/admin/invites",
		map[string]string{"email": email, "role": "editor"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	acceptURL, _ := decode(t, rec)["accept_url"].(string)
	parsed, err := url.Parse(acceptURL)
	require.NoError(t, err)
	require.Empty(t, parsed.RawQuery)
	require.Contains(t, parsed.Fragment, "invite=")
	return strings.TrimPrefix(parsed.Fragment, "invite=")
}

// inviteAndAccept creates a second ordinary account and returns its id.
func (h *harness) inviteAndAccept(t *testing.T, admin *client, email, password string) authctx.UserID {
	t.Helper()
	token := h.createInvite(t, admin, email)

	rec := h.client(t).do(http.MethodPost, "/api/auth/invites/accept", map[string]string{
		"token": token, "name": "Invited", "password": password,
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return authctx.UserID(int64(decode(t, rec)["user"].(map[string]any)["id"].(float64)))
}

// login signs in and returns the resulting client.
func (h *harness) login(t *testing.T, email, password string) *client {
	t.Helper()
	c := h.client(t)
	rec := c.do(http.MethodPost, "/api/auth/login",
		map[string]string{"email": email, "password": password})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return c
}

// The per-IP cap on /start is what bounds oauth_state, a table an
// unauthenticated caller writes to on every request. A limiter that reset its
// bucket on each call — CommitSuccess zeroes a scalar key's failure count —
// would leave the cap decorative, and nothing else bounds that table between
// sweeps.
func TestOAuthStart_IsRateLimitedPerAddress(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	c := h.client(t)

	var last int
	for range 31 {
		last = c.do(http.MethodGet, "/api/auth/oauth/google/start?purpose=login", nil).Code
	}
	assert.Equal(t, http.StatusTooManyRequests, last)
	assert.NotEmpty(t, c.do(http.MethodGet,
		"/api/auth/oauth/google/start?purpose=login", nil).Header().Get("Retry-After"))
}

// ─────────────────────────────────────────────────────────────────────
// Identities on the account screen
// ─────────────────────────────────────────────────────────────────────

// The account screen offers "connect" or "disconnect" based on this, so an
// empty answer and a populated one have to be told apart honestly — guessing
// from has_password would show "disconnect" on an account with no link.
func TestOAuth_ListIdentitiesReflectsWhatIsLinked(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	rec := admin.do(http.MethodGet, "/api/auth/identities", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Empty(t, decode(t, rec)["identities"])

	g.as("google-sub", "personal@gmail.test", true)
	requireLinkRoundTrip(t, admin, "correct horse battery", "", "linked")

	rec = admin.do(http.MethodGet, "/api/auth/identities", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	list := decode(t, rec)["identities"].([]any)
	require.Len(t, list, 1)
	row := list[0].(map[string]any)
	assert.Equal(t, "google", row["provider"])
	// The address is recorded AT LINK TIME. It is a label for the account
	// screen, never a lookup key — the subject is what resolves a login, so a
	// user changing their Google address must not move anything.
	assert.Equal(t, "personal@gmail.test", row["email_at_link"])
}

// One account's identities must never appear on another's screen.
func TestOAuth_ListIdentitiesIsScopedToTheCaller(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	h.inviteAndAccept(t, admin, "user@example.com", "another good password")

	g.as("google-sub", "admin@example.com", true)
	requireLinkRoundTrip(t, admin, "correct horse battery", "", "linked")

	other := h.login(t, "user@example.com", "another good password")
	rec := other.do(http.MethodGet, "/api/auth/identities", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, decode(t, rec)["identities"])
}

// ─────────────────────────────────────────────────────────────────────
// Unlink — the refusal paths
// ─────────────────────────────────────────────────────────────────────

func TestOAuth_UnlinkWithNothingLinkedIs404(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	rec := admin.do(http.MethodDelete, "/api/auth/oauth/google",
		map[string]string{"password": "correct horse battery"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "not_linked", errCode(t, rec))
}

// Unlinking is a credential change, so a live session alone is not enough: a
// stolen cookie must not be able to detach the owner's other way in.
func TestOAuth_UnlinkRequiresTheCorrectPassword(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	g.as("google-sub", "admin@example.com", true)
	requireLinkRoundTrip(t, admin, "correct horse battery", "", "linked")

	rec := admin.do(http.MethodDelete, "/api/auth/oauth/google", map[string]string{
		"password": "correct horse battery", "subject": "must-not-be-accepted",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_json", errCode(t, rec))

	rec = admin.do(http.MethodDelete, "/api/auth/oauth/google",
		map[string]string{"password": "not the password"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "invalid_credentials", errCode(t, rec))

	var n int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM user_identity`).Scan(&n))
	assert.Equal(t, 1, n, "the identity must survive a wrong password")
}

func TestOAuth_UnlinkCannotUsePasswordProofAfterConcurrentPasswordChange(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	g.as("google-sub", "admin@example.com", true)
	admin := h.login(t, "admin@example.com", "correct horse battery")
	requireLinkRoundTrip(t, admin, "correct horse battery", "", "linked")
	user, err := h.repo.UserByEmail(context.Background(), "admin@example.com")
	require.NoError(t, err)

	ctx := context.Background()
	blocker, err := h.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(ctx) }()
	var locked int64
	require.NoError(t, blocker.QueryRow(ctx,
		`SELECT id FROM app_user WHERE id = $1 FOR UPDATE`, int64(user.ID)).Scan(&locked))

	changeResult := make(chan error, 1)
	go func() {
		changeResult <- h.repo.ChangePassword(ctx, user.ID, 1,
			"correct horse battery", "a changed password")
	}()
	waitForBlockedSQL(t, h.pool, "SELECT password_hash FROM app_user WHERE id = $1 FOR NO KEY UPDATE")

	unlinkResult := make(chan error, 1)
	go func() {
		unlinkResult <- h.repo.UnlinkIdentity(ctx, user.ID, 1, user.TokenVersion,
			auth.ProviderGoogle, "correct horse battery")
	}()
	waitForBlockedSQL(t, h.pool, "SELECT password_hash,")

	require.NoError(t, blocker.Commit(ctx))
	require.NoError(t, <-changeResult)
	assert.ErrorIs(t, <-unlinkResult, auth.ErrSessionInvalid)

	var identities int
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT count(*) FROM user_identity WHERE user_id = $1`, int64(user.ID)).Scan(&identities))
	assert.Equal(t, 1, identities)
}

func TestOAuth_UnlinkBumpsCredentialEpochAndRevokesOtherSessions(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	g.as("google-sub", "admin@example.com", true)

	caller := h.login(t, "admin@example.com", "correct horse battery")
	requireLinkRoundTrip(t, caller, "correct horse battery", "", "linked")
	other := h.login(t, "admin@example.com", "correct horse battery")
	user, err := h.repo.UserByEmail(context.Background(), "admin@example.com")
	require.NoError(t, err)

	rec := caller.do(http.MethodDelete, "/api/auth/oauth/google",
		map[string]string{"password": "correct horse battery"})
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	updated, err := h.repo.GetUser(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Equal(t, user.TokenVersion+1, updated.TokenVersion)
	assert.Equal(t, http.StatusUnauthorized, other.do(http.MethodGet, "/api/links", nil).Code)
	assert.Equal(t, http.StatusOK, caller.do(http.MethodGet, "/api/links", nil).Code)
}

func TestOAuth_UnlinkRollsBackIdentityAndEpochWhenSessionRevocationFails(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	g.as("google-sub", "admin@example.com", true)

	caller := h.login(t, "admin@example.com", "correct horse battery")
	requireLinkRoundTrip(t, caller, "correct horse battery", "", "linked")
	h.login(t, "admin@example.com", "correct horse battery")
	user, err := h.repo.UserByEmail(context.Background(), "admin@example.com")
	require.NoError(t, err)

	_, err = h.pool.Exec(context.Background(), `
		CREATE FUNCTION fail_unlink_session_revoke() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'forced unlink revocation failure'; END $$;
		CREATE TRIGGER fail_unlink_session_revoke
		BEFORE UPDATE OF revoked_at ON session FOR EACH ROW
		WHEN (NEW.revoked_at IS NOT NULL AND OLD.revoked_at IS NULL)
		EXECUTE FUNCTION fail_unlink_session_revoke()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `
			DROP TRIGGER IF EXISTS fail_unlink_session_revoke ON session;
			DROP FUNCTION IF EXISTS fail_unlink_session_revoke()`)
	})

	rec := caller.do(http.MethodDelete, "/api/auth/oauth/google",
		map[string]string{"password": "correct horse battery"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())

	var identities, tokenVersion int
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT count(*), min(u.token_version)
		FROM app_user u
		JOIN user_identity i ON i.user_id = u.id
		WHERE u.id = $1`, int64(user.ID)).Scan(&identities, &tokenVersion))
	assert.Equal(t, 1, identities, "identity deletion committed without required session revocation")
	assert.Equal(t, user.TokenVersion, tokenVersion, "credential epoch bumped despite rollback")
}

func TestOAuth_CallbackChallengeFailureStillRedirects(t *testing.T) {
	g := &fakeGoogle{enabled: true}
	h := newHarnessWith(t, testdb.New(t), harnessOpts{Google: g})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "victim@example.com", "the real password")
	g.as("google-sub", "victim@example.com", true)
	c := h.client(t)
	state := c.startOAuth(t, "purpose=login")

	_, err := h.pool.Exec(context.Background(), `DROP TABLE auth_challenge CASCADE`)
	require.NoError(t, err)
	outcome, failure := c.callback(t, state, "auth-code")
	assert.Empty(t, outcome)
	assert.Equal(t, "server_error", failure)
}

// ─────────────────────────────────────────────────────────────────────
// Setting a password — the second lockout exit
// ─────────────────────────────────────────────────────────────────────

// With an authenticator configured there is no current password to prove, so
// the code is the only step-up available — and the credential being created
// outlives the session that asked for it.
func TestSetPassword_RequiresTheAuthenticatorCodeWhenOneExists(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{TwoFactor: true})
	h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	e := enrolUser(t, h, "admin@example.com", "correct horse battery")
	g.as("google-sub", "admin@example.com", true)

	// The state a conversion leaves behind: Google linked, no password. The
	// helper does BOTH in one transaction — an active account is never allowed
	// to sit without a credential, not even between two statements.
	testdb.ConvertToGoogleOnly(t, h.pool, authctx.UserID(1), "admin@example.com", "google-sub")

	rec := e.client.do(http.MethodPost, "/api/auth/password/set",
		map[string]string{"password": "a brand new password"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "no code was demanded")
	assert.Equal(t, "invalid_code", errCode(t, rec))

	rec = e.client.do(http.MethodPost, "/api/auth/password/set", map[string]string{
		"password": "a brand new password", "code": codeNextStep(t, e.secret),
	})
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	// And the new password actually signs in.
	fresh := h.client(t)
	assert.Equal(t, http.StatusOK, fresh.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a brand new password"}).Code)
}

func TestSetPassword_RejectsAShortPassword(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	g.as("google-sub", "admin@example.com", true)
	testdb.ConvertToGoogleOnly(t, h.pool, authctx.UserID(1), "admin@example.com", "google-sub")

	rec := admin.do(http.MethodPost, "/api/auth/password/set", map[string]string{"password": "short"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "password_too_short", errCode(t, rec))
}

// Setting a password is a credential change, so every OTHER session dies while
// the caller's own survives — the same treatment a password change gets, and
// for the same reason: signing them out of the browser they are using would be
// hostile.
func TestSetPassword_RevokesEveryOtherSession(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	g.as("google-sub", "admin@example.com", true)
	testdb.ConvertToGoogleOnly(t, h.pool, authctx.UserID(1), "admin@example.com", "google-sub")

	// Two devices, both signed in through Google — the account has no password
	// to log in with any more, which is the whole situation this endpoint fixes.
	caller := h.client(t)
	requireRoundTrip(t, caller, "login", "signed_in")
	second := h.client(t)
	requireRoundTrip(t, second, "login", "signed_in")
	require.Equal(t, http.StatusOK, second.do(http.MethodGet, "/api/links", nil).Code)

	require.Equal(t, http.StatusNoContent, caller.do(http.MethodPost, "/api/auth/password/set",
		map[string]string{"password": "a brand new password"}).Code)

	assert.Equal(t, http.StatusUnauthorized, second.do(http.MethodGet, "/api/links", nil).Code,
		"a session from before the credential change survived")
	assert.Equal(t, http.StatusOK, caller.do(http.MethodGet, "/api/links", nil).Code,
		"the caller's own session must survive")
}

func TestSetPassword_RollsBackCredentialWhenSessionRevocationFails(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	g.as("google-sub", "admin@example.com", true)
	testdb.ConvertToGoogleOnly(t, h.pool, authctx.UserID(1), "admin@example.com", "google-sub")

	caller := h.client(t)
	requireRoundTrip(t, caller, "login", "signed_in")
	other := h.client(t)
	requireRoundTrip(t, other, "login", "signed_in")
	_, err := h.pool.Exec(context.Background(), `
		CREATE FUNCTION fail_session_revoke() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'forced session revocation failure'; END $$;
		CREATE TRIGGER fail_session_revoke
		BEFORE UPDATE OF revoked_at ON session FOR EACH ROW
		WHEN (NEW.revoked_at IS NOT NULL AND OLD.revoked_at IS NULL)
		EXECUTE FUNCTION fail_session_revoke()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `
			DROP TRIGGER IF EXISTS fail_session_revoke ON session;
			DROP FUNCTION IF EXISTS fail_session_revoke()`)
	})

	rec := caller.do(http.MethodPost, "/api/auth/password/set",
		map[string]string{"password": "a brand new password"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())

	user, err := h.repo.GetUser(context.Background(), authctx.UserID(1))
	require.NoError(t, err)
	assert.False(t, user.HasPassword, "password committed without its required session revocation")
}

func TestSetPassword_RefusesTOTPProofFromAStaleCredentialEpoch(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{TwoFactor: true})
	h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	e := enrolUser(t, h, "admin@example.com", "correct horse battery")
	g.as("google-sub", "admin@example.com", true)
	testdb.ConvertToGoogleOnly(t, h.pool, authctx.UserID(1), "admin@example.com", "google-sub")

	user, err := h.repo.GetUser(context.Background(), authctx.UserID(1))
	require.NoError(t, err)
	row, err := h.repo.LoadTOTPSecret(context.Background(), user.ID)
	require.NoError(t, err)
	require.NotNil(t, row.LastUsedCounter)
	var sid int64
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT id FROM session WHERE access_token_hash = $1`,
		secrets.Hash(e.client.cookies[auth.CookieAccess])).Scan(&sid))

	require.NoError(t, h.repo.RevokeAllForUser(context.Background(), user.ID, auth.ReasonLogoutAll))
	err = h.repo.SetPassword(context.Background(), user.ID, sid, user.TokenVersion,
		"a brand new password", auth.SecondFactorProof{
			Method: auth.MethodTOTP,
			TOTP: &auth.TOTPProof{
				Counter: *row.LastUsedCounter + 1, Ciphertext: row.Ciphertext, Nonce: row.Nonce,
			},
		})
	assert.ErrorIs(t, err, auth.ErrSessionInvalid)

	current, err := h.repo.GetUser(context.Background(), user.ID)
	require.NoError(t, err)
	assert.False(t, current.HasPassword)
}

// ─────────────────────────────────────────────────────────────────────
// Error surfacing
// ─────────────────────────────────────────────────────────────────────

// Every repository method in the OAuth and API-token layers wraps its database
// errors, and none of those branches is reachable while the database is
// healthy. Closing the pool exercises all of them at once — and what it proves
// is worth having: each one must return an ERROR, never a zero value a caller
// could mistake for "no rows" and treat as success.
//
// That distinction is not academic here. UserByIdentity returning (User{}, nil)
// on a failed query would read as "no account linked to this subject", and the
// callback would walk on to the conversion branch for an account it never
// actually looked up.
func TestOAuthRepository_SurfacesDatabaseErrors(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	ctx := context.Background()
	uid := authctx.UserID(1)
	h.pool.Close() // every subsequent query fails

	t.Run("redirect state", func(t *testing.T) {
		_, err := h.repo.CreateOAuthState(ctx, auth.ProviderGoogle, auth.OAuthPurposeLogin,
			"verifier", nil, nil, time.Minute)
		assert.Error(t, err)
		_, err = h.repo.ConsumeOAuthState(ctx, "whatever")
		assert.Error(t, err)
		_, err = h.repo.SweepOAuthState(ctx, time.Hour)
		assert.Error(t, err)
	})

	t.Run("identities", func(t *testing.T) {
		_, err := h.repo.UserByIdentity(ctx, auth.ProviderGoogle, "sub")
		assert.Error(t, err)
		assert.NotErrorIs(t, err, auth.ErrNoUser,
			"a dead database must not look like an unlinked subject")

		_, err = h.repo.UserByEmail(ctx, "admin@example.com")
		assert.Error(t, err)
		assert.NotErrorIs(t, err, auth.ErrNoUser)

		_, err = h.repo.ListIdentities(ctx, uid)
		assert.Error(t, err)
		sid, version, proofAt := int64(1), 0, time.Now()
		state := auth.OAuthState{
			UserID: &uid, SessionID: &sid, TokenVersion: &version, ProofAt: &proofAt,
		}
		assert.Error(t, h.repo.ValidateOAuthLinkProof(ctx, state, "access", time.Minute))
		assert.Error(t, h.repo.LinkIdentity(ctx, state, "access", auth.ProviderGoogle,
			"sub", "a@b.test", time.Minute))
		_, _, err = h.repo.ConvertToProvider(ctx, 1, "password")
		assert.Error(t, err)
		assert.Error(t, h.repo.UnlinkIdentity(ctx, uid, 1, 0, auth.ProviderGoogle, "password"))
		// TouchIdentity is deliberately silent — a failed "last used" write must
		// never fail the login it belongs to — so the only thing to assert is
		// that it does not panic.
		assert.NotPanics(t, func() { h.repo.TouchIdentity(ctx, auth.ProviderGoogle, "sub") })
	})

	t.Run("api tokens", func(t *testing.T) {
		_, err := h.repo.CreateAPIToken(ctx, uid, "name", 0)
		assert.Error(t, err)
		_, err = h.repo.ResolveAPIToken(ctx, "fx_1_secret")
		assert.Error(t, err)
		assert.NotErrorIs(t, err, auth.ErrTokenInvalid,
			"a dead database must not look like a bad token")
		_, err = h.repo.ListAPITokens(ctx, uid)
		assert.Error(t, err)
		assert.Error(t, h.repo.RevokeAPIToken(ctx, uid, 1))
		_, err = h.repo.SweepAPITokens(ctx, time.Hour)
		assert.Error(t, err)
		assert.NotPanics(t, func() { h.repo.TouchAPIToken(ctx, 1) })
	})
}

// A malformed bearer value never reaches the database at all: parseAPIToken
// rejects it first. That matters because ResolveAPIToken is on the hot path of
// every extension request, and a garbage header must not cost a round-trip.
func TestResolveAPIToken_MalformedValueSkipsTheDatabase(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	h.pool.Close()

	_, err := h.repo.ResolveAPIToken(context.Background(), "not-a-token")
	assert.ErrorIs(t, err, auth.ErrTokenInvalid,
		"a malformed value must be refused without querying")
}

// ─────────────────────────────────────────────────────────────────────
// Refusals on the way through the callback
// ─────────────────────────────────────────────────────────────────────

// An invitation revoked while the user was on Google's consent screen must not
// be claimable when they come back. The window is small but real, and the
// liveness predicates live in the same statement that locates the invite
// precisely so an id-located claim cannot skip them.
func TestOAuth_InviteRevokedDuringTheRoundTripIsRefused(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	token := h.createInvite(t, admin, "invited@example.com")

	c := h.client(t)
	state := c.startOAuthInvite(t, token)

	// The admin changes their mind while the browser is at Google.
	rec := admin.do(http.MethodGet, "/api/admin/invites", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	id := int64(decode(t, rec)["invites"].([]any)[0].(map[string]any)["id"].(float64))
	require.Equal(t, http.StatusNoContent,
		admin.do(http.MethodDelete, "/api/admin/invites/"+itoa(id), nil).Code)

	g.as("invited-sub", "invited@example.com", true)
	_, failure := c.callback(t, state, "code")
	assert.Equal(t, "invite_invalid", failure)

	var n int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM app_user`).Scan(&n))
	assert.Equal(t, 1, n, "a revoked invitation created an account")
}

// A Google account already linked elsewhere cannot also claim an invitation:
// one provider account maps to at most one foldex user, and the unique index
// is what enforces it no matter which path gets there.
func TestOAuth_InviteWithAnAlreadyLinkedSubjectIsRefused(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	g.as("shared-sub", "admin@example.com", true)
	requireLinkRoundTrip(t, admin, "correct horse battery", "", "linked")

	token := h.createInvite(t, admin, "invited@example.com")
	g.as("shared-sub", "invited@example.com", true)

	c := h.client(t)
	state := c.startOAuthInvite(t, token)
	_, failure := c.callback(t, state, "code")
	assert.Equal(t, "already_linked", failure)
}

func TestOAuthStart_InviteTokenMustBeLive(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	rec := h.client(t).do(http.MethodPost,
		"/api/auth/oauth/google/invite/start", map[string]string{"invite": "nonsense"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "invite_invalid", errCode(t, rec))
}

// Google can redirect back with a state but no code — a truncated URL, a user
// editing the address bar. It must land on the same opaque failure a bad state
// gets rather than reaching the exchange with an empty code.
func TestOAuthCallback_MissingCodeIsRefused(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	c := h.client(t)
	state := c.startOAuth(t, "purpose=login")

	rec := c.do(http.MethodGet, "/api/auth/oauth/google/callback?state="+url.QueryEscape(state), nil)
	require.Equal(t, http.StatusFound, rec.Code)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	assert.Equal(t, "state_invalid", loc.Query().Get("oauth_error"))
}

// Any provider error that is not access_denied is one opaque code. Telling a
// caller which of Google's failure modes fired helps nobody but a prober.
func TestOAuthCallback_OtherProviderErrorsAreOpaque(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	c := h.client(t)
	c.startOAuth(t, "purpose=login")

	rec := c.do(http.MethodGet, "/api/auth/oauth/google/callback?error=server_error", nil)
	require.Equal(t, http.StatusFound, rec.Code)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	assert.Equal(t, "provider_error", loc.Query().Get("oauth_error"))
}

func TestOAuthCallback_UserInfoFailureIsOpaque(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	g.infoErr = errors.New("boom")

	_, failure := h.client(t).googleRoundTrip(t, "login")
	assert.Equal(t, "provider_error", failure)
}

// The whole OAuth surface answers the same way when no client is configured,
// including the endpoints reached mid-flow.
func TestOAuth_ConvertAndCallbackRefuseWhenTheProviderIsOff(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	g.enabled = false

	rec := h.client(t).do(http.MethodPost, "/api/auth/oauth/google/convert",
		map[string]string{"password": "whatever"})
	assert.Equal(t, http.StatusNotImplemented, rec.Code)
	assert.Equal(t, "oauth_disabled", errCode(t, rec))

	rec = h.client(t).do(http.MethodGet, "/api/auth/oauth/google/callback?state=x&code=y", nil)
	require.Equal(t, http.StatusFound, rec.Code)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	assert.Equal(t, "oauth_disabled", loc.Query().Get("oauth_error"))
}

// Converting without a live challenge is not a 500 and not a silent no-op: the
// caller holds no proof of anything, and the answer has to say so.
func TestOAuth_ConvertWithoutAChallengeIsRefused(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	rec := h.client(t).do(http.MethodPost, "/api/auth/oauth/google/convert",
		map[string]string{"password": "correct horse battery"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "challenge_invalid", errCode(t, rec))
}

// A pre-auth cookie from the SECOND-FACTOR flow must not drive a conversion —
// they are different steps with different proofs.
func TestOAuth_ConvertRefusesATwoFactorChallenge(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{TwoFactor: true})
	h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	enrolUser(t, h, "admin@example.com", "correct horse battery")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "correct horse battery"}).Code)
	require.NotEmpty(t, c.cookies[auth.CookiePreAuth])

	rec := c.do(http.MethodPost, "/api/auth/oauth/google/convert",
		map[string]string{"password": "correct horse battery"})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "wrong_challenge", errCode(t, rec))
}

func TestAdmin_ForcePasswordResetOnAnUnknownUserIs404(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{SMTP: true})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	assert.Equal(t, http.StatusNotFound,
		admin.do(http.MethodPost, "/api/admin/users/9999/force-password-reset", nil).Code)
	assert.Equal(t, http.StatusBadRequest,
		admin.do(http.MethodPost, "/api/admin/users/not-a-number/force-password-reset", nil).Code)
}

// Every handler in the OAuth and token layers wraps its database errors and
// writes a generic envelope. None of those branches is reachable by closing the
// pool — the session middleware resolves first and answers 401, so the handler
// body never runs — so the only way in is to remove the tables the handler
// itself touches while leaving app_user and session intact.
//
// What it proves is CLAUDE.md §7's rule: a pgx error never reaches a client. A
// 500 is the right answer; a driver string naming a column is not.
func TestOAuthAndTokenHandlers_DegradeWithoutLeakingDriverText(t *testing.T) {
	// A dedicated container: this destroys schema, and the damage outlives the
	// test that did it.
	h := newHarnessWith(t, testdb.New(t), harnessOpts{Google: &fakeGoogle{enabled: true}})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	invite := h.createInvite(t, admin, "invited@example.com")

	_, err := h.pool.Exec(context.Background(),
		`DROP TABLE api_token, user_identity, oauth_state CASCADE`)
	require.NoError(t, err)

	for _, tc := range []struct {
		name, method, path string
		body               any
	}{
		{"list tokens", http.MethodGet, "/api/auth/tokens", nil},
		{"create token", http.MethodPost, "/api/auth/tokens", map[string]any{"name": "x"}},
		{"revoke token", http.MethodDelete, "/api/auth/tokens/1", nil},
		{"list identities", http.MethodGet, "/api/auth/identities", nil},
		{"unlink", http.MethodDelete, "/api/auth/oauth/google",
			map[string]string{"password": "correct horse battery"}},
		{"start", http.MethodGet, "/api/auth/oauth/google/start?purpose=login", nil},
		{"invite start", http.MethodPost, "/api/auth/oauth/google/invite/start",
			map[string]string{"invite": invite}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := admin.do(tc.method, tc.path, tc.body)
			assert.GreaterOrEqual(t, rec.Code, 400, "expected a failure, got %d", rec.Code)
			body := rec.Body.String()
			for _, leak := range []string{"pgx", "SQLSTATE", "relation", "user_identity", "api_token"} {
				assert.NotContains(t, body, leak, "driver detail leaked to the client")
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────
// The credential-coherence trigger (mig 000021)
// ─────────────────────────────────────────────────────────────────────

// TestOAuth_GoogleOnlyAccountCannotUnlinkUntilItSetsAPassword covers the
// APPLICATION guard — UnlinkIdentity re-checks and answers 409. This covers the
// DATABASE one, which exists precisely because that discipline can fail: the
// trigger is what makes "an active account always holds at least one
// credential" true of any writer, including a future handler, a migration, or
// an operator at psql.
//
// Written against SQL rather than the API on purpose. Every route into this
// state is already refused upstream, so a test that went through handlers could
// only prove the handlers say no — and would keep passing if the trigger were
// dropped tomorrow.
func TestInvariant_NoActiveUserEndsUpWithoutAnyCredential(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	t.Run("dropping the password of an account with no identity is refused", func(t *testing.T) {
		uid := testdb.SeedUserWithPassword(t, h.pool, "pw-only@example.com", "a good password", "editor")
		_, err := h.pool.Exec(ctx,
			`UPDATE app_user SET password_hash = NULL WHERE id = $1`, int64(uid))
		require.Error(t, err, "the last credential was removed and the database allowed it")
		assert.Contains(t, err.Error(), "no way to sign in")
	})

	t.Run("deleting the last identity of a Google-only account is refused", func(t *testing.T) {
		uid := testdb.SeedUserWithPassword(t, h.pool, "google-only@example.com", "a good password", "editor")
		testdb.ConvertToGoogleOnly(t, h.pool, uid, "google-only@example.com", "sub-lockout")

		_, err := h.pool.Exec(ctx, `DELETE FROM user_identity WHERE user_id = $1`, int64(uid))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no way to sign in")
	})

	// DEFERRABLE is not a detail: the conversion legitimately violates the rule
	// mid-transaction — nulling the password and inserting the identity are two
	// statements, and one of them has to go first. What must hold is the state
	// at COMMIT. An immediate trigger would make the supported flow impossible.
	t.Run("swapping one credential for another inside a transaction commits", func(t *testing.T) {
		uid := testdb.SeedUserWithPassword(t, h.pool, "swap@example.com", "a good password", "editor")

		tx, err := h.pool.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(ctx) }()

		_, err = tx.Exec(ctx, `UPDATE app_user SET password_hash = NULL WHERE id = $1`, int64(uid))
		require.NoError(t, err, "the check must not fire mid-transaction")
		_, err = tx.Exec(ctx,
			`INSERT INTO user_identity (user_id, provider, subject, email_at_link) VALUES ($1, 'google', $2, $3)`,
			int64(uid), "sub-swap", "swap@example.com")
		require.NoError(t, err)
		require.NoError(t, tx.Commit(ctx), "the state at COMMIT is coherent and must be accepted")
	})

	// pending and disabled are outside the rule deliberately: the bootstrap
	// placeholder ships pending with no password at all, and that row is what
	// the setup screen claims. Including them would make a fresh install
	// unbootable.
	t.Run("a non-active account may hold no credential", func(t *testing.T) {
		uid := testdb.SeedUserWithPassword(t, h.pool, "pending@example.com", "a good password", "editor")
		_, err := h.pool.Exec(ctx,
			`UPDATE app_user SET status = 'disabled', password_hash = NULL WHERE id = $1`, int64(uid))
		require.NoError(t, err, "a disabled account with no credential is a legitimate state")
	})
}
