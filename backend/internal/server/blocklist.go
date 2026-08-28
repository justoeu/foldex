package server

import (
	"net/http"

	"foldex/internal/auth"
	"foldex/internal/pkg/httperr"
)

// clientIP is the address a request arrived from, in the one spelling the
// blocklist and the trail both use.
//
// It reads RemoteAddr, which trustedProxyRealIP has already rewritten from
// X-Forwarded-For when — and only when — a configured proxy vouched for it.
// Normalizing through auth is what keeps "::ffff:1.2.3.4" from being a second
// address that no block ever matches.
func clientIP(r *http.Request) string { return auth.NormalizeIP(r.RemoteAddr) }

// blocklistGate refuses requests from a permanently blocked address — ADR-46.
//
// Mounted after trustedProxyRealIP so it tests the address that middleware
// resolved, and BEFORE routing, so a blocked caller reaches no handler and
// costs no query beyond the cached snapshot lookup.
//
// The answer is 403 with a named code rather than a silent drop or a 404. A
// blocked address belongs to a person, and the overwhelmingly likely person is
// the operator who blocked their own office: telling them what happened is what
// lets them go fix it from somewhere else, and telling an attacker they are
// blocked reveals nothing they cannot infer from every request failing.
//
// The health endpoint is exempt. It is how a container orchestrator decides
// whether this process is alive, and an instance that reports itself unhealthy
// because someone blocked a probe's address would be restarted in a loop.
func blocklistGate(list *auth.Blocklist) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/healthz" || !list.Blocked(r.Context(), clientIP(r)) {
				next.ServeHTTP(w, r)
				return
			}
			httperr.Write(w, httperr.New(http.StatusForbidden, "ip_blocked",
				"this address has been blocked by an administrator of this instance"))
		})
	}
}
