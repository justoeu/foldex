package backupstatus_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"

	"foldex/internal/backupstatus"
	"foldex/internal/pkg/authctx"
	"foldex/internal/roleperm"
)

func TestValidJob(t *testing.T) {
	for _, job := range []string{"dump", "drill", "mirror", "user_zip"} {
		assert.True(t, backupstatus.ValidJob(job), "job %q", job)
	}
	for _, bad := range []string{"", "DUMP", "userzip", "restore", "dump "} {
		assert.False(t, backupstatus.ValidJob(bad), "job %q", bad)
	}
}

// Validation must refuse before any query runs — these tests hand the handler
// a repository with no database at all, so reaching it would panic.
func newValidationRouter() http.Handler {
	h := backupstatus.NewHandler(
		backupstatus.NewRepository(nil),
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
		nil,
		roleperm.Default(),
	)
	r := chi.NewRouter()
	r.Route("/api/admin/backup", h.Mount)
	return r
}

func doAs(router http.Handler, role authctx.Role, method, path, body string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req = req.WithContext(authctx.WithPrincipal(req.Context(), authctx.Principal{
		UserID: 1, Role: role, SessionID: 1, Via: "session",
	}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestListRuns_RefusesAnUnknownJobBeforeTheDatabase(t *testing.T) {
	rec := doAs(newValidationRouter(), authctx.RoleAdmin,
		http.MethodGet, "/api/admin/backup/runs?job=restore", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_job")
}

func TestRequestRun_RefusesAnUnknownJobAndMalformedJSON(t *testing.T) {
	router := newValidationRouter()

	rec := doAs(router, authctx.RoleAdmin, http.MethodPost, "/api/admin/backup/run", `{"job":"restore"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_job")

	rec = doAs(router, authctx.RoleAdmin, http.MethodPost, "/api/admin/backup/run", `{"job":`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_json")

	rec = doAs(router, authctx.RoleAdmin, http.MethodPost, "/api/admin/backup/run", `{"job":"dump","extra":1}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "unknown fields are refused, like every JSON body here")
}

// The permission gate is part of Mount, not of the router that hosts it: a
// role without instance.backup is refused even before the admin group's 404
// shielding is considered.
func TestMount_GatesOnInstanceBackup(t *testing.T) {
	rec := doAs(newValidationRouter(), authctx.RoleEditor,
		http.MethodGet, "/api/admin/backup/runs", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "forbidden_role")
}
