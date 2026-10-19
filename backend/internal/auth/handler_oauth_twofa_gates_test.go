package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/oauthgoogle"
	"foldex/internal/pkg/attemptlimit"
	"foldex/internal/testsupport"
)

// The gate branches of the OAuth and 2FA handlers — disabled provider,
// purpose routing, the challenge-error vocabulary — answer before any
// credential is checked, so a closed pool plus a fake provider reaches all
// of them. The integration suite drives the happy round-trips against a
// live database and never sees these refusals.

type fakeGoogle struct{ enabled bool }

func (f fakeGoogle) Enabled() bool { return f.enabled }
func (f fakeGoogle) AuthCodeURL(state, _ string) (string, error) {
	return "https://accounts.example.test/o/oauth2/auth?state=" + state, nil
}
func (f fakeGoogle) Exchange(context.Context, string, string) (string, error) {
	return "", errors.New("no exchange in gate tests")
}
func (f fakeGoogle) UserInfo(context.Context, string) (oauthgoogle.UserInfo, error) {
	return oauthgoogle.UserInfo{}, errors.New("no userinfo in gate tests")
}

func oauthHandler(t *testing.T, google GoogleProvider) *Handler {
	t.Helper()
	return &Handler{
		repo:               NewRepository(testsupport.ClosedPool(t)),
		logger:             slog.New(slog.DiscardHandler),
		google:             google,
		oauthIP:            attemptlimit.New(30, time.Hour),
		stepUpPasswordUser: attemptlimit.New(5, 15*time.Minute),
	}
}

func TestOAuthStartGates(t *testing.T) {
	t.Run("disabled provider is 501, not a redirect", func(t *testing.T) {
		rec := httptest.NewRecorder()
		oauthHandler(t, nil).OAuthStart(rec, httptest.NewRequest(http.MethodGet, "/api/auth/oauth/start", nil))
		assert.Equal(t, http.StatusNotImplemented, rec.Code)
	})

	h := oauthHandler(t, fakeGoogle{enabled: true})
	cases := map[string]struct {
		target string
		want   int
	}{
		"link purpose must start from the step-up POST": {"/x?purpose=link", http.StatusMethodNotAllowed},
		"invite purpose must start from a POST body":    {"/x?purpose=accept_invite", http.StatusMethodNotAllowed},
		"unknown purpose is 400":                        {"/x?purpose=bogus", http.StatusBadRequest},
		"login purpose reaches state creation (500)":    {"/x?purpose=login", http.StatusInternalServerError},
		"absent purpose defaults to login (500)":        {"/x", http.StatusInternalServerError},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, tc.target, nil)
			r.RemoteAddr = "192.0.2.1:1234"
			h.OAuthStart(rec, r)
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

func TestOAuthInviteAndLinkStartGates(t *testing.T) {
	t.Run("invite: disabled provider is 501", func(t *testing.T) {
		rec := httptest.NewRecorder()
		oauthHandler(t, fakeGoogle{}).OAuthInviteStart(rec,
			httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"invite":"t"}`)))
		assert.Equal(t, http.StatusNotImplemented, rec.Code)
	})

	h := oauthHandler(t, fakeGoogle{enabled: true})

	t.Run("invite: malformed JSON is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.OAuthInviteStart(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{oops")))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("invite: lookup failure is the indistinguishable 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"invite":"raw-token"}`))
		r.RemoteAddr = "192.0.2.1:1234"
		h.OAuthInviteStart(rec, r)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("link: disabled provider is 501", func(t *testing.T) {
		rec := httptest.NewRecorder()
		oauthHandler(t, nil).OAuthLinkStart(rec, principalReq(http.MethodPost, "/x", `{}`))
		assert.Equal(t, http.StatusNotImplemented, rec.Code)
	})

	t.Run("link: malformed JSON is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.OAuthLinkStart(rec, principalReq(http.MethodPost, "/x", "{oops"))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("link: password verification failure is 500", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.OAuthLinkStart(rec, principalReq(http.MethodPost, "/x",
			`{"current_password":"correct horse battery staple","code":"123456"}`))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}

// The challenge-error vocabulary is one mapping shared by every factor
// endpoint (writeFactorTxnError delegates here): the statuses AND which
// ones destroy the pre-auth cookie are security semantics, not cosmetics —
// clearing on a wrong-purpose challenge would kill a live sign-in attempt.
func TestWriteChallengeErrorVocabulary(t *testing.T) {
	h := oauthHandler(t, nil)
	cases := map[string]struct {
		err         error
		want        int
		wantCleared bool
	}{
		"wrong purpose keeps the challenge alive": {ErrChallengeWrongPurpose, http.StatusConflict, false},
		"exhausted is 429 and clears":             {ErrChallengeExhausted, http.StatusTooManyRequests, true},
		"invalid is 401 and clears":               {ErrChallengeInvalid, http.StatusUnauthorized, true},
		"unexpected failure is 500 and keeps":     {errors.New("db on fire"), http.StatusInternalServerError, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.writeChallengeError(rec, tc.err)
			assert.Equal(t, tc.want, rec.Code)
			cleared := false
			for _, c := range rec.Result().Cookies() {
				if c.Name == CookiePreAuth && c.MaxAge < 0 {
					cleared = true
				}
			}
			assert.Equal(t, tc.wantCleared, cleared, "pre-auth cookie clearing")
		})
	}
}

func TestWriteFactorTxnErrorMapping(t *testing.T) {
	h := oauthHandler(t, nil)

	t.Run("nil writes nothing and reports false", func(t *testing.T) {
		rec := httptest.NewRecorder()
		assert.False(t, h.writeFactorTxnError(rec, nil, "ctx"))
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	cases := map[string]struct {
		err  error
		want int
	}{
		"challenge invalid delegates": {ErrChallengeInvalid, http.StatusUnauthorized},
		"session invalid delegates":   {ErrSessionInvalid, http.StatusUnauthorized},
		"no pending factor is 409":    {ErrNoPendingFactor, http.StatusConflict},
		"unexpected failure is 500":   {errors.New("db on fire"), http.StatusInternalServerError},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			require.True(t, h.writeFactorTxnError(rec, tc.err, "factor ctx"))
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

func TestTwoFactorGatesWithoutChallenge(t *testing.T) {
	h := oauthHandler(t, nil)

	// No fx_pa cookie: ResolveChallenge refuses before any code is looked
	// at, and the refusal is the 401 challenge_invalid (plus cookie clear),
	// never a 500 leak about the store.
	t.Run("verify without a challenge cookie is 401", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.Verify2FA(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"code":"123456"}`)))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("status with a dead store is 500", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.TwoFactorStatus(rec, principalReq(http.MethodGet, "/x", ""))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}
