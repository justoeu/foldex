//go:build integration

package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/auth"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/secrets"
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
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
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

func TestAdminEnrollmentChallengeCannotSurvivePasswordReset(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{Require2FAForAdmins: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	old := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	start := old.do(http.MethodPost, "/api/auth/2fa/totp/start", nil)
	require.Equal(t, http.StatusOK, start.Code, start.Body.String())
	var enrollment struct {
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(start.Body.Bytes(), &enrollment))

	user, err := h.repo.UserByEmail(context.Background(), "admin@example.com")
	require.NoError(t, err)
	reset, err := h.repo.CreatePasswordReset(context.Background(), user.ID, time.Minute, "")
	require.NoError(t, err)
	_, err = h.repo.ConsumePasswordReset(context.Background(), reset, "a reset password")
	require.NoError(t, err)

	rec := old.do(http.MethodPost, "/api/auth/2fa/totp/confirm",
		map[string]string{"code": codeNow(t, enrollment.Secret)})
	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, "challenge_invalid", errCode(t, rec))
	assert.Empty(t, old.cookies[auth.CookieAccess])

	var confirmed bool
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT EXISTS (SELECT 1 FROM totp_secret WHERE user_id = $1 AND confirmed_at IS NOT NULL)`,
		int64(user.ID)).Scan(&confirmed))
	assert.False(t, confirmed, "the old-password challenge enrolled an authenticator after reset")
}

func TestSettingsEnrollmentCannotSurvivePasswordChange(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	invalid := c.do(http.MethodPost, "/api/auth/2fa/totp/start", map[string]string{
		"password": "a good password", "unexpected": "refused",
	})
	assert.Equal(t, http.StatusBadRequest, invalid.Code)
	assert.Equal(t, "invalid_json", errCode(t, invalid))

	start := c.do(http.MethodPost, "/api/auth/2fa/totp/start",
		map[string]string{"password": "a good password"})
	require.Equal(t, http.StatusOK, start.Code, start.Body.String())
	var enrollment struct {
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(start.Body.Bytes(), &enrollment))

	changed := c.do(http.MethodPost, "/api/auth/password/change", map[string]string{
		"current_password": "a good password", "new_password": "a changed password",
	})
	require.Equal(t, http.StatusNoContent, changed.Code, changed.Body.String())

	rec := c.do(http.MethodPost, "/api/auth/2fa/totp/confirm",
		map[string]string{"code": codeNow(t, enrollment.Secret)})
	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, "challenge_invalid", errCode(t, rec))

	var confirmed bool
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT confirmed_at IS NOT NULL FROM totp_secret WHERE user_id = 1`).Scan(&confirmed))
	assert.False(t, confirmed, "an old-password settings proof confirmed a factor")
}

func TestChallengeMutationsCannotCommitAfterCredentialEpochChange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*auth.Repository, int64) error
	}{
		{"attempt", func(repo *auth.Repository, id int64) error {
			_, err := repo.BumpChallengeAttempt(context.Background(), id)
			return err
		}},
		{"send", func(repo *auth.Repository, id int64) error {
			_, err := repo.CreateChallengeEmailOTP(context.Background(), id, []byte("hash"), time.Minute)
			return err
		}},
		{"consume", func(repo *auth.Repository, id int64) error {
			return repo.ConsumeChallenge(context.Background(), id)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
			require.NoError(t, testdb.Reset(context.Background(), h.pool))
			h.bootstrapAdmin(t, "admin@example.com", "a good password")
			user, err := h.repo.UserByEmail(context.Background(), "admin@example.com")
			require.NoError(t, err)
			_, challengeID, err := h.repo.CreateChallenge(context.Background(), auth.NewChallenge{
				UserID: user.ID, Purpose: auth.PurposeTOTP,
				TokenVersion: user.TokenVersion, TTL: time.Minute,
			})
			require.NoError(t, err)

			ctx := context.Background()
			blocker, err := h.pool.Begin(ctx)
			require.NoError(t, err)
			defer func() { _ = blocker.Rollback(ctx) }()
			var locked int64
			require.NoError(t, blocker.QueryRow(ctx,
				`SELECT id FROM app_user WHERE id = $1 FOR UPDATE`, int64(user.ID)).Scan(&locked))

			revokeResult := make(chan error, 1)
			go func() {
				revokeResult <- h.repo.RevokeAllForUser(ctx, user.ID, auth.ReasonLogoutAll)
			}()
			waitForBlockedSQL(t, h.pool, "UPDATE app_user SET token_version = token_version + 1")

			mutationResult := make(chan error, 1)
			go func() { mutationResult <- tc.mutate(h.repo, challengeID) }()
			waitForBlockedSQL(t, h.pool, "SELECT id FROM app_user WHERE id = $1 FOR NO KEY UPDATE")

			require.NoError(t, blocker.Commit(ctx))
			require.NoError(t, <-revokeResult)
			assert.ErrorIs(t, <-mutationResult, auth.ErrChallengeInvalid)
		})
	}
}

func TestIssueSessionCannotCommitAfterCredentialEpochChange(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	user, err := h.repo.UserByEmail(context.Background(), "admin@example.com")
	require.NoError(t, err)

	ctx := context.Background()
	blocker, err := h.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(ctx) }()
	var locked int64
	require.NoError(t, blocker.QueryRow(ctx,
		`SELECT id FROM app_user WHERE id = $1 FOR UPDATE`, int64(user.ID)).Scan(&locked))

	revokeResult := make(chan error, 1)
	go func() { revokeResult <- h.repo.RevokeAllForUser(ctx, user.ID, auth.ReasonLogoutAll) }()
	waitForBlockedSQL(t, h.pool, "UPDATE app_user SET token_version = token_version + 1")

	sessionResult := make(chan error, 1)
	go func() {
		_, _, err := h.repo.IssueSession(ctx, user.ID, user.TokenVersion, auth.DefaultTTL(), "", "")
		sessionResult <- err
	}()
	waitForBlockedSQL(t, h.pool, "SELECT id FROM app_user")

	require.NoError(t, blocker.Commit(ctx))
	require.NoError(t, <-revokeResult)
	assert.ErrorIs(t, <-sessionResult, auth.ErrSessionInvalid)
}

func TestVerify2FA_RollsBackProofAndChallengeWhenSessionIssueFails(t *testing.T) {
	h := newHarnessWith(t, testdb.New(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	enrolled := enrolUser(t, h, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password",
	}).Code)

	var challengeID, counter int64
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT c.id, t.last_used_counter
		FROM auth_challenge c
		JOIN totp_secret t ON t.user_id = c.user_id
		WHERE c.purpose = 'totp' AND c.consumed_at IS NULL`).Scan(&challengeID, &counter))
	_, err := h.pool.Exec(context.Background(), `
		CREATE FUNCTION fail_auth_session_insert() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'forced session failure'; END $$;
		CREATE TRIGGER fail_auth_session_insert
		BEFORE INSERT ON session FOR EACH ROW EXECUTE FUNCTION fail_auth_session_insert()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `
			DROP TRIGGER IF EXISTS fail_auth_session_insert ON session;
			DROP FUNCTION IF EXISTS fail_auth_session_insert()`)
	})

	rec := c.do(http.MethodPost, "/api/auth/2fa/verify",
		map[string]string{"code": codeNextStep(t, enrolled.secret)})
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())

	var consumed bool
	var counterAfter int64
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT c.consumed_at IS NOT NULL, t.last_used_counter
		FROM auth_challenge c
		JOIN totp_secret t ON t.user_id = c.user_id
		WHERE c.id = $1`, challengeID).Scan(&consumed, &counterAfter))
	assert.False(t, consumed, "a failed session insert spent the challenge")
	assert.Equal(t, counter, counterAfter, "a failed session insert spent the TOTP proof")
}

func TestVerify2FA_ReportsProofConsumptionFailureAsInternal(t *testing.T) {
	h := newHarnessWith(t, testdb.New(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	enrolled := enrolUser(t, h, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password",
	}).Code)

	_, err := h.pool.Exec(context.Background(), `
		CREATE FUNCTION fail_totp_proof_update() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'forced TOTP proof failure'; END $$;
		CREATE TRIGGER fail_totp_proof_update
		BEFORE UPDATE ON totp_secret FOR EACH ROW EXECUTE FUNCTION fail_totp_proof_update()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `
			DROP TRIGGER IF EXISTS fail_totp_proof_update ON totp_secret;
			DROP FUNCTION IF EXISTS fail_totp_proof_update()`)
	})

	rec := c.do(http.MethodPost, "/api/auth/2fa/verify",
		map[string]string{"code": codeNextStep(t, enrolled.secret)})
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "forced TOTP proof failure")
}

func TestTwoFactorChallengeCannotIssueSessionAfterPasswordReset(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	enrolled := enrolUser(t, h, "admin@example.com", "a good password")

	old := h.client(t)
	require.Equal(t, http.StatusOK, old.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password",
	}).Code)

	user, err := h.repo.UserByEmail(context.Background(), "admin@example.com")
	require.NoError(t, err)
	reset, err := h.repo.CreatePasswordReset(context.Background(), user.ID, time.Minute, "")
	require.NoError(t, err)
	_, err = h.repo.ConsumePasswordReset(context.Background(), reset, "a reset password")
	require.NoError(t, err)

	rec := old.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": enrolled.codes[0]})
	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, "challenge_invalid", errCode(t, rec))
	assert.Empty(t, old.cookies[auth.CookieAccess])
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
	invalid := c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{
		"code": "000000", "unexpected": "refused",
	})
	assert.Equal(t, http.StatusBadRequest, invalid.Code)
	assert.Equal(t, "invalid_json", errCode(t, invalid))

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

func TestTwoFactor_ReloginDoesNotResetAttemptBudget(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	enrolUser(t, h, "admin@example.com", "a good password")

	first := h.client(t)
	require.Equal(t, http.StatusOK, first.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	for i := 0; i < 4; i++ {
		rec := first.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": "000000"})
		require.Equal(t, http.StatusUnauthorized, rec.Code, "attempt %d", i+1)
	}

	second := h.client(t)
	require.Equal(t, http.StatusOK, second.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	rec := second.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": "000000"})
	assert.Equal(t, http.StatusTooManyRequests, rec.Code,
		"a correct-password relogin restored the five-attempt budget")

	third := h.client(t)
	require.Equal(t, http.StatusOK, third.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	assert.Equal(t, http.StatusTooManyRequests,
		third.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": "000000"}).Code,
		"an exhausted budget became renewable before its window expired")

	_, err := h.pool.Exec(context.Background(),
		`UPDATE auth_challenge SET expires_at = now() - interval '1 second'`)
	require.NoError(t, err)
	fresh := h.client(t)
	require.Equal(t, http.StatusOK, fresh.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	assert.Equal(t, http.StatusUnauthorized,
		fresh.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": "000000"}).Code,
		"an expired window did not receive a fresh budget")
}

func TestCreateChallenge_PreservesLivePurposeState(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	ctx := context.Background()
	user, err := h.repo.UserByEmail(ctx, "admin@example.com")
	require.NoError(t, err)
	uid := user.ID

	firstRaw, firstID, err := h.repo.CreateChallenge(ctx, auth.NewChallenge{
		UserID: uid, Purpose: auth.PurposeTOTP, TokenVersion: user.TokenVersion,
		TTL: time.Minute, MailboxAlreadyProven: true,
	})
	require.NoError(t, err)
	for range 2 {
		_, err = h.repo.BumpChallengeAttempt(ctx, firstID)
		require.NoError(t, err)
	}
	_, err = h.repo.CreateChallengeEmailOTP(ctx, firstID, []byte("hash"), time.Minute)
	require.NoError(t, err)
	var firstExpiry time.Time
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT expires_at FROM auth_challenge WHERE id = $1`, firstID).Scan(&firstExpiry))

	raw, secondID, err := h.repo.CreateChallenge(ctx, auth.NewChallenge{
		UserID: uid, Purpose: auth.PurposeTOTP, TokenVersion: user.TokenVersion, TTL: time.Minute,
	})
	require.NoError(t, err)
	_, err = h.repo.ResolveChallenge(ctx, firstRaw, auth.PurposeTOTP)
	assert.ErrorIs(t, err, auth.ErrChallengeInvalid)
	challenge, err := h.repo.ResolveChallenge(ctx, raw, auth.PurposeTOTP)
	require.NoError(t, err)
	assert.Equal(t, 2, challenge.Attempts)
	assert.Equal(t, 1, challenge.Sends)
	assert.True(t, challenge.MailboxAlreadyProven,
		"relogin discarded proof that the mailbox supplied the first factor")
	var secondExpiry time.Time
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT expires_at FROM auth_challenge WHERE id = $1`, secondID).Scan(&secondExpiry))
	assert.True(t, firstExpiry.Equal(secondExpiry), "relogin restarted the challenge window")

	otherRaw, otherID, err := h.repo.CreateChallenge(ctx, auth.NewChallenge{
		UserID: uid, Purpose: auth.PurposeConvertGoogle, TokenVersion: user.TokenVersion, TTL: time.Minute,
	})
	require.NoError(t, err)
	other, err := h.repo.ResolveChallenge(ctx, otherRaw, auth.PurposeConvertGoogle)
	require.NoError(t, err)
	assert.Zero(t, other.Attempts, "attempts leaked across challenge purposes")
	_, err = h.pool.Exec(ctx, `UPDATE auth_challenge SET expires_at = now() - interval '1 second' WHERE id = $1`, otherID)
	require.NoError(t, err)
	_, err = h.repo.BumpChallengeAttempt(ctx, otherID)
	assert.ErrorIs(t, err, auth.ErrChallengeInvalid, "an expired challenge reserved an attempt")
}

func TestChallengeCredentialEpochInvalidatesOnPasswordChangeAndLogoutAll(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, h *harness, c *client, user auth.User)
	}{
		{
			name: "password change",
			mutate: func(t *testing.T, _ *harness, c *client, _ auth.User) {
				rec := c.do(http.MethodPost, "/api/auth/password/change", map[string]string{
					"current_password": "a good password", "new_password": "a changed password",
				})
				require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
			},
		},
		{
			name: "logout all",
			mutate: func(t *testing.T, h *harness, _ *client, user auth.User) {
				require.NoError(t, h.repo.RevokeAllForUser(context.Background(), user.ID, auth.ReasonLogoutAll))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
			require.NoError(t, testdb.Reset(context.Background(), h.pool))
			c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
			user, err := h.repo.UserByEmail(context.Background(), "admin@example.com")
			require.NoError(t, err)
			raw, _, err := h.repo.CreateChallenge(context.Background(), auth.NewChallenge{
				UserID: user.ID, Purpose: auth.PurposeTOTP,
				TokenVersion: user.TokenVersion, TTL: time.Minute,
			})
			require.NoError(t, err)

			tc.mutate(t, h, c, user)
			_, err = h.repo.ResolveChallenge(context.Background(), raw, auth.PurposeTOTP)
			assert.ErrorIs(t, err, auth.ErrChallengeInvalid)
			_, err = h.repo.BumpChallengeAttempt(context.Background(), 1)
			assert.ErrorIs(t, err, auth.ErrChallengeInvalid)
			assert.ErrorIs(t, h.repo.ConsumeChallenge(context.Background(), 1), auth.ErrChallengeInvalid)
			_, _, err = h.repo.IssueSession(context.Background(), user.ID, user.TokenVersion,
				auth.DefaultTTL(), "", "")
			assert.ErrorIs(t, err, auth.ErrSessionInvalid)
		})
	}
}

func TestCredentialEpochRepositoryRefusalsAreTyped(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	ctx := context.Background()
	user, err := h.repo.UserByEmail(ctx, "admin@example.com")
	require.NoError(t, err)
	stale := user.TokenVersion + 1

	_, _, err = h.repo.CreateChallenge(ctx, auth.NewChallenge{
		UserID: user.ID, Purpose: auth.PurposeTOTP, TokenVersion: stale, TTL: time.Minute,
	})
	assert.ErrorIs(t, err, auth.ErrChallengeInvalid)
	assert.ErrorIs(t, h.repo.StartTOTPEnrollment(ctx, user.ID, stale, 0, []byte("cipher"), []byte("nonce")),
		auth.ErrChallengeInvalid)
	_, _, err = h.repo.CompleteTOTPEnrollment(ctx, user.ID, stale,
		auth.TOTPProof{Counter: 1, Ciphertext: []byte("cipher"), Nonce: []byte("nonce")},
		[][]byte{[]byte("h")}, 0, nil, auth.DefaultTTL(), "", "")
	assert.ErrorIs(t, err, auth.ErrChallengeInvalid)
	_, _, err = h.repo.IssueSession(ctx, user.ID, stale, auth.DefaultTTL(), "", "")
	assert.ErrorIs(t, err, auth.ErrSessionInvalid)

	raw, challengeID, err := h.repo.CreateChallenge(ctx, auth.NewChallenge{
		UserID: user.ID, Purpose: auth.PurposeTOTP, TokenVersion: user.TokenVersion, TTL: time.Minute,
	})
	require.NoError(t, err)
	require.NoError(t, h.repo.StartTOTPEnrollment(ctx, user.ID, user.TokenVersion, 0,
		[]byte("cipher"), []byte("nonce")))
	testdb.SetUserStatus(t, h.pool, user.ID, "disabled")

	_, err = h.repo.ResolveChallenge(ctx, raw, auth.PurposeTOTP)
	assert.ErrorIs(t, err, auth.ErrChallengeInvalid)
	_, err = h.repo.BumpChallengeAttempt(ctx, challengeID)
	assert.ErrorIs(t, err, auth.ErrChallengeInvalid)
	assert.ErrorIs(t, h.repo.ConsumeChallenge(ctx, challengeID), auth.ErrChallengeInvalid)
	_, err = h.repo.CreateChallengeEmailOTP(ctx, challengeID, []byte("hash"), time.Minute)
	assert.ErrorIs(t, err, auth.ErrChallengeInvalid)
	_, _, err = h.repo.CreateChallenge(ctx, auth.NewChallenge{
		UserID: user.ID, Purpose: auth.PurposeTOTP, TokenVersion: user.TokenVersion, TTL: time.Minute,
	})
	assert.ErrorIs(t, err, auth.ErrChallengeInvalid)
	assert.ErrorIs(t, h.repo.StartTOTPEnrollment(ctx, user.ID, user.TokenVersion, 0,
		[]byte("replacement"), []byte("replacement")), auth.ErrChallengeInvalid)
	_, _, err = h.repo.CompleteTOTPEnrollment(ctx, user.ID, user.TokenVersion,
		auth.TOTPProof{Counter: 1, Ciphertext: []byte("cipher"), Nonce: []byte("nonce")},
		[][]byte{[]byte("h")}, 0, nil, auth.DefaultTTL(), "", "")
	assert.ErrorIs(t, err, auth.ErrChallengeInvalid)
	_, _, err = h.repo.IssueSession(ctx, user.ID, user.TokenVersion, auth.DefaultTTL(), "", "")
	assert.ErrorIs(t, err, auth.ErrSessionInvalid)
	testdb.SetUserStatus(t, h.pool, user.ID, "active")

	missing := authctx.UserID(999_999)
	assert.ErrorIs(t, h.repo.ChangePassword(ctx, missing, 1, "old password", "a new password"), auth.ErrNoUser)
	assert.ErrorIs(t, h.repo.RevokeAllForUser(ctx, missing, auth.ReasonLogoutAll), auth.ErrNoUser)
	assert.ErrorIs(t, h.repo.ChangePassword(ctx, user.ID, 1, "wrong password", "a new password"),
		auth.ErrBadCredentials)

	testdb.ConvertToGoogleOnly(t, h.pool, user.ID, "admin@example.com", "google-sub")
	assert.ErrorIs(t, h.repo.ChangePassword(ctx, user.ID, 1, "a good password", "a new password"),
		auth.ErrPasswordMissing)
	_, err = h.repo.CreatePasswordReset(ctx, user.ID, time.Minute, "")
	assert.ErrorIs(t, err, auth.ErrResetInvalid)
}

func TestCredentialEpochRepositorySurfacesDatabaseErrors(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	user, err := h.repo.UserByEmail(context.Background(), "admin@example.com")
	require.NoError(t, err)

	h.pool.Close()
	ctx := context.Background()

	assert.ErrorContains(t,
		h.repo.ChangePassword(ctx, user.ID, 1, "a good password", "a new password"),
		"change password begin")
	_, _, err = h.repo.IssueSession(ctx, user.ID, user.TokenVersion, auth.DefaultTTL(), "", "")
	assert.ErrorContains(t, err, "issue session begin")
	assert.ErrorContains(t,
		h.repo.RevokeAllForUser(ctx, user.ID, auth.ReasonLogoutAll),
		"revoke all begin")
}

func TestChallengeAttemptReservationIsAtomic(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	user, err := h.repo.UserByEmail(context.Background(), "admin@example.com")
	require.NoError(t, err)
	uid := user.ID
	_, id, err := h.repo.CreateChallenge(context.Background(), auth.NewChallenge{
		UserID: uid, Purpose: auth.PurposeTOTP, TokenVersion: user.TokenVersion, TTL: time.Minute,
	})
	require.NoError(t, err)

	const requests = 20
	errs := make(chan error, requests)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := h.repo.BumpChallengeAttempt(context.Background(), id)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	reserved := 0
	exhausted := 0
	for err := range errs {
		switch {
		case err == nil:
			reserved++
		case errors.Is(err, auth.ErrChallengeExhausted):
			exhausted++
		default:
			t.Fatalf("unexpected reservation error: %v", err)
		}
	}
	assert.Equal(t, 5, reserved)
	assert.Equal(t, requests-5, exhausted)

	var attempts int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT attempts FROM auth_challenge WHERE id = $1`, id).Scan(&attempts))
	assert.Equal(t, 5, attempts)
}

func TestChallengeConsumptionHasOneWinner(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	user, err := h.repo.UserByEmail(context.Background(), "admin@example.com")
	require.NoError(t, err)
	uid := user.ID
	_, id, err := h.repo.CreateChallenge(context.Background(), auth.NewChallenge{
		UserID: uid, Purpose: auth.PurposeTOTP, TokenVersion: user.TokenVersion, TTL: time.Minute,
	})
	require.NoError(t, err)

	errs := make(chan error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- h.repo.ConsumeChallenge(context.Background(), id)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	winners := 0
	losers := 0
	for err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, auth.ErrChallengeInvalid):
			losers++
		default:
			t.Fatalf("unexpected consume error: %v", err)
		}
	}
	assert.Equal(t, 1, winners)
	assert.Equal(t, 1, losers)
}

func TestTwoFactor_ParallelValidFactorsIssueOneSession(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	enrolled := enrolUser(t, h, "admin@example.com", "a good password")

	first := h.client(t)
	require.Equal(t, http.StatusOK, first.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	second := h.client(t)
	second.cookies[auth.CookiePreAuth] = first.cookies[auth.CookiePreAuth]

	var before int
	require.NoError(t, h.pool.QueryRow(context.Background(), `SELECT count(*) FROM session`).Scan(&before))
	statuses := make(chan int, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, c := range []*client{first, second} {
		wg.Add(1)
		go func(code string, c *client) {
			defer wg.Done()
			<-start
			statuses <- c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": code}).Code
		}(enrolled.codes[i], c)
	}
	close(start)
	wg.Wait()
	close(statuses)

	successes := 0
	for status := range statuses {
		if status == http.StatusOK {
			successes++
		}
	}
	assert.Equal(t, 1, successes, "more than one request issued a session from one challenge")

	var after int
	require.NoError(t, h.pool.QueryRow(context.Background(), `SELECT count(*) FROM session`).Scan(&after))
	assert.Equal(t, before+1, after)
}

func TestTwoFactor_ConcurrentSameTOTPCounterAcrossIndependentChallengesHasOneWinner(t *testing.T) {
	type verificationEvent struct {
		uid   authctx.UserID
		proof auth.TOTPProof
	}
	verified := make(chan verificationEvent, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseBoth := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseBoth)

	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{
		TwoFactor: true,
		AfterTOTPVerification: func(ctx context.Context, uid authctx.UserID, proof auth.TOTPProof) {
			select {
			case verified <- verificationEvent{uid: uid, proof: proof}:
			case <-ctx.Done():
				return
			}
			select {
			case <-release:
			case <-ctx.Done():
			}
		},
	})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	enrolUser(t, h, "admin@example.com", "a good password")

	user, err := h.repo.UserByEmail(context.Background(), "admin@example.com")
	require.NoError(t, err)
	baseCounter := time.Now().Unix() / 30
	targetCounter := baseCounter + 1
	secret := ""
	code := ""
	for _, candidate := range []string{
		"JBSWY3DPEHPK3PXP",
		"GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ",
		"KRSXG5DSNFXGOIDB",
	} {
		candidateCode := codeAt(t, candidate, time.Unix(targetCounter*30, 0))
		collision := false
		for counter := baseCounter - 1; counter < targetCounter; counter++ {
			if candidateCode == codeAt(t, candidate, time.Unix(counter*30, 0)) {
				collision = true
				break
			}
		}
		if !collision {
			secret = candidate
			code = candidateCode
			break
		}
	}
	require.NotEmpty(t, secret, "test seeds collided across every accepted TOTP counter")
	ciphertext, nonce, err := h.cipher.Encrypt([]byte(secret))
	require.NoError(t, err)
	_, err = h.pool.Exec(context.Background(), `
		UPDATE totp_secret
		SET secret_ciphertext = $2, secret_nonce = $3, last_used_counter = $4
		WHERE user_id = $1`, int64(user.ID), ciphertext, nonce, targetCounter-1)
	require.NoError(t, err)

	clients := []*client{h.client(t), h.client(t)}
	challengeIDs := make([]int64, len(clients))
	for i, c := range clients {
		raw, hash, tokenErr := secrets.NewToken()
		require.NoError(t, tokenErr)
		require.NoError(t, h.pool.QueryRow(context.Background(), `
			INSERT INTO auth_challenge (user_id, token_hash, purpose, token_version, expires_at)
			VALUES ($1, $2, 'totp', $3, now() + interval '10 minutes')
			RETURNING id`, int64(user.ID), hash, user.TokenVersion).Scan(&challengeIDs[i]))
		c.cookies[auth.CookiePreAuth] = raw
	}
	var liveChallenges int
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM auth_challenge
		WHERE id IN ($1, $2) AND consumed_at IS NULL AND expires_at > now()`,
		challengeIDs[0], challengeIDs[1]).Scan(&liveChallenges))
	require.Equal(t, 2, liveChallenges)

	var sessionsBefore int
	var maxSessionID int64
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT count(*), COALESCE(max(id), 0) FROM session`).Scan(&sessionsBefore, &maxSessionID))

	type verifyResult struct {
		index  int
		status int
		body   string
	}
	results := make(chan verifyResult, len(clients))
	var wg sync.WaitGroup
	for i, c := range clients {
		wg.Add(1)
		go func(index int, c *client) {
			defer wg.Done()
			rec := c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": code})
			results <- verifyResult{index: index, status: rec.Code, body: rec.Body.String()}
		}(i, c)
	}

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	for range clients {
		select {
		case event := <-verified:
			assert.Equal(t, user.ID, event.uid)
			assert.Equal(t, targetCounter, event.proof.Counter)
			assert.Equal(t, ciphertext, event.proof.Ciphertext)
			assert.Equal(t, nonce, event.proof.Nonce)
		case <-waitCtx.Done():
			require.FailNow(t, "both requests did not complete TOTP verification", waitCtx.Err().Error())
		}
	}
	select {
	case result := <-results:
		require.FailNow(t, "request completed before proof-consumption release", "result: %+v", result)
	default:
	}

	var counterAtBarrier int64
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT last_used_counter FROM totp_secret WHERE user_id = $1`, int64(user.ID)).Scan(&counterAtBarrier))
	assert.Equal(t, targetCounter-1, counterAtBarrier,
		"cryptographic verification consumed the counter before the transactional update")
	for _, challengeID := range challengeIDs {
		var attempts int
		var consumed bool
		require.NoError(t, h.pool.QueryRow(context.Background(), `
			SELECT attempts, consumed_at IS NOT NULL FROM auth_challenge WHERE id = $1`, challengeID).
			Scan(&attempts, &consumed))
		assert.Equal(t, 1, attempts)
		assert.False(t, consumed, "challenge was consumed before proof-consumption release")
	}
	var sessionsAtBarrier int
	require.NoError(t, h.pool.QueryRow(context.Background(), `SELECT count(*) FROM session`).Scan(&sessionsAtBarrier))
	assert.Equal(t, sessionsBefore, sessionsAtBarrier,
		"session committed before proof-consumption release")

	releaseBoth()
	wg.Wait()
	close(results)

	winner := -1
	loser := -1
	for result := range results {
		switch result.status {
		case http.StatusOK:
			require.Equal(t, -1, winner, "more than one request issued a session")
			winner = result.index
		case http.StatusUnauthorized:
			require.Equal(t, -1, loser, "more than one request lost the counter race")
			loser = result.index
			var payload struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal([]byte(result.body), &payload))
			assert.Equal(t, "invalid_code", payload.Error.Code)
		default:
			t.Fatalf("unexpected verification response %d: %s", result.status, result.body)
		}
	}
	require.NotEqual(t, -1, winner)
	require.NotEqual(t, -1, loser)
	assert.NotEmpty(t, clients[winner].cookies[auth.CookieAccess])
	assert.Empty(t, clients[winner].cookies[auth.CookiePreAuth])
	assert.Empty(t, clients[loser].cookies[auth.CookieAccess])
	assert.NotEmpty(t, clients[loser].cookies[auth.CookiePreAuth])

	assert.Equal(t, http.StatusOK, clients[winner].do(http.MethodGet, "/api/links", nil).Code)
	assert.Equal(t, http.StatusUnauthorized, clients[loser].do(http.MethodGet, "/api/links", nil).Code)

	var sessionsAfter, newSessions int
	require.NoError(t, h.pool.QueryRow(context.Background(), `SELECT count(*) FROM session`).Scan(&sessionsAfter))
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM session WHERE id > $1`, maxSessionID).Scan(&newSessions))
	for i, challengeID := range challengeIDs {
		var attempts int
		var consumed bool
		require.NoError(t, h.pool.QueryRow(context.Background(), `
			SELECT attempts, consumed_at IS NOT NULL FROM auth_challenge WHERE id = $1`, challengeID).
			Scan(&attempts, &consumed))
		assert.Equal(t, 1, attempts)
		assert.Equal(t, i == winner, consumed,
			"challenge consumption did not match the client that received the session")
	}
	var counterAfter int64
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT last_used_counter FROM totp_secret WHERE user_id = $1`, int64(user.ID)).Scan(&counterAfter))
	assert.Equal(t, sessionsBefore+1, sessionsAfter)
	assert.Equal(t, 1, newSessions)
	assert.Equal(t, targetCounter, counterAfter)
	var winningSessionRows int
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM session
		WHERE id > $1 AND access_token_hash = $2 AND revoked_at IS NULL`,
		maxSessionID, secrets.Hash(clients[winner].cookies[auth.CookieAccess])).Scan(&winningSessionRows))
	assert.Equal(t, 1, winningSessionRows,
		"the sole committed session does not belong to the sole protected-action winner")
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

func TestRecoveryCode_DigestRequiresTheServerKey(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	var stored []byte
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT code_hash FROM recovery_code ORDER BY id LIMIT 1`).Scan(&stored))
	rawSHA := sha256.Sum256([]byte(strings.ReplaceAll(e.codes[0], "-", "")))
	assert.NotEqual(t, rawSHA[:], stored,
		"a database reader can enumerate recovery codes without the server key")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	wrongKey := newHarnessWith(t, h.pool, harnessOpts{TwoFactor: true, CipherSeed: 1})
	wrongKeyClient := wrongKey.client(t)
	wrongKeyClient.cookies[auth.CookiePreAuth] = c.cookies[auth.CookiePreAuth]
	assert.Equal(t, http.StatusUnauthorized,
		wrongKeyClient.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": e.codes[0]}).Code,
		"a recovery code verified without the AUTH_ENCRYPTION_KEY that minted its digest")
	assert.Equal(t, http.StatusOK,
		c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": e.codes[0]}).Code,
		"the correct key no longer verifies the recovery code")
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

func TestEmailOTP_QueueFullDoesNotChargeOrPublishACode(t *testing.T) {
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{
		TwoFactor: true, SMTP: true, MailWorkers: 1, MailQueue: 1, MailGate: gate,
	})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	enrolUser(t, h, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password"}).Code)
	saturateMailDispatcher(t, h)

	rec := c.do(http.MethodPost, "/api/auth/2fa/email", nil)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "mail_queue_full", errCode(t, rec))

	var sends, codes int
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT sends FROM auth_challenge WHERE consumed_at IS NULL`).Scan(&sends))
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM email_otp WHERE purpose = 'login_2fa' AND consumed_at IS NULL`).Scan(&codes))
	assert.Zero(t, sends, "queue refusal consumed one of the persistent send budget")
	assert.Zero(t, codes, "queue refusal published a code that cannot be mailed")
}

func TestEmailOTP_DigestRequiresTheServerKey(t *testing.T) {
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

	var stored []byte
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT code_hash FROM email_otp WHERE purpose = 'login_2fa' AND consumed_at IS NULL`).Scan(&stored))
	rawSHA := sha256.Sum256([]byte(code))
	assert.NotEqual(t, rawSHA[:], stored,
		"a database reader can enumerate the six-digit OTP without the server key")

	wrongKey := newHarnessWith(t, h.pool, harnessOpts{TwoFactor: true, SMTP: true, CipherSeed: 1})
	wrongKeyClient := wrongKey.client(t)
	wrongKeyClient.cookies[auth.CookiePreAuth] = c.cookies[auth.CookiePreAuth]
	assert.Equal(t, http.StatusUnauthorized,
		wrongKeyClient.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": code}).Code,
		"an e-mail OTP verified without the AUTH_ENCRYPTION_KEY that minted its digest")
	assert.Equal(t, http.StatusOK,
		c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": code}).Code,
		"the correct key no longer verifies the e-mail OTP")
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

func TestAdminPolicy_BootstrapMustEnrolBeforeGettingASession(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{Require2FAForAdmins: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))

	c := h.client(t)
	rec := c.do(http.MethodPost, "/api/auth/bootstrap", map[string]string{
		"email": "admin@example.com", "name": "Admin", "password": "a good password",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := decode(t, rec)
	assert.Equal(t, "two_factor_required", body["status"])
	assert.Equal(t, "enroll_2fa", body["purpose"])
	assert.Empty(t, c.cookies[auth.CookieAccess], "bootstrap issued an admin session before enrollment")
	assert.NotEmpty(t, c.cookies[auth.CookiePreAuth])
	assert.Equal(t, http.StatusUnauthorized, c.do(http.MethodGet, "/api/links", nil).Code)

	rec = c.do(http.MethodPost, "/api/auth/2fa/totp/start", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var start struct {
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &start))
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/2fa/totp/confirm",
		map[string]string{"code": codeNow(t, start.Secret)}).Code)
	assert.NotEmpty(t, c.cookies[auth.CookieAccess])
	assert.Equal(t, http.StatusOK, c.do(http.MethodGet, "/api/admin/users", nil).Code)
}

func TestAdminPolicy_AdminInviteMustEnrolBeforeGettingASession(t *testing.T) {
	pool := testdb.Shared(t)
	base := newHarnessWith(t, pool, harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), pool))
	base.bootstrapAdmin(t, "owner@example.com", "a good password")
	owner := enrolUser(t, base, "owner@example.com", "a good password")

	strict := newHarnessWith(t, pool, harnessOpts{Require2FAForAdmins: true})
	admin := clientOnHarness(t, strict, owner.client)
	invite := admin.do(http.MethodPost, "/api/admin/invites", map[string]string{
		"email": "invited-admin@example.com", "role": "admin",
	})
	require.Equal(t, http.StatusCreated, invite.Code, invite.Body.String())

	invited := strict.client(t)
	rec := invited.do(http.MethodPost, "/api/auth/invites/accept", map[string]string{
		"token": inviteToken(t, invite), "name": "Invited Admin", "password": "a fresh password",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decode(t, rec)
	assert.Equal(t, "two_factor_required", body["status"])
	assert.Equal(t, "enroll_2fa", body["purpose"])
	assert.Empty(t, invited.cookies[auth.CookieAccess], "admin invite acceptance issued a session")
	assert.NotEmpty(t, invited.cookies[auth.CookiePreAuth])

	rec = invited.do(http.MethodPost, "/api/auth/2fa/totp/start", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var start struct {
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &start))
	require.Equal(t, http.StatusOK, invited.do(http.MethodPost, "/api/auth/2fa/totp/confirm",
		map[string]string{"code": codeNow(t, start.Secret)}).Code)
	assert.NotEmpty(t, invited.cookies[auth.CookieAccess])
	assert.Equal(t, http.StatusOK, invited.do(http.MethodGet, "/api/admin/users", nil).Code)
}

func TestAdminPolicy_PromotionRevokesTheLiveSessionAndRequiresEnrollment(t *testing.T) {
	pool := testdb.Shared(t)
	base := newHarnessWith(t, pool, harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), pool))
	base.bootstrapAdmin(t, "owner@example.com", "a good password")
	owner := enrolUser(t, base, "owner@example.com", "a good password")

	strict := newHarnessWith(t, pool, harnessOpts{Require2FAForAdmins: true})
	admin := clientOnHarness(t, strict, owner.client)
	uid := testdb.SeedUserWithPassword(t, pool, "member@example.com", "a good password", "user")
	member := strict.client(t)
	require.Equal(t, http.StatusOK, member.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "member@example.com", "password": "a good password",
	}).Code)
	require.Equal(t, http.StatusOK, member.do(http.MethodGet, "/api/links", nil).Code)

	rec := admin.do(http.MethodPatch, fmt.Sprintf("/api/admin/users/%d", int64(uid)),
		map[string]string{"role": "admin"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, http.StatusUnauthorized, member.do(http.MethodGet, "/api/links", nil).Code,
		"promotion left a pre-promotion session alive")
	assert.Equal(t, http.StatusUnauthorized, member.do(http.MethodPost, "/api/auth/refresh", nil).Code,
		"promotion left the old refresh credential alive")

	fresh := strict.client(t)
	rec = fresh.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "member@example.com", "password": "a good password",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decode(t, rec)
	assert.Equal(t, "two_factor_required", body["status"])
	assert.Equal(t, "enroll_2fa", body["purpose"])
	assert.Empty(t, fresh.cookies[auth.CookieAccess])
}

func TestAdminPolicy_EnablingPolicyBlocksLegacyAndRefreshedSessionsButKeepsEnrollmentReachable(t *testing.T) {
	pool := testdb.Shared(t)
	loose := newHarnessWith(t, pool, harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), pool))
	legacy := loose.bootstrapAdmin(t, "admin@example.com", "a good password")

	strict := newHarnessWith(t, pool, harnessOpts{Require2FAForAdmins: true})
	legacy = clientOnHarness(t, strict, legacy)
	rec := legacy.do(http.MethodGet, "/api/admin/users", nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "admin_2fa_required", errCode(t, rec))

	// The old session remains sufficient to prove the password and enroll; only
	// admin authority is withheld until the authenticator is confirmed.
	rec = legacy.do(http.MethodPost, "/api/auth/2fa/totp/start", map[string]string{
		"password": "a good password",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	require.Equal(t, http.StatusOK, legacy.do(http.MethodPost, "/api/auth/refresh", nil).Code)
	rec = legacy.do(http.MethodGet, "/api/admin/users", nil)
	assert.Equal(t, http.StatusForbidden, rec.Code, "refresh restored admin authority without confirmed TOTP")
	assert.Equal(t, "admin_2fa_required", errCode(t, rec))

	testdb.SeedUserWithPassword(t, pool, "user@example.com", "a good password", "user")
	plain := strict.client(t)
	require.Equal(t, http.StatusOK, plain.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "user@example.com", "password": "a good password",
	}).Code)
	assert.Equal(t, http.StatusNotFound, plain.do(http.MethodGet, "/api/admin/users", nil).Code,
		"the policy gate changed the non-admin 404 contract")
}

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

func TestConfirmTOTP_SeedReplacementBetweenVerifyAndConfirmCannotActivateTheReplacement(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	uid := testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "user")
	_, sid, err := h.repo.IssueSession(context.Background(), uid, 0, auth.DefaultTTL(), "", "")
	require.NoError(t, err)

	firstCiphertext, firstNonce, err := h.cipher.Encrypt([]byte("JBSWY3DPEHPK3PXP"))
	require.NoError(t, err)
	user, err := h.repo.GetUser(context.Background(), uid)
	require.NoError(t, err)
	require.NoError(t, h.repo.StartTOTPEnrollment(context.Background(), uid, user.TokenVersion, sid,
		firstCiphertext, firstNonce))
	verified, err := h.repo.LoadTOTPSecret(context.Background(), uid)
	require.NoError(t, err)

	replacementCiphertext, replacementNonce, err := h.cipher.Encrypt([]byte("KRSXG5DSNFXGOIDB"))
	require.NoError(t, err)
	require.NoError(t, h.repo.StartTOTPEnrollment(context.Background(), uid, user.TokenVersion, sid,
		replacementCiphertext, replacementNonce))

	_, _, err = h.repo.CompleteTOTPEnrollment(context.Background(), uid, user.TokenVersion,
		auth.TOTPProof{Counter: time.Now().Unix() / 30,
			Ciphertext: verified.Ciphertext, Nonce: verified.Nonce},
		[][]byte{[]byte("h")}, sid, nil, auth.DefaultTTL(), "", "")
	require.ErrorIs(t, err, auth.ErrTOTPEnrollmentChanged,
		"confirmation activated a seed that was not the one verified")

	current, err := h.repo.LoadTOTPSecret(context.Background(), uid)
	require.NoError(t, err)
	assert.False(t, current.Confirmed)
	assert.Equal(t, replacementCiphertext, current.Ciphertext)
	assert.Equal(t, replacementNonce, current.Nonce)
	assert.NotEqual(t, verified.Ciphertext, current.Ciphertext)
}

func TestConfirmTOTP_RollsBackFactorWhenRecoveryCodeWriteFails(t *testing.T) {
	h := newHarnessWith(t, testdb.New(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	rec := c.do(http.MethodPost, "/api/auth/2fa/totp/start", map[string]string{
		"password": "a good password",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var start struct {
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &start))

	_, err := h.pool.Exec(context.Background(), `
		CREATE FUNCTION fail_recovery_code_insert() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'forced recovery-code failure'; END $$;
		CREATE TRIGGER fail_recovery_code_insert
		BEFORE INSERT ON recovery_code FOR EACH ROW EXECUTE FUNCTION fail_recovery_code_insert()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `
			DROP TRIGGER IF EXISTS fail_recovery_code_insert ON recovery_code;
			DROP FUNCTION IF EXISTS fail_recovery_code_insert()`)
	})

	rec = c.do(http.MethodPost, "/api/auth/2fa/totp/confirm",
		map[string]string{"code": codeNow(t, start.Secret)})
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())

	var confirmed bool
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT confirmed_at IS NOT NULL FROM totp_secret WHERE user_id = 1`).Scan(&confirmed))
	assert.False(t, confirmed, "the factor was activated without its recovery codes")
}

func TestConfirmTOTP_SettingsEnrollmentRefusesARevokedSession(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	rec := c.do(http.MethodPost, "/api/auth/2fa/totp/start", map[string]string{
		"password": "a good password",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var start struct {
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &start))

	var sid int64
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT id FROM session WHERE access_token_hash = $1`,
		secrets.Hash(c.cookies[auth.CookieAccess])).Scan(&sid))
	require.NoError(t, h.repo.RevokeSession(context.Background(), authctx.UserID(1), sid, auth.ReasonLogout))

	user, err := h.repo.GetUser(context.Background(), authctx.UserID(1))
	require.NoError(t, err)
	row, err := h.repo.LoadTOTPSecret(context.Background(), user.ID)
	require.NoError(t, err)
	_, _, err = h.repo.CompleteTOTPEnrollment(context.Background(), user.ID, user.TokenVersion,
		auth.TOTPProof{Counter: time.Now().Unix() / 30,
			Ciphertext: row.Ciphertext, Nonce: row.Nonce},
		[][]byte{[]byte("recovery")}, sid, nil, auth.DefaultTTL(), "", "")
	assert.ErrorIs(t, err, auth.ErrSessionInvalid)

	var confirmed bool
	var codes int
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT confirmed_at IS NOT NULL FROM totp_secret WHERE user_id = 1`).Scan(&confirmed))
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM recovery_code WHERE user_id = 1`).Scan(&codes))
	assert.False(t, confirmed)
	assert.Zero(t, codes)
}

func TestConfirmTOTP_SettingsEnrollmentIsBoundToTheStartingSession(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	first := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	rec := first.do(http.MethodPost, "/api/auth/2fa/totp/start", map[string]string{
		"password": "a good password",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	second := h.client(t)
	require.Equal(t, http.StatusOK, second.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password",
	}).Code)

	user, err := h.repo.GetUser(context.Background(), authctx.UserID(1))
	require.NoError(t, err)
	row, err := h.repo.LoadTOTPSecret(context.Background(), user.ID)
	require.NoError(t, err)
	var secondSession int64
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT id FROM session WHERE access_token_hash = $1`,
		secrets.Hash(second.cookies[auth.CookieAccess])).Scan(&secondSession))

	_, _, err = h.repo.CompleteTOTPEnrollment(context.Background(), user.ID, user.TokenVersion,
		auth.TOTPProof{Counter: time.Now().Unix() / 30,
			Ciphertext: row.Ciphertext, Nonce: row.Nonce},
		[][]byte{[]byte("recovery")}, secondSession, nil, auth.DefaultTTL(), "", "")
	assert.ErrorIs(t, err, auth.ErrTOTPEnrollmentChanged)

	current, err := h.repo.LoadTOTPSecret(context.Background(), user.ID)
	require.NoError(t, err)
	assert.False(t, current.Confirmed)
	assert.NotNil(t, current.EnrollmentSessionID)
}

func TestConfirmTOTP_RollsBackEnrollmentWhenMandatoryLoginSessionFails(t *testing.T) {
	h := newHarnessWith(t, testdb.New(t), harnessOpts{TwoFactor: true, Require2FAForAdmins: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password",
	}).Code)
	rec := c.do(http.MethodPost, "/api/auth/2fa/totp/start", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var start struct {
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &start))

	_, err := h.pool.Exec(context.Background(), `
		CREATE FUNCTION fail_enrollment_session_insert() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'forced enrollment session failure'; END $$;
		CREATE TRIGGER fail_enrollment_session_insert
		BEFORE INSERT ON session FOR EACH ROW EXECUTE FUNCTION fail_enrollment_session_insert()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `
			DROP TRIGGER IF EXISTS fail_enrollment_session_insert ON session;
			DROP FUNCTION IF EXISTS fail_enrollment_session_insert()`)
	})

	rec = c.do(http.MethodPost, "/api/auth/2fa/totp/confirm",
		map[string]string{"code": codeNow(t, start.Secret)})
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())

	var confirmed, challengeConsumed bool
	var recoveryCodes int
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT confirmed_at IS NOT NULL FROM totp_secret WHERE user_id = 1`).Scan(&confirmed))
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT consumed_at IS NOT NULL FROM auth_challenge
		WHERE user_id = 1 AND purpose = 'enroll_2fa' ORDER BY id DESC LIMIT 1`).Scan(&challengeConsumed))
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM recovery_code WHERE user_id = 1`).Scan(&recoveryCodes))
	assert.False(t, confirmed)
	assert.False(t, challengeConsumed)
	assert.Zero(t, recoveryCodes)
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

func TestTOTP_DisableRollsBackWhenRecoveryCodeDeletionFails(t *testing.T) {
	h := newHarnessWith(t, testdb.New(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "user")
	e := enrolUser(t, h, "user@example.com", "a good password")

	_, err := h.pool.Exec(context.Background(), `
		CREATE FUNCTION fail_recovery_code_delete() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'forced recovery-code delete failure'; END $$;
		CREATE TRIGGER fail_recovery_code_delete
		BEFORE DELETE ON recovery_code FOR EACH ROW EXECUTE FUNCTION fail_recovery_code_delete()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `
			DROP TRIGGER IF EXISTS fail_recovery_code_delete ON recovery_code;
			DROP FUNCTION IF EXISTS fail_recovery_code_delete()`)
	})

	rec := e.client.do(http.MethodPost, "/api/auth/2fa/totp/disable", map[string]string{
		"password": "a good password", "code": codeNextStep(t, e.secret),
	})
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())

	var confirmed bool
	var codes int
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT confirmed_at IS NOT NULL FROM totp_secret
		WHERE user_id = (SELECT id FROM app_user WHERE email_normalized = 'user@example.com')`).Scan(&confirmed))
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM recovery_code
		WHERE user_id = (SELECT id FROM app_user WHERE email_normalized = 'user@example.com')`).Scan(&codes))
	assert.True(t, confirmed)
	assert.Equal(t, 10, codes)
}

func TestTOTPMutationsRefuseProofFromAStaleCredentialEpoch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*auth.Repository, authctx.UserID, int64, int, auth.TOTPProof) error
	}{
		{"disable", func(repo *auth.Repository, uid authctx.UserID, sid int64, version int, proof auth.TOTPProof) error {
			return repo.DisableTOTP(context.Background(), uid, sid, version, "a good password", proof)
		}},
		{"regenerate", func(repo *auth.Repository, uid authctx.UserID, sid int64, version int, proof auth.TOTPProof) error {
			return repo.RegenerateRecoveryCodes(context.Background(), uid, sid, version,
				"a good password", proof, [][]byte{[]byte("replacement")})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
			require.NoError(t, testdb.Reset(context.Background(), h.pool))
			h.bootstrapAdmin(t, "admin@example.com", "a good password")
			uid := testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "user")
			e := enrolUser(t, h, "user@example.com", "a good password")
			user, err := h.repo.GetUser(context.Background(), uid)
			require.NoError(t, err)
			row, err := h.repo.LoadTOTPSecret(context.Background(), uid)
			require.NoError(t, err)
			require.NotNil(t, row.LastUsedCounter)
			var sid int64
			require.NoError(t, h.pool.QueryRow(context.Background(), `
				SELECT id FROM session WHERE access_token_hash = $1`,
				secrets.Hash(e.client.cookies[auth.CookieAccess])).Scan(&sid))

			require.NoError(t, h.repo.RevokeAllForUser(context.Background(), uid, auth.ReasonLogoutAll))
			err = tc.mutate(h.repo, uid, sid, user.TokenVersion, auth.TOTPProof{
				Counter: *row.LastUsedCounter + 1, Ciphertext: row.Ciphertext, Nonce: row.Nonce,
			})
			assert.ErrorIs(t, err, auth.ErrSessionInvalid)

			current, err := h.repo.LoadTOTPSecret(context.Background(), uid)
			require.NoError(t, err)
			assert.True(t, current.Confirmed)
			var codes int
			require.NoError(t, h.pool.QueryRow(context.Background(), `
				SELECT count(*) FROM recovery_code WHERE user_id = $1`, int64(uid)).Scan(&codes))
			assert.Equal(t, 10, codes)
		})
	}
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
// Recovery codes are sixteen symbols from a 32-character alphabet, ten of which
// are digits, so roughly 18% contain EXACTLY six digits. A
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

	// Plant a code with exactly six digits among its sixteen symbols. It is
	// stored the way the product stores one: a user-bound keyed MAC.
	const planted = "1A2B-3C4D-5EFG-6HJK"
	normalized := strings.ReplaceAll(planted, "-", "")
	digest := h.codeMAC.RecoveryCodeDigest(authctx.UserID(1), normalized)
	_, err := h.pool.Exec(context.Background(),
		`INSERT INTO recovery_code (user_id, code_hash)
		 SELECT id, $1 FROM app_user WHERE email = 'admin@example.com'`, digest)
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

func TestStepUp_ReplayedValidTOTPDoesNotResetTheAttemptBudget(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "user")
	e := enrolUser(t, h, "user@example.com", "a good password")

	// Enrollment spent the current time-step. It still verifies
	// cryptographically, but the repository replay guard must reject it.
	replayed := codeNow(t, e.secret)
	for i := range 5 {
		rec := e.client.do(http.MethodPost, "/api/auth/2fa/totp/disable", map[string]string{
			"password": "a good password", "code": replayed})
		require.Equal(t, http.StatusUnauthorized, rec.Code, "attempt %d", i)
	}

	rec := e.client.do(http.MethodPost, "/api/auth/2fa/totp/disable", map[string]string{
		"password": "a good password", "code": replayed})
	assert.Equal(t, http.StatusTooManyRequests, rec.Code,
		"a replayed valid code kept resetting the step-up budget")
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

	rec := c.do(http.MethodPost, "/api/auth/2fa/totp/confirm", map[string]string{
		"code": "123456", "unexpected": "refused",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_json", errCode(t, rec))

	rec = c.do(http.MethodPost, "/api/auth/2fa/totp/confirm", map[string]string{"code": "123456"})
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

func TestRegenerateRecoveryCodes_RollsBackOldSheetWhenInsertFails(t *testing.T) {
	h := newHarnessWith(t, testdb.New(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	var counterBefore int64
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT last_used_counter FROM totp_secret WHERE user_id = 1`).Scan(&counterBefore))
	_, err := h.pool.Exec(context.Background(), `
		CREATE FUNCTION fail_recovery_code_regeneration() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'forced recovery-code insert failure'; END $$;
		CREATE TRIGGER fail_recovery_code_regeneration
		BEFORE INSERT ON recovery_code FOR EACH ROW EXECUTE FUNCTION fail_recovery_code_regeneration()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `
			DROP TRIGGER IF EXISTS fail_recovery_code_regeneration ON recovery_code;
			DROP FUNCTION IF EXISTS fail_recovery_code_regeneration()`)
	})

	rec := e.client.do(http.MethodPost, "/api/auth/2fa/recovery-codes/regenerate", map[string]string{
		"password": "a good password", "code": codeNextStep(t, e.secret),
	})
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())

	var codes int
	var counterAfter int64
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM recovery_code WHERE user_id = 1`).Scan(&codes))
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT last_used_counter FROM totp_secret WHERE user_id = 1`).Scan(&counterAfter))
	assert.Equal(t, 10, codes)
	assert.Equal(t, counterBefore, counterAfter)
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
		var sends int
		require.NoError(t, h.pool.QueryRow(context.Background(),
			`SELECT sends FROM auth_challenge WHERE consumed_at IS NULL`).Scan(&sends))
		assert.Zero(t, sends, "failed OTP publication consumed the persistent send budget")

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
