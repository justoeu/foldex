//go:build integration

package auth_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/auth"
	"foldex/internal/mailer"
	"foldex/internal/roleperm"
	"foldex/internal/testdb"
)

func (c *client) doBrokenJSON(method, path string) *httptest.ResponseRecorder {
	c.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(`{`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:1234"
	for k, v := range c.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	if csrf, ok := c.cookies[auth.CookieCSRF]; ok && csrf != "" {
		req.Header.Set(auth.CSRFHeader, csrf)
	}
	rec := httptest.NewRecorder()
	c.h.router.ServeHTTP(rec, req)
	return rec
}

func TestCoverage_ConfirmTOTPWithoutEnrollment(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password",
	}).Code)

	rec := c.do(http.MethodPost, "/api/auth/2fa/totp/confirm", map[string]string{"code": "123456"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "no_enrollment", errCode(t, rec))
}

func TestCoverage_ConfirmTOTPMalformedJSON(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password",
	}).Code)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/2fa/totp/start",
		map[string]string{"password": "a good password"}).Code)

	rec := c.doBrokenJSON(http.MethodPost, "/api/auth/2fa/totp/confirm")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCoverage_ConfirmTOTPWhenAlreadyEnabled(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")

	rec := e.client.do(http.MethodPost, "/api/auth/2fa/totp/confirm",
		map[string]string{"code": codeNextStep(t, e.secret)})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "totp_already_enabled", errCode(t, rec))
}

func TestCoverage_Verify2FAMalformedJSON(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")
	e.client.do(http.MethodPost, "/api/auth/logout", nil)

	victim := h.client(t)
	require.Equal(t, http.StatusOK, victim.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password",
	}).Code)
	require.NotEmpty(t, victim.cookies[auth.CookiePreAuth])

	rec := victim.doBrokenJSON(http.MethodPost, "/api/auth/2fa/verify")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCoverage_SendEmailOTPUnavailableWithoutEnrollment(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true, SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	e := enrolUser(t, h, "admin@example.com", "a good password")
	e.client.do(http.MethodPost, "/api/auth/logout", nil)

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password",
	}).Code)

	rec := c.do(http.MethodPost, "/api/auth/2fa/email", nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "email_factor_unavailable", errCode(t, rec))
}

func TestCoverage_AdminListUsersCursorAndBadUpdate(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	uid := testdb.SeedUser(t, h.pool, "other@example.com", "editor")

	rec := admin.do(http.MethodGet, "/api/admin/users?limit=1&after=1", nil)
	assert.Equal(t, http.StatusOK, rec.Code)

	rec = admin.do(http.MethodPatch, fmt.Sprintf("/api/admin/users/%d", int64(uid)),
		map[string]string{"status": "nope"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_status", errCode(t, rec))

	rec = admin.do(http.MethodPatch, fmt.Sprintf("/api/admin/users/%d", int64(uid)),
		map[string]string{"role": "owner"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_role", errCode(t, rec))
}

func TestCoverage_LoginDisabledAccountIsOpaque(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	uid := testdb.SeedUserWithPassword(t, h.pool, "gone@example.com", "a good password", "editor")
	require.Equal(t, http.StatusOK, admin.do(http.MethodPatch,
		fmt.Sprintf("/api/admin/users/%d", int64(uid)), map[string]string{"status": "disabled"}).Code)

	rec := h.client(t).do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "gone@example.com", "password": "a good password",
	})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "invalid_credentials", errCode(t, rec))
}

func TestCoverage_Verify2FADisabledViaSQL(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	uid := testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
	e := enrolUser(t, h, "user@example.com", "a good password")
	e.client.do(http.MethodPost, "/api/auth/logout", nil)

	victim := h.client(t)
	require.Equal(t, http.StatusOK, victim.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "user@example.com", "password": "a good password",
	}).Code)
	require.NotEmpty(t, victim.cookies[auth.CookiePreAuth])

	_, err := h.pool.Exec(context.Background(), `UPDATE app_user SET status = 'disabled' WHERE id = $1`, int64(uid))
	require.NoError(t, err)

	rec := victim.do(http.MethodPost, "/api/auth/2fa/verify",
		map[string]string{"code": codeNextStep(t, e.secret)})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, victim.cookies[auth.CookieAccess])
}

func TestCoverage_Verify2FAExhaustsAttempts(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	enrolUser(t, h, "admin@example.com", "a good password")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password",
	}).Code)

	var last *httptest.ResponseRecorder
	for i := 0; i < 5; i++ {
		last = c.do(http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": "000000"})
	}
	assert.Equal(t, http.StatusTooManyRequests, last.Code)
	assert.Equal(t, "too_many_attempts", errCode(t, last))
}

func TestCoverage_RevokeSessionsUnknownUser(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	rec := admin.do(http.MethodPost, "/api/admin/users/999999/sessions/revoke", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestCoverage_DisableTOTPMalformedAndMissing(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	rec := c.doBrokenJSON(http.MethodPost, "/api/auth/2fa/totp/disable")
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = c.do(http.MethodPost, "/api/auth/2fa/totp/disable",
		map[string]string{"password": "a good password", "code": "123456"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = c.doBrokenJSON(http.MethodPost, "/api/auth/2fa/recovery-codes/regenerate")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCoverage_TOTPQRRefusesANewSession(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/2fa/totp/start",
		map[string]string{"password": "a good password"}).Code)

	require.Equal(t, http.StatusNoContent, c.do(http.MethodPost, "/api/auth/logout", nil).Code)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password",
	}).Code)

	rec := c.do(http.MethodGet, "/api/auth/2fa/totp/qr.png", nil)
	assert.NotEqual(t, http.StatusOK, rec.Code)
}

func TestCoverage_TransferOwnershipSelfAndUnknown(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	var ownerID int64
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT id FROM app_user WHERE email_normalized = 'owner@example.com'`).Scan(&ownerID))

	rec := admin.do(http.MethodPost, fmt.Sprintf("/api/admin/users/%d/transfer-ownership", ownerID), nil)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "self_target", errCode(t, rec))

	rec = admin.do(http.MethodPost, "/api/admin/users/999999/transfer-ownership", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	rec = admin.do(http.MethodPost, "/api/admin/users/nope/transfer-ownership", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCoverage_UpdateProfileInvalidLocaleAndUsername(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "owner@example.com", "a good password")

	rec := c.doBrokenJSON(http.MethodPatch, "/api/auth/profile")
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = c.do(http.MethodPatch, "/api/auth/profile", map[string]string{"locale": "xx-ZZ"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_locale", errCode(t, rec))

	rec = c.do(http.MethodPatch, "/api/auth/profile", map[string]string{"username": "ab"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_username", errCode(t, rec))
}

func TestCoverage_CreateInviteOwnerRoleAndRevokeUnknown(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "owner@example.com", "a good password")

	rec := admin.do(http.MethodPost, "/api/admin/invites",
		map[string]string{"email": "new@example.com", "role": "owner"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_role", errCode(t, rec))

	rec = admin.doBrokenJSON(http.MethodPost, "/api/admin/invites")
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = admin.do(http.MethodDelete, "/api/admin/invites/nope", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = admin.do(http.MethodGet, "/api/admin/metrics", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	rec = admin.do(http.MethodGet, "/api/admin/roles", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	rec = admin.do(http.MethodGet, "/api/admin/invites", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestCoverage_CancelledContextSurfacesRepoErrors(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	uid := testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := h.repo.GetUser(ctx, uid)
	require.Error(t, err)
	_, err = h.repo.ListUsers(ctx, 0, 0)
	require.Error(t, err)
	_, err = h.repo.ListUsers(ctx, 999, 0)
	require.Error(t, err)
	err = h.repo.EnqueueMail(ctx, mailer.Envelope{Template: "x", To: "a@b.c"}, "en")
	require.Error(t, err)
	_, err = h.repo.UpdateOwnProfile(ctx, uid, nil, nil, nil, nil)
	require.Error(t, err)
	err = h.repo.ChangePassword(ctx, uid, 1, "old", "new-password-ok")
	require.Error(t, err)
	_, _, err = h.repo.IssueSession(ctx, uid, 0, testSessionTTL(), "127.0.0.1", "ua")
	require.Error(t, err)
	_, err = h.repo.ResolveAccess(ctx, "dead")
	require.Error(t, err)
	err = h.repo.Audit(ctx, auth.AuditRecord{Action: "x"})
	require.Error(t, err)
	_, err = h.repo.ListAudit(ctx, auth.AuditFilter{})
	require.Error(t, err)
	_, err = h.repo.AuditStatsSince(ctx, time.Time{})
	require.Error(t, err)
	_, err = h.repo.BlockedIPs(ctx)
	require.Error(t, err)
	_, err = h.repo.AdminCreateUser(ctx, auth.NewUser{Email: "n@e.c", Password: "abcdefgh", Role: "editor"})
	require.Error(t, err)
	_, _, err = h.repo.CreateInvite(ctx, "n@e.c", "editor", uid, time.Hour, auth.MailDraft{})
	require.Error(t, err)
	_, err = h.repo.AcceptInvite(ctx, "dead", "n", "abcdefgh")
	require.Error(t, err)
	err = h.repo.StartTOTPEnrollment(ctx, uid, 0, 1, []byte("c"), []byte("n"))
	require.Error(t, err)
	_, err = h.repo.CreatePasswordReset(ctx, uid, time.Hour, "127.0.0.1", auth.MailDraft{})
	require.Error(t, err)
	_, err = h.repo.NeedsBootstrap(ctx)
	require.Error(t, err)
	_, err = h.repo.ListSessions(ctx, uid, 1)
	require.Error(t, err)
	_, err = h.repo.ListInvites(ctx, 10)
	require.Error(t, err)
	_, err = h.repo.LookupInvite(ctx, "not-a-token")
	require.Error(t, err)
	err = h.repo.RevokeInvite(ctx, 1)
	require.Error(t, err)
	err = h.repo.RevokeSession(ctx, uid, 1, auth.ReasonLogout)
	require.Error(t, err)
	err = h.repo.RevokeAllForUser(ctx, uid, auth.ReasonLogoutAll)
	require.Error(t, err)
	err = h.repo.RevokeFamilyByTokens(ctx, "a", "b", auth.ReasonLogout)
	require.Error(t, err)
	_, err = h.repo.Sweep(ctx, 0)
	require.Error(t, err)
	_, _, err = h.repo.CreateChallenge(ctx, auth.NewChallenge{
		UserID: uid, Purpose: auth.PurposeTOTP, TokenVersion: 1,
	})
	require.Error(t, err)
	_, err = h.repo.ResolveChallenge(ctx, "dead")
	require.Error(t, err)
	_, err = h.repo.BumpChallengeAttempt(ctx, 1)
	require.Error(t, err)
	err = h.repo.ConsumeChallenge(ctx, 1)
	require.Error(t, err)
	_, err = h.repo.LoadTOTPSecret(ctx, uid)
	require.Error(t, err)
	_, err = h.repo.HasConfirmedSecondFactor(ctx, uid, false)
	require.Error(t, err)
	_, err = h.repo.CountRecoveryCodes(ctx, uid)
	require.Error(t, err)
	_, _, err = h.repo.UserForPasswordReset(ctx, "user@example.com")
	require.Error(t, err)
	_, err = h.repo.SweepTwoFactor(ctx, 0)
	require.Error(t, err)
	_, err = h.repo.UsernameAvailable(ctx, uid, "someone")
	require.Error(t, err)
	_, _, err = h.repo.EmailAvailable(ctx, "user@example.com")
	require.Error(t, err)
	_, err = h.repo.TransferOwnership(ctx, uid, uid)
	require.Error(t, err)
	err = h.repo.DeleteUser(ctx, uid)
	require.Error(t, err)
	_, err = h.repo.Metrics(ctx)
	require.Error(t, err)
	_, err = h.repo.Roles(ctx, roleperm.Default())
	require.Error(t, err)
}
