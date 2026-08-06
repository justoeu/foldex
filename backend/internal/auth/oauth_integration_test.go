//go:build integration

package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/auth"
	"foldex/internal/oauthgoogle"
	"foldex/internal/pkg/authctx"
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
}

func (f *fakeGoogle) Enabled() bool { return f.enabled }

func (f *fakeGoogle) AuthCodeURL(state, challenge string) (string, error) {
	if !f.enabled {
		return "", oauthgoogle.ErrDisabled
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

// Linking binds the account the SESSION proves, captured at START time.
// Reading it at callback time would bind whatever session happens to exist when
// Google redirects back — and the redirect's timing is attacker-controlled.
func TestOAuthStart_LinkWithoutASessionIsRefused(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})

	rec := h.client(t).do(http.MethodGet, "/api/auth/oauth/google/start?purpose=link", nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestOAuthStart_RejectsAnUnknownPurpose(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	rec := h.client(t).do(http.MethodGet, "/api/auth/oauth/google/start?purpose=whatever", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_purpose", errCode(t, rec))
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

	outcome, failure := admin.googleRoundTrip(t, "link")
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
	requireRoundTrip(t, admin, "link", "linked")

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
	requireRoundTrip(t, admin, "link", "linked")

	second := h.login(t, "other@example.com", "another good password")
	_, failure := second.googleRoundTrip(t, "link")
	assert.Equal(t, "already_linked", failure)
}

// Linking does NOT require the addresses to match: a personal Gmail on a work
// account is legitimate, precisely because the session already proved
// possession. That is also why linking without a session can never be allowed.
func TestOAuth_LinkAllowsADifferentAddress(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{})
	admin := h.bootstrapAdmin(t, "work@example.com", "correct horse battery")
	g.as("personal-sub", "personal@gmail.test", true)

	outcome, failure := admin.googleRoundTrip(t, "link")
	assert.Equal(t, "linked", outcome, "failure: %s", failure)
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
	state := c.startOAuth(t, "purpose=accept_invite&invite="+url.QueryEscape(token))
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
	state := c.startOAuth(t, "purpose=accept_invite&invite="+url.QueryEscape(token))
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
	state := c.startOAuth(t, "purpose=accept_invite&invite="+url.QueryEscape(token))
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
	h, g := newGoogleHarness(t, harnessOpts{})
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

	rec := admin.do(http.MethodPost, "/api/admin/users/"+itoa(uid)+"/force-password-reset", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	temp, _ := decode(t, rec)["temporary_password"].(string)
	require.NotEmpty(t, temp)

	// The temporary password actually signs in.
	fresh := h.client(t)
	login := fresh.do(http.MethodPost, "/api/auth/login",
		map[string]string{"email": "user@example.com", "password": temp})
	assert.Equal(t, http.StatusOK, login.Code, login.Body.String())
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

// The password is handed over out of band. Mailing it would put a working
// credential in an inbox — possibly the very channel the account lost.
func TestAdmin_ForcePasswordResetNeverMailsThePassword(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{SMTP: true})
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	uid := h.inviteAndAccept(t, admin, "user@example.com", "the user's password")
	h.mail.reset()

	rec := admin.do(http.MethodPost, "/api/admin/users/"+itoa(int64(uid))+"/force-password-reset", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	temp := decode(t, rec)["temporary_password"].(string)

	msg := h.mail.waitFor(t, "user@example.com")
	assert.NotContains(t, msg.Text, temp)
	assert.NotContains(t, msg.HTML, temp)
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

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// createInvite issues an invitation and returns the raw token from its URL.
func (h *harness) createInvite(t *testing.T, admin *client, email string) string {
	t.Helper()
	rec := admin.do(http.MethodPost, "/api/admin/invites",
		map[string]string{"email": email, "role": "user"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	acceptURL, _ := decode(t, rec)["accept_url"].(string)
	require.Contains(t, acceptURL, "?invite=")
	return strings.SplitN(acceptURL, "?invite=", 2)[1]
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
// bucket on each call — CommitSuccess deletes the entry outright — would leave
// the cap decorative, and nothing else bounds that table between sweeps.
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
	requireRoundTrip(t, admin, "link", "linked")

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
	requireRoundTrip(t, admin, "link", "linked")

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
	requireRoundTrip(t, admin, "link", "linked")

	rec := admin.do(http.MethodDelete, "/api/auth/oauth/google",
		map[string]string{"password": "not the password"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "invalid_credentials", errCode(t, rec))

	var n int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM user_identity`).Scan(&n))
	assert.Equal(t, 1, n, "the identity must survive a wrong password")
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
		assert.Error(t, h.repo.LinkIdentity(ctx, uid, auth.ProviderGoogle, "sub", "a@b.test"))
		assert.Error(t, h.repo.ConvertToProvider(ctx, uid, auth.ProviderGoogle, "sub", "a@b.test", 1))
		assert.Error(t, h.repo.UnlinkIdentity(ctx, uid, auth.ProviderGoogle))
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
	state := c.startOAuth(t, "purpose=accept_invite&invite="+url.QueryEscape(token))

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
	requireRoundTrip(t, admin, "link", "linked")

	token := h.createInvite(t, admin, "invited@example.com")
	g.as("shared-sub", "invited@example.com", true)

	c := h.client(t)
	state := c.startOAuth(t, "purpose=accept_invite&invite="+url.QueryEscape(token))
	_, failure := c.callback(t, state, "code")
	assert.Equal(t, "already_linked", failure)
}

func TestOAuthStart_InviteTokenMustBeLive(t *testing.T) {
	h, _ := newGoogleHarness(t, harnessOpts{})
	h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	rec := h.client(t).do(http.MethodGet,
		"/api/auth/oauth/google/start?purpose=accept_invite&invite=nonsense", nil)
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
	h, _ := newGoogleHarness(t, harnessOpts{})
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
