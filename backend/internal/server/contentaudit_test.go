package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/auth"
	"foldex/internal/pkg/auditctx"
	"foldex/internal/pkg/authctx"
)

// mount builds the smallest router that reproduces the real shape: chi routing
// (so RoutePattern is filled), the middleware on the group, and a handler whose
// status and annotation the test controls.
func mount(t *testing.T, method, pattern string, h http.HandlerFunc) (*chi.Mux, *[]auth.AuditRecord) {
	t.Helper()
	var got []auth.AuditRecord
	r := chi.NewRouter()
	r.Group(func(g chi.Router) {
		g.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				ctx := authctx.WithPrincipal(req.Context(), authctx.Principal{UserID: 7})
				next.ServeHTTP(w, req.WithContext(ctx))
			})
		})
		g.Use(contentAudit(func(_ *http.Request, rec auth.AuditRecord) { got = append(got, rec) }))
		g.Method(method, pattern, h)
	})
	return r, &got
}

func do(r *chi.Mux, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// The point of a middleware over a call in each handler: coverage that depends
// on every author remembering is coverage with invisible holes.
func TestContentAudit_RecordsAnAcceptedMutation(t *testing.T) {
	r, got := mount(t, "POST", "/api/links", func(w http.ResponseWriter, req *http.Request) {
		auditctx.SetRequest(req, "link", 42, "ADR-46 draft")
		w.WriteHeader(http.StatusCreated)
	})
	do(r, "POST", "/api/links")

	require.Len(t, *got, 1)
	rec := (*got)[0]
	assert.Equal(t, auth.AuditLinkCreated, rec.Action)
	assert.Equal(t, "link", rec.EntityKind)
	require.NotNil(t, rec.EntityID)
	assert.Equal(t, int64(42), *rec.EntityID)
	assert.Equal(t, "ADR-46 draft", rec.Subject)
	require.NotNil(t, rec.ActorID)
	assert.Equal(t, authctx.UserID(7), *rec.ActorID)
}

// A rejected write changed nothing, and a trail listing attempts beside effects
// makes "who deleted this" unanswerable on the screen that exists to answer it.
func TestContentAudit_IgnoresRejectedWrites(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden,
		http.StatusNotFound, http.StatusConflict, http.StatusInternalServerError} {
		r, got := mount(t, "DELETE", "/api/links/{id}", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		})
		do(r, "DELETE", "/api/links/9")
		assert.Empty(t, *got, "status %d must record nothing", status)
	}
}

// A handler that writes a body without calling WriteHeader is the ORDINARY
// success path. Reading that as status 0 would drop every such event.
func TestContentAudit_TreatsAnImplicitWriteAsSuccess(t *testing.T) {
	r, got := mount(t, "PATCH", "/api/links/{id}", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	do(r, "PATCH", "/api/links/9")
	require.Len(t, *got, 1)
	assert.Equal(t, auth.AuditLinkUpdated, (*got)[0].Action)
}

// Reads are the overwhelming majority of traffic through this group.
func TestContentAudit_RecordsNothingForReads(t *testing.T) {
	r, got := mount(t, "GET", "/api/links", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	do(r, "GET", "/api/links")
	assert.Empty(t, *got)
}

// The early return for reads is an OPTIMISATION, not a guard — the route map
// would reject "GET /api/links" anyway. It earns its place by not allocating a
// context holder and not wrapping the writer on the path that carries almost
// all the traffic, and that is what this asserts: the handler is handed the
// ORIGINAL writer. Without it the assertion on behaviour alone would pass with
// the branch deleted, leaving the only reason it exists untested.
func TestContentAudit_DoesNotWrapTheWriterForReads(t *testing.T) {
	var seen http.ResponseWriter
	r, _ := mount(t, "GET", "/api/links", func(w http.ResponseWriter, _ *http.Request) {
		seen = w
		w.WriteHeader(http.StatusOK)
	})
	do(r, "GET", "/api/links")
	_, wrapped := seen.(*statusWriter)
	assert.False(t, wrapped, "a read must not pay for the audit wrapper")

	r, _ = mount(t, "POST", "/api/links", func(w http.ResponseWriter, _ *http.Request) {
		seen = w
		w.WriteHeader(http.StatusCreated)
	})
	do(r, "POST", "/api/links")
	_, wrapped = seen.(*statusWriter)
	assert.True(t, wrapped, "a mutation must be wrapped, or its status is invisible")
}

// A route absent from the map records nothing — deliberately. A new route
// should be a decision, not an automatic entry under a name nobody translated.
func TestContentAudit_IgnoresUnmappedRoutes(t *testing.T) {
	r, got := mount(t, "POST", "/api/links/{id}/refresh-preview", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	do(r, "POST", "/api/links/9/refresh-preview")
	assert.Empty(t, *got)
}

// Annotation is optional by construction: a handler that never calls Set still
// produces the event and loses only the label.
func TestContentAudit_RecordsWithoutAnAnnotation(t *testing.T) {
	r, got := mount(t, "DELETE", "/api/tags/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	do(r, "DELETE", "/api/tags/3")
	require.Len(t, *got, 1)
	assert.Equal(t, auth.AuditTagDeleted, (*got)[0].Action)
	assert.Empty(t, (*got)[0].Subject)
	assert.Nil(t, (*got)[0].EntityID)
}

// Every action the map names has to be one the vocabulary knows and classifies
// as content — otherwise the middleware writes rows the read split treats as
// identity, and a private label reaches the administrative projection.
func TestContentAuditActions_AreAllKnownContentActions(t *testing.T) {
	require.NotEmpty(t, contentAuditActions)
	for route, action := range contentAuditActions {
		assert.True(t, auth.KnownAuditAction(action), "route %q maps to unknown action %q", route, action)
		assert.Equal(t, auth.CategoryContent, auth.AuditCategory(action),
			"route %q records %q, which is not a content action", route, action)
	}
}

// The map is keyed by "METHOD /pattern", and a key that is not a mutating
// method can never fire — the middleware returns before the lookup.
func TestContentAuditActions_OnlyNameMutatingMethods(t *testing.T) {
	for route := range contentAuditActions {
		method, _, ok := strings.Cut(route, " ")
		require.True(t, ok, "route key %q is not \"METHOD /pattern\"", route)
		assert.True(t, mutating(method), "route %q is keyed by a non-mutating method", route)
	}
}

// Wrapping a streaming response must not disable flushing — backup export is
// one, and it goes through this group.
func TestStatusWriter_UnwrapsToTheRealWriter(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec}
	assert.Same(t, http.ResponseWriter(rec), sw.Unwrap())
	require.NoError(t, http.NewResponseController(sw).Flush())
}

// The FIRST status is the answer: a handler that writes a header and later
// calls WriteHeader again (net/http logs it and ignores it) must not be
// recorded under the second one.
func TestStatusWriter_KeepsTheFirstStatus(t *testing.T) {
	sw := &statusWriter{ResponseWriter: httptest.NewRecorder()}
	sw.WriteHeader(http.StatusCreated)
	sw.WriteHeader(http.StatusInternalServerError)
	assert.Equal(t, http.StatusCreated, sw.status)
}
