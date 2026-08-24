// Package authgate contains authorization checks that depend only on the
// principal already stored in a request context.
package authgate

import (
	"net/http"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
)

// RequireAdmin hides administrator routes from non-administrators.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := authctx.FromContext(r.Context())
		if !ok || !p.Role.IsAdmin() {
			httperr.Write(w, httperr.ErrNotFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Grants answers the one question the gate asks. The concrete implementation
// lives in internal/roleperm; an interface here keeps authgate a leaf and lets
// a test gate on a fixed matrix without a database.
//
// It is a positional argument on both gates below rather than a package-level
// default, and that is the whole safety of ADR-42: as a default, a mount site
// that forgot to pass the configured matrix would silently keep enforcing the
// compiled one, and an owner's revocation would appear to save while changing
// nothing. A parameter makes forgetting a compile error.
type Grants interface {
	Can(authctx.Role, authctx.Permission) bool
}

// RequireWrite gates the unsafe verbs of a whole route group on a permission
// and lets safe methods through untouched.
//
// Mounted on a group rather than on each mutating route so that a route added
// later is covered by construction. That is only sound where every unsafe verb
// in the group really is a write: /api/folders and /api/backup both answer POST
// to operations that only READ — unlocking a folder proves a password in order
// to see its contents, and exporting a backup serializes rows the caller
// already owns — so those two gate per route instead, with RequirePermission.
func RequireWrite(g Grants, p authctx.Permission) func(http.Handler) http.Handler {
	gate := RequirePermission(g, p)
	return func(next http.Handler) http.Handler {
		gated := gate(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
			default:
				gated.ServeHTTP(w, r)
			}
		})
	}
}

// RequirePermission gates a route on one entry of the ADR-33 matrix.
//
// The answer is 403, not the 404 RequireAdmin gives. The two hide different
// things and the distinction is deliberate: RequireAdmin conceals that an
// administrative surface exists at all, because a 403 there would confirm the
// route to anyone who asked. Past that gate the caller already knows the
// surface exists — they are an administrator — so an honest "your role cannot
// do this" leaks nothing and is the only answer that lets an admin understand
// why the owner-only button failed.
//
// Content routes use it for the opposite reason: a viewer knows their own
// library exists, so 404 on their own row would be a lie.
func RequirePermission(g Grants, p authctx.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := authctx.FromContext(r.Context())
			// A nil matrix denies. It can only mean a mount site was built with
			// no grants at all, and the safe reading of "authorization is not
			// wired" is that nothing is authorized.
			if !ok || g == nil || !g.Can(principal.Role, p) {
				httperr.Write(w, httperr.New(http.StatusForbidden, "forbidden_role",
					"your role does not allow this action"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RejectAPIToken keeps content-scoped bearer credentials off sensitive routes.
func RejectAPIToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := authctx.FromContext(r.Context()); ok && p.Via == authctx.ViaAPIToken {
			httperr.Write(w, httperr.New(http.StatusForbidden, "token_scope",
				"an API token cannot be used on this endpoint"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
