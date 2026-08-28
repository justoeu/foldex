package auth

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every POST this handler mounts is classified against nginx's tight
// rate-limit zone, and adding a route forces the classification.
//
// scripts/test-nginx-headers.sh bursts every path the zone NAMES and proves
// each is actually throttled. What it cannot see is the opposite and likelier
// mistake — a route that belongs in the zone and is not there — because it
// parses the alternation out of the same file it is checking. That is how
// `invites/lookup` and `2fa/email` spent a commit at 30 r/s under a comment
// claiming "the seven paths that guess something". A count in prose is not a
// guard, and neither is a guard that reads its own answer key.
//
// The first version of this test lived in internal/server and walked the router
// built by fullyWiredDeps(), which mounts no AuthHandler: it iterated ZERO
// routes and survived deleting `login` from the zone. It is here instead
// because this is where the routes are, and it asserts a non-empty walk for
// exactly that reason.
func TestEdgeZone_EveryAuthPOSTIsClassified(t *testing.T) {
	t.Parallel()

	// Deliberately loose, each with the durable control that makes 2 r/s
	// unnecessary. Adding a line is a decision; omitting one is a failure.
	looseByDesign := map[string]string{
		"/refresh": "rotates a 256-bit opaque token the caller already holds — it proves nothing " +
			"about a secret being guessed, and the SPA calls it on every 401",
		"/logout":               "revokes a session the caller already holds",
		"/logout-all":           "same, and it is inside the authenticated group",
		"/email/verify":         "consumes a single-use 256-bit link token, spent on first use (INV-008)",
		"/email-change/confirm": "same shape as email/verify",
		"/oauth/google/start":   "starts a redirect; tests nothing",
		"/oauth/google/invite/start": "starts a redirect for an invite already looked up, " +
			"and the lookup IS in the tight zone",
		// Everything below requires a live session, so the edge zone — which
		// knows only an address — is not the layer that should bound it. The
		// per-user budgets in the database are (INV-010).
		"/2fa/totp/start":                "session or an in-progress enrolment; budget lives in the database",
		"/2fa/email/send":                "step-up for a signed-in caller; stepUpUser bucket plus the DB budget",
		"/2fa/totp/disable":              "signed-in, and gated on a fresh credential proof",
		"/2fa/email/disable":             "signed-in, and gated on a fresh credential proof",
		"/2fa/recovery-codes/regenerate": "signed-in, and gated on a fresh credential proof",
		"/password/change":               "signed-in; the current password IS the step-up (INV-147)",
		"/password/set":                  "signed-in, and gated on a fresh credential proof",
		"/email/resend":                  "signed-in; cooldown enforced by the policy, not by the edge",
		"/email/change":                  "signed-in, and gated on a fresh credential proof",
		"/tokens":                        "signed-in API token creation",
	}

	zone := tightZonePaths(t)
	require.NotEmpty(t, zone, "parsed no paths out of the nginx tight zone — the regex stopped matching")

	r := chi.NewRouter()
	r.Route("/", newLimiterOnlyHandler(t).Mount)

	posts := 0
	require.NoError(t, chi.Walk(r,
		func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			if method != http.MethodPost {
				return nil
			}
			posts++
			path := strings.TrimSuffix(route, "/")
			if zone["/api/auth"+path] {
				return nil
			}
			assert.Containsf(t, looseByDesign, path,
				"POST /api/auth%s is neither in nginx's tight rate-limit zone nor listed as "+
					"deliberately loose.\nIf it tests a credential or spends a resource for the "+
					"caller, add it to the alternation in web/nginx.conf. If it does not, add it to "+
					"looseByDesign with the reason — the point is that the choice is written down.", path)
			return nil
		}))

	// Without this the whole test is the shape it replaced: a walk over nothing.
	require.Greater(t, posts, 15, "walked only %d POST routes; the handler mounts far more", posts)
}

// tightZonePaths parses the fx_login location's alternation out of the shipped
// config — the same file the container gets, before entrypoint substitution,
// none of which touches a location block.
func tightZonePaths(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("../../../web/nginx.conf")
	require.NoError(t, err, "the shipped nginx config must be readable from the test")

	m := regexp.MustCompile(`location ~ \^/api/auth/\(([^)]+)\)\$`).FindSubmatch(raw)
	require.NotNil(t, m, "could not find the fx_login location in web/nginx.conf")

	out := map[string]bool{}
	for _, branch := range strings.Split(string(m[1]), "|") {
		out["/api/auth/"+branch] = true
	}
	return out
}
