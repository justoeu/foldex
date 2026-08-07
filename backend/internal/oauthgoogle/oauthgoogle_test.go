package oauthgoogle

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func configured(t *testing.T, h http.Handler) (*Provider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	p := New(Config{
		ClientID:     "cid",
		ClientSecret: "csecret",
		RedirectURL:  "https://foldex.test/api/auth/oauth/google/callback",
		HTTPClient:   srv.Client(),
	})
	p.tokenEndpoint = srv.URL + "/token"
	p.userInfoEndpoint = srv.URL + "/userinfo"
	p.authEndpoint = srv.URL + "/auth"
	return p, srv
}

func TestEnabled_RequiresAllThreeValues(t *testing.T) {
	full := Config{ClientID: "a", ClientSecret: "b", RedirectURL: "c"}
	assert.True(t, New(full).Enabled())

	// Each one missing on its own disables it. A client id without a redirect
	// URL would otherwise build a redirect Google refuses, and the operator
	// sees an opaque Google error page instead of "OAuth is off".
	for _, drop := range []func(*Config){
		func(c *Config) { c.ClientID = "" },
		func(c *Config) { c.ClientSecret = "" },
		func(c *Config) { c.RedirectURL = "" },
	} {
		cfg := full
		drop(&cfg)
		assert.False(t, New(cfg).Enabled())
	}
}

// Whitespace-only values are the realistic .env accident: a trailing space on
// a key that was pasted, or a quoted empty string.
func TestEnabled_TreatsWhitespaceAsEmpty(t *testing.T) {
	assert.False(t, New(Config{ClientID: "  ", ClientSecret: "b", RedirectURL: "c"}).Enabled())
}

func TestDisabledProviderRefusesEveryCall(t *testing.T) {
	p := New(Config{})
	_, err := p.AuthCodeURL("state", "challenge")
	assert.ErrorIs(t, err, ErrDisabled)
	_, err = p.Exchange(context.Background(), "code", "verifier")
	assert.ErrorIs(t, err, ErrDisabled)
	_, err = p.UserInfo(context.Background(), "token")
	assert.ErrorIs(t, err, ErrDisabled)
}

// ─────────────────────────────────────────────────────────────────────
// PKCE
// ─────────────────────────────────────────────────────────────────────

func TestNewPKCE_ChallengeIsS256OfTheVerifier(t *testing.T) {
	p, err := NewPKCE()
	require.NoError(t, err)

	sum := sha256.Sum256([]byte(p.Verifier))
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(sum[:]), p.Challenge)
}

// RFC 7636 §4.1 puts the verifier between 43 and 128 characters. Shorter and
// the challenge is grindable; longer and Google rejects the exchange.
func TestNewPKCE_VerifierIsWithinTheSpecLength(t *testing.T) {
	p, err := NewPKCE()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(p.Verifier), 43)
	assert.LessOrEqual(t, len(p.Verifier), 128)
	// base64url alphabet only — a '+' or '/' would need escaping in the form
	// body and in the redirect.
	assert.NotContains(t, p.Verifier, "+")
	assert.NotContains(t, p.Verifier, "/")
	assert.NotContains(t, p.Verifier, "=")
}

func TestNewPKCE_IsDifferentEveryTime(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		p, err := NewPKCE()
		require.NoError(t, err)
		require.False(t, seen[p.Verifier], "verifier repeated")
		seen[p.Verifier] = true
	}
}

// ─────────────────────────────────────────────────────────────────────
// The redirect
// ─────────────────────────────────────────────────────────────────────

func TestAuthCodeURL_CarriesEverythingGoogleNeeds(t *testing.T) {
	p, _ := configured(t, http.NotFoundHandler())

	raw, err := p.AuthCodeURL("the-state", "the-challenge")
	require.NoError(t, err)

	u, err := url.Parse(raw)
	require.NoError(t, err)
	q := u.Query()
	assert.Equal(t, "cid", q.Get("client_id"))
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, "the-state", q.Get("state"))
	assert.Equal(t, "the-challenge", q.Get("code_challenge"))
	assert.Equal(t, "https://foldex.test/api/auth/oauth/google/callback", q.Get("redirect_uri"))
	assert.Contains(t, q.Get("scope"), "openid")
	assert.Contains(t, q.Get("scope"), "email")
}

// `plain` puts the verifier itself in a URL another app on the device can
// observe, which defeats the whole point of PKCE.
func TestAuthCodeURL_OnlyEverAsksForS256(t *testing.T) {
	p, _ := configured(t, http.NotFoundHandler())
	raw, err := p.AuthCodeURL("s", "c")
	require.NoError(t, err)

	assert.Equal(t, "S256", mustQuery(t, raw).Get("code_challenge_method"))
}

// Without select_account, a browser already signed into one Google account
// completes the flow silently — so a user linking a second identity, or one on
// a shared machine, lands in the wrong account with no decision point.
func TestAuthCodeURL_AlwaysAsksWhichAccount(t *testing.T) {
	p, _ := configured(t, http.NotFoundHandler())
	raw, err := p.AuthCodeURL("s", "c")
	require.NoError(t, err)
	assert.Equal(t, "select_account", mustQuery(t, raw).Get("prompt"))
}

// Requesting offline access would hand foldex a Google refresh token it has no
// use for — and a database dump would then be standing access to a mailbox.
func TestAuthCodeURL_NeverRequestsOfflineAccess(t *testing.T) {
	p, _ := configured(t, http.NotFoundHandler())
	raw, err := p.AuthCodeURL("s", "c")
	require.NoError(t, err)
	assert.Equal(t, "online", mustQuery(t, raw).Get("access_type"))
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u.Query()
}

// ─────────────────────────────────────────────────────────────────────
// Exchange
// ─────────────────────────────────────────────────────────────────────

func TestExchange_SendsTheVerifierAndReturnsTheToken(t *testing.T) {
	var got url.Values
	p, _ := configured(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		got = r.PostForm
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at-123", "token_type": "Bearer"})
	}))

	tok, err := p.Exchange(context.Background(), "the-code", "the-verifier")
	require.NoError(t, err)
	assert.Equal(t, "at-123", tok)

	assert.Equal(t, "the-code", got.Get("code"))
	assert.Equal(t, "the-verifier", got.Get("code_verifier"))
	assert.Equal(t, "authorization_code", got.Get("grant_type"))
	assert.Equal(t, "csecret", got.Get("client_secret"))
}

func TestExchange_FailsOnNon2xx(t *testing.T) {
	p, _ := configured(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))

	_, err := p.Exchange(context.Background(), "c", "v")
	assert.ErrorIs(t, err, ErrProvider)
}

// The error is logged, and a provider error body can echo request parameters
// back — including the client secret this very request sent.
func TestExchange_ErrorNeverCarriesTheResponseBody(t *testing.T) {
	p, _ := configured(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad","client_secret":"csecret"}`))
	}))

	_, err := p.Exchange(context.Background(), "c", "v")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "csecret")
}

// A 200 with no token is not success. Treating it as one would carry an empty
// bearer into UserInfo and produce a confusing 401 one layer away from the
// real cause.
func TestExchange_RejectsAnEmptyToken(t *testing.T) {
	p, _ := configured(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"token_type":"Bearer"}`))
	}))

	_, err := p.Exchange(context.Background(), "c", "v")
	assert.ErrorIs(t, err, ErrProvider)
}

func TestExchange_RejectsMalformedJSON(t *testing.T) {
	p, _ := configured(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))

	_, err := p.Exchange(context.Background(), "c", "v")
	assert.ErrorIs(t, err, ErrProvider)
}

func TestExchange_HonoursContextCancellation(t *testing.T) {
	p, _ := configured(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"x"}`))
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Exchange(ctx, "c", "v")
	assert.ErrorIs(t, err, ErrProvider)
}

// ─────────────────────────────────────────────────────────────────────
// UserInfo
// ─────────────────────────────────────────────────────────────────────

func TestUserInfo_ReadsTheProfileWithABearerToken(t *testing.T) {
	var auth string
	p, _ := configured(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sub": "google-sub-1", "email": "a@b.test", "email_verified": true, "name": "A",
		})
	}))

	info, err := p.UserInfo(context.Background(), "at-123")
	require.NoError(t, err)
	assert.Equal(t, "Bearer at-123", auth)
	assert.Equal(t, "google-sub-1", info.Subject)
	assert.Equal(t, "a@b.test", info.Email)
	assert.True(t, info.EmailVerified)
	assert.Equal(t, "A", info.Name)
}

// sub is the login key. A profile without one cannot be linked to anything, and
// accepting it would leave the caller to notice an empty string later.
func TestUserInfo_RejectsAProfileWithoutASubject(t *testing.T) {
	p, _ := configured(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"email":"a@b.test","email_verified":true}`))
	}))

	_, err := p.UserInfo(context.Background(), "at")
	assert.ErrorIs(t, err, ErrProvider)
}

func TestUserInfo_RejectsAProfileWithoutAnEmail(t *testing.T) {
	p, _ := configured(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"sub":"s","email_verified":true}`))
	}))

	_, err := p.UserInfo(context.Background(), "at")
	assert.ErrorIs(t, err, ErrProvider)
}

func TestUserInfo_FailsOnNon2xx(t *testing.T) {
	p, _ := configured(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))

	_, err := p.UserInfo(context.Background(), "at")
	assert.ErrorIs(t, err, ErrProvider)
}

// A provider that streams forever must not be able to exhaust this process's
// memory. The cap is what makes that a property of our code rather than of
// Google's good behaviour.
func TestUserInfo_CapsHowMuchIsRead(t *testing.T) {
	p, _ := configured(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Valid JSON prefix, then far more padding than the cap allows. The
		// truncated read cannot parse, so this surfaces as a provider error
		// rather than as a successful giant allocation.
		_, _ = w.Write([]byte(`{"sub":"s","email":"a@b.test","name":"`))
		_, _ = w.Write([]byte(strings.Repeat("x", maxBodyBytes+1024)))
		_, _ = w.Write([]byte(`"}`))
	}))

	_, err := p.UserInfo(context.Background(), "at")
	assert.ErrorIs(t, err, ErrProvider)
}

// ─────────────────────────────────────────────────────────────────────
// email_verified — the field that gates account conversion
// ─────────────────────────────────────────────────────────────────────

// Some providers render this as a string. Accepting both shapes is fine; what
// must never happen is a shape that is not a real `true` decoding as verified.
func TestEmailVerified_OnlyARealTrueCounts(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`true`, true},
		{`"true"`, true},
		{`false`, false},
		{`"false"`, false},
		{`null`, false},
		{`0`, false},
		{`1`, false},
		{`"yes"`, false},
		{`"TRUE"`, false},
		{`{}`, false},
	} {
		var info UserInfo
		body := `{"sub":"s","email":"a@b.test","email_verified":` + tc.raw + `}`
		require.NoError(t, json.Unmarshal([]byte(body), &info), tc.raw)
		assert.Equal(t, tc.want, info.EmailVerified, "input %s", tc.raw)
	}
}

// An absent field must decode to false, not to the zero value of some other
// interpretation. This is the shape a provider that simply does not send the
// claim produces.
func TestEmailVerified_AbsentFieldIsNotVerified(t *testing.T) {
	var info UserInfo
	require.NoError(t, json.Unmarshal([]byte(`{"sub":"s","email":"a@b.test"}`), &info))
	assert.False(t, info.EmailVerified)
}
