package server

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"foldex/internal/auth"
	"foldex/internal/pkg/auditctx"
	"foldex/internal/pkg/authctx"
)

// contentAuditActions maps an accepted mutation to the action it is recorded
// under — ADR-46.
//
// The key is chi's ROUTE PATTERN, not the request path: "/api/links/{id}" is
// one entry however many links exist, and a path-based map would either miss
// every row or need a matcher of its own. The pattern is available only after
// routing, which is why this middleware is mounted inside the group rather than
// at the root.
//
// A closed map, matching the audit vocabulary's own rule: a route absent from
// it records nothing. That is deliberate — a new route should be a decision
// about whether it belongs in the trail, not an automatic entry under an action
// name derived from its URL that nobody translated.
var contentAuditActions = map[string]string{
	"POST /api/links":               auth.AuditLinkCreated,
	"PATCH /api/links/{id}":         auth.AuditLinkUpdated,
	"DELETE /api/links/{id}":        auth.AuditLinkDeleted,
	"POST /api/notes":               auth.AuditNoteCreated,
	"PATCH /api/notes/{id}":         auth.AuditNoteUpdated,
	"DELETE /api/notes/{id}":        auth.AuditNoteDeleted,
	"POST /api/folders":             auth.AuditFolderCreated,
	"PATCH /api/folders/{id}":       auth.AuditFolderUpdated,
	"DELETE /api/folders/{id}":      auth.AuditFolderDeleted,
	"POST /api/folders/{id}/unlock": auth.AuditFolderUnlock,
	"POST /api/tags":                auth.AuditTagCreated,
	"PATCH /api/tags/{id}":          auth.AuditTagUpdated,
	"DELETE /api/tags/{id}":         auth.AuditTagDeleted,
	"POST /api/import/apply":        auth.AuditImportApplied,
	"POST /api/backup/restore":      auth.AuditBackupRestore,
}

// auditWriteTimeout bounds a trail write that has outlived its request. Lives
// here rather than in router.go so the middleware and the deadline its callers
// need are read together.
const auditWriteTimeout = 5 * time.Second

// auditRecorder is the write hook. A function rather than the auth repository
// so this package keeps depending on auth for its vocabulary only, and the
// router decides what the write actually is.
type auditRecorder func(*http.Request, auth.AuditRecord)

// contentAudit records accepted mutations of the caller's own library.
//
// A middleware rather than a call in each handler, for the reason credential
// redaction lives at the root log handler: coverage that depends on every
// author remembering is coverage that has holes, and the holes are invisible —
// a missing entry looks exactly like a quiet day.
//
// Only 2xx is recorded. A rejected write changed nothing, and a trail that
// listed attempts alongside effects would make "who deleted this" unanswerable
// from the screen it exists to answer it on.
func contentAudit(record auditRecorder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Cheap rejection before anything is allocated: reads are the
			// overwhelming majority of traffic through this group.
			if !mutating(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			ctx := auditctx.With(r.Context())
			r = r.WithContext(ctx)
			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r)

			// Resolved AFTER the handler: chi fills the route pattern during
			// routing, which happens inside next.ServeHTTP for a middleware
			// mounted on a group.
			action, ok := contentAuditActions[r.Method+" "+chi.RouteContext(ctx).RoutePattern()]
			if !ok || sw.status < 200 || sw.status >= 300 {
				return
			}
			rec := auth.AuditRecord{Action: action}
			if p, found := authctx.FromContext(ctx); found && p.UserID != 0 {
				id := p.UserID
				rec.ActorID = &id
			}
			rec.EntityKind, rec.EntityID, rec.Subject = auditctx.Get(ctx)
			record(r, rec)
		})
	}
}

func mutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
		return true
	}
	return false
}

// statusWriter remembers the status code.
//
// It records the FIRST WriteHeader and treats an implicit write as 200, which
// is what net/http does. Without the implicit case every handler that writes a
// body without calling WriteHeader — the ordinary success path — would be read
// as status 0 and recorded as nothing.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the real writer, so a handler that
// flushes or hijacks still can. Without it, wrapping a streaming response —
// backup export is one — would silently disable flushing.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
