//go:build integration

package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/auth"
	"foldex/internal/pkg/authctx"
	"foldex/internal/testdb"
)

// ─────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────

// codeAt produces the TOTP code for a given instant, so tests can move
// deliberately between time steps instead of sleeping 30 seconds.
func codeAt(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	code, err := totp.GenerateCodeCustom(secret, at, totp.ValidateOpts{
		Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	require.NoError(t, err)
	return code
}

func codeNow(t *testing.T, secret string) string { return codeAt(t, secret, time.Now()) }

// nextStep returns an instant in the following 30-second window.
func nextStep(from time.Time) time.Time { return from.Add(31 * time.Second) }

// codeNextStep returns the code for the step AFTER the current one.
//
// Enrolment confirms with the current step's code, and confirming CONSUMES that
// step — that is the replay guard doing its job. So the next verification in a
// test cannot reuse it, and must reach one step forward. It cannot reach two:
// the accepted skew is ±1 step, so a code from now+60s is outside the window
// the server will even look at. In practice this means each test gets exactly
// one successful verification without sleeping through a real window.
func codeNextStep(t *testing.T, secret string) string {
	t.Helper()
	// Anchored on the step BOUNDARY, not on "now + 31s". The naive form lands
	// two steps ahead whenever the current instant falls in the last second of
	// a step — outside the ±1 skew the server accepts, and a one-in-thirty
	// flake that looks exactly like a real rejection.
	base := time.Now().Unix() / 30
	return codeAt(t, secret, time.Unix((base+1)*30, 0))
}

type enrolled struct {
	client *client
	secret string
	codes  []string
}

// enrolUser signs a user in and completes a full TOTP enrollment, returning the
// live client, the seed and the recovery codes.
func enrolUser(t *testing.T, h *harness, email, password string) enrolled {
	t.Helper()
	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": email, "password": password,
	}).Code)

	rec := c.do(http.MethodPost, "/api/auth/2fa/totp/start", map[string]string{"password": password})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var start struct {
		Secret  string `json:"secret"`
		OTPAuth string `json:"otpauth"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &start))
	require.NotEmpty(t, start.Secret)

	rec = c.do(http.MethodPost, "/api/auth/2fa/totp/confirm",
		map[string]string{"code": codeNow(t, start.Secret)})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var confirm struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &confirm))
	require.Len(t, confirm.RecoveryCodes, 10)

	return enrolled{client: c, secret: start.Secret, codes: confirm.RecoveryCodes}
}

// ─────────────────────────────────────────────────────────────────────
// Enrollment
// ─────────────────────────────────────────────────────────────────────

func TestTOTP_EnrollmentRoundTrip(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	e := enrolUser(t, h, "admin@example.com", "a good password")

	rec := e.client.do(http.MethodGet, "/api/auth/2fa", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var status struct {
		Enabled   bool `json:"enabled"`
		Remaining int  `json:"recovery_codes_remaining"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	assert.True(t, status.Enabled)
	assert.Equal(t, 10, status.Remaining)
}

// The seed must never be readable from the database alone: a pg_dump that
// contained plaintext seeds would be a permanent 2FA bypass for every user.
func TestTOTP_SeedIsEncryptedAtRest(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	e := enrolUser(t, h, "admin@example.com", "a good password")

	var ciphertext, nonce []byte
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT secret_ciphertext, secret_nonce FROM totp_secret`).Scan(&ciphertext, &nonce))

	assert.NotContains(t, string(ciphertext), e.secret,
		"the base32 seed appears verbatim in the ciphertext column")
	assert.NotEmpty(t, nonce)

	// And it must round-trip under the right key — otherwise "encrypted" would
	// be indistinguishable from "corrupted".
	plain, err := h.cipher.Decrypt(ciphertext, nonce)
	require.NoError(t, err)
	assert.Equal(t, e.secret, string(plain))
}

// Starting a second enrollment over a CONFIRMED one would be a quiet way to
// swap the second factor of an account whose session was stolen.
func TestTOTP_CannotReEnrolOverAConfirmedFactor(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	rec := e.client.do(http.MethodPost, "/api/auth/2fa/totp/start",
		map[string]string{"password": "a good password"})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "totp_already_enabled", errCode(t, rec))
}

// Enrolment from a live session still demands the password: a hijacked cookie
// must not be enough to attach an authenticator the real owner cannot satisfy.
func TestTOTP_EnrollmentRequiresThePassword(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)

	rec := c.do(http.MethodPost, "/api/auth/2fa/totp/start", map[string]string{"password": "wrong"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	var n int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM totp_secret`).Scan(&n))
	assert.Zero(t, n, "a refused enrollment must not leave a secret behind")
}

func TestTOTP_ConfirmRejectsAWrongCode(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/2fa/totp/start",
		map[string]string{"password": "a good password"}).Code)

	rec := c.do(http.MethodPost, "/api/auth/2fa/totp/confirm", map[string]string{"code": "000000"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// Still unconfirmed, so /me must not claim the account is protected.
	var confirmed *time.Time
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT confirmed_at FROM totp_secret`).Scan(&confirmed))
	assert.Nil(t, confirmed)
}

func TestTOTP_QRIsRenderedAndNotCached(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/2fa/totp/start",
		map[string]string{"password": "a good password"}).Code)

	rec := c.do(http.MethodGet, "/api/auth/2fa/totp/qr.png", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	// The QR IS the secret in visual form; a cached copy in a shared browser
	// profile is a copy of the second factor.
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, []byte{0x89, 'P', 'N', 'G'}, rec.Body.Bytes()[:4])
}

// ─────────────────────────────────────────────────────────────────────
// The login divert
// ─────────────────────────────────────────────────────────────────────

func TestLogin_WithTOTPStopsAtTheChallenge(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	// A FRESH client: correct password, but now there is a second factor.
	c := h.client(t)
	rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"})
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Status  string   `json:"status"`
		Purpose string   `json:"purpose"`
		Email   string   `json:"email"`
		Methods []string `json:"methods"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "two_factor_required", body.Status)
	assert.Equal(t, "totp", body.Purpose)
	assert.Contains(t, body.Methods, "totp")
	// The address is masked: this payload is also what a successful credential
	// stuffing hit sees, and echoing it back confirms the pairing for free.
	assert.NotEqual(t, "admin@example.com", body.Email)
	assert.Contains(t, body.Email, "•")

	// Crucially: NO session cookie, and the data surface stays closed.
	assert.Empty(t, c.cookies[auth.CookieAccess], "a session was issued before the second factor")
	assert.Equal(t, http.StatusUnauthorized, c.do(http.MethodGet, "/api/links", nil).Code)

	// The code completes it.
	rec = c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{
		"code": codeNextStep(t, e.secret)})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotEmpty(t, c.cookies[auth.CookieAccess])
	assert.Equal(t, http.StatusOK, c.do(http.MethodGet, "/api/links", nil).Code)
}

// The pre-auth cookie proves a password and nothing else. If it reached the
// data surface it would BE the session it exists to withhold.
func TestPreAuthCookieDoesNotReachTheDataSurface(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	enrolUser(t, h, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	require.NotEmpty(t, c.cookies[auth.CookiePreAuth])

	for _, path := range []string{"/api/links", "/api/admin/users"} {
		assert.Equal(t, http.StatusUnauthorized, c.do(http.MethodGet, path, nil).Code,
			"pre-auth cookie reached %s", path)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Replay and attempt caps
// ─────────────────────────────────────────────────────────────────────

// A code seen over the user's shoulder must not be spendable a second time
// inside its own 30-second window.
func TestTOTP_CodeCannotBeReplayedWithinItsWindow(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	code := codeNextStep(t, e.secret)

	first := h.client(t)
	require.Equal(t, http.StatusOK, first.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	require.Equal(t, http.StatusOK,
		first.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": code}).Code)

	second := h.client(t)
	require.Equal(t, http.StatusOK, second.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	rec := second.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": code})
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"the same code was accepted twice inside one time step")
	assert.Empty(t, second.cookies[auth.CookieAccess])
}

// The counter guard must not reject the NEXT window — a replay defence that
// also refuses every subsequent code would be a lockout.
//
// Enrolment consumed the current step, so this verification uses the following
// one. That it succeeds is exactly the property under test.
func TestTOTP_NextWindowIsStillAccepted(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)

	rec := c.do(http.MethodPost, "/api/auth/2fa/verify",
		map[string]string{"code": codeNextStep(t, e.secret)})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotEmpty(t, c.cookies[auth.CookieAccess])
}

// The code the user typed to ENROL must not still work afterwards: it is spent
// the moment it proves the enrollment.
func TestTOTP_EnrollmentCodeIsConsumedByConfirming(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	rec := c.do(http.MethodPost, "/api/auth/2fa/totp/start",
		map[string]string{"password": "a good password"})
	require.Equal(t, http.StatusOK, rec.Code)
	var start struct {
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &start))

	enrolCode := codeNow(t, start.Secret)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/2fa/totp/confirm",
		map[string]string{"code": enrolCode}).Code)

	other := h.client(t)
	require.Equal(t, http.StatusOK, other.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	assert.Equal(t, http.StatusUnauthorized,
		other.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": enrolCode}).Code,
		"the enrollment code was still spendable after confirming")
}

// The attempt budget lives in the DATABASE, on auth_challenge.attempts. That is
// the whole point: a restart must not hand an attacker a fresh set of guesses.
func TestTwoFactor_AttemptBudgetIsSpentAndSurvivesARestart(t *testing.T) {
	pool := testdb.Shared(t)
	h := newHarnessWith(t, pool, harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	enrolUser(t, h, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)

	// Four wrong codes, each reporting one fewer attempt left.
	for i := 1; i <= 4; i++ {
		rec := c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": "000000"})
		require.Equal(t, http.StatusUnauthorized, rec.Code, "attempt %d", i)
		var body struct {
			Remaining int `json:"attempts_remaining"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, 5-i, body.Remaining)
	}

	// A "restart": a brand-new handler over the same database, so every
	// in-memory limiter is empty. The budget must NOT come back.
	restarted := newHarnessWith(t, pool, harnessOpts{TwoFactor: true})
	c2 := restarted.client(t)
	c2.cookies[auth.CookiePreAuth] = c.cookies[auth.CookiePreAuth]

	rec := c2.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": "000000"})
	assert.Equal(t, http.StatusTooManyRequests, rec.Code,
		"the attempt budget reset across a restart — it is not durable")
}

// ─────────────────────────────────────────────────────────────────────
// Recovery codes
// ─────────────────────────────────────────────────────────────────────

func TestRecoveryCode_SignsInOnceAndOnlyOnce(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	rec := c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": e.codes[0]})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotEmpty(t, c.cookies[auth.CookieAccess])

	// The same code again must fail — single use is what makes a printed sheet
	// safe to keep.
	c2 := h.client(t)
	require.Equal(t, http.StatusOK, c2.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	assert.Equal(t, http.StatusUnauthorized,
		c2.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": e.codes[0]}).Code)

	// A DIFFERENT code still works.
	assert.Equal(t, http.StatusOK,
		c2.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": e.codes[1]}).Code)
}

// Codes are printed with a hyphen and typed however the user manages.
func TestRecoveryCode_AcceptsLooselyTypedInput(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	messy := "  " + strings.ToLower(strings.ReplaceAll(e.codes[0], "-", " ")) + " "

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	assert.Equal(t, http.StatusOK,
		c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": messy}).Code)
}

// A recovery code belonging to ANOTHER account must never authenticate this one.
// The unique index on code_hash makes the lookup succeed globally; only the
// user_id predicate makes it wrong for the wrong user.
func TestRecoveryCode_IsScopedToItsOwner(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "alice@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "bob@example.com", "a good password", "user")

	alice := enrolUser(t, h, "alice@example.com", "a good password")
	bob := enrolUser(t, h, "bob@example.com", "a good password")
	_ = bob

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "bob@example.com", "password": "a good password"}).Code)

	rec := c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": alice.codes[0]})
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"alice's recovery code authenticated bob")
	assert.Empty(t, c.cookies[auth.CookieAccess])

	// And alice's codes must ALL still be unspent — a failed cross-account
	// attempt that nonetheless burned the real owner's code would be a denial
	// of service against her.
	var spent int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM recovery_code rc
		 JOIN app_user u ON u.id = rc.user_id
		 WHERE u.email = 'alice@example.com' AND rc.used_at IS NOT NULL`).Scan(&spent))
	assert.Zero(t, spent, "bob's attempt consumed one of alice's recovery codes")
}

func TestRecoveryCodes_RegenerateInvalidatesTheOldSheet(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	rec := e.client.do(http.MethodPost, "/api/auth/2fa/recovery-codes/regenerate", map[string]string{
		"password": "a good password",
		"code":     codeNextStep(t, e.secret),
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out.RecoveryCodes, 10)
	assert.NotEqual(t, e.codes[0], out.RecoveryCodes[0])

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	assert.Equal(t, http.StatusUnauthorized,
		c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": e.codes[0]}).Code,
		"an old recovery code survived regeneration")
	assert.Equal(t, http.StatusOK,
		c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": out.RecoveryCodes[0]}).Code)
}

// ─────────────────────────────────────────────────────────────────────
// E-mail OTP
// ─────────────────────────────────────────────────────────────────────

func TestEmailOTP_DeliversACodeThatSignsIn(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	enrolUser(t, h, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/2fa/email", nil).Code)

	msg := h.mail.waitFor(t, "admin@example.com")
	code := extractSixDigits(t, msg.Text)

	rec := c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": code})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotEmpty(t, c.cookies[auth.CookieAccess])
}

// A mailed code is single-use, like every other credential here.
func TestEmailOTP_CannotBeUsedTwice(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	enrolUser(t, h, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	h.mail.reset()
	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/2fa/email", nil).Code)
	code := extractSixDigits(t, h.mail.waitFor(t, "admin@example.com").Text)
	require.Equal(t, http.StatusOK,
		c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": code}).Code)

	c2 := h.client(t)
	require.Equal(t, http.StatusOK, c2.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	assert.Equal(t, http.StatusUnauthorized,
		c2.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": code}).Code)
}

// The resend cooldown is enforced in SQL, so a double-clicked button cannot
// produce two mails. It answers 202 either way — a distinct status would let
// the endpoint be used to probe the send counter.
func TestEmailOTP_ResendIsThrottledButAlwaysAnswers202(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	enrolUser(t, h, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)

	h.mail.reset()
	for i := range 3 {
		assert.Equal(t, http.StatusAccepted,
			c.do(http.MethodPost, "/api/auth/2fa/email", nil).Code, "send %d", i)
	}
	h.mail.waitFor(t, "admin@example.com")

	var sends int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT sends FROM auth_challenge WHERE consumed_at IS NULL`).Scan(&sends))
	assert.Equal(t, 1, sends, "the cooldown did not stop the rapid resends")
}

// ─────────────────────────────────────────────────────────────────────
// Admin enrollment policy
// ─────────────────────────────────────────────────────────────────────

// With the policy on, an admin who has no authenticator is diverted into
// mandatory enrollment rather than refused (which would lock every existing
// admin out the moment the flag flips) or admitted (which would make the rule
// meaningless).
func TestAdminPolicy_AdminWithoutTOTPMustEnrolBeforeGettingASession(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{Require2FAForAdmins: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	c := h.client(t)
	rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"})
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Status  string `json:"status"`
		Purpose string `json:"purpose"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "two_factor_required", body.Status)
	assert.Equal(t, "enroll_2fa", body.Purpose)
	assert.Empty(t, c.cookies[auth.CookieAccess])

	// The enrollment endpoints are reachable with the pre-auth cookie ALONE —
	// the password was proven moments ago to obtain it.
	rec = c.do(http.MethodPost, "/api/auth/2fa/totp/start", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var start struct {
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &start))

	// Confirming completes the login in one step: both factors are now proven.
	rec = c.do(http.MethodPost, "/api/auth/2fa/totp/confirm",
		map[string]string{"code": codeNow(t, start.Secret)})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotEmpty(t, c.cookies[auth.CookieAccess])
	assert.Equal(t, http.StatusOK, c.do(http.MethodGet, "/api/links", nil).Code)
}

// A non-admin is unaffected by the admin policy.
func TestAdminPolicy_PlainUserIsNotForcedToEnrol(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{Require2FAForAdmins: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "user")

	c := h.client(t)
	rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "user@example.com", "password": "a good password"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotEmpty(t, c.cookies[auth.CookieAccess], "a plain user was diverted into enrollment")
}

// An admin must not be able to strip the factor the policy requires.
func TestAdminPolicy_AdminCannotDisableTOTP(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{Require2FAForAdmins: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	rec := c.do(http.MethodPost, "/api/auth/2fa/totp/start", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var start struct {
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &start))
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/2fa/totp/confirm",
		map[string]string{"code": codeNow(t, start.Secret)}).Code)

	rec = c.do(http.MethodPost, "/api/auth/2fa/totp/disable", map[string]string{
		"password": "a good password",
		"code":     codeNextStep(t, start.Secret),
	})
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "totp_required_for_admins", errCode(t, rec))
}

// A plain user may disable it, but only with BOTH proofs.
func TestTOTP_DisableRequiresPasswordAndCode(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "user")
	e := enrolUser(t, h, "user@example.com", "a good password")

	// Right password, wrong code.
	rec := e.client.do(http.MethodPost, "/api/auth/2fa/totp/disable", map[string]string{
		"password": "a good password", "code": "000000"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// Wrong password, right code.
	rec = e.client.do(http.MethodPost, "/api/auth/2fa/totp/disable", map[string]string{
		"password": "nope", "code": codeNextStep(t, e.secret)})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// Both correct.
	rec = e.client.do(http.MethodPost, "/api/auth/2fa/totp/disable", map[string]string{
		"password": "a good password",
		"code":     codeNextStep(t, e.secret),
	})
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	// Recovery codes go with it: they exist only to bypass a factor that is now
	// gone, and leaving them behind keeps long-lived bearer credentials alive.
	var n int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM recovery_code`).Scan(&n))
	assert.Zero(t, n, "recovery codes outlived the second factor they protected")
}

// sixDigits finds the OTP inside a mail body. Anchored on a word boundary so a
// timestamp or a port number elsewhere in the text cannot match.
var sixDigits = regexp.MustCompile(`\b\d{6}\b`)

func extractSixDigits(t *testing.T, body string) string {
	t.Helper()
	m := sixDigits.FindString(body)
	require.NotEmpty(t, m, "no six-digit code in mail body: %q", body)
	return m
}

// TestRecoveryCode_WithSixDigitsIsNotMistakenForATOTPCode locks the routing rule
// that decides which credential the user just typed.
//
// Recovery codes are ten symbols from a 32-character alphabet, ten of which are
// digits, so roughly one code in twenty-three contains EXACTLY six digits. A
// discriminator that filters the input down to its digits and asks "is it six
// long?" sends those to the TOTP path, where they can never match — the holder
// simply cannot use that code, and nothing in the response explains why.
//
// The case is constructed rather than hoped for: relying on a random code to
// hit the 4% shape is precisely how this reached the suite as a one-in-twenty
// flake instead of a failing test.
func TestRecoveryCode_WithSixDigitsIsNotMistakenForATOTPCode(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	// Plant a code with exactly six digits among its ten symbols. It is stored
	// the way the product stores one: sha256 of the normalized form.
	const planted = "1A2B3-4C5D6"
	normalized := "1A2B34C5D6"
	sum := sha256.Sum256([]byte(normalized))
	_, err := h.pool.Exec(context.Background(),
		`INSERT INTO recovery_code (user_id, code_hash)
		 SELECT id, $1 FROM app_user WHERE email = 'admin@example.com'`, sum[:])
	require.NoError(t, err)
	require.Len(t, strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, planted), 6, "the fixture must actually have six digits or it tests nothing")
	_ = e

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)

	rec := c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": planted})
	require.Equal(t, http.StatusOK, rec.Code,
		"a recovery code containing six digits was routed to the TOTP path")
	assert.NotEmpty(t, c.cookies[auth.CookieAccess])
}

// ─────────────────────────────────────────────────────────────────────
// One channel must not satisfy both factors
// ─────────────────────────────────────────────────────────────────────

// TestReset_MailboxAloneCannotSatisfyBothFactors is the takeover this PR nearly
// shipped.
//
// Resetting a password correctly diverts a 2FA account into a challenge — the
// reset proves ONE factor. But the e-mail OTP fallback would then mail a code
// to the SAME address the reset link arrived at, so someone who can read the
// mailbox completes both steps on one channel and the second factor buys
// nothing:
//
//	/password/forgot → link → /password/reset → challenge
//	                 → /2fa/email → code in the SAME inbox → /2fa/verify → session
//
// The challenge records that its first factor came from the mailbox, and the
// e-mail factor is refused for it. An authenticator code still works, because
// that is a credential the mailbox does not contain.
func TestReset_MailboxAloneCannotSatisfyBothFactors(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, h.client(t).do(http.MethodPost, "/api/auth/password/forgot",
		map[string]string{"email": "admin@example.com"}).Code)
	token := resetTokenFrom(t, h.mail.waitFor(t, "admin@example.com").Text)

	attacker := h.client(t)
	rec := attacker.do(http.MethodPost, "/api/auth/password/reset", map[string]string{
		"token": token, "password": "attacker chosen password"})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, attacker.cookies[auth.CookieAccess], "the reset alone issued a session")

	// The challenge must not advertise the e-mail factor...
	var body struct {
		Methods []string `json:"methods"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.NotContains(t, body.Methods, "email_otp",
		"a reset-issued challenge offered the very channel the reset came from")

	// ...and must refuse to send one even when asked directly.
	h.mail.reset()
	send := attacker.do(http.MethodPost, "/api/auth/2fa/email", nil)
	assert.Equal(t, http.StatusForbidden, send.Code)
	assert.Equal(t, "email_factor_unavailable", errCode(t, send))

	time.Sleep(150 * time.Millisecond)
	assert.Empty(t, h.mail.all(), "a sign-in code was mailed for a reset-issued challenge")
	assert.Empty(t, attacker.cookies[auth.CookieAccess])

	// The authenticator still finishes it — the fix closes a channel, not the
	// flow.
	require.Equal(t, http.StatusOK, attacker.do(http.MethodPost, "/api/auth/2fa/verify",
		map[string]string{"code": codeNextStep(t, e.secret)}).Code)
	assert.NotEmpty(t, attacker.cookies[auth.CookieAccess])
}

// An ordinary password login DOES get the e-mail factor: there the first factor
// was the password, so the mailbox is still an independent channel.
func TestLogin_OffersTheEmailFactorWhenTheFirstFactorWasThePassword(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	enrolUser(t, h, "admin@example.com", "a good password")

	c := h.client(t)
	rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"})
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Methods []string `json:"methods"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Contains(t, body.Methods, "email_otp")
	assert.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/2fa/email", nil).Code)
}

// The `log` mail driver prints the message body to stdout. That is a documented,
// deliberate trade for INVITE links — on an instance with no SMTP the log IS
// the mailbox — but a second factor written to the container log is readable by
// anyone with the docker group or a log shipper, so it stops being a factor.
func TestEmailOTP_IsNotOfferedWhenMailOnlyGoesToTheLog(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true}) // log driver
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	enrolUser(t, h, "admin@example.com", "a good password")

	c := h.client(t)
	rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"})
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Methods []string `json:"methods"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.NotContains(t, body.Methods, "email_otp")

	h.mail.reset()
	assert.Equal(t, http.StatusForbidden, c.do(http.MethodPost, "/api/auth/2fa/email", nil).Code)
	time.Sleep(150 * time.Millisecond)
	assert.Empty(t, h.mail.all(), "a sign-in code was written to the log")
}

// A challenge is minted before the second factor; an administrator who disables
// the account in that window expects the half-finished login to die with it.
func TestVerify2FA_RefusesAnAccountDisabledMidChallenge(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	uid := testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "user")
	e := enrolUser(t, h, "user@example.com", "a good password")

	victim := h.client(t)
	require.Equal(t, http.StatusOK, victim.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "user@example.com", "password": "a good password"}).Code)
	require.NotEmpty(t, victim.cookies[auth.CookiePreAuth])

	// Disabled AFTER the challenge was minted.
	require.Equal(t, http.StatusOK, admin.do(http.MethodPatch,
		fmt.Sprintf("/api/admin/users/%d", int64(uid)), map[string]string{"status": "disabled"}).Code)

	rec := victim.do(http.MethodPost, "/api/auth/2fa/verify",
		map[string]string{"code": codeNextStep(t, e.secret)})
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"a challenge outlived the account it belonged to")
	assert.Empty(t, victim.cookies[auth.CookieAccess])
}

// Touching the wrong endpoint must not DESTROY a live challenge — an
// enroll_2fa user who lands on /2fa/verify should still be able to enrol.
func TestChallenge_WrongEndpointDoesNotKillIt(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{Require2FAForAdmins: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	preAuth := c.cookies[auth.CookiePreAuth]
	require.NotEmpty(t, preAuth)

	rec := c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": "123456"})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "wrong_challenge", errCode(t, rec))
	assert.Equal(t, preAuth, c.cookies[auth.CookiePreAuth], "the pre-auth cookie was cleared")

	// And the enrollment it was actually for still works.
	assert.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/2fa/totp/start", nil).Code)
}

// The raw pre-auth token lives in an httpOnly cookie so JS cannot read it.
// Echoing it in the body would hand it straight back to any script on the page.
func TestChallengeResponseDoesNotEchoTheRawPreAuthToken(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	enrolUser(t, h, "admin@example.com", "a good password")

	c := h.client(t)
	rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"})
	require.Equal(t, http.StatusOK, rec.Code)

	cookie := c.cookies[auth.CookiePreAuth]
	require.NotEmpty(t, cookie)
	assert.NotContains(t, rec.Body.String(), cookie,
		"the pre-auth token appeared in the response body")
	assert.NotContains(t, rec.Body.String(), "pre_auth")
}

// The session-authenticated step-up paths have no challenge, so nothing bounded
// their guessing until an explicit limiter was added.
func TestStepUp_TOTPGuessingIsCapped(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "user")
	e := enrolUser(t, h, "user@example.com", "a good password")

	for i := range 5 {
		rec := e.client.do(http.MethodPost, "/api/auth/2fa/totp/disable", map[string]string{
			"password": "a good password", "code": "000000"})
		require.Equal(t, http.StatusUnauthorized, rec.Code, "attempt %d", i)
	}

	rec := e.client.do(http.MethodPost, "/api/auth/2fa/totp/disable", map[string]string{
		"password": "a good password", "code": "000000"})
	assert.Equal(t, http.StatusTooManyRequests, rec.Code,
		"TOTP guessing on the step-up path is unbounded")
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
}

// ─────────────────────────────────────────────────────────────────────
// Remaining enrollment edges
// ─────────────────────────────────────────────────────────────────────

// The settings screen hides the "turn off" button when `required` is true, so
// the flag has to be right — a wrong one offers a dead end the server refuses.
func TestTwoFactorStatus_ReportsThePolicyPerRole(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{Require2FAForAdmins: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "user")

	read := func(t *testing.T, c *client) (enabled, required bool, remaining int) {
		t.Helper()
		rec := c.do(http.MethodGet, "/api/auth/2fa", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var out struct {
			Enabled   bool `json:"enabled"`
			Required  bool `json:"required"`
			Remaining int  `json:"recovery_codes_remaining"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out.Enabled, out.Required, out.Remaining
	}

	user := h.client(t)
	require.Equal(t, http.StatusOK, user.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "user@example.com", "password": "a good password"}).Code)
	enabled, required, remaining := read(t, user)
	assert.False(t, enabled)
	assert.False(t, required, "the admin policy must not apply to a plain user")
	assert.Zero(t, remaining)

	// An admin diverts into enrollment, so drive it to completion and then read.
	admin := h.client(t)
	require.Equal(t, http.StatusOK, admin.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	rec := admin.do(http.MethodPost, "/api/auth/2fa/totp/start", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var start struct {
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &start))
	require.Equal(t, http.StatusOK, admin.do(http.MethodPost, "/api/auth/2fa/totp/confirm",
		map[string]string{"code": codeNow(t, start.Secret)}).Code)

	enabled, required, remaining = read(t, admin)
	assert.True(t, enabled)
	assert.True(t, required)
	assert.Equal(t, 10, remaining)
}

// The QR is only meaningful for a PENDING enrollment. Serving it afterwards
// would hand out the live second factor in visual form to anyone holding the
// session; serving it before there is one would be a confusing 500.
func TestTOTPQR_OnlyExistsForAPendingEnrollment(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)

	// Before any enrollment.
	assert.Equal(t, http.StatusNotFound, c.do(http.MethodGet, "/api/auth/2fa/totp/qr.png", nil).Code)

	rec := c.do(http.MethodPost, "/api/auth/2fa/totp/start", map[string]string{
		"password": "a good password"})
	require.Equal(t, http.StatusOK, rec.Code)
	var start struct {
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &start))
	require.Equal(t, http.StatusOK, c.do(http.MethodGet, "/api/auth/2fa/totp/qr.png", nil).Code)

	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/2fa/totp/confirm",
		map[string]string{"code": codeNow(t, start.Secret)}).Code)

	// And gone again once confirmed.
	assert.Equal(t, http.StatusNotFound, c.do(http.MethodGet, "/api/auth/2fa/totp/qr.png", nil).Code)
}

func TestConfirmTOTP_RefusesWithoutAndAfterAnEnrollment(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)

	rec := c.do(http.MethodPost, "/api/auth/2fa/totp/confirm", map[string]string{"code": "123456"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "no_enrollment", errCode(t, rec))

	start := c.do(http.MethodPost, "/api/auth/2fa/totp/start", map[string]string{
		"password": "a good password"})
	require.Equal(t, http.StatusOK, start.Code)
	var out struct {
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(start.Body.Bytes(), &out))
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/2fa/totp/confirm",
		map[string]string{"code": codeNow(t, out.Secret)}).Code)

	rec = c.do(http.MethodPost, "/api/auth/2fa/totp/confirm",
		map[string]string{"code": codeNextStep(t, out.Secret)})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "totp_already_enabled", errCode(t, rec))
}

// Regenerating replaces the sheet, so it demands the same two proofs enrolling
// did — a hijacked session alone must not be able to mint a fresh set of
// long-lived bypass credentials and lock the owner's copy out of date.
func TestRegenerateRecoveryCodes_RequiresPasswordAndCode(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	rec := e.client.do(http.MethodPost, "/api/auth/2fa/recovery-codes/regenerate",
		map[string]string{"password": "wrong", "code": codeNextStep(t, e.secret)})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "invalid_credentials", errCode(t, rec))

	rec = e.client.do(http.MethodPost, "/api/auth/2fa/recovery-codes/regenerate",
		map[string]string{"password": "a good password", "code": "000000"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "invalid_code", errCode(t, rec))

	// The old sheet must be untouched by the refused attempts.
	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	assert.Equal(t, http.StatusOK,
		c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": e.codes[0]}).Code)
}

// ─────────────────────────────────────────────────────────────────────
// When the stored secret cannot be used
// ─────────────────────────────────────────────────────────────────────

// The disaster AUTH_ENCRYPTION_KEY's AllowEphemeral:false exists to prevent.
//
// A seed encrypted under one key is unopenable under another, so a rotated or
// regenerated key locks every enrolled user out. What this test pins is the
// BEHAVIOUR when it happens anyway: the login must fail closed — no session, no
// hint that the server's own state is broken — because the caller is
// unauthenticated and cannot be told anything useful.
func TestTOTP_UndecryptableSeedFailsClosed(t *testing.T) {
	pool := testdb.Shared(t)
	h := newHarnessWith(t, pool, harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	// A second stack over the SAME database with a DIFFERENT encryption key —
	// exactly what a regenerated key file produces on the next boot.
	rotated := newHarnessWith(t, pool, harnessOpts{TwoFactor: true, CipherSeed: 99})
	c := rotated.client(t)
	rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"})
	require.Equal(t, http.StatusOK, rec.Code, "the password check must still work")

	rec = c.do(http.MethodPost, "/api/auth/2fa/verify",
		map[string]string{"code": codeNextStep(t, e.secret)})
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"an undecryptable seed must not verify anything")
	assert.Empty(t, c.cookies[auth.CookieAccess])
	// And it must not leak that the server's key is the problem.
	assert.NotContains(t, strings.ToLower(rec.Body.String()), "decrypt")
	assert.NotContains(t, strings.ToLower(rec.Body.String()), "key")
}

// The schema admits SHA256 and 8 digits for a future in which authenticator
// apps honour them; the code refuses today, because every one of them silently
// ignores non-defaults and then produces codes that never validate. Refusing is
// what makes that a loud server-side error instead of a user who simply cannot
// sign in.
func TestTOTP_UnsupportedStoredParametersFailClosed(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	_, err := h.pool.Exec(context.Background(),
		`UPDATE totp_secret SET digits = 8`)
	require.NoError(t, err)

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)

	rec := c.do(http.MethodPost, "/api/auth/2fa/verify",
		map[string]string{"code": codeNextStep(t, e.secret)})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, c.cookies[auth.CookieAccess])

	// A recovery code still works, so the account is not bricked.
	assert.Equal(t, http.StatusOK,
		c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": e.codes[0]}).Code)
}

// Spending a recovery code is either a user with a new phone or an attacker
// holding the printed sheet. Only the owner can tell, and only if told — this
// mail is the entire signal.
func TestRecoveryCode_UseNotifiesTheOwner(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)

	h.mail.reset()
	require.Equal(t, http.StatusOK,
		c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": e.codes[0]}).Code)

	msg := h.mail.waitFor(t, "admin@example.com")
	assert.Contains(t, strings.ToLower(msg.Text), "recovery code")
	// The count is what tells the owner how much of the sheet is gone.
	assert.Contains(t, msg.Text, "9")
	// And no link: an unexpected "click here" mail about your account is the
	// exact shape of a phishing message.
	assert.NotContains(t, msg.Text, "http")
}

// A TOTP sign-in must NOT send that warning — it is the ordinary path, and a
// mail on every login would train the user to ignore the one that matters.
func TestTOTP_SuccessfulSignInSendsNoMail(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	h.mail.reset()
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/2fa/verify",
		map[string]string{"code": codeNextStep(t, e.secret)}).Code)

	time.Sleep(200 * time.Millisecond)
	assert.Empty(t, h.mail.all(), "an ordinary TOTP sign-in sent a recovery-code warning")
}

// The send budget has TWO limits — a 60-second interval and a hard cap of three
// — and only the interval was proven. The cap is what stops a patient attacker
// from mailbombing an address one code per minute for as long as the challenge
// lives.
func TestEmailOTP_TotalSendCapIsEnforced(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	enrolUser(t, h, "admin@example.com", "a good password")
	ctx := context.Background()

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)

	h.mail.reset()
	for i := 1; i <= 3; i++ {
		require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/2fa/email", nil).Code)
		h.mail.waitFor(t, "admin@example.com")
		// Step past the cooldown rather than sleeping a real minute.
		_, err := h.pool.Exec(ctx,
			`UPDATE email_otp SET created_at = created_at - interval '2 minutes'`)
		require.NoError(t, err)
	}

	var sends int
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT sends FROM auth_challenge WHERE consumed_at IS NULL`).Scan(&sends))
	require.Equal(t, 3, sends)

	h.mail.reset()
	// The fourth is refused — and still answers 202, so the endpoint cannot be
	// used to read the counter.
	assert.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/2fa/email", nil).Code)
	time.Sleep(200 * time.Millisecond)
	assert.Empty(t, h.mail.all(), "a fourth code was mailed past the cap")
}

// A botched enrollment — wrong app, closed tab, scanned into the wrong account
// — has to be restartable. Replacing an UNCONFIRMED secret is allowed for
// exactly that reason, and is the mirror image of refusing to replace a
// confirmed one.
func TestTOTP_AnUnconfirmedEnrollmentCanBeRestarted(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)

	start := func() string {
		rec := c.do(http.MethodPost, "/api/auth/2fa/totp/start",
			map[string]string{"password": "a good password"})
		require.Equal(t, http.StatusOK, rec.Code)
		var out struct {
			Secret string `json:"secret"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out.Secret
	}

	first := start()
	second := start()
	assert.NotEqual(t, first, second, "restarting must mint a fresh secret")

	// The abandoned one must be dead — otherwise a QR left on a screen stays
	// live indefinitely.
	rec := c.do(http.MethodPost, "/api/auth/2fa/totp/confirm",
		map[string]string{"code": codeNow(t, first)})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/2fa/totp/confirm",
		map[string]string{"code": codeNow(t, second)}).Code)
}

// Turning it off and back on is a complete cycle a user will actually perform
// (new phone, lost device). Each half is covered; what this adds is that the
// second enrollment is not blocked by remnants of the first.
func TestTOTP_DisableThenReEnrol(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "user")
	e := enrolUser(t, h, "user@example.com", "a good password")

	require.Equal(t, http.StatusNoContent,
		e.client.do(http.MethodPost, "/api/auth/2fa/totp/disable", map[string]string{
			"password": "a good password", "code": codeNextStep(t, e.secret)}).Code)

	// A fresh login must NOT ask for a code any more.
	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "user@example.com", "password": "a good password"}).Code)
	require.NotEmpty(t, c.cookies[auth.CookieAccess], "2FA was disabled but login still diverted")

	// And enrolling again works from scratch, with a brand-new sheet.
	again := enrolUser(t, h, "user@example.com", "a good password")
	assert.NotEqual(t, e.secret, again.secret)
	assert.NotEqual(t, e.codes[0], again.codes[0])
}

// An account with no password credential cannot enrol from a session: the
// endpoint demands the password, and there is none to give. It must say so as a
// credential failure rather than a 500.
func TestTOTP_EnrollmentOnAPasswordlessAccountIsRefused(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)

	// Exactly what an ADR-31 Google conversion leaves behind. Nulling the hash
	// alone is not a state the database permits: an active account must hold a
	// credential, so the identity has to arrive in the same transaction.
	testdb.ConvertToGoogleOnly(t, h.pool, authctx.UserID(1), "admin@example.com", "google-sub-1")

	rec := c.do(http.MethodPost, "/api/auth/2fa/totp/start", map[string]string{"password": "anything"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "invalid_credentials", errCode(t, rec))
}

// Every 2FA handler wraps its database errors and writes a generic envelope,
// and none of those branches is reachable by closing the pool: the session
// middleware resolves first and answers 401, so the handler body never runs.
//
// Dropping the 2FA tables reaches them. The session still resolves — app_user
// and session are untouched — while every query the handler makes fails. It is
// not a contrived state either: a half-applied migration produces exactly this,
// and what must hold is CLAUDE.md §7's rule that a pgx error never reaches a
// client. A 500 is the correct answer; a panic or a leaked driver string is not.
func TestTwoFactorHandlers_DegradeCleanlyWhenTheTablesAreGone(t *testing.T) {
	h := newHarnessWith(t, testdb.New(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	// A live pre-auth cookie, captured before the schema goes away, so the
	// challenge-authenticated routes reach their handlers too.
	pre := h.client(t)
	require.Equal(t, http.StatusOK, pre.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	require.NotEmpty(t, pre.cookies[auth.CookiePreAuth])

	_, err := h.pool.Exec(context.Background(),
		`DROP TABLE totp_secret, recovery_code, email_otp, auth_challenge, password_reset CASCADE`)
	require.NoError(t, err)

	cases := []struct {
		name   string
		c      *client
		method string
		path   string
		body   any
	}{
		{"verify", pre, http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": "123456"}},
		{"email otp", pre, http.MethodPost, "/api/auth/2fa/email", nil},
		{"status", e.client, http.MethodGet, "/api/auth/2fa", nil},
		{"totp start", e.client, http.MethodPost, "/api/auth/2fa/totp/start",
			map[string]string{"password": "a good password"}},
		{"totp qr", e.client, http.MethodGet, "/api/auth/2fa/totp/qr.png", nil},
		{"totp confirm", e.client, http.MethodPost, "/api/auth/2fa/totp/confirm",
			map[string]string{"code": "123456"}},
		{"totp disable", e.client, http.MethodPost, "/api/auth/2fa/totp/disable",
			map[string]string{"password": "a good password", "code": "123456"}},
		{"regenerate codes", e.client, http.MethodPost, "/api/auth/2fa/recovery-codes/regenerate",
			map[string]string{"password": "a good password", "code": "123456"}},
		{"email verify", e.client, http.MethodPost, "/api/auth/email/verify",
			map[string]string{"code": "123456"}},
		{"email resend", e.client, http.MethodPost, "/api/auth/email/resend", nil},
		{"password reset", h.client(t), http.MethodPost, "/api/auth/password/reset",
			map[string]string{"token": "t", "password": "a good password"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.c.do(tc.method, tc.path, tc.body)
			assert.GreaterOrEqual(t, rec.Code, 400, "a broken schema cannot produce a success")
			body := rec.Body.String()
			for _, leak := range []string{"pgx", "pgconn", "does not exist", "SQLSTATE", "relation "} {
				assert.NotContains(t, body, leak,
					"%s leaked driver internals: %s", tc.name, body)
			}
		})
	}

	// And no session was minted along the way.
	assert.Empty(t, pre.cookies[auth.CookieAccess])
}

// A PARTIAL schema failure reaches deeper than the total one above.
//
// Dropping every 2FA table makes the first query in each handler fail, so the
// handler returns before touching anything else. Dropping ONE table lets the
// handler get most of the way through and fail on a later step — which is both
// a more faithful model of a half-applied migration and the only way to
// exercise the error handling that sits after the first hop.
func TestTwoFactorHandlers_DegradeCleanlyOnAPartialSchemaFailure(t *testing.T) {
	t.Run("recovery_code missing", func(t *testing.T) {
		h := newHarnessWith(t, testdb.New(t), harnessOpts{TwoFactor: true})
		require.NoError(t, testdb.Reset(context.Background(), h.pool))
		h.bootstrapAdmin(t, "admin@example.com", "a good password")

		c := h.client(t)
		require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
			"email": "admin@example.com", "password": "a good password"}).Code)
		rec := c.do(http.MethodPost, "/api/auth/2fa/totp/start",
			map[string]string{"password": "a good password"})
		require.Equal(t, http.StatusOK, rec.Code)
		var start struct {
			Secret string `json:"secret"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &start))

		_, err := h.pool.Exec(context.Background(), `DROP TABLE recovery_code CASCADE`)
		require.NoError(t, err)

		// The code verifies and the enrollment is confirmed — then minting the
		// sheet fails. A 500 is right; a 200 with no codes would leave the user
		// believing they have a recovery path they do not have.
		rec = c.do(http.MethodPost, "/api/auth/2fa/totp/confirm",
			map[string]string{"code": codeNow(t, start.Secret)})
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.NotContains(t, rec.Body.String(), "recovery_code")

		// And the status endpoint, which counts them.
		assert.Equal(t, http.StatusInternalServerError,
			c.do(http.MethodGet, "/api/auth/2fa", nil).Code)
	})

	t.Run("email_otp missing", func(t *testing.T) {
		h := newHarnessWith(t, testdb.New(t), harnessOpts{TwoFactor: true, SMTP: true})
		require.NoError(t, testdb.Reset(context.Background(), h.pool))
		h.bootstrapAdmin(t, "admin@example.com", "a good password")
		enrolUser(t, h, "admin@example.com", "a good password")

		c := h.client(t)
		require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
			"email": "admin@example.com", "password": "a good password"}).Code)

		_, err := h.pool.Exec(context.Background(), `DROP TABLE email_otp CASCADE`)
		require.NoError(t, err)

		h.mail.reset()
		rec := c.do(http.MethodPost, "/api/auth/2fa/email", nil)
		assert.GreaterOrEqual(t, rec.Code, 400)
		assert.NotContains(t, rec.Body.String(), "email_otp")

		// Nothing may have been mailed: a code the server could not store is a
		// code that can never be redeemed.
		time.Sleep(150 * time.Millisecond)
		assert.Empty(t, h.mail.all(), "a code was mailed that the server failed to store")

		// The authenticator path still works — one broken table must not take
		// the whole login with it.
		assert.Equal(t, http.StatusUnauthorized,
			c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": "000000"}).Code)
	})

	t.Run("app_user missing", func(t *testing.T) {
		h := newHarnessWith(t, testdb.New(t), harnessOpts{TwoFactor: true, SMTP: true})
		require.NoError(t, testdb.Reset(context.Background(), h.pool))
		h.bootstrapAdmin(t, "admin@example.com", "a good password")
		enrolUser(t, h, "admin@example.com", "a good password")

		c := h.client(t)
		require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
			"email": "admin@example.com", "password": "a good password"}).Code)

		// The challenge survives but the account behind it cannot be loaded.
		_, err := h.pool.Exec(context.Background(),
			`ALTER TABLE app_user RENAME TO app_user_gone`)
		require.NoError(t, err)

		rec := c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": "123456"})
		assert.GreaterOrEqual(t, rec.Code, 400)
		assert.Empty(t, c.cookies[auth.CookieAccess])
		assert.NotContains(t, rec.Body.String(), "app_user")
	})
}
