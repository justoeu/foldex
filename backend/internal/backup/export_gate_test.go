package backup

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

// noExport is a matrix where the caller holds everything on this surface
// EXCEPT backup.export, so a 403 can only come from that gate.
type noExport struct{}

func (noExport) Can(_ authctx.Role, p authctx.Permission) bool {
	return p != authctx.PermBackupExport
}

// TestExportGateIsMountedOnEveryRouteThatSendsTheArchiveOut.
//
// The AST guard in internal/security proves backup.export appears as an
// argument to RequirePermission somewhere. It cannot see an `r.With(export)`
// dropped from the routes while the variable stays — that mutation survived
// both this package and the guard, which is precisely the gap between "a gate
// exists" and "a gate is on the route".
//
// Every one of these five hands the archive, or a capability for it, to the
// caller. Missing any single one leaves a way out that the unticked box says
// is closed.
func TestExportGateIsMountedOnEveryRouteThatSendsTheArchiveOut(t *testing.T) {
	h := NewHandler(&fakeBackupSvc{}, nil, noExport{})
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(authctx.WithPrincipal(req.Context(),
				authctx.Principal{UserID: 1, Role: authctx.RoleEditor})))
		})
	})
	r.Route("/api/backup", h.Mount)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/backup"},
		{http.MethodPost, "/api/backup/download"},
		{http.MethodGet, "/api/backup/download"},
		{http.MethodGet, "/api/backup/download/status"},
		{http.MethodPost, "/api/backup/validate"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			assert.Equal(t, http.StatusForbidden, rec.Code,
				"this route sends the archive out and must refuse without backup.export")
		})
	}
}

// The mirror. Without it the test above passes on a router that refuses
// everything for some unrelated reason.
func TestExportRoutesAreReachableWithThePermission(t *testing.T) {
	h := NewHandler(&fakeBackupSvc{}, nil, nil) // nil => compiled matrix
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(authctx.WithPrincipal(req.Context(),
				authctx.Principal{UserID: 1, Role: authctx.RoleEditor})))
		})
	})
	r.Route("/api/backup", h.Mount)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/backup/download/status", nil))
	require.NotEqual(t, http.StatusForbidden, rec.Code,
		"an editor holds backup.export, so the gate must let it through")
}
