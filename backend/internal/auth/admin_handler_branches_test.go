package auth

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/testsupport"
)

// Admin-handler branches that answer before (or by translating) a
// repository failure: id parsing, the self-target guards, input rails, the
// audit-filter vocabulary, and the 500 translations. The integration suite
// drives these handlers with well-formed requests against a live database,
// so the refusals are asserted here against a closed pool.

func closedPoolAdmin(t *testing.T) *AdminHandler {
	t.Helper()
	return &AdminHandler{
		repo:   NewRepository(testsupport.ClosedPool(t)),
		logger: slog.New(slog.DiscardHandler),
	}
}

func adminReq(method, target, body string, params map[string]string) *http.Request {
	r := principalReq(method, target, body)
	if len(params) > 0 {
		rctx := chi.NewRouteContext()
		for k, v := range params {
			rctx.URLParams.Add(k, v)
		}
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	}
	return r
}

func TestAdminListAndMetricsBranches(t *testing.T) {
	h := closedPoolAdmin(t)
	cases := map[string]func(*httptest.ResponseRecorder){
		"ListUsers": func(rec *httptest.ResponseRecorder) {
			h.ListUsers(rec, adminReq(http.MethodGet, "/x?limit=10&after=3", "", nil))
		},
		"ListInvites":  func(rec *httptest.ResponseRecorder) { h.ListInvites(rec, adminReq(http.MethodGet, "/x", "", nil)) },
		"Metrics":      func(rec *httptest.ResponseRecorder) { h.Metrics(rec, adminReq(http.MethodGet, "/x", "", nil)) },
		"Roles":        func(rec *httptest.ResponseRecorder) { h.Roles(rec, adminReq(http.MethodGet, "/x", "", nil)) },
		"ListIPBlocks": func(rec *httptest.ResponseRecorder) { h.ListIPBlocks(rec, adminReq(http.MethodGet, "/x", "", nil)) },
		"AuditStats": func(rec *httptest.ResponseRecorder) {
			h.AuditStats(rec, adminReq(http.MethodGet, "/x?window=7d", "", nil))
		},
	}
	for name, call := range cases {
		t.Run(name+" repository failure is 500", func(t *testing.T) {
			rec := httptest.NewRecorder()
			call(rec)
			assert.Equal(t, http.StatusInternalServerError, rec.Code)
		})
	}

	t.Run("AuditVocabulary answers without the database", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.AuditVocabulary(rec, adminReq(http.MethodGet, "/x", "", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"windows":["24h","7d","30d"]`)
	})
}

func TestAdminUserManagementBranches(t *testing.T) {
	h := closedPoolAdmin(t)
	idp := map[string]string{"id": "7"}
	self := map[string]string{"id": "1"}

	t.Run("UpdateUser: malformed id is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.UpdateUser(rec, adminReq(http.MethodPatch, "/x", `{}`, map[string]string{"id": "abc"}))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("UpdateUser: malformed JSON is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.UpdateUser(rec, adminReq(http.MethodPatch, "/x", "{oops", idp))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("UpdateUser: self-demotion is 409 before any write", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.UpdateUser(rec, adminReq(http.MethodPatch, "/x", `{"role":"viewer"}`, self))
		assert.Equal(t, http.StatusConflict, rec.Code)
	})
	t.Run("UpdateUser: repository failure is 500", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.UpdateUser(rec, adminReq(http.MethodPatch, "/x", `{"name":"Renamed"}`, idp))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
	t.Run("DeleteUser: self-deletion is 409", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.DeleteUser(rec, adminReq(http.MethodDelete, "/x", "", self))
		assert.Equal(t, http.StatusConflict, rec.Code)
	})
	t.Run("DeleteUser: repository failure is 500", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.DeleteUser(rec, adminReq(http.MethodDelete, "/x", "", idp))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
	t.Run("RevokeUserSessions: malformed id is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.RevokeUserSessions(rec, adminReq(http.MethodPost, "/x", "", map[string]string{"id": "0"}))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("RevokeUserSessions: unloadable target is 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.RevokeUserSessions(rec, adminReq(http.MethodPost, "/x", "", idp))
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
	t.Run("ForcePasswordReset: self-target is 409", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ForcePasswordReset(rec, adminReq(http.MethodPost, "/x", "", self))
		assert.Equal(t, http.StatusConflict, rec.Code)
	})
	t.Run("TransferOwnership: repository failure is 500", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.TransferOwnership(rec, adminReq(http.MethodPost, "/x", "", idp))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
	t.Run("RevokeInvite: malformed id is 400, repository failure is 500", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.RevokeInvite(rec, adminReq(http.MethodDelete, "/x", "", map[string]string{"id": "x"}))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		rec = httptest.NewRecorder()
		h.RevokeInvite(rec, adminReq(http.MethodDelete, "/x", "", idp))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
	t.Run("SetRolePermissions: unconfigured grants store is 503", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.SetRolePermissions(rec, adminReq(http.MethodPut, "/x", `{"permissions":[]}`, nil))
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})
}

func TestAdminCreateBranches(t *testing.T) {
	h := closedPoolAdmin(t)

	t.Run("CreateUser: malformed JSON is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.CreateUser(rec, adminReq(http.MethodPost, "/x", "{oops", nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("CreateUser: invalid e-mail is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.CreateUser(rec, adminReq(http.MethodPost, "/x",
			`{"email":"nope","name":"N","password":"correct horse battery staple"}`, nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("CreateUser: owner is not an assignable role", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.CreateUser(rec, adminReq(http.MethodPost, "/x",
			`{"email":"a@example.com","name":"N","password":"correct horse battery staple","role":"owner"}`, nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("CreateUser: repository failure is 500", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.CreateUser(rec, adminReq(http.MethodPost, "/x",
			`{"email":"a@example.com","name":"N","password":"correct horse battery staple","role":"viewer"}`, nil))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
	t.Run("CreateInvite: invalid e-mail is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.CreateInvite(rec, adminReq(http.MethodPost, "/x", `{"email":"nope"}`, nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("CreateInvite: owner is not an assignable role", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.CreateInvite(rec, adminReq(http.MethodPost, "/x", `{"email":"a@example.com","role":"owner"}`, nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("CreateInvite: repository failure is 500", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.CreateInvite(rec, adminReq(http.MethodPost, "/x", `{"email":"a@example.com","role":"viewer"}`, nil))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}

func TestAdminAuditAndIPBlockBranches(t *testing.T) {
	h := closedPoolAdmin(t)

	for name, target := range map[string]string{
		"unknown action":   "/x?action=bogus.action",
		"unknown category": "/x?category=bogus",
		"unknown window":   "/x?window=90d",
		"negative cursor":  "/x?before=-1",
		"limit above page": "/x?limit=100000",
	} {
		t.Run("ListAudit: "+name+" is 400", func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ListAudit(rec, adminReq(http.MethodGet, target, "", nil))
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}

	t.Run("ListAudit: repository failure is 500", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ListAudit(rec, adminReq(http.MethodGet, "/x?window=24h&order=asc", "", nil))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("ExportAudit: invalid filter is 400 before headers", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ExportAudit(rec, adminReq(http.MethodGet, "/x?window=90d", "", nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	// The export commits its headers first by design: with the store gone it
	// still yields a well-formed CSV containing just the header row — a
	// truncated download, never a 500 after the fact.
	t.Run("ExportAudit: dead store yields the header-only CSV", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ExportAudit(rec, adminReq(http.MethodGet, "/x?window=24h", "", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "text/csv; charset=utf-8", rec.Header().Get("Content-Type"))
		assert.True(t, strings.HasPrefix(rec.Body.String(), "id,created_at,action,"))
	})

	t.Run("BlockIP: malformed JSON is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.BlockIP(rec, adminReq(http.MethodPost, "/x", "{oops", nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("BlockIP: malformed address is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.BlockIP(rec, adminReq(http.MethodPost, "/x", `{"ip":"not-an-ip","reason":"r"}`, nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("BlockIP: loopback is a 409 rail", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r := adminReq(http.MethodPost, "/x", `{"ip":"127.0.0.1","reason":"r"}`, nil)
		r.RemoteAddr = "192.0.2.1:1234"
		h.BlockIP(rec, r)
		assert.Equal(t, http.StatusConflict, rec.Code)
	})
	t.Run("BlockIP: repository failure is 500", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r := adminReq(http.MethodPost, "/x", `{"ip":"203.0.113.9","reason":"r"}`, nil)
		r.RemoteAddr = "192.0.2.1:1234"
		h.BlockIP(rec, r)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
	t.Run("UnblockIP: garbage address is 400, repository failure is 500", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.UnblockIP(rec, adminReq(http.MethodDelete, "/x", "", map[string]string{"ip": "zzz"}))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		rec = httptest.NewRecorder()
		h.UnblockIP(rec, adminReq(http.MethodDelete, "/x", "", map[string]string{"ip": "203.0.113.9"}))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}
