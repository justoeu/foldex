//go:build integration

package auth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/auth"
	"foldex/internal/pkg/authctx"
	"foldex/internal/testdb"
)

// enrolEmailFactor takes an already-signed-in client through the e-mail factor
// enrollment and returns the recovery codes it issued.
//
// Exported through the test package rather than inlined because ADR-37 changed
// what "this account may use a mailed code" means: it is now something the
// account ENROLLED, so every test that exercises the e-mail login method has to
// enrol first, exactly as a user would.
func enrolEmailFactor(t *testing.T, h *harness, c *client, email, password string) []string {
	t.Helper()
	h.mail.reset()
	rec := c.do(http.MethodPost, "/api/auth/2fa/email/start", map[string]string{"password": password})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	code := h.mail.waitForCode(t, email)
	rec = c.do(http.MethodPost, "/api/auth/2fa/email/confirm", map[string]string{"code": code})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var confirm struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &confirm))
	require.Len(t, confirm.RecoveryCodes, 10,
		"enrolling e-mail must issue recovery codes: they are the only exit from the "+
			"reset-link lockout the mailbox_already_proven guard creates on purpose")
	return confirm.RecoveryCodes
}

// A mailed code is now a factor the account HOLDS, not a capability the server
// happens to have. Before ADR-37 any TOTP account on an SMTP instance was
// offered e-mail as an alternative — an undeclared second factor nobody chose.
func TestEmailFactor_IsOfferedOnlyAfterEnrollment(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	methods := func(t *testing.T) []string {
		t.Helper()
		c := h.client(t)
		rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
			"email": "admin@example.com", "password": "a good password"})
		require.Equal(t, http.StatusOK, rec.Code)
		var body struct {
			Methods []string `json:"methods"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		return body.Methods
	}

	assert.NotContains(t, methods(t), "email_otp",
		"a TOTP account that never enrolled e-mail must not be offered it")

	enrolEmailFactor(t, h, e.client, "admin@example.com", "a good password")
	assert.Contains(t, methods(t), "email_otp")
}

// The endpoint refuses rather than answering 202 and sending nothing: a silent
// accept would leave the user waiting for a code that is never coming.
func TestEmailFactor_SendIsRefusedBeforeEnrollment(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	enrolUser(t, h, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)

	h.mail.reset()
	rec := c.do(http.MethodPost, "/api/auth/2fa/email", nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, h.drainMail(t), "no code may be sent for a factor nobody enrolled")
}

// THE guard that cannot fall. An account arriving through a password-reset link
// has proven the mailbox; offering it a mailed code would close both steps on
// one channel. Enrolling e-mail must not weaken this.
func TestEmailFactor_ResetLinkStillCannotSatisfyBothFactors(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")
	enrolEmailFactor(t, h, e.client, "admin@example.com", "a good password")

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, h.client(t).do(http.MethodPost, "/api/auth/password/forgot",
		map[string]string{"email": "admin@example.com"}).Code)
	token := resetTokenFrom(t, h.mail.waitFor(t, "admin@example.com").Text)

	c := h.client(t)
	rec := c.do(http.MethodPost, "/api/auth/password/reset", map[string]string{
		"token": token, "password": "another good password"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var body struct {
		Methods []string `json:"methods"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.NotContains(t, body.Methods, "email_otp",
		"the mailbox proved the FIRST factor; it must not be offered for the second")

	h.mail.reset()
	assert.Equal(t, http.StatusForbidden, c.do(http.MethodPost, "/api/auth/2fa/email", nil).Code)
	assert.Empty(t, h.drainMail(t))
}

// Enrolling against the log driver would install a factor whose codes anyone
// with the container logs can read — the same reason it is refused as a login
// method.
func TestEmailFactor_CannotBeEnrolledWithoutSMTP(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true}) // log driver
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	h.mail.reset()
	rec := e.client.do(http.MethodPost, "/api/auth/2fa/email/start",
		map[string]string{"password": "a good password"})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Empty(t, h.drainMail(t))
}

// A wrong code must not confirm, and must not consume the enrollment either —
// the user gets to try again with the code they actually received.
func TestEmailFactor_ConfirmRejectsAWrongCodeAndKeepsTheEnrollment(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	h.mail.reset()
	require.Equal(t, http.StatusOK, e.client.do(http.MethodPost, "/api/auth/2fa/email/start",
		map[string]string{"password": "a good password"}).Code)
	real := extractSixDigits(t, h.mail.waitFor(t, "admin@example.com").Text)

	rec := e.client.do(http.MethodPost, "/api/auth/2fa/email/confirm", map[string]string{"code": "000000"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	var enabled bool
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM email_factor WHERE confirmed_at IS NOT NULL)`).Scan(&enabled))
	assert.False(t, enabled)

	// The genuine code still works.
	rec = e.client.do(http.MethodPost, "/api/auth/2fa/email/confirm", map[string]string{"code": real})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// Single-use, enforced by the UPDATE's own WHERE clause rather than a preceding
// read: a code presented twice at the same instant must confirm exactly once.
func TestEmailFactor_ConfirmationCodeIsSpentWhenUsed(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	h.mail.reset()
	require.Equal(t, http.StatusOK, e.client.do(http.MethodPost, "/api/auth/2fa/email/start",
		map[string]string{"password": "a good password"}).Code)
	code := h.mail.waitForCode(t, "admin@example.com")

	require.Equal(t, http.StatusOK, e.client.do(http.MethodPost, "/api/auth/2fa/email/confirm",
		map[string]string{"code": code}).Code)
	// Replaying it must not re-confirm — and by then there is nothing pending.
	rec := e.client.do(http.MethodPost, "/api/auth/2fa/email/confirm", map[string]string{"code": code})
	assert.NotEqual(t, http.StatusOK, rec.Code)
}

// Disabling requires the password AND a second-factor proof — the same two the
// enrollment demanded, so a stolen session alone cannot strip the protection it
// cannot satisfy.
func TestEmailFactor_DisableNeedsPasswordAndAProof(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	uid := testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
	_ = uid
	e := enrolUser(t, h, "user@example.com", "a good password")
	enrolEmailFactor(t, h, e.client, "user@example.com", "a good password")

	// Wrong password is refused before anything else.
	assert.Equal(t, http.StatusUnauthorized, e.client.do(http.MethodPost, "/api/auth/2fa/email/disable",
		map[string]string{"password": "wrong password", "code": codeNextStep(t, e.secret)}).Code)

	// Right password, wrong code.
	assert.Equal(t, http.StatusUnauthorized, e.client.do(http.MethodPost, "/api/auth/2fa/email/disable",
		map[string]string{"password": "a good password", "code": "000000"}).Code)

	assert.Equal(t, http.StatusNoContent, e.client.do(http.MethodPost, "/api/auth/2fa/email/disable",
		map[string]string{"password": "a good password", "code": codeNextStep(t, e.secret)}).Code)

	var enabled bool
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM email_factor WHERE confirmed_at IS NOT NULL)`).Scan(&enabled))
	assert.False(t, enabled)
}

// Recovery codes guard the FACTORS, so they survive only while one remains.
// Leaving them on an account with no second factor would be a standing set of
// single-use credentials protecting nothing.
func TestEmailFactor_DisablingTheLastFactorClearsRecoveryCodes(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
	e := enrolUser(t, h, "user@example.com", "a good password")
	codes := enrolEmailFactor(t, h, e.client, "user@example.com", "a good password")

	// TOTP goes first; the e-mail factor still stands, so the codes stay.
	require.Equal(t, http.StatusNoContent, e.client.do(http.MethodPost, "/api/auth/2fa/totp/disable",
		map[string]string{"password": "a good password", "code": codeNextStep(t, e.secret)}).Code)

	var left int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM recovery_code WHERE used_at IS NULL`).Scan(&left))
	assert.Positive(t, left, "recovery codes guard the remaining factor and must survive")

	// Now the last one. A recovery code is the only proof left.
	require.Equal(t, http.StatusNoContent, e.client.do(http.MethodPost, "/api/auth/2fa/email/disable",
		map[string]string{"password": "a good password", "code": codes[0]}).Code)

	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM recovery_code`).Scan(&left))
	assert.Zero(t, left, "no factor remains, so the codes guard nothing")
}

// An account whose ONLY factor is e-mail still owes a step-up proof before it
// can set a password, and the mailed code is what satisfies it.
//
// Reading TOTPEnabled instead of HasSecondFactor — in the handler OR in the
// repository's own re-check inside the transaction — would let this account
// install a password with no second factor at all.
func TestEmailFactor_OnlyEmailStillOwesAStepUpForSetPassword(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")
	enrolEmailFactor(t, h, e.client, "admin@example.com", "a good password")

	// Drop TOTP so e-mail is the sole factor, then take the password away —
	// the state a Google conversion leaves behind, and the only one in which
	// /password/set is reachable at all.
	require.Equal(t, http.StatusNoContent, e.client.do(http.MethodPost, "/api/auth/2fa/totp/disable",
		map[string]string{"password": "a good password", "code": codeNextStep(t, e.secret)}).Code)
	testdb.ConvertToGoogleOnly(t, h.pool, authctx.UserID(1), "admin@example.com", "google-sub-email")

	rec := e.client.do(http.MethodPost, "/api/auth/password/set",
		map[string]string{"password": "a brand new password"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"no proof was demanded of an account whose only factor is e-mail")

	var hasPassword bool
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT password_hash IS NOT NULL FROM app_user WHERE id = 1`).Scan(&hasPassword))
	require.False(t, hasPassword, "the refusal must not have installed a credential")

	code := stepUpCode(t, h, e.client, "admin@example.com")
	rec = e.client.do(http.MethodPost, "/api/auth/password/set", map[string]string{
		"password": "a brand new password", "code": code,
	})
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	fresh := h.client(t)
	assert.Equal(t, http.StatusOK, fresh.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a brand new password"}).Code)
}

// stepUpCode asks for a mailed step-up code and returns the digits.
func stepUpCode(t *testing.T, h *harness, c *client, email string) string {
	t.Helper()
	h.mail.reset()
	rec := c.do(http.MethodPost, "/api/auth/2fa/email/send", nil)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	return h.mail.waitForCode(t, email)
}

// THE guard on the step-up path. A mailed code must prove an ENROLLED factor,
// never merely that someone can read the mailbox — otherwise a mailbox alone
// authorizes removing an authenticator, which is the same one-channel failure
// mailbox_already_proven exists to prevent on the login path.
func TestStepUp_MailedCodeIsRefusedWithoutTheEnrolledFactor(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
	e := enrolUser(t, h, "user@example.com", "a good password")

	// TOTP only: the send endpoint has nothing to mail a code FOR.
	rec := e.client.do(http.MethodPost, "/api/auth/2fa/email/send", nil)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "email_factor_not_enabled")

}

// The guard asserted with a code that is REAL and LIVE, which the previous
// version of this test failed to do: posting "000000" for an account with no
// e-mail factor is refused because no such digest exists, so deleting the
// `user.Email2FAEnabled` condition would have left it green.
//
// Here the code is genuinely mailed and genuinely valid, and only the enrolled
// factor is taken away — so the 401 can only come from the guard.
func TestStepUp_MailedCodeDiesWithTheFactorThatMintedIt(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
	e := enrolUser(t, h, "user@example.com", "a good password")
	enrolEmailFactor(t, h, e.client, "user@example.com", "a good password")

	code := stepUpCode(t, h, e.client, "user@example.com")

	// Remove the e-mail factor using the authenticator, leaving the mailed code
	// live in the table but no longer backed by an enrolled factor.
	require.Equal(t, http.StatusNoContent, e.client.do(http.MethodPost, "/api/auth/2fa/email/disable",
		map[string]string{"password": "a good password", "code": codeNextStep(t, e.secret)}).Code)

	rec := e.client.do(http.MethodPost, "/api/auth/2fa/recovery-codes/regenerate",
		map[string]string{"password": "a good password", "code": code})
	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())

	// Cleanup at the repository is the second half of the same property: the
	// row is gone, not merely ignored.
	var live int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM email_otp WHERE purpose = 'step_up_2fa' AND consumed_at IS NULL`).Scan(&live))
	assert.Zero(t, live, "codes minted by a removed factor must not outlive it")
}

// The whole point of the endpoint: an account whose only factor is e-mail can
// step up with a mailed code instead of spending a finite recovery code on an
// ordinary settings change.
func TestStepUp_MailedCodeAuthorizesAnEmailOnlyAccount(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
	e := enrolUser(t, h, "user@example.com", "a good password")
	enrolEmailFactor(t, h, e.client, "user@example.com", "a good password")

	// Drop TOTP so e-mail is the sole factor.
	require.Equal(t, http.StatusNoContent, e.client.do(http.MethodPost, "/api/auth/2fa/totp/disable",
		map[string]string{"password": "a good password", "code": codeNextStep(t, e.secret)}).Code)

	var before int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM recovery_code WHERE used_at IS NULL`).Scan(&before))

	code := stepUpCode(t, h, e.client, "user@example.com")
	rec := e.client.do(http.MethodPost, "/api/auth/2fa/email/disable",
		map[string]string{"password": "a good password", "code": code})
	assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	// The mailed code is what paid for it: no recovery code was spent.
	var used int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM recovery_code WHERE used_at IS NOT NULL`).Scan(&used))
	assert.Zero(t, used, "a mailed step-up must not cost the user a lockout credential")
	assert.Positive(t, before)
}

// Single-use, by the same conditional UPDATE the other codes use. Two requests
// presenting one code at the same instant must succeed exactly once.
func TestStepUp_MailedCodeIsSpentWhenUsed(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
	e := enrolUser(t, h, "user@example.com", "a good password")
	enrolEmailFactor(t, h, e.client, "user@example.com", "a good password")

	code := stepUpCode(t, h, e.client, "user@example.com")
	require.Equal(t, http.StatusOK, e.client.do(http.MethodPost, "/api/auth/2fa/recovery-codes/regenerate",
		map[string]string{"password": "a good password", "code": code}).Code)

	assert.Equal(t, http.StatusUnauthorized, e.client.do(http.MethodPost,
		"/api/auth/2fa/recovery-codes/regenerate",
		map[string]string{"password": "a good password", "code": code}).Code,
		"a spent step-up code must not authorize a second operation")
}

// A six-digit code is shape-indistinguishable between an authenticator and a
// mailed step-up, so the discriminator falls THROUGH rather than committing to
// the TOTP branch. Committing would make the e-mail factor unusable for exactly
// the accounts with no authenticator to fall back on.
func TestStepUp_SixDigitsFallThroughFromTOTPToEmail(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
	e := enrolUser(t, h, "user@example.com", "a good password")
	enrolEmailFactor(t, h, e.client, "user@example.com", "a good password")

	// Both factors are live. The mailed code is not a valid TOTP code, so it
	// only works if the TOTP miss falls through instead of short-circuiting.
	code := stepUpCode(t, h, e.client, "user@example.com")
	rec := e.client.do(http.MethodPost, "/api/auth/2fa/recovery-codes/regenerate",
		map[string]string{"password": "a good password", "code": code})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// Purpose separation, and the guard that enforces it. A PENDING enrollment code
// proves a mailbox nobody has accepted yet; a step-up code is an accepted factor
// presenting itself. Spending the first as the second would let mailbox control
// during a half-finished enrollment reach the operations that demand a live
// factor — here, removing the authenticator.
func TestStepUp_PendingEnrollmentCodeIsNotAStepUpProof(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
	e := enrolUser(t, h, "user@example.com", "a good password")

	// Start but do NOT confirm: the factor is pending, so the account does not
	// hold it yet.
	h.mail.reset()
	require.Equal(t, http.StatusOK, e.client.do(http.MethodPost, "/api/auth/2fa/email/start",
		map[string]string{"password": "a good password"}).Code)
	pending := h.mail.waitForCode(t, "user@example.com")

	rec := e.client.do(http.MethodPost, "/api/auth/2fa/totp/disable",
		map[string]string{"password": "a good password", "code": pending})
	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())

	var confirmed bool
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM totp_secret WHERE confirmed_at IS NOT NULL)`).Scan(&confirmed))
	assert.True(t, confirmed, "the authenticator must survive a code that proved nothing")
}

// The status endpoint is what the settings screen renders from, so it has to
// name both methods. Reporting only `enabled` would call an e-mail-only account
// unprotected, and offering a disable button the server refuses reads as a bug
// in the account rather than a policy.
func TestTwoFactorStatus_ReportsEachMethodAndWhatMayBeRemoved(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
	e := enrolUser(t, h, "user@example.com", "a good password")

	read := func() map[string]any {
		rec := e.client.do(http.MethodGet, "/api/auth/2fa", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}

	got := read()
	assert.Equal(t, true, got["enabled"])
	assert.Equal(t, true, got["totp_enabled"])
	assert.Equal(t, false, got["email_enabled"])
	assert.Equal(t, true, got["email_available"], "SMTP is configured on this harness")
	assert.Equal(t, false, got["can_disable_email"], "nothing to remove")

	enrolEmailFactor(t, h, e.client, "user@example.com", "a good password")
	got = read()
	assert.Equal(t, true, got["totp_enabled"])
	assert.Equal(t, true, got["email_enabled"])
	assert.Equal(t, true, got["can_disable_totp"])
	assert.Equal(t, true, got["can_disable_email"])
}

// An administrator under a mandatory-2FA policy may drop ONE factor while the
// other remains. Refusing outright would treat "holds two factors" as stricter
// than "holds one", and the guard exists to keep them holding at least one.
func TestStepUp_AdminMayDropOneFactorButNotTheLast(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{
		TwoFactor: true, SMTP: true, Require2FAForAdmins: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))

	// Bootstrap under the policy diverts into mandatory enrollment rather than
	// issuing a session, so the authenticator is set up on the pre-auth path.
	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/bootstrap", map[string]string{
		"email": "admin@example.com", "name": "Admin", "password": "a good password",
	}).Code)
	rec := c.do(http.MethodPost, "/api/auth/2fa/totp/start", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var start struct {
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &start))
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/2fa/totp/confirm",
		map[string]string{"code": codeNow(t, start.Secret)}).Code)

	enrolEmailFactor(t, h, c, "admin@example.com", "a good password")

	rec = c.do(http.MethodGet, "/api/auth/2fa", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var status map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	assert.Equal(t, true, status["can_disable_totp"],
		"the e-mail factor satisfies the policy, so the authenticator may go")

	require.Equal(t, http.StatusNoContent, c.do(http.MethodPost, "/api/auth/2fa/totp/disable",
		map[string]string{"password": "a good password", "code": codeNextStep(t, start.Secret)}).Code)

	// Now e-mail is the last one and the policy holds it in place.
	code := stepUpCode(t, h, c, "admin@example.com")
	rec = c.do(http.MethodPost, "/api/auth/2fa/email/disable",
		map[string]string{"password": "a good password", "code": code})
	// The SAME refusal DisableTOTP gives, deliberately: one rule answering 403
	// on one endpoint and 409 on the other reads as two different policies, and
	// `admin_2fa_required` is what the middleware emits for a different
	// condition — an admin holding no factor at all.
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "totp_required_for_admins")
}

// The pre-auth branch — an administrator under AUTH_REQUIRE_2FA_FOR_ADMINS who
// chooses e-mail at mandatory enrollment. Confirming here CONSUMES the
// challenge and ISSUES the session, and every test above used an authenticated
// client, so none of that was covered.
func TestEmailFactor_SatisfiesMandatoryAdminEnrollment(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{
		TwoFactor: true, SMTP: true, Require2FAForAdmins: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))

	c := h.client(t)
	rec := c.do(http.MethodPost, "/api/auth/bootstrap", map[string]string{
		"email": "admin@example.com", "name": "Admin", "password": "a good password",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Empty(t, c.cookies[auth.CookieAccess], "bootstrap issued a session before enrollment")

	// No password on this branch: it was proven moments ago to get the challenge.
	h.mail.reset()
	rec = c.do(http.MethodPost, "/api/auth/2fa/email/start", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	code := h.mail.waitForCode(t, "admin@example.com")

	rec = c.do(http.MethodPost, "/api/auth/2fa/email/confirm", map[string]string{"code": code})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var confirm struct {
		Status        string   `json:"status"`
		RecoveryCodes []string `json:"recovery_codes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &confirm))
	assert.Equal(t, "authenticated", confirm.Status)
	assert.Len(t, confirm.RecoveryCodes, 10)
	assert.NotEmpty(t, c.cookies[auth.CookieAccess])
	assert.Empty(t, c.cookies[auth.CookiePreAuth], "the pre-auth cookie must be cleared")
	assert.Equal(t, http.StatusOK, c.do(http.MethodGet, "/api/admin/users", nil).Code)

	var consumed bool
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT consumed_at IS NOT NULL FROM auth_challenge`).Scan(&consumed))
	assert.True(t, consumed, "the challenge must be spent, or the cookie is replayable")
}

// The confirming code goes to the account's MAILBOX and never to the caller, so
// unlike its TOTP twin it is guessable — and on the pre-auth branch confirming
// issues an administrator session. Without a budget, a stolen password plus a
// million guesses is a takeover.
func TestEmailFactor_ConfirmationIsBudgetedOnThePreAuthPath(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{
		TwoFactor: true, SMTP: true, Require2FAForAdmins: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/bootstrap", map[string]string{
		"email": "admin@example.com", "name": "Admin", "password": "a good password",
	}).Code)
	h.mail.reset()
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/2fa/email/start", nil).Code)

	last := 0
	for range 8 {
		last = c.do(http.MethodPost, "/api/auth/2fa/email/confirm",
			map[string]string{"code": "000000"}).Code
		if last == http.StatusTooManyRequests {
			break
		}
	}
	assert.Equal(t, http.StatusTooManyRequests, last,
		"unlimited guesses against a mailed code that issues an admin session")

	var enabled bool
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM email_factor WHERE confirmed_at IS NOT NULL)`).Scan(&enabled))
	assert.False(t, enabled)
}

// Removing a factor is a credential-set mutation: it bumps the epoch and drops
// every OTHER session. An account removing the e-mail factor BECAUSE the
// mailbox was compromised must not leave the intruder's session live.
func TestEmailFactor_DisableRevokesOtherSessionsAndBumpsTheEpoch(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
	e := enrolUser(t, h, "user@example.com", "a good password")
	codes := enrolEmailFactor(t, h, e.client, "user@example.com", "a good password")

	// A SECOND session, standing in for the intruder's. Without it there is
	// nothing for the revocation to act on and the assertion below passes or
	// fails for reasons unrelated to the behaviour under test.
	other := h.client(t)
	require.Equal(t, http.StatusOK, other.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "user@example.com", "password": "a good password",
	}).Code)
	require.Equal(t, http.StatusOK, other.do(http.MethodPost, "/api/auth/2fa/verify",
		map[string]string{"code": codeNextStep(t, e.secret)}).Code)
	require.Equal(t, http.StatusOK, other.do(http.MethodGet, "/api/links", nil).Code)

	before := tokenVersionOf(t, h, "user@example.com")
	require.Equal(t, http.StatusNoContent, e.client.do(http.MethodPost, "/api/auth/2fa/email/disable",
		map[string]string{"password": "a good password", "code": codes[0]}).Code)

	assert.Greater(t, tokenVersionOf(t, h, "user@example.com"), before,
		"a credential-set mutation must invalidate proofs bound to the old epoch")
	assert.Equal(t, http.StatusOK, e.client.do(http.MethodGet, "/api/links", nil).Code,
		"the device performing the change keeps its own session")
	assert.Equal(t, http.StatusUnauthorized, other.do(http.MethodGet, "/api/links", nil).Code,
		"the other session must be gone")

	var revoked int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM session WHERE revoked_at IS NOT NULL`).Scan(&revoked))
	assert.Positive(t, revoked, "the sessions opened before the change must be dropped")
}

func tokenVersionOf(t *testing.T, h *harness, email string) int {
	t.Helper()
	var v int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT token_version FROM app_user WHERE email_normalized = $1`, email).Scan(&v))
	return v
}

// A successful disable must settle the attempt-limit slot it reserved.
// attemptlimit.Begin requires exactly one of CommitFail/CommitSuccess/Release,
// Sweep skips in-flight entries, and the key is SHARED with every other
// step-up — so a leak here would lock the account out of all of them.
func TestStepUp_SuccessfulDisableDoesNotLeakTheAttemptSlot(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
	e := enrolUser(t, h, "user@example.com", "a good password")

	// Enrol and disable more times than the limiter's cap of five, each one a
	// SUCCESS. A leaked slot never decrements, so the sixth round would answer
	// 429 out of a budget that nothing ever spent.
	//
	// Each round pays with a RECOVERY code from that round's enrollment. A TOTP
	// code is spent when used, and seven calls to codeNextStep inside one
	// 30-second window return the same code, which the replay guard rejects — as
	// it should. A mailed step-up code has its own 60-second cooldown. Recovery
	// codes are the one proof reissued fresh by every enrollment, and the TOTP
	// factor surviving each disable is what keeps them alive.
	for i := range 7 {
		codes := enrolEmailFactor(t, h, e.client, "user@example.com", "a good password")
		rec := e.client.do(http.MethodPost, "/api/auth/2fa/email/disable",
			map[string]string{"password": "a good password", "code": codes[0]})
		require.Equalf(t, http.StatusNoContent, rec.Code, "round %d: %s", i, rec.Body.String())
	}

	// And an unrelated step-up still works afterwards.
	codes := enrolEmailFactor(t, h, e.client, "user@example.com", "a good password")
	rec := e.client.do(http.MethodPost, "/api/auth/2fa/recovery-codes/regenerate",
		map[string]string{"password": "a good password", "code": codes[1]})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// The refusals nobody exercises, each of which is a real answer a real client
// can receive — and each of which was reachable only by reading the code.
func TestEmailFactor_RefusalsAreSpecific(t *testing.T) {
	t.Run("step-up send needs SMTP", func(t *testing.T) {
		// The log driver prints the body to stdout, so a code mailed through it
		// is readable by anyone with the container logs — the same reason
		// enrollment is refused there.
		h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
		require.NoError(t, testdb.Reset(context.Background(), h.pool))
		h.bootstrapAdmin(t, "admin@example.com", "a good password")
		testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
		e := enrolUser(t, h, "user@example.com", "a good password")

		rec := e.client.do(http.MethodPost, "/api/auth/2fa/email/send", nil)
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "email_factor_unavailable")
	})

	t.Run("confirm without start", func(t *testing.T) {
		h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
		require.NoError(t, testdb.Reset(context.Background(), h.pool))
		h.bootstrapAdmin(t, "admin@example.com", "a good password")
		testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
		e := enrolUser(t, h, "user@example.com", "a good password")

		// No pending enrollment: the code cannot match, so this is the same
		// answer a wrong code gets. Telling the two apart would say whether an
		// enrollment is in flight.
		rec := e.client.do(http.MethodPost, "/api/auth/2fa/email/confirm",
			map[string]string{"code": "123456"})
		assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	})

	t.Run("disable without the factor", func(t *testing.T) {
		h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
		require.NoError(t, testdb.Reset(context.Background(), h.pool))
		h.bootstrapAdmin(t, "admin@example.com", "a good password")
		testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
		e := enrolUser(t, h, "user@example.com", "a good password")

		rec := e.client.do(http.MethodPost, "/api/auth/2fa/email/disable",
			map[string]string{"password": "a good password", "code": codeNextStep(t, e.secret)})
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "email_factor_not_enabled")
	})

	t.Run("start coalesces inside the cooldown", func(t *testing.T) {
		// 202 rather than 429: the caller is authenticated and this is their own
		// enrollment, so there is nothing to conceal — but answering with an
		// error would invite a retry loop against a code already in flight.
		h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
		require.NoError(t, testdb.Reset(context.Background(), h.pool))
		h.bootstrapAdmin(t, "admin@example.com", "a good password")
		testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
		e := enrolUser(t, h, "user@example.com", "a good password")

		require.Equal(t, http.StatusOK, e.client.do(http.MethodPost, "/api/auth/2fa/email/start",
			map[string]string{"password": "a good password"}).Code)
		rec := e.client.do(http.MethodPost, "/api/auth/2fa/email/start",
			map[string]string{"password": "a good password"})
		assert.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

		var live int
		require.NoError(t, h.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM email_otp WHERE purpose = 'enroll_email_2fa' AND consumed_at IS NULL`).
			Scan(&live))
		assert.Equal(t, 1, live, "the cooldown must not mint a second live code")
	})

	t.Run("step-up send coalesces inside the cooldown", func(t *testing.T) {
		h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
		require.NoError(t, testdb.Reset(context.Background(), h.pool))
		h.bootstrapAdmin(t, "admin@example.com", "a good password")
		testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
		e := enrolUser(t, h, "user@example.com", "a good password")
		enrolEmailFactor(t, h, e.client, "user@example.com", "a good password")

		require.Equal(t, http.StatusAccepted,
			e.client.do(http.MethodPost, "/api/auth/2fa/email/send", nil).Code)
		assert.Equal(t, http.StatusAccepted,
			e.client.do(http.MethodPost, "/api/auth/2fa/email/send", nil).Code)

		var live int
		require.NoError(t, h.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM email_otp WHERE purpose = 'step_up_2fa' AND consumed_at IS NULL`).
			Scan(&live))
		assert.Equal(t, 1, live, "pressing the button must not accumulate live credentials")
	})
}

// The SESSION half of the confirmation budget.
//
// The pre-auth half is bounded by auth_challenge.attempts and locked by the test
// above; this path has no challenge, so its ceiling is the in-memory limiter —
// and a review found that deleting the reservation outright left the whole suite
// green. An attacker holding a stolen session could then grind the six digits of
// an enrollment in flight, installing a factor on an account they only half own.
//
// Keyed as "enroll:", separate from "stepup:", so a wrong enrollment code does
// not spend the budget that guards disabling a factor.
func TestEmailFactor_ConfirmationIsBudgetedOnTheSessionPath(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
	e := enrolUser(t, h, "user@example.com", "a good password")

	require.Equal(t, http.StatusOK, e.client.do(http.MethodPost, "/api/auth/2fa/email/start",
		map[string]string{"password": "a good password"}).Code)

	last := 0
	for range 8 {
		last = e.client.do(http.MethodPost, "/api/auth/2fa/email/confirm",
			map[string]string{"code": "000000"}).Code
		if last == http.StatusTooManyRequests {
			break
		}
		require.Equal(t, http.StatusUnauthorized, last, "a wrong code is a 401 until the budget runs out")
	}
	assert.Equal(t, http.StatusTooManyRequests, last,
		"unlimited guesses against a mailed enrollment code")

	var confirmed bool
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM email_factor WHERE confirmed_at IS NOT NULL)`).Scan(&confirmed))
	assert.False(t, confirmed)

	// The step-up budget is a DIFFERENT bucket: burning the enrollment one must
	// not lock the account out of disabling a factor it already holds.
	rec := e.client.do(http.MethodPost, "/api/auth/2fa/recovery-codes/regenerate",
		map[string]string{"password": "a good password", "code": codeNextStep(t, e.secret)})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}
