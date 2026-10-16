package auth

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/attemptlimit"
	"foldex/internal/pkg/authctx"
	"foldex/internal/testsupport"
)

// Handler branches that answer BEFORE (or by translating) a repository
// failure — decode errors, cursor/limit validation, the anti-enumeration
// 202/404 shapes — are invisible to the integration suite, whose requests
// are well-formed against a live database. A closed pool makes every
// repository call fail deterministically, so each of those translations can
// be asserted at the HTTP boundary where its contract lives.

func closedPoolHandler(t *testing.T) *Handler {
	t.Helper()
	return &Handler{
		repo:         NewRepository(testsupport.ClosedPool(t)),
		logger:       slog.New(slog.DiscardHandler),
		pwResetIP:    attemptlimit.New(10, time.Hour),
		pwResetEmail: attemptlimit.New(3, time.Hour),
	}
}

func principalReq(method, target, body string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	ctx := authctx.WithPrincipal(r.Context(), authctx.Principal{
		UserID: 1, Role: authctx.RoleAdmin, SessionID: 1, Via: authctx.ViaSession,
	})
	return r.WithContext(ctx)
}

func TestListOwnActivityBranches(t *testing.T) {
	h := closedPoolHandler(t)

	t.Run("no principal is 401", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ListOwnActivity(rec, httptest.NewRequest(http.MethodGet, "/api/activity", nil))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	cases := map[string]string{
		"non-numeric before":    "/api/activity?before=abc",
		"non-numeric limit":     "/api/activity?limit=abc",
		"negative limit":        "/api/activity?limit=-1",
		"limit above page size": "/api/activity?limit=201",
		"malformed before_at":   "/api/activity?before_at=banana",
	}
	for name, target := range cases {
		t.Run(name+" is 400", func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ListOwnActivity(rec, principalReq(http.MethodGet, target, ""))
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}

	t.Run("repository failure is 500, not an empty feed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ListOwnActivity(rec, principalReq(http.MethodGet,
			"/api/activity?before=-9&before_at=2026-10-16T12:00:00Z&limit=25", ""))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}

func TestEmailChangeHandlerBranches(t *testing.T) {
	h := closedPoolHandler(t)

	t.Run("request: malformed JSON is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.RequestEmailChange(rec, principalReq(http.MethodPost, "/x", "{oops"))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("request: invalid address is refused before any password check", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.RequestEmailChange(rec, principalReq(http.MethodPost, "/x",
			`{"new_email":"not-an-email","password":"whatever-123"}`))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("get: repository failure is 500", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.GetEmailChange(rec, principalReq(http.MethodGet, "/x", ""))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("cancel: repository failure is 500", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.CancelEmailChange(rec, principalReq(http.MethodDelete, "/x", ""))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("confirm: malformed JSON is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ConfirmEmailChange(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{oops")))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("confirm: empty token is the indistinguishable 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ConfirmEmailChange(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"token":""}`)))
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("confirm: repository failure is 500, not the 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ConfirmEmailChange(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"token":"abc"}`)))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}

func TestPasswordHandlerBranches(t *testing.T) {
	h := closedPoolHandler(t)

	t.Run("forgot: malformed JSON is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ForgotPassword(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{oops")))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("forgot: malformed address still answers the unconditional 202", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ForgotPassword(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"email":"nope"}`)))
		assert.Equal(t, http.StatusAccepted, rec.Code)
	})

	t.Run("forgot: lookup failure still answers the unconditional 202", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"email":"a@example.com"}`))
		r.RemoteAddr = "192.0.2.1:1234"
		h.ForgotPassword(rec, r)
		assert.Equal(t, http.StatusAccepted, rec.Code)
	})

	t.Run("send verification: user load failure is 500", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.SendEmailVerification(rec, principalReq(http.MethodPost, "/x", ""))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("verify: malformed JSON is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.VerifyEmail(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{oops")))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("verify: empty token is the indistinguishable 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.VerifyEmail(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"token":""}`)))
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("verify: repository failure is 500, not the 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.VerifyEmail(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"token":"abc"}`)))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}

// The AuditBackup* hooks are how internal/backupstatus writes into the
// trail without an import cycle. An audit write that fails is logged, never
// propagated — these calls must complete (and not panic) with the database
// gone.
func TestAdminAuditHooksSurviveRepositoryFailure(t *testing.T) {
	h := &AdminHandler{
		repo:   NewRepository(testsupport.ClosedPool(t)),
		logger: slog.New(slog.DiscardHandler),
	}
	r := principalReq(http.MethodPost, "/x", "")
	require.NotPanics(t, func() {
		h.AuditBackupRun(r, "manual")
		h.AuditBackupSchedule(r, "daily 03:00")
		h.AuditBackupDownload(r, "backups/2026-10-16.zip")
		h.AuditPolicyChange(r, "password floor")
	})
}
