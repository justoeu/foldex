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
func RequirePermission(p authctx.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := authctx.FromContext(r.Context())
			if !ok || !principal.Role.Can(p) {
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
