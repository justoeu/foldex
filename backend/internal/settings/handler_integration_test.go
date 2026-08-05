//go:build integration

package settings_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/settings"
	"foldex/internal/testdb"

	"foldex/internal/pkg/authctx/authctxtest"
)

func newSettingsRouter(t *testing.T) http.Handler {
	t.Helper()
	pool := testdb.New(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(uid))
	r.Route("/settings", settings.NewHandler(settings.NewRepository(pool)).Mount)
	return r
}

func do(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func statusConfigured(t *testing.T, h http.Handler) bool {
	t.Helper()
	rr := do(t, h, http.MethodGet, "/settings/master-password", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	var out struct {
		Configured bool `json:"configured"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	return out.Configured
}

func TestHandler_MasterPassword_SetChangeClear(t *testing.T) {
	h := newSettingsRouter(t)

	assert.False(t, statusConfigured(t, h), "starts unconfigured")

	// Too short → 400.
	rr := do(t, h, http.MethodPut, "/settings/master-password", map[string]any{"password": "short"})
	assert.Equal(t, http.StatusBadRequest, rr.Code)

	// First set → 200, no current required.
	rr = do(t, h, http.MethodPut, "/settings/master-password", map[string]any{"password": "first-master-pw"})
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, statusConfigured(t, h))

	// Change without current → 401.
	rr = do(t, h, http.MethodPut, "/settings/master-password", map[string]any{"password": "second-master-pw"})
	assert.Equal(t, http.StatusUnauthorized, rr.Code)

	// Change with wrong current → 401.
	rr = do(t, h, http.MethodPut, "/settings/master-password", map[string]any{"password": "second-master-pw", "current_password": "nope"})
	assert.Equal(t, http.StatusUnauthorized, rr.Code)

	// Change with correct current → 200.
	rr = do(t, h, http.MethodPut, "/settings/master-password", map[string]any{"password": "second-master-pw", "current_password": "first-master-pw"})
	assert.Equal(t, http.StatusOK, rr.Code)

	// Clear with wrong current → 401.
	rr = do(t, h, http.MethodDelete, "/settings/master-password", map[string]any{"current_password": "first-master-pw"})
	assert.Equal(t, http.StatusUnauthorized, rr.Code)

	// Clear with correct current → 200, now unconfigured.
	rr = do(t, h, http.MethodDelete, "/settings/master-password", map[string]any{"current_password": "second-master-pw"})
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.False(t, statusConfigured(t, h))

	// Clear when nothing configured → idempotent 200.
	rr = do(t, h, http.MethodDelete, "/settings/master-password", map[string]any{"current_password": "whatever"})
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestHandler_MasterPassword_HintRoundTrip(t *testing.T) {
	h := newSettingsRouter(t)

	rr := do(t, h, http.MethodPut, "/settings/master-password", map[string]any{
		"password": "first-master-pw",
		"hint":     " starts with f ",
	})
	require.Equal(t, http.StatusOK, rr.Code)
	var out struct {
		Configured bool    `json:"configured"`
		Hint       *string `json:"hint"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.NotNil(t, out.Hint)
	assert.Equal(t, "starts with f", *out.Hint)

	rr = do(t, h, http.MethodGet, "/settings/master-password", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.NotNil(t, out.Hint)
	assert.Equal(t, "starts with f", *out.Hint)

	// Change password, omit hint → keep existing.
	rr = do(t, h, http.MethodPut, "/settings/master-password", map[string]any{
		"password":         "second-master-pw",
		"current_password": "first-master-pw",
	})
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.NotNil(t, out.Hint)
	assert.Equal(t, "starts with f", *out.Hint)

	// Explicit empty hint clears it.
	rr = do(t, h, http.MethodPut, "/settings/master-password", map[string]any{
		"password":         "third-master-pw",
		"current_password": "second-master-pw",
		"hint":             "",
	})
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	assert.Nil(t, out.Hint)
}

func TestHandler_MasterPassword_InvalidJSON(t *testing.T) {
	h := newSettingsRouter(t)
	req := httptest.NewRequest(http.MethodPut, "/settings/master-password", bytes.NewBufferString(`{`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)

	req = httptest.NewRequest(http.MethodDelete, "/settings/master-password", bytes.NewBufferString(`{`))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandler_MasterPassword_HintEqualsPassword(t *testing.T) {
	h := newSettingsRouter(t)
	rr := do(t, h, http.MethodPut, "/settings/master-password", map[string]any{
		"password": "longenough",
		"hint":     "longenough",
	})
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid_input")
}

func TestHandler_Status_UnconfiguredHasNilHint(t *testing.T) {
	h := newSettingsRouter(t)
	rr := do(t, h, http.MethodGet, "/settings/master-password", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	var out struct {
		Configured bool    `json:"configured"`
		Hint       *string `json:"hint"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	assert.False(t, out.Configured)
	assert.Nil(t, out.Hint)
}

func TestHandler_ClosedPool_SurfacesErrors(t *testing.T) {
	pool := testdb.New(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := settings.NewRepository(pool)
	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(uid))
	r.Route("/settings", settings.NewHandler(repo).Mount)
	// Seed then close so subsequent handler calls hit repo errors.
	require.NoError(t, repo.SetMasterPassword(context.Background(), uid, "seeded-master-pw", nil))
	pool.Close()

	rr := do(t, r, http.MethodGet, "/settings/master-password", nil)
	assert.NotEqual(t, http.StatusOK, rr.Code)

	rr = do(t, r, http.MethodPut, "/settings/master-password", map[string]any{
		"password":         "new-master-pw",
		"current_password": "seeded-master-pw",
	})
	assert.NotEqual(t, http.StatusOK, rr.Code)

	// First-set path on closed pool (fresh handler without seed would still fail Configured check).
	pool2 := testdb.New(t)
	r2 := chi.NewRouter()
	r2.Use(authctxtest.Middleware(uid))
	r2.Route("/settings", settings.NewHandler(settings.NewRepository(pool2)).Mount)
	pool2.Close()
	rr = do(t, r2, http.MethodPut, "/settings/master-password", map[string]any{"password": "brand-new-pw"})
	assert.NotEqual(t, http.StatusOK, rr.Code)

	rr = do(t, r2, http.MethodDelete, "/settings/master-password", map[string]any{"current_password": "x"})
	assert.NotEqual(t, http.StatusOK, rr.Code)
}
