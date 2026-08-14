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
