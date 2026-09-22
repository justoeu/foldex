//go:build integration

package server_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/addon"
	"foldex/internal/auth"
	"foldex/internal/config"
	"foldex/internal/server"
	"foldex/internal/testdb"
)

// addonZip is a stand-in for the real bundle: injected through Deps so the
// routing contract does not depend on whether this tree was built with
// `make extension`.
var addonZip = []byte("PK\x03\x04 the embedded extension zip")

func addonRouter(t *testing.T, pool *pgxpool.Pool, h *addon.Handler) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repo := auth.NewRepository(pool)
	return server.New(server.Deps{
		Pool:   pool,
		Worker: nopWorker{},
		Logger: logger,
		Config: config.Config{AuthEnabled: true, CORSOrigins: []string{"http://localhost:9088"}},
		AdminHandler: auth.NewAdminHandler(repo, nil, logger,
			"http://localhost:9088", nil, nil),
		AuthMiddleware: auth.NewMiddleware(repo, auth.CookieOptions{}, logger, false),
		AddonHandler:   h,
	})
}

func TestAddonDownload_ServesTheBundleToAnAdminSession(t *testing.T) {
	pool := testdb.Shared(t)
	adminID := testdb.SeedUser(t, pool, "admin@test.local", "admin")
	ctx := context.Background()
	repo := auth.NewRepository(pool)
	session, _, err := repo.IssueSession(ctx, adminID, 0, auth.SessionTTL{
		Access: time.Minute, Refresh: time.Hour, Absolute: 24 * time.Hour, Grace: time.Second,
	}, "", "")
	require.NoError(t, err)

	get := func(h *addon.Handler) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/addon/download", nil)
		req.AddCookie(&http.Cookie{Name: auth.CookieAccess, Value: session.Access})
		rec := httptest.NewRecorder()
		addonRouter(t, pool, h).ServeHTTP(rec, req)
		return rec
	}

	rec := get(addon.NewHandler(addon.NewBundle(addonZip, "3.1.0")))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, addonZip, rec.Body.Bytes())
	assert.Equal(t, "application/zip", rec.Header().Get("Content-Type"))
	assert.Equal(t, `attachment; filename="foldex-extension-3.1.0.zip"`,
		rec.Header().Get("Content-Disposition"))
	assert.Equal(t, "3.1.0", rec.Header().Get("X-Addon-Version"))

	// Nil AddonHandler falls back to the build-time embed: placeholder or
	// real, the HANDLER must answer — a 404 here would mean the route was
	// never mounted.
	rec = get(nil)
	assert.Contains(t, []int{http.StatusOK, http.StatusServiceUnavailable}, rec.Code,
		"the default handler must own the route (200 = real bundle, 503 = placeholder), got %d", rec.Code)
}

// The download route must inherit the admin surface's exact gate shape: a
// non-admin token learns nothing (404, like any missing route), an admin's
// token is out of scope, and the anonymous caller is just unauthorized.
func TestAddonDownload_GateMatchesTheAdminSurfaceShape(t *testing.T) {
	pool := testdb.Shared(t)
	adminID := testdb.SeedUser(t, pool, "admin@test.local", "admin")
	userID := testdb.SeedUser(t, pool, "user@test.local", "editor")
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repo := auth.NewRepository(pool)
	adminToken, err := repo.CreateAPIToken(ctx, adminID, "admin", time.Hour)
	require.NoError(t, err)
	userToken, err := repo.CreateAPIToken(ctx, userID, "user", time.Hour)
	require.NoError(t, err)

	authOff := server.New(server.Deps{
		Pool: pool, Logger: logger,
		Config:       config.Config{CORSOrigins: []string{"http://localhost:9088"}},
		AdminHandler: auth.NewAdminHandler(repo, nil, logger, "http://localhost:9088", nil, nil),
		AddonHandler: addon.NewHandler(addon.NewBundle(addonZip, "3.1.0")),
	})
	authOn := server.New(server.Deps{
		Pool: pool, Logger: logger,
		Config:         config.Config{AuthEnabled: true, CORSOrigins: []string{"http://localhost:9088"}},
		AdminHandler:   auth.NewAdminHandler(repo, nil, logger, "http://localhost:9088", nil, nil),
		AuthMiddleware: auth.NewMiddleware(repo, auth.CookieOptions{}, logger, false),
		AddonHandler:   addon.NewHandler(addon.NewBundle(addonZip, "3.1.0")),
	})

	for _, tc := range []struct {
		name, authorization string
		router              http.Handler
		want                int
		code                string
	}{
		{name: "auth disabled bootstrap admin", router: authOff, want: http.StatusOK},
		{name: "auth enabled missing credential", router: authOn, want: http.StatusUnauthorized, code: "unauthorized"},
		{name: "non-admin API token stays hidden", router: authOn, authorization: "Bearer " + userToken.Token, want: http.StatusNotFound, code: "not_found"},
		{name: "admin API token is out of scope", router: authOn, authorization: "Bearer " + adminToken.Token, want: http.StatusForbidden, code: "token_scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/admin/addon/download", nil)
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}
			rec := httptest.NewRecorder()

			tc.router.ServeHTTP(rec, req)

			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
			if tc.code != "" {
				assert.Contains(t, rec.Body.String(), `"code":"`+tc.code+`"`)
			}
		})
	}
}

func TestAddonDownload_WithoutABuiltBundleAnswers503(t *testing.T) {
	pool := testdb.Shared(t)
	_ = testdb.SeedUser(t, pool, "admin@test.local", "admin")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := server.New(server.Deps{
		Pool: pool, Logger: logger,
		Config:       config.Config{CORSOrigins: []string{"http://localhost:9088"}},
		AdminHandler: auth.NewAdminHandler(auth.NewRepository(pool), nil, logger, "http://localhost:9088", nil, nil),
		AddonHandler: addon.NewHandler(addon.NewBundle(nil, "0.0.0")),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/admin/addon/download", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), `"code":"addon_not_built"`)

	head := httptest.NewRecorder()
	router.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/api/admin/addon/download", nil))
	assert.Equal(t, http.StatusServiceUnavailable, head.Code)
}

func TestAddonDownload_HEADAnswersHeadersWithNoBody(t *testing.T) {
	pool := testdb.Shared(t)
	_ = testdb.SeedUser(t, pool, "admin@test.local", "admin")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := server.New(server.Deps{
		Pool: pool, Logger: logger,
		Config:       config.Config{CORSOrigins: []string{"http://localhost:9088"}},
		AdminHandler: auth.NewAdminHandler(auth.NewRepository(pool), nil, logger, "http://localhost:9088", nil, nil),
		AddonHandler: addon.NewHandler(addon.NewBundle(addonZip, "3.1.0")),
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/api/admin/addon/download", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Empty(t, rec.Body.Bytes())
	assert.Equal(t, "application/zip", rec.Header().Get("Content-Type"))
	assert.Equal(t, `attachment; filename="foldex-extension-3.1.0.zip"`,
		rec.Header().Get("Content-Disposition"))
	assert.Equal(t, "3.1.0", rec.Header().Get("X-Addon-Version"))
	assert.Equal(t, "31", rec.Header().Get("Content-Length"))
}
