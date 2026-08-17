//go:build integration

package auth_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The self-service profile surface: a signed-in account may rename itself and
// nothing else. E-mail is identity, role/status are administration — the
// endpoint must not become a side door to either.
func TestUpdateProfile_RenamesTheCaller(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	rec := c.do(http.MethodPatch, "/api/auth/profile", map[string]string{"name": "  Valmir Justo  "})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	user := decode(t, rec)["user"].(map[string]any)
	assert.Equal(t, "Valmir Justo", user["name"])

	// The rename is visible to /me — one source of truth, not a PATCH-only
	// illusion.
	rec = c.do(http.MethodGet, "/api/auth/me", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "Valmir Justo", decode(t, rec)["user"].(map[string]any)["name"])

	// An empty name clears the display name (the column's default shape), and
	// an over-long one is refused — the column is TEXT, so the handler is the
	// only cap.
	rec = c.do(http.MethodPatch, "/api/auth/profile", map[string]string{"name": "   "})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "", decode(t, rec)["user"].(map[string]any)["name"])

	rec = c.do(http.MethodPatch, "/api/auth/profile", map[string]string{"name": strings.Repeat("x", 121)})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_name", errCode(t, rec))

	// The cap counts CHARACTERS, not bytes — a 120-CJK-char name is 360 bytes
	// on the wire but legal, matching what the SPA's maxLength=120 allows.
	rec = c.do(http.MethodPatch, "/api/auth/profile", map[string]string{"name": strings.Repeat("谷", 120)})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, strings.Repeat("谷", 120), decode(t, rec)["user"].(map[string]any)["name"])
}

func TestUpdateProfile_OnlyTouchesTheName(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	// The DTO refuses unknown fields, so smuggling role/status into /profile
	// dies as invalid_json before reaching the repository — a self-demotion
	// attempt cannot even discover what the guard checks. Same contract as
	// every other strict DTO in the auth surface.
	rec := c.do(http.MethodPatch, "/api/auth/profile", map[string]any{"name": "Someone", "role": "user", "status": "disabled"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_json", errCode(t, rec))

	// A plain rename still works and leaves role/status untouched.
	rec = c.do(http.MethodPatch, "/api/auth/profile", map[string]string{"name": "Someone"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	user := decode(t, rec)["user"].(map[string]any)
	assert.Equal(t, "Someone", user["name"])
	assert.Equal(t, "admin", user["role"])
	assert.Equal(t, "active", user["status"])
}

func TestUpdateProfile_RequiresASessionAndRefusesTokens(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")
	token := h.mintToken(t, admin, "extension")

	// Anonymous: the identity surface answers 401 like every other route
	// behind Authenticate.
	rec := h.client(t).do(http.MethodPatch, "/api/auth/profile", map[string]string{"name": "n"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// Bearer token: content-scoped credentials must never reach profile
	// mutations — the same RejectAPIToken contract as password/tokens routes.
	rec = h.client(t).doRaw(http.MethodPatch, "/api/auth/profile",
		map[string]string{"name": "n"}, bearerHeader(token))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "token_scope", errCode(t, rec))
}
