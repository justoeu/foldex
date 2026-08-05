//go:build integration

package auth_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/auth"
	"foldex/internal/pkg/authctx"
	"foldex/internal/testdb"
)

// resetTokenFrom pulls the raw token out of the link in a reset e-mail. The
// server keeps only its sha256, so the mail is the only place it exists.
func resetTokenFrom(t *testing.T, body string) string {
	t.Helper()
	const marker = "?reset="
	i := strings.Index(body, marker)
	require.GreaterOrEqual(t, i, 0, "no reset link in mail: %q", body)
	tok := body[i+len(marker):]
	if j := strings.IndexAny(tok, "\n\r "); j >= 0 {
		tok = tok[:j]
	}
	require.NotEmpty(t, tok)
	return tok
}

func TestForgotPassword_HappyPathResetsAndSignsIn(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	h.mail.reset()
	rec := h.client(t).do(http.MethodPost, "/api/auth/password/forgot",
		map[string]string{"email": "admin@example.com"})
	require.Equal(t, http.StatusAccepted, rec.Code)

	token := resetTokenFrom(t, h.mail.waitFor(t, "admin@example.com").Text)

	c := h.client(t)
	rec = c.do(http.MethodPost, "/api/auth/password/reset", map[string]string{
		"token": token, "password": "a brand new password",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	// Resetting signs the user in: they proved the mailbox AND chose a password,
	// which is strictly more than the login screen asks for.
	assert.NotEmpty(t, c.cookies[auth.CookieAccess])

	// The new password works and the old one does not.
	fresh := h.client(t)
	assert.Equal(t, http.StatusOK, fresh.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a brand new password"}).Code)
	assert.Equal(t, http.StatusUnauthorized, h.client(t).do(http.MethodPost, "/api/auth/login",
		map[string]string{"email": "admin@example.com", "password": "a good password"}).Code)
}

// A reset is how someone evicts an intruder, so every other session must die.
func TestResetPassword_RevokesEveryExistingSession(t *testing.T) {
	h := newHarness(t)
	victim := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	require.Equal(t, http.StatusOK, victim.do(http.MethodGet, "/api/links", nil).Code)

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, h.client(t).do(http.MethodPost, "/api/auth/password/forgot",
		map[string]string{"email": "admin@example.com"}).Code)
	token := resetTokenFrom(t, h.mail.waitFor(t, "admin@example.com").Text)

	require.Equal(t, http.StatusOK, h.client(t).do(http.MethodPost, "/api/auth/password/reset",
		map[string]string{"token": token, "password": "a brand new password"}).Code)

	assert.Equal(t, http.StatusUnauthorized, victim.do(http.MethodGet, "/api/links", nil).Code,
		"a session that existed before the reset still works")
}

// The token is single-use. A reset link that stayed valid would be a standing
// account-takeover primitive sitting in a mailbox.
func TestResetPassword_TokenCannotBeReplayed(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, h.client(t).do(http.MethodPost, "/api/auth/password/forgot",
		map[string]string{"email": "admin@example.com"}).Code)
	token := resetTokenFrom(t, h.mail.waitFor(t, "admin@example.com").Text)

	require.Equal(t, http.StatusOK, h.client(t).do(http.MethodPost, "/api/auth/password/reset",
		map[string]string{"token": token, "password": "first new password"}).Code)

	rec := h.client(t).do(http.MethodPost, "/api/auth/password/reset",
		map[string]string{"token": token, "password": "second new password"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "reset_invalid", errCode(t, rec))

	// The second attempt must not have taken effect.
	assert.Equal(t, http.StatusUnauthorized, h.client(t).do(http.MethodPost, "/api/auth/login",
		map[string]string{"email": "admin@example.com", "password": "second new password"}).Code)
}

// Requesting a second link must invalidate the first, so a user who clicks
// twice does not leave two live takeover tokens in two e-mails.
func TestForgotPassword_ASecondRequestSupersedesTheFirst(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, h.client(t).do(http.MethodPost, "/api/auth/password/forgot",
		map[string]string{"email": "admin@example.com"}).Code)
	first := resetTokenFrom(t, h.mail.waitFor(t, "admin@example.com").Text)

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, h.client(t).do(http.MethodPost, "/api/auth/password/forgot",
		map[string]string{"email": "admin@example.com"}).Code)
	second := resetTokenFrom(t, h.mail.waitFor(t, "admin@example.com").Text)
	require.NotEqual(t, first, second)

	assert.Equal(t, http.StatusNotFound, h.client(t).do(http.MethodPost, "/api/auth/password/reset",
		map[string]string{"token": first, "password": "a brand new password"}).Code,
		"the superseded link still worked")
	assert.Equal(t, http.StatusOK, h.client(t).do(http.MethodPost, "/api/auth/password/reset",
		map[string]string{"token": second, "password": "a brand new password"}).Code)
}

// The endpoint must not become an account-existence oracle. Three channels have
// to stay uniform: status, body and — for anyone who owns the address — timing.
func TestForgotPassword_IsIndistinguishableForUnknownAddresses(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	cases := []struct {
		name, email string
		wantMail    bool
	}{
		{"registered", "admin@example.com", true},
		{"unknown", "nobody@example.com", false},
		{"malformed", "not-an-email", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h.mail.reset()
			rec := h.client(t).do(http.MethodPost, "/api/auth/password/forgot",
				map[string]string{"email": tc.email})
			// Identical on the wire in all three cases.
			assert.Equal(t, http.StatusAccepted, rec.Code)
			assert.Empty(t, strings.TrimSpace(rec.Body.String()))

			if tc.wantMail {
				h.mail.waitFor(t, tc.email)
			} else {
				// Give any (incorrect) send a chance to land before asserting.
				time.Sleep(150 * time.Millisecond)
				assert.Empty(t, h.mail.all(), "mail was sent for %s", tc.name)
			}
		})
	}
}

// A disabled account gets the same 202 and no link: an attacker must not learn
// that an address is registered-but-disabled either.
func TestForgotPassword_DisabledAccountGetsNoLink(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	other := testdb.SeedUserWithPassword(t, h.pool, "other@example.com", "a good password", "user")
	require.Equal(t, http.StatusOK, admin.do(http.MethodPatch,
		fmt.Sprintf("/api/admin/users/%d", int64(other)), map[string]string{"status": "disabled"}).Code)

	h.mail.reset()
	rec := h.client(t).do(http.MethodPost, "/api/auth/password/forgot",
		map[string]string{"email": "other@example.com"})
	assert.Equal(t, http.StatusAccepted, rec.Code)

	time.Sleep(150 * time.Millisecond)
	assert.Empty(t, h.mail.all(), "a disabled account received a reset link")
}

// An account with no password credential (Google-only, ADR-31) receives a
// message SAYING SO rather than nothing at all.
//
// Silence would leave the INBOX as the oracle: the endpoint would be uniform
// but only registered addresses would ever receive anything. It carries no
// link, because a link here would let control of the mailbox alone resurrect a
// password credential — exactly what requiring the current password during
// conversion refused to allow.
func TestForgotPassword_PasswordlessAccountGetsAnExplanationNotALink(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	uid := testdb.SeedUserWithPassword(t, h.pool, "google@example.com", "a good password", "user")
	// Strip the credential, the way an ADR-31 conversion will.
	_, err := h.pool.Exec(context.Background(),
		`UPDATE app_user SET password_hash = NULL WHERE id = $1`, int64(uid))
	require.NoError(t, err)

	h.mail.reset()
	rec := h.client(t).do(http.MethodPost, "/api/auth/password/forgot",
		map[string]string{"email": "google@example.com"})
	require.Equal(t, http.StatusAccepted, rec.Code)

	msg := h.mail.waitFor(t, "google@example.com")
	assert.Contains(t, strings.ToLower(msg.Text), "google")
	assert.NotContains(t, msg.Text, "?reset=",
		"a passwordless account was sent a reset link")
}

// A reset proves the FIRST factor only. An account with an authenticator must
// still present a code, or a compromised mailbox would bypass 2FA entirely.
func TestResetPassword_StillRequiresTheSecondFactor(t *testing.T) {
	h := newHarnessWith(t, testdb.New(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, h.client(t).do(http.MethodPost, "/api/auth/password/forgot",
		map[string]string{"email": "admin@example.com"}).Code)
	token := resetTokenFrom(t, h.mail.waitFor(t, "admin@example.com").Text)

	c := h.client(t)
	rec := c.do(http.MethodPost, "/api/auth/password/reset", map[string]string{
		"token": token, "password": "a brand new password"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var body struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "two_factor_required", body.Status)
	assert.Empty(t, c.cookies[auth.CookieAccess],
		"a mailbox alone produced a session on a 2FA-protected account")

	// The code finishes it.
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/2fa/verify",
		map[string]string{"code": codeNextStep(t, e.secret)}).Code)
	assert.NotEmpty(t, c.cookies[auth.CookieAccess])
}

// Resetting proves control of the address, so it also marks it verified —
// asking the user to prove the same fact twice is pure friction.
func TestResetPassword_MarksTheAddressVerified(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	_, err := h.pool.Exec(context.Background(), `UPDATE app_user SET email_verified_at = NULL`)
	require.NoError(t, err)

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, h.client(t).do(http.MethodPost, "/api/auth/password/forgot",
		map[string]string{"email": "admin@example.com"}).Code)
	token := resetTokenFrom(t, h.mail.waitFor(t, "admin@example.com").Text)
	require.Equal(t, http.StatusOK, h.client(t).do(http.MethodPost, "/api/auth/password/reset",
		map[string]string{"token": token, "password": "a brand new password"}).Code)

	var verified *time.Time
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT email_verified_at FROM app_user WHERE email = 'admin@example.com'`).Scan(&verified))
	assert.NotNil(t, verified)
}

func TestResetPassword_RejectsAWeakPassword(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, h.client(t).do(http.MethodPost, "/api/auth/password/forgot",
		map[string]string{"email": "admin@example.com"}).Code)
	token := resetTokenFrom(t, h.mail.waitFor(t, "admin@example.com").Text)

	rec := h.client(t).do(http.MethodPost, "/api/auth/password/reset",
		map[string]string{"token": token, "password": "short"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// The token must survive a rejected attempt — spending it on a validation
	// failure would strand the user with a dead link and no new password.
	assert.Equal(t, http.StatusOK, h.client(t).do(http.MethodPost, "/api/auth/password/reset",
		map[string]string{"token": token, "password": "a brand new password"}).Code)
}

// The per-address budget caps how many e-mails one victim can be made to
// receive, and it must hold whether or not the address exists.
func TestForgotPassword_MailbombingIsCapped(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	h.mail.reset()
	for range 8 {
		require.Equal(t, http.StatusAccepted, h.client(t).do(http.MethodPost,
			"/api/auth/password/forgot", map[string]string{"email": "admin@example.com"}).Code)
	}
	require.Eventually(t, func() bool { return len(h.mail.all()) > 0 },
		2*time.Second, 10*time.Millisecond)
	time.Sleep(200 * time.Millisecond)

	assert.LessOrEqual(t, len(h.mail.all()), 3,
		"the per-address cap did not hold: %d mails sent", len(h.mail.all()))
}

// ─────────────────────────────────────────────────────────────────────
// Expiry
// ─────────────────────────────────────────────────────────────────────

// Every credential here carries a TTL, and until this test none of them had it
// proven. Deleting `AND expires_at > now()` from the three consume queries left
// the whole suite green — which means a 30-minute account-takeover link sitting
// in a mailbox would have become permanent, with nothing to notice.
func TestExpiry_ResetTokenStopsWorking(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, h.client(t).do(http.MethodPost, "/api/auth/password/forgot",
		map[string]string{"email": "admin@example.com"}).Code)
	token := resetTokenFrom(t, h.mail.waitFor(t, "admin@example.com").Text)

	// Backdate rather than sleep: the same trick the invite suite already uses.
	_, err := h.pool.Exec(context.Background(),
		`UPDATE password_reset SET expires_at = now() - interval '1 minute'`)
	require.NoError(t, err)

	rec := h.client(t).do(http.MethodPost, "/api/auth/password/reset",
		map[string]string{"token": token, "password": "a brand new password"})
	assert.Equal(t, http.StatusNotFound, rec.Code, "an expired reset link still worked")
	assert.Equal(t, "reset_invalid", errCode(t, rec))

	// And the password must be unchanged.
	assert.Equal(t, http.StatusOK, h.client(t).do(http.MethodPost, "/api/auth/login",
		map[string]string{"email": "admin@example.com", "password": "a good password"}).Code)
}

func TestExpiry_PreAuthChallengeStopsWorking(t *testing.T) {
	h := newHarnessWith(t, testdb.New(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)

	_, err := h.pool.Exec(context.Background(),
		`UPDATE auth_challenge SET expires_at = now() - interval '1 minute' WHERE consumed_at IS NULL`)
	require.NoError(t, err)

	rec := c.do(http.MethodPost, "/api/auth/2fa/verify",
		map[string]string{"code": codeNextStep(t, e.secret)})
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "an expired challenge still minted a session")
	assert.Equal(t, "challenge_invalid", errCode(t, rec))
	assert.Empty(t, c.cookies[auth.CookieAccess])
}

func TestExpiry_MailedCodeStopsWorking(t *testing.T) {
	h := newHarnessWith(t, testdb.New(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	enrolUser(t, h, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	h.mail.reset()
	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/2fa/email", nil).Code)
	code := extractSixDigits(t, h.mail.waitFor(t, "admin@example.com").Text)

	_, err := h.pool.Exec(context.Background(),
		`UPDATE email_otp SET expires_at = now() - interval '1 minute'`)
	require.NoError(t, err)

	rec := c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": code})
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "an expired mailed code still signed in")
	assert.Empty(t, c.cookies[auth.CookieAccess])
}

// The sweeper is the only thing bounding three tables that unauthenticated
// callers can insert into, so "it runs" is not enough — it has to actually
// delete.
func TestSweepTwoFactor_DeletesExpiredRows(t *testing.T) {
	h := newHarnessWith(t, testdb.New(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	enrolUser(t, h, "admin@example.com", "a good password")
	ctx := context.Background()

	// One row in each table, all long past their retention window.
	require.Equal(t, http.StatusOK, h.client(t).do(http.MethodPost, "/api/auth/login",
		map[string]string{"email": "admin@example.com", "password": "a good password"}).Code)
	h.mail.reset()
	require.Equal(t, http.StatusAccepted, h.client(t).do(http.MethodPost, "/api/auth/password/forgot",
		map[string]string{"email": "admin@example.com"}).Code)
	h.mail.waitFor(t, "admin@example.com")

	for _, q := range []string{
		`UPDATE auth_challenge SET expires_at = now() - interval '30 days'`,
		`UPDATE password_reset  SET expires_at = now() - interval '30 days'`,
		`INSERT INTO email_otp (user_id, purpose, code_hash, expires_at)
		 SELECT id, 'login_2fa', '\x00'::bytea, now() - interval '30 days' FROM app_user LIMIT 1`,
	} {
		_, err := h.pool.Exec(ctx, q)
		require.NoError(t, err)
	}

	before := countRows(t, h.pool)
	require.Positive(t, before, "the fixture inserted nothing to sweep")

	n, err := h.repo.SweepTwoFactor(ctx, 24*time.Hour)
	require.NoError(t, err)
	assert.Positive(t, n, "the sweep reported deleting nothing")
	assert.Zero(t, countRows(t, h.pool), "expired 2FA rows survived the sweep")
}

// A row inside the retention window must SURVIVE — a sweep that deletes
// everything would silently sign users out mid-login.
func TestSweepTwoFactor_KeepsLiveRows(t *testing.T) {
	h := newHarnessWith(t, testdb.New(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	enrolUser(t, h, "admin@example.com", "a good password")

	require.Equal(t, http.StatusOK, h.client(t).do(http.MethodPost, "/api/auth/login",
		map[string]string{"email": "admin@example.com", "password": "a good password"}).Code)

	n, err := h.repo.SweepTwoFactor(context.Background(), 24*time.Hour)
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Positive(t, countRows(t, h.pool), "a live challenge was swept away")
}

func countRows(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM auth_challenge)
		     + (SELECT count(*) FROM email_otp)
		     + (SELECT count(*) FROM password_reset)`).Scan(&n))
	return n
}

// ─────────────────────────────────────────────────────────────────────
// E-mail verification
// ─────────────────────────────────────────────────────────────────────

func TestVerifyEmail_RoundTrip(t *testing.T) {
	h := newHarnessWith(t, testdb.New(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	_, err := h.pool.Exec(context.Background(), `UPDATE app_user SET email_verified_at = NULL`)
	require.NoError(t, err)

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/email/resend", nil).Code)
	code := extractSixDigits(t, h.mail.waitFor(t, "admin@example.com").Text)

	require.Equal(t, http.StatusNoContent,
		c.do(http.MethodPost, "/api/auth/email/verify", map[string]string{"code": code}).Code)

	var verified *time.Time
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT email_verified_at FROM app_user WHERE email = 'admin@example.com'`).Scan(&verified))
	assert.NotNil(t, verified)

	// Single use, like every other code here.
	assert.Equal(t, http.StatusUnauthorized,
		c.do(http.MethodPost, "/api/auth/email/verify", map[string]string{"code": code}).Code)
}

func TestVerifyEmail_RejectsAWrongCode(t *testing.T) {
	h := newHarnessWith(t, testdb.New(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	_, err := h.pool.Exec(context.Background(), `UPDATE app_user SET email_verified_at = NULL`)
	require.NoError(t, err)

	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/email/resend", nil).Code)
	for _, bad := range []string{"000000", "abc", ""} {
		rec := c.do(http.MethodPost, "/api/auth/email/verify", map[string]string{"code": bad})
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "code %q", bad)
		assert.Equal(t, "invalid_code", errCode(t, rec))
	}
}

// Another user's verification code must not verify THIS account.
func TestVerifyEmail_CodeIsScopedToItsOwner(t *testing.T) {
	h := newHarnessWith(t, testdb.New(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	alice := h.bootstrapAdmin(t, "alice@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "bob@example.com", "a good password", "user")
	_, err := h.pool.Exec(context.Background(), `UPDATE app_user SET email_verified_at = NULL`)
	require.NoError(t, err)

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, alice.do(http.MethodPost, "/api/auth/email/resend", nil).Code)
	aliceCode := extractSixDigits(t, h.mail.waitFor(t, "alice@example.com").Text)

	bob := h.client(t)
	require.Equal(t, http.StatusOK, bob.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "bob@example.com", "password": "a good password"}).Code)

	assert.Equal(t, http.StatusUnauthorized,
		bob.do(http.MethodPost, "/api/auth/email/verify", map[string]string{"code": aliceCode}).Code,
		"alice's confirmation code verified bob's address")

	var verified *time.Time
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT email_verified_at FROM app_user WHERE email = 'bob@example.com'`).Scan(&verified))
	assert.Nil(t, verified)
}

// The recipient is whoever is signed in — never a value from the request — so
// the endpoint cannot be turned into a mail relay.
func TestVerifyEmail_ResendIsANoOpOnceVerified(t *testing.T) {
	h := newHarnessWith(t, testdb.New(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	h.mail.reset()
	// Bootstrap leaves the address verified, so this is already satisfied.
	assert.Equal(t, http.StatusNoContent, c.do(http.MethodPost, "/api/auth/email/resend", nil).Code)
	time.Sleep(150 * time.Millisecond)
	assert.Empty(t, h.mail.all(), "a code was mailed for an already-verified address")
}

// Every repository method in the 2FA layer wraps its database errors, and none
// of those branches is reachable while the database is healthy. Closing the
// pool is the cheapest honest way to exercise all of them at once — and what it
// proves is worth having: each one must return an ERROR, never a zero value
// that a caller could mistake for "no rows" and treat as success.
func TestTwoFactorRepository_SurfacesDatabaseErrors(t *testing.T) {
	h := newHarnessWith(t, testdb.New(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	ctx := context.Background()
	uid := authctx.UserID(1)
	h.pool.Close() // every subsequent query fails

	t.Run("challenges", func(t *testing.T) {
		_, _, err := h.repo.CreateChallenge(ctx, uid, auth.PurposeTOTP, time.Minute, "", "", false)
		assert.Error(t, err)
		_, err = h.repo.ResolveChallenge(ctx, "whatever")
		assert.Error(t, err)
		_, err = h.repo.BumpChallengeAttempt(ctx, 1)
		assert.Error(t, err)
		assert.Error(t, h.repo.ConsumeChallenge(ctx, 1))
		_, err = h.repo.ReserveChallengeSend(ctx, 1)
		assert.Error(t, err)
	})

	t.Run("totp", func(t *testing.T) {
		assert.Error(t, h.repo.StartTOTPEnrollment(ctx, uid, []byte("x"), []byte("y")))
		_, err := h.repo.LoadTOTPSecret(ctx, uid)
		assert.Error(t, err)
		assert.Error(t, h.repo.ConfirmTOTP(ctx, uid, 1))
		assert.Error(t, h.repo.ConsumeTOTPCounter(ctx, uid, 1))
		assert.Error(t, h.repo.DisableTOTP(ctx, uid))
	})

	t.Run("recovery codes", func(t *testing.T) {
		assert.Error(t, h.repo.ReplaceRecoveryCodes(ctx, uid, [][]byte{[]byte("h")}))
		assert.Error(t, h.repo.ConsumeRecoveryCode(ctx, uid, []byte("h")))
		_, err := h.repo.CountRecoveryCodes(ctx, uid)
		assert.Error(t, err)
	})

	t.Run("otp and reset", func(t *testing.T) {
		assert.Error(t, h.repo.CreateEmailOTP(ctx, uid, nil, auth.OTPPurposeLogin2FA, []byte("h"), time.Minute))
		assert.Error(t, h.repo.ConsumeEmailOTP(ctx, uid, auth.OTPPurposeLogin2FA, []byte("h"), nil))
		_, _, err := h.repo.UserForPasswordReset(ctx, "admin@example.com")
		assert.Error(t, err)
		_, err = h.repo.CreatePasswordReset(ctx, uid, time.Minute, "")
		assert.Error(t, err)
		_, err = h.repo.ConsumePasswordReset(ctx, "tok", "a brand new password")
		assert.Error(t, err)
		assert.Error(t, h.repo.MarkEmailVerified(ctx, uid))
		_, err = h.repo.SweepTwoFactor(ctx, time.Hour)
		assert.Error(t, err)
	})
}

// The "not found" answers must be typed sentinels rather than bare errors, so
// handlers can tell "no such row" from "the database is on fire" — the two
// deserve a 404 and a 500 respectively.
func TestTwoFactorRepository_NotFoundIsTyped(t *testing.T) {
	h := newHarnessWith(t, testdb.New(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	ctx := context.Background()
	uid := authctx.UserID(1)

	_, err := h.repo.LoadTOTPSecret(ctx, uid)
	assert.ErrorIs(t, err, auth.ErrNoTOTP)

	assert.ErrorIs(t, h.repo.ConfirmTOTP(ctx, uid, 1), auth.ErrNoTOTP)
	assert.ErrorIs(t, h.repo.ConsumeTOTPCounter(ctx, uid, 1), auth.ErrTOTPReplay)
	assert.ErrorIs(t, h.repo.ConsumeRecoveryCode(ctx, uid, []byte("nope")), auth.ErrBadCredentials)
	assert.ErrorIs(t, h.repo.ConsumeEmailOTP(ctx, uid, auth.OTPPurposeLogin2FA, []byte("nope"), nil),
		auth.ErrBadCredentials)

	_, err = h.repo.ResolveChallenge(ctx, "no such token")
	assert.ErrorIs(t, err, auth.ErrChallengeInvalid)
	_, err = h.repo.ResolveChallenge(ctx, "")
	assert.ErrorIs(t, err, auth.ErrChallengeInvalid)
	_, err = h.repo.BumpChallengeAttempt(ctx, 999_999)
	assert.ErrorIs(t, err, auth.ErrChallengeInvalid)

	_, err = h.repo.ConsumePasswordReset(ctx, "no such token", "a brand new password")
	assert.ErrorIs(t, err, auth.ErrResetInvalid)
	_, err = h.repo.ConsumePasswordReset(ctx, "", "a brand new password")
	assert.ErrorIs(t, err, auth.ErrResetInvalid)

	// An unknown address is "not eligible", not an error: the caller answers
	// 202 either way and must not be able to tell the two apart.
	_, ok, err := h.repo.UserForPasswordReset(ctx, "nobody@example.com")
	require.NoError(t, err)
	assert.False(t, ok)
}

// The reset endpoint answers 202 for everything, so TIMING is the last channel
// left that could separate a registered address from an unknown one — the work
// differs enormously between "no row found" and "insert a token and queue a
// mail". The same duration floor login uses closes it.
//
// Asserted against the floor rather than against the ratio between the two
// measurements: a ratio is what makes this kind of test flaky on a loaded CI
// box, and the floor is the thing actually doing the work.
func TestForgotPassword_TakesTheSameTimeForKnownAndUnknownAddresses(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	measure := func(email string) time.Duration {
		// A fresh router each time: the per-address bucket is in memory and
		// would otherwise short-circuit the second measurement, which returns
		// early and would read as "faster" for reasons unrelated to the lookup.
		hh := newHarnessOn(t, h.pool)
		start := time.Now()
		hh.client(t).do(http.MethodPost, "/api/auth/password/forgot",
			map[string]string{"email": email})
		return time.Since(start)
	}

	miss := measure("ghost@example.com")
	hit := measure("admin@example.com")

	assert.GreaterOrEqual(t, miss, 240*time.Millisecond,
		"an unknown address returned in %v — the duration floor is not being applied", miss)
	assert.GreaterOrEqual(t, hit, 240*time.Millisecond,
		"a known address returned in %v", hit)
}
