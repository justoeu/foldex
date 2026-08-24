//go:build integration

package auth_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"foldex/internal/pkg/spantest"
	"foldex/internal/tracing"
	"github.com/stretchr/testify/require"
)

// The authenticated half of /api/auth — sessions, password change, 2FA, API
// tokens — is mounted INSIDE the auth handler, behind its own Authenticate,
// and therefore outside the /api group every other authenticated route lives
// in. The first draft of this feature annotated spans from a middleware
// mounted on that group and silently missed all ~20 of these routes: exactly
// the credential-management surface an operator wants attributed ("who
// revoked that session?"). Nothing failed — no build error, no panic, an
// identical response.
//
// Hanging the annotation off Authenticate's principal seam instead is what
// makes this reachable, and this test is what says so. Revert the seam and it
// fails; move it back to a group mount and it fails.
func TestAuthenticate_StampsUserIDOnSpansOfTheAuthSurfaceItself(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "span-admin@test.local", "correct horse battery staple")

	var uid int64
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT id FROM app_user WHERE email = $1`, "span-admin@test.local").Scan(&uid))

	// Wrap only now: the bootstrap above is a pre-auth flow and would add
	// spans with no principal, which is correct but noise for this assertion.
	rec := spantest.Recorder(t)
	h.router = tracing.Middleware(h.router)

	res := c.do(http.MethodGet, "/api/auth/sessions", nil)
	require.Equal(t, http.StatusOK, res.Code, "the route itself must work: %s", res.Body.String())

	span := spantest.Last(t, rec)
	got, ok := spantest.Attr(span, "user.id")
	require.True(t, ok,
		"an authenticated /api/auth route produced a span with no user.id — the annotation is not on Authenticate's principal seam")
	require.Equal(t, strconv.FormatInt(uid, 10), got)

	role, ok := spantest.Attr(span, "user.roles")
	require.True(t, ok)
	require.Contains(t, role, "owner", "the bootstrap account holds the owner seat")
}

// Optional resolves a principal when one is present and serves anonymously
// when it is not. /api/auth/me is its canonical route and its contract is
// "always 200", so it is reached both ways — and a signed-in caller must be
// attributed on the span exactly like any other authenticated request.
func TestOptional_StampsUserIDWhenASessionIsPresentAndNothingWhenItIsNot(t *testing.T) {
	h := newHarness(t)
	signedIn := h.bootstrapAdmin(t, "optional-admin@test.local", "correct horse battery staple")

	rec := spantest.Recorder(t)
	h.router = tracing.Middleware(h.router)

	require.Equal(t, http.StatusOK, signedIn.do(http.MethodGet, "/api/auth/me", nil).Code)
	if _, ok := spantest.Attr(spantest.Last(t, rec), "user.id"); !ok {
		t.Fatal("a signed-in /api/auth/me must carry user.id — Optional's principal seam is not annotating")
	}

	// A caller with no cookie at all: the span must carry NO user.id rather
	// than user.id="0", which would collapse every anonymous request onto one
	// fictional account in Tempo.
	anon := h.client(t)
	require.Equal(t, http.StatusOK, anon.do(http.MethodGet, "/api/auth/me", nil).Code)
	if v, ok := spantest.Attr(spantest.Last(t, rec), "user.id"); ok {
		t.Fatalf("an anonymous request must carry no user.id, got %q", v)
	}
}
