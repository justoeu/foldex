// Package authctxtest injects a principal into requests so handler tests can
// mount a handler without standing up the real authentication stack.
//
// It exists as its own package rather than as helpers inside authctx so no
// test-only affordance ships in the production import graph. Handlers call
// authctx.MustUser, which panics without a principal — deliberately, since a
// zero owner would silently match no rows — so every handler test has to supply
// one, exactly as the router does in production.
package authctxtest

import (
	"net/http"

	"foldex/internal/pkg/authctx"
)

// DefaultUser is the owner used when a test does not care which user it is.
const DefaultUser = authctx.UserID(1)

// Middleware wraps a router so every request carries uid as an admin principal.
// Mirrors what server.bootstrapPrincipal (and, from PR2, auth.Authenticate)
// does in production.
func Middleware(uid authctx.UserID) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, Request(r, uid))
		})
	}
}

// Request returns r with uid attached as an admin principal. For tests that
// call a handler func directly instead of going through a router.
func Request(r *http.Request, uid authctx.UserID) *http.Request {
	return r.WithContext(authctx.WithPrincipal(r.Context(), authctx.Principal{
		UserID: uid,
		Role:   authctx.RoleAdmin,
		Via:    authctx.ViaSession,
	}))
}
