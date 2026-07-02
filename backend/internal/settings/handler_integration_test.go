//go:build integration

package settings_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/settings"
	"foldex/internal/testdb"
)

func newSettingsRouter(t *testing.T) http.Handler {
	t.Helper()
	pool := testdb.New(t)
	r := chi.NewRouter()
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
