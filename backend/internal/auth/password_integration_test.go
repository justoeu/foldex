//go:build integration

package auth_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/auth"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/secrets"
	"foldex/internal/testdb"
)

// resetTokenFrom pulls the raw token out of a fragment link. Fragments are not
// sent in the initial HTTP request, so nginx never sees the credential.
func resetTokenFrom(t *testing.T, body string) string {
	return tokenFromFragment(t, body, "reset")
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

func TestResetPassword_LinkDiesWhenThePasswordChanges(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	token, err := h.repo.CreatePasswordReset(context.Background(), authctx.UserID(1), time.Minute, "")
	require.NoError(t, err)

	rec := c.do(http.MethodPost, "/api/auth/password/change", map[string]string{
		"current_password": "a good password", "new_password": "a changed password",
	})
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	rec = h.client(t).do(http.MethodPost, "/api/auth/password/reset", map[string]string{
		"token": token, "password": "a stale reset password",
	})
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, "reset_invalid", errCode(t, rec))
	assert.Equal(t, http.StatusOK, h.client(t).do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a changed password",
	}).Code)
}

func TestResetPassword_LinkDiesOnCredentialEpochMutations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, admin *client, uid authctx.UserID)
	}{
		{name: "logout all"},
		{
			name: "administrator role change",
			mutate: func(t *testing.T, admin *client, uid authctx.UserID) {
				t.Helper()
				rec := admin.do(http.MethodPatch, fmt.Sprintf("/api/admin/users/%d", int64(uid)),
					map[string]string{"role": "admin"})
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			},
		},
		{
			name: "administrator status changes",
			mutate: func(t *testing.T, admin *client, uid authctx.UserID) {
				t.Helper()
				path := fmt.Sprintf("/api/admin/users/%d", int64(uid))
				rec := admin.do(http.MethodPatch, path, map[string]string{"status": "disabled"})
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				rec = admin.do(http.MethodPatch, path, map[string]string{"status": "active"})
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			},
		},
		{
			name: "administrator session revocation",
			mutate: func(t *testing.T, admin *client, uid authctx.UserID) {
				t.Helper()
				rec := admin.do(http.MethodPost,
					fmt.Sprintf("/api/admin/users/%d/sessions/revoke", int64(uid)), nil)
				require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			admin := h.bootstrapAdmin(t, "admin@example.com", "an admin password")
			uid := testdb.SeedUserWithPassword(t, h.pool, "target@example.com", "a target password", "editor")
			target := h.client(t)
			require.Equal(t, http.StatusOK, target.do(http.MethodPost, "/api/auth/login", map[string]string{
				"email": "target@example.com", "password": "a target password",
			}).Code)
			token, err := h.repo.CreatePasswordReset(context.Background(), uid, time.Minute, "")
			require.NoError(t, err)

			if tc.mutate == nil {
				rec := target.do(http.MethodPost, "/api/auth/logout-all", nil)
				require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
			} else {
				tc.mutate(t, admin, uid)
			}

			rec := h.client(t).do(http.MethodPost, "/api/auth/password/reset", map[string]string{
				"token": token, "password": "a stale reset password",
			})
			assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
			assert.Equal(t, "reset_invalid", errCode(t, rec))
		})
	}
}

func TestPasswordResetCannotCommitAfterConcurrentCredentialEpochBump(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	uid := authctx.UserID(1)
	token, err := h.repo.CreatePasswordReset(context.Background(), uid, time.Minute, "")
	require.NoError(t, err)

	ctx := context.Background()
	blocker, err := h.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(ctx) }()
	var locked int64
	require.NoError(t, blocker.QueryRow(ctx,
		`SELECT id FROM app_user WHERE id = $1 FOR UPDATE`, int64(uid)).Scan(&locked))

	revokeResult := make(chan error, 1)
	go func() { revokeResult <- h.repo.RevokeAllForUser(ctx, uid, auth.ReasonLogoutAll) }()
	waitForBlockedSQL(t, h.pool, "UPDATE app_user SET token_version = token_version + 1")

	resetResult := make(chan error, 1)
	go func() {
		_, err := h.repo.ConsumePasswordReset(ctx, token, "a stale reset password")
		resetResult <- err
	}()
	waitForBlockedSQL(t, h.pool, "SELECT status, token_version FROM app_user WHERE id = $1 FOR NO KEY UPDATE")

	require.NoError(t, blocker.Commit(ctx))
	require.NoError(t, <-revokeResult)
	assert.ErrorIs(t, <-resetResult, auth.ErrResetInvalid)
	assert.Equal(t, http.StatusOK, h.client(t).do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password",
	}).Code)
}

func TestLegacyPasswordResetWithoutCredentialEpochFailsClosed(t *testing.T) {
	h := newHarnessWith(t, testdb.New(t), harnessOpts{})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	uid := testdb.SeedUserWithPassword(t, h.pool, "legacy-reset@example.com", "a good password", "editor")
	raw := "legacy-reset-token"

	_, err := h.pool.Exec(context.Background(),
		`ALTER TABLE password_reset DROP CONSTRAINT password_reset_token_version_present`)
	require.NoError(t, err)
	_, err = h.pool.Exec(context.Background(), `
		INSERT INTO password_reset (user_id, token_hash, expires_at)
		VALUES ($1, $2, now() + interval '10 minutes')`, int64(uid), secrets.Hash(raw))
	require.NoError(t, err)

	_, err = h.repo.ConsumePasswordReset(context.Background(), raw, "a replacement password")
	assert.ErrorIs(t, err, auth.ErrResetInvalid)
	assert.Equal(t, http.StatusOK, h.client(t).do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "legacy-reset@example.com", "password": "a good password",
	}).Code)
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

func TestConcurrentPasswordResetCreationLeavesExactlyOneLiveToken(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	ctx := context.Background()

	blocker, err := h.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(ctx) }()
	var uid int64
	require.NoError(t, blocker.QueryRow(ctx,
		`SELECT id FROM app_user WHERE email = 'admin@example.com' FOR UPDATE`).Scan(&uid))

	type result struct {
		token string
		err   error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			token, err := h.repo.CreatePasswordReset(ctx, authctx.UserID(uid), time.Minute, "")
			results <- result{token: token, err: err}
		}()
	}
	waitForBlockedSQLCount(t, h.pool, "FROM app_user WHERE id = $1 FOR NO KEY UPDATE", 2)
	require.NoError(t, blocker.Commit(ctx))

	tokens := make([]string, 0, 2)
	for range 2 {
		result := <-results
		require.NoError(t, result.err)
		tokens = append(tokens, result.token)
	}
	var live int
	require.NoError(t, h.pool.QueryRow(ctx, `
		SELECT count(*) FROM password_reset
		WHERE user_id = $1 AND consumed_at IS NULL AND expires_at > now()`, uid).Scan(&live))
	assert.Equal(t, 1, live)
	var liveHash []byte
	require.NoError(t, h.pool.QueryRow(ctx, `
		SELECT token_hash FROM password_reset
		WHERE user_id = $1 AND consumed_at IS NULL AND expires_at > now()`, uid).Scan(&liveHash))
	matches := 0
	for _, token := range tokens {
		if secrets.Equal(secrets.Hash(token), liveHash) {
			matches++
		}
	}
	assert.Equal(t, 1, matches, "more than one concurrently minted reset remained live")
}

func TestForgotPassword_QueueFullPreservesTheExistingReset(t *testing.T) {
	gate := make(chan struct{})
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{
		MailWorkers: 1, MailQueue: 1, MailGate: gate,
	})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	oldToken, err := h.repo.CreatePasswordReset(context.Background(), authctx.UserID(1), time.Minute, "")
	require.NoError(t, err)
	saturateMailDispatcher(t, h)

	for range 3 {
		rec := h.client(t).do(http.MethodPost, "/api/auth/password/forgot",
			map[string]string{"email": "admin@example.com"})
		assert.Equal(t, http.StatusAccepted, rec.Code)
	}

	var live int
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM password_reset
		WHERE consumed_at IS NULL AND expires_at > now()`).Scan(&live))
	assert.Equal(t, 1, live, "queue refusal superseded the usable reset")

	close(gate)
	h.mail.waitFor(t, "queued@example.com")
	h.mail.reset()
	rec := h.client(t).do(http.MethodPost, "/api/auth/password/forgot",
		map[string]string{"email": "admin@example.com"})
	require.Equal(t, http.StatusAccepted, rec.Code)
	newToken := resetTokenFrom(t, h.mail.waitFor(t, "admin@example.com").Text)
	assert.NotEqual(t, oldToken, newToken)
	assert.Equal(t, http.StatusOK, h.client(t).do(http.MethodPost, "/api/auth/password/reset",
		map[string]string{"token": newToken, "password": "a brand new password"}).Code,
		"queue-full requests consumed the per-address reset budget")
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
				assert.Empty(t, h.drainMail(t), "mail was sent for %s", tc.name)
			}
		})
	}
}

// A disabled account gets the same 202 and no link: an attacker must not learn
// that an address is registered-but-disabled either.
func TestForgotPassword_DisabledAccountGetsNoLink(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	other := testdb.SeedUserWithPassword(t, h.pool, "other@example.com", "a good password", "editor")
	require.Equal(t, http.StatusOK, admin.do(http.MethodPatch,
		fmt.Sprintf("/api/admin/users/%d", int64(other)), map[string]string{"status": "disabled"}).Code)

	h.mail.reset()
	rec := h.client(t).do(http.MethodPost, "/api/auth/password/forgot",
		map[string]string{"email": "other@example.com"})
	assert.Equal(t, http.StatusAccepted, rec.Code)

	assert.Empty(t, h.drainMail(t), "a disabled account received a reset link")
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
	uid := testdb.SeedUserWithPassword(t, h.pool, "google@example.com", "a good password", "editor")
	// Exactly what an ADR-31 conversion leaves behind: a linked Google identity
	// and no password. Stripping the password ALONE is not a state the database
	// permits — an active account must hold a credential — so a fixture that
	// only nulled the hash would be testing a shape the product cannot reach.
	testdb.ConvertToGoogleOnly(t, h.pool, uid, "google@example.com", "google-sub-1")

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
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
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
	sent := h.drainMail(t)
	assert.NotEmpty(t, sent)
	assert.LessOrEqual(t, len(sent), 3,
		"the per-address cap did not hold: %d mails sent", len(sent))
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
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
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
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
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
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
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
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	_, err := h.pool.Exec(context.Background(), `UPDATE app_user SET email_verified_at = NULL`)
	require.NoError(t, err)

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/email/resend", nil).Code)
	token := verifyTokenFrom(t, h.mail.waitFor(t, "admin@example.com").Text)

	// Deliberately from a client with NO session: the link is followed from a
	// mail client, often on a device that has never signed in.
	require.Equal(t, http.StatusNoContent, h.client(t).do(http.MethodPost,
		"/api/auth/email/verify", map[string]string{"token": token}).Code)

	var verified *time.Time
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT email_verified_at FROM app_user WHERE email = 'admin@example.com'`).Scan(&verified))
	assert.NotNil(t, verified)

	// Single use, like every other credential here.
	assert.Equal(t, http.StatusNotFound, h.client(t).do(http.MethodPost,
		"/api/auth/email/verify", map[string]string{"token": token}).Code)
}

func TestVerifyEmail_QueueFullPreservesTheExistingLink(t *testing.T) {
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{
		SMTP: true, MailWorkers: 1, MailQueue: 1, MailGate: gate,
	})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	_, err := h.pool.Exec(context.Background(), `UPDATE app_user SET email_verified_at = NULL`)
	require.NoError(t, err)

	oldToken, oldHash, err := secrets.NewToken()
	require.NoError(t, err)
	require.NoError(t, h.repo.CreateEmailOTP(context.Background(), authctx.UserID(1), nil,
		auth.OTPPurposeVerifyEmail, oldHash, time.Minute))
	saturateMailDispatcher(t, h)

	rec := c.do(http.MethodPost, "/api/auth/email/resend", nil)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "mail_queue_full", errCode(t, rec))
	assert.Equal(t, http.StatusNoContent, h.client(t).do(http.MethodPost,
		"/api/auth/email/verify", map[string]string{"token": oldToken}).Code,
		"queue refusal invalidated the previously issued verification link")
}

func TestVerifyEmail_RapidResendCoalescesWithoutReplacingTheFirstLink(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	_, err := h.pool.Exec(context.Background(), `UPDATE app_user SET email_verified_at = NULL`)
	require.NoError(t, err)

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/email/resend", nil).Code)
	first := verifyTokenFrom(t, h.mail.waitFor(t, "admin@example.com").Text)
	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/email/resend", nil).Code)
	assert.Len(t, h.drainMail(t), 1, "rapid resend queued a second verification message")
	assert.Equal(t, http.StatusNoContent, h.client(t).do(http.MethodPost,
		"/api/auth/email/verify", map[string]string{"token": first}).Code,
		"rapid resend invalidated the first link")
}

func TestVerifyEmail_RejectsAnUnusableToken(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	_, err := h.pool.Exec(context.Background(), `UPDATE app_user SET email_verified_at = NULL`)
	require.NoError(t, err)

	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/email/resend", nil).Code)
	// One answer for unknown, malformed and empty: distinguishing them would
	// let an unauthenticated caller probe which tokens ever existed.
	for _, bad := range []string{"not-a-real-token", "", "0000000000"} {
		rec := h.client(t).do(http.MethodPost, "/api/auth/email/verify",
			map[string]string{"token": bad})
		assert.Equal(t, http.StatusNotFound, rec.Code, "token %q", bad)
		assert.Equal(t, "verify_invalid", errCode(t, rec))
	}
}

// The token expires. A confirmation link that stayed valid forever would sit in
// a mailbox as a standing way to prove an address the owner may have since
// given up.
func TestVerifyEmail_ExpiredTokenIsRefused(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	_, err := h.pool.Exec(context.Background(), `UPDATE app_user SET email_verified_at = NULL`)
	require.NoError(t, err)

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/email/resend", nil).Code)
	token := verifyTokenFrom(t, h.mail.waitFor(t, "admin@example.com").Text)

	_, err = h.pool.Exec(context.Background(),
		`UPDATE email_otp SET expires_at = now() - interval '1 minute'`)
	require.NoError(t, err)

	rec := h.client(t).do(http.MethodPost, "/api/auth/email/verify",
		map[string]string{"token": token})
	assert.Equal(t, http.StatusNotFound, rec.Code)

	var verified *time.Time
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT email_verified_at FROM app_user WHERE email = 'admin@example.com'`).Scan(&verified))
	assert.Nil(t, verified, "an expired link still verified the address")
}

// verifyTokenFrom pulls the raw token out of the confirmation link.
func verifyTokenFrom(t *testing.T, body string) string {
	return tokenFromFragment(t, body, "verify")
}

func tokenFromFragment(t *testing.T, body, key string) string {
	t.Helper()
	for _, line := range strings.Fields(body) {
		if !strings.HasPrefix(line, "http://") && !strings.HasPrefix(line, "https://") {
			continue
		}
		link, err := url.Parse(line)
		require.NoError(t, err)
		require.Empty(t, link.RawQuery, "credential leaked into initial request query: %q", line)
		params, err := url.ParseQuery(link.Fragment)
		require.NoError(t, err)
		if token := params.Get(key); token != "" {
			return token
		}
	}
	require.FailNow(t, "no fragment credential in mail", "key %q, body %q", key, body)
	return ""
}

// The recipient is whoever is signed in — never a value from the request — so
// /email/resend cannot be turned into a mail relay. And once the address is
// already verified there is nothing to prove, so nothing is sent.
func TestVerifyEmail_ResendIsANoOpOnceVerified(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	h.mail.reset()
	// Bootstrap leaves the address verified, so this is already satisfied.
	assert.Equal(t, http.StatusNoContent, c.do(http.MethodPost, "/api/auth/email/resend", nil).Code)
	assert.Empty(t, h.drainMail(t), "a code was mailed for an already-verified address")
}

// Every repository method in the 2FA layer wraps its database errors, and none
// of those branches is reachable while the database is healthy. Closing the
// pool is the cheapest honest way to exercise all of them at once — and what it
// proves is worth having: each one must return an ERROR, never a zero value
// that a caller could mistake for "no rows" and treat as success.
func TestTwoFactorRepository_SurfacesDatabaseErrors(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	ctx := context.Background()
	uid := authctx.UserID(1)
	h.pool.Close() // every subsequent query fails

	t.Run("challenges", func(t *testing.T) {
		_, _, err := h.repo.CreateChallenge(ctx, auth.NewChallenge{
			UserID: uid, Purpose: auth.PurposeTOTP, TokenVersion: 0, TTL: time.Minute,
		})
		assert.Error(t, err)
		_, err = h.repo.ResolveChallenge(ctx, "whatever")
		assert.Error(t, err)
		_, err = h.repo.BumpChallengeAttempt(ctx, 1)
		assert.Error(t, err)
		assert.Error(t, h.repo.ConsumeChallenge(ctx, 1))
		_, err = h.repo.CreateChallengeEmailOTP(ctx, 1, []byte("hash"), time.Minute)
		assert.Error(t, err)
	})

	t.Run("totp", func(t *testing.T) {
		assert.Error(t, h.repo.StartTOTPEnrollment(ctx, uid, 0, 0, []byte("x"), []byte("y")))
		_, err := h.repo.LoadTOTPSecret(ctx, uid)
		assert.Error(t, err)
		_, _, err = h.repo.CompleteTOTPEnrollment(ctx, uid, 0,
			auth.TOTPProof{Counter: 1, Ciphertext: []byte("x"), Nonce: []byte("y")},
			[][]byte{[]byte("h")}, 0, nil, testSessionTTL(), "", "")
		assert.Error(t, err)
		assert.Error(t, h.repo.ConsumeTOTPProof(ctx, uid, auth.TOTPProof{Counter: 1}))
		assert.Error(t, h.repo.DisableTOTP(ctx, uid, 1, 0, "password", auth.TOTPProof{}))
	})

	t.Run("recovery codes", func(t *testing.T) {
		assert.Error(t, h.repo.RegenerateRecoveryCodes(ctx, uid, 1, 0, "password",
			auth.TOTPProof{}, [][]byte{[]byte("h")}))
		assert.Error(t, h.repo.ConsumeRecoveryCode(ctx, uid, []byte("h")))
		_, err := h.repo.CountRecoveryCodes(ctx, uid)
		assert.Error(t, err)
	})

	t.Run("otp and reset", func(t *testing.T) {
		assert.Error(t, h.repo.CreateEmailOTP(ctx, uid, nil, auth.OTPPurposeLogin2FA, []byte("h"), time.Minute))
		_, err := h.repo.CreateEmailVerification(ctx, uid, time.Minute)
		assert.Error(t, err)
		assert.Error(t, h.repo.ConsumeEmailOTP(ctx, uid, auth.OTPPurposeLogin2FA, []byte("h"), nil))
		_, _, err = h.repo.UserForPasswordReset(ctx, "admin@example.com")
		assert.Error(t, err)
		_, err = h.repo.CreatePasswordReset(ctx, uid, time.Minute, "")
		assert.Error(t, err)
		_, err = h.repo.ConsumePasswordReset(ctx, "tok", "a brand new password")
		assert.Error(t, err)
		_, err = h.repo.ConsumeEmailVerification(ctx, []byte("hash"))
		assert.Error(t, err)
		_, err = h.repo.SweepTwoFactor(ctx, time.Hour)
		assert.Error(t, err)
	})
}

// The "not found" answers must be typed sentinels rather than bare errors, so
// handlers can tell "no such row" from "the database is on fire" — the two
// deserve a 404 and a 500 respectively.
func TestTwoFactorRepository_NotFoundIsTyped(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	ctx := context.Background()
	uid := authctx.UserID(1)
	var sid int64
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT id FROM session WHERE access_token_hash = $1`,
		secrets.Hash(c.cookies[auth.CookieAccess])).Scan(&sid))

	_, err := h.repo.LoadTOTPSecret(ctx, uid)
	assert.ErrorIs(t, err, auth.ErrNoTOTP)

	_, _, err = h.repo.CompleteTOTPEnrollment(ctx, uid, 0,
		auth.TOTPProof{Counter: 1, Ciphertext: []byte("x"), Nonce: []byte("y")},
		[][]byte{[]byte("h")}, sid, nil, testSessionTTL(), "", "")
	assert.ErrorIs(t, err, auth.ErrNoTOTP)
	assert.ErrorIs(t, h.repo.ConsumeTOTPProof(ctx, uid, auth.TOTPProof{Counter: 1}), auth.ErrTOTPReplay)
	assert.ErrorIs(t, h.repo.ConsumeRecoveryCode(ctx, uid, []byte("nope")), auth.ErrBadCredentials)
	assert.ErrorIs(t, h.repo.ConsumeEmailOTP(ctx, uid, auth.OTPPurposeLogin2FA, []byte("nope"), nil),
		auth.ErrBadCredentials)

	_, err = h.repo.ResolveChallenge(ctx, "no such token")
	assert.ErrorIs(t, err, auth.ErrChallengeInvalid)
	_, err = h.repo.ResolveChallenge(ctx, "")
	assert.ErrorIs(t, err, auth.ErrChallengeInvalid)
	_, err = h.repo.BumpChallengeAttempt(ctx, 999_999)
	assert.ErrorIs(t, err, auth.ErrChallengeInvalid)
	assert.ErrorIs(t, h.repo.ConsumeChallenge(ctx, 999_999), auth.ErrChallengeInvalid)
	_, err = h.repo.CreateChallengeEmailOTP(ctx, 999_999, []byte("hash"), time.Minute)
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

func TestLegacyPendingTOTPEnrollmentWithoutEpochFailsClosed(t *testing.T) {
	h := newHarnessWith(t, testdb.New(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	uid := testdb.SeedUserWithPassword(t, h.pool, "legacy-enrollment@example.com", "a good password", "editor")
	ctx := context.Background()
	_, sid, err := h.repo.IssueSession(ctx, uid, 0, testSessionTTL(), "", "")
	require.NoError(t, err)

	_, err = h.pool.Exec(ctx, `ALTER TABLE totp_secret DROP CONSTRAINT totp_secret_pending_epoch_present`)
	require.NoError(t, err)
	_, err = h.pool.Exec(ctx, `
		INSERT INTO totp_secret (user_id, secret_ciphertext, secret_nonce)
		VALUES ($1, $2, $3)`, int64(uid), []byte("cipher"), []byte("nonce"))
	require.NoError(t, err)

	_, _, err = h.repo.CompleteTOTPEnrollment(ctx, uid, 0,
		auth.TOTPProof{Counter: 1, Ciphertext: []byte("cipher"), Nonce: []byte("nonce")},
		[][]byte{[]byte("h")}, sid, nil, testSessionTTL(), "", "")
	assert.ErrorIs(t, err, auth.ErrChallengeInvalid)
	var confirmed bool
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT confirmed_at IS NOT NULL FROM totp_secret WHERE user_id = $1`, int64(uid)).Scan(&confirmed))
	assert.False(t, confirmed)
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

// Spending the token and marking the address verified are ONE statement. Split
// in two, a failure between them burns the token while leaving the address
// unverified — and /email/resend needs a session, so someone following the link
// on a device that never signed in would be stuck with no way to get another.
func TestVerifyEmail_SpendAndMarkAreOneStatement(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	ctx := context.Background()
	_, err := h.pool.Exec(ctx, `UPDATE app_user SET email_verified_at = NULL`)
	require.NoError(t, err)

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/email/resend", nil).Code)
	token := verifyTokenFrom(t, h.mail.waitFor(t, "admin@example.com").Text)

	// Drive the repository directly: the two writes must land together or not
	// at all, with no HTTP layer in between to mask a partial result.
	uid, err := h.repo.ConsumeEmailVerification(ctx, secrets.Hash(token))
	require.NoError(t, err)
	assert.Equal(t, authctx.UserID(1), uid)

	var consumed, verified *time.Time
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT o.consumed_at, u.email_verified_at
		   FROM email_otp o JOIN app_user u ON u.id = o.user_id`).Scan(&consumed, &verified))
	assert.NotNil(t, consumed, "the token was not spent")
	assert.NotNil(t, verified, "the token was spent without the address being marked verified")

	// And it is still single-use.
	_, err = h.repo.ConsumeEmailVerification(ctx, secrets.Hash(token))
	assert.ErrorIs(t, err, auth.ErrBadCredentials)
}
