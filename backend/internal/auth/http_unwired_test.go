package auth

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/mailer"
	"foldex/internal/pkg/attemptlimit"
	"foldex/internal/pkg/authctx"
)

type stubMailer struct{ driver string }

func (s stubMailer) Send(context.Context, mailer.Message) error { return nil }
func (s stubMailer) Driver() string                             { return s.driver }

func chiIDRequest(method, body, id string) *http.Request {
	r := httptest.NewRequest(method, "/x/"+id, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestUnwiredHandlers_RefuseBeforeTheRepo(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.DiscardHandler)
	h := &Handler{
		logger:      log,
		mailer:      stubMailer{driver: "log"},
		bootstrapIP: attemptlimit.New(5, time.Hour),
		loginByIP:   attemptlimit.New(20, time.Minute),
		cookies:     CookieOptions{},
	}
	adminH := &AdminHandler{logger: log}

	t.Run("email factor without MAC", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.StartEmailFactor(rec, httptest.NewRequest(http.MethodPost, "/", nil))
		assert.Equal(t, http.StatusNotImplemented, rec.Code)
		rec = httptest.NewRecorder()
		h.ConfirmEmailFactor(rec, httptest.NewRequest(http.MethodPost, "/", nil))
		assert.Equal(t, http.StatusNotImplemented, rec.Code)
		rec = httptest.NewRecorder()
		h.SendStepUpEmailOTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
		assert.Equal(t, http.StatusConflict, rec.Code)
	})

	t.Run("email factor without SMTP", func(t *testing.T) {
		wired := *h
		wired.codeMAC = &CodeMAC{}
		rec := httptest.NewRecorder()
		wired.StartEmailFactor(rec, httptest.NewRequest(http.MethodPost, "/", nil))
		assert.Equal(t, http.StatusConflict, rec.Code)
	})

	t.Run("login malformed JSON", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{`))
		h.Login(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("bootstrap malformed JSON", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/bootstrap", strings.NewReader(`{`))
		h.Bootstrap(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("bootstrap invalid email", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/bootstrap", strings.NewReader(`{"email":"nope","password":"abcdefgh"}`))
		h.Bootstrap(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("refresh without cookie", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.Refresh(rec, httptest.NewRequest(http.MethodPost, "/refresh", nil))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("pending payload unknown purpose", func(t *testing.T) {
		_, err := h.pendingPayload(context.Background(), User{}, ChallengePurpose("nope"), false)
		require.ErrorIs(t, err, errInvalidChallengePurpose)
		rec := httptest.NewRecorder()
		h.startChallenge(rec, httptest.NewRequest(http.MethodPost, "/", nil), User{}, ChallengePurpose("nope"), false)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("admin invalid ids", func(t *testing.T) {
		for _, fn := range []func(http.ResponseWriter, *http.Request){
			adminH.UpdateUser, adminH.DeleteUser, adminH.RevokeUserSessions, adminH.ForcePasswordReset,
		} {
			rec := httptest.NewRecorder()
			fn(rec, chiIDRequest(http.MethodPost, `{}`, "x"))
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		}
	})

	t.Run("admin update malformed JSON", func(t *testing.T) {
		rec := httptest.NewRecorder()
		adminH.UpdateUser(rec, chiIDRequest(http.MethodPatch, `{`, "1"))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("admin update self demote", func(t *testing.T) {
		role := authctx.RoleEditor
		body := `{"role":"editor"}`
		req := chiIDRequest(http.MethodPatch, body, "7")
		req = req.WithContext(authctx.WithPrincipal(req.Context(), authctx.Principal{UserID: 7, Role: authctx.RoleAdmin}))
		rec := httptest.NewRecorder()
		adminH.UpdateUser(rec, req)
		assert.Equal(t, http.StatusConflict, rec.Code)
		_ = role
	})

	t.Run("admin delete self", func(t *testing.T) {
		req := chiIDRequest(http.MethodDelete, "", "7")
		req = req.WithContext(authctx.WithPrincipal(req.Context(), authctx.Principal{UserID: 7, Role: authctx.RoleAdmin}))
		rec := httptest.NewRecorder()
		adminH.DeleteUser(rec, req)
		assert.Equal(t, http.StatusConflict, rec.Code)
	})

	t.Run("export audit bad filter", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/audit.csv?category=nope", nil)
		adminH.ExportAudit(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("notify reuse uid zero", func(t *testing.T) {
		h.notifyReuse(context.Background(), 0, httptest.NewRequest(http.MethodPost, "/", nil))
	})

	t.Run("disable email factor malformed JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{`))
		req = req.WithContext(authctx.WithPrincipal(req.Context(), authctx.Principal{UserID: 1}))
		rec := httptest.NewRecorder()
		h.DisableEmailFactor(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("verify 2fa without challenge", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.Verify2FA(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"code":"123456"}`)))
		assert.NotEqual(t, http.StatusOK, rec.Code)
	})

	t.Run("send email otp without challenge", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.SendEmailOTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
		assert.NotEqual(t, http.StatusOK, rec.Code)
	})

	t.Run("challengeProof empty code", func(t *testing.T) {
		h.challengeProof(context.Background(), User{ID: 1}, "", nil)
		h.challengeProof(context.Background(), User{ID: 1}, "---", nil)
	})

	t.Run("totp start without session", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.StartTOTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"password":"x"}`)))
		assert.NotEqual(t, http.StatusOK, rec.Code)
	})

	t.Run("totp start session malformed JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{`))
		req = req.WithContext(authctx.WithPrincipal(req.Context(), authctx.Principal{UserID: 1}))
		rec := httptest.NewRecorder()
		h.StartTOTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("totp qr without session", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.TOTPQR(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		assert.NotEqual(t, http.StatusOK, rec.Code)
	})

	t.Run("enrollment session match", func(t *testing.T) {
		assert.True(t, enrollmentSessionMatches(nil, 0))
		assert.False(t, enrollmentSessionMatches(nil, 1))
		id := int64(3)
		assert.True(t, enrollmentSessionMatches(&id, 3))
		assert.False(t, enrollmentSessionMatches(&id, 4))
	})

	t.Run("reserve email confirm session budget", func(t *testing.T) {
		wired := &Handler{
			logger:     log,
			stepUpUser: attemptlimit.New(1, time.Minute),
		}
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req = req.WithContext(authctx.WithPrincipal(req.Context(), authctx.Principal{UserID: 4}))
		rec := httptest.NewRecorder()
		ch, key, ok := wired.reserveEmailFactorConfirm(rec, req, 4)
		assert.True(t, ok)
		assert.Nil(t, ch)
		assert.NotEmpty(t, key)
		rec = httptest.NewRecorder()
		_, _, ok = wired.reserveEmailFactorConfirm(rec, req, 4)
		assert.False(t, ok)
		assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	})
}

func TestEnqueueMail_WithoutOutbox(t *testing.T) {
	t.Parallel()
	err := (&Repository{}).EnqueueMail(context.Background(), mailer.Envelope{}, "en")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without an outbox")
}

func TestCSVSafeAndAuditRow(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", csvSafe(""))
	assert.Equal(t, "ok", csvSafe("ok"))
	for _, lead := range []string{"=", "+", "-", "@", "\t", "\r"} {
		got := csvSafe(lead + "payload")
		assert.True(t, len(got) > 0 && got[0] == '\'', lead)
	}
	email := "a@b.c"
	ref := int64(9)
	row := auditCSVRow(AuditEntry{
		ID: 1, Action: "login.failed", Category: "identity", Severity: "info",
		ActorEmail: &email, ActorRef: &ref, TargetEmail: &email, Detail: &email,
		IP: &email, UserAgent: &email,
	})
	assert.Equal(t, "1", row[0])
	assert.Equal(t, "login.failed", row[2])
	blank := auditCSVRow(AuditEntry{ID: 2, CreatedAt: time.Now()})
	assert.Equal(t, "", blank[5])
}
