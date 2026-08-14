//go:build integration

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/auth"
	"foldex/internal/config"
	"foldex/internal/folders"
	"foldex/internal/links"
	"foldex/internal/notes"
	"foldex/internal/server"
	"foldex/internal/testdb"
)

// TestMain owns the lifetime of this package's shared Postgres container.
//
// It cannot be a t.Cleanup: os.Exit skips deferred work, and a cleanup hung off
// whichever test ran first would tear the database down while the rest of the
// package still needed it. The Makefile disables testcontainers' reaper, so
// nothing else would collect it.
func TestMain(m *testing.M) {
	code := m.Run()
	testdb.StopShared()
	os.Exit(code)
}

func newServer(t *testing.T, secret string) (*httptest.Server, func()) {
	t.Helper()
	pool := testdb.Shared(t)

	// server.New mounts bootstrapPrincipal, which resolves the admin itself;
	// the seed just has to exist.
	_ = testdb.SeedUser(t, pool, "owner@test.local", "admin")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := server.New(server.Deps{
		Pool:   pool,
		Worker: nopWorker{},
		Logger: logger,
		Config: config.Config{
			Port:               "0",
			CORSOrigins:        []string{"*"},
			PreviewConcurrency: 1,
			PreviewTimeoutSec:  1,
			SharedSecret:       secret,
		},
	})
	srv := httptest.NewServer(router)
	return srv, srv.Close
}

type nopWorker struct{}

func (nopWorker) Enqueue(int64) error { return nil }

// TestServerNewPanicsWhenScreenshotterMissingPolicy locks the P2.7 boot-time
// guard: mounting the screenshot endpoint without an SSRF gate is a misconfig
// that must fail at startup, not silently return 500 per request. Without
// this assertion, removing the panic guard in router.go would ship green.
func TestServerNewPanicsWhenScreenshotterMissingPolicy(t *testing.T) {
	pool := testdb.Shared(t)

	// server.New mounts bootstrapPrincipal, which resolves the admin itself;
	// the seed just has to exist.
	_ = testdb.SeedUser(t, pool, "owner@test.local", "admin")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	assert.Panics(t, func() {
		_ = server.New(server.Deps{
			Pool:          pool,
			Worker:        nopWorker{},
			Logger:        logger,
			Config:        config.Config{CORSOrigins: []string{"*"}},
			Screenshotter: stubScreenshotter{},
			Storage:       stubStorage{},
			// ScreenshotURL is deliberately nil — must panic.
		})
	})
}

func TestFolderScopedContentResponseMatrixAcrossAuthModes(t *testing.T) {
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	ctx := context.Background()
	unlockKey := []byte("01234567890123456789012345678901")
	password := "a good folder password"

	frepo := folders.NewRepository(pool)
	folder, err := frepo.Create(ctx, uid, folders.CreateInput{
		Name: "Private", Color: "#6366F1", Password: &password,
	})
	require.NoError(t, err)
	_, err = links.NewRepository(pool).Create(ctx, uid, links.CreateInput{
		URL: "https://private.example", Title: "Link secret", FolderID: &folder.ID,
	})
	require.NoError(t, err)
	_, err = notes.NewRepository(pool).Create(ctx, uid, notes.CreateInput{
		Title: "Note secret", BodyHTML: "<p>private</p>", FolderID: &folder.ID,
	})
	require.NoError(t, err)
	hash, err := frepo.PasswordHashFor(ctx, uid, folder.ID)
	require.NoError(t, err)
	require.NotNil(t, hash)
	validUnlock := folders.IssueUnlockToken(unlockKey, folder.ID, *hash)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authRepo := auth.NewRepository(pool)
	issued, err := authRepo.CreateAPIToken(ctx, uid, "integration", time.Hour)
	require.NoError(t, err)

	for _, mode := range []struct {
		name          string
		authEnabled   bool
		middleware    *auth.Middleware
		authorization string
	}{
		{name: "auth disabled"},
		{
			name: "auth enabled API token", authEnabled: true,
			middleware:    auth.NewMiddleware(authRepo, auth.CookieOptions{}, logger, false),
			authorization: "Bearer " + issued.Token,
		},
	} {
		t.Run(mode.name, func(t *testing.T) {
			router := server.New(server.Deps{
				Pool: pool, Worker: nopWorker{}, Logger: logger,
				Config:         config.Config{AuthEnabled: mode.authEnabled, CORSOrigins: []string{"http://localhost:9088"}},
				AuthMiddleware: mode.middleware, FolderUnlockKey: unlockKey,
			})

			for _, endpoint := range []struct {
				path, marker string
			}{
				{path: "/api/links?folder_id=" + intToStr(folder.ID), marker: "Link secret"},
				{path: "/api/notes?folder_id=" + intToStr(folder.ID), marker: "Note secret"},
				{path: "/api/entries?folder_id=" + intToStr(folder.ID), marker: "Link secret"},
			} {
				for _, proof := range []struct {
					name, token string
					want        int
				}{
					{name: "missing unlock", want: http.StatusForbidden},
					{name: "invalid unlock", token: "invalid", want: http.StatusForbidden},
					{name: "valid unlock", token: validUnlock, want: http.StatusOK},
				} {
					t.Run(endpoint.path+" "+proof.name, func(t *testing.T) {
						req := httptest.NewRequest(http.MethodGet, endpoint.path, nil)
						if mode.authorization != "" {
							req.Header.Set("Authorization", mode.authorization)
						}
						if proof.token != "" {
							req.Header.Set(folders.UnlockHeader, proof.token)
						}
						rec := httptest.NewRecorder()

						router.ServeHTTP(rec, req)

						assert.Equal(t, proof.want, rec.Code, rec.Body.String())
						if proof.want == http.StatusOK {
							assert.Contains(t, rec.Body.String(), endpoint.marker)
						} else {
							assert.Contains(t, rec.Body.String(), "folder_locked")
							assert.NotContains(t, rec.Body.String(), endpoint.marker)
						}
					})
				}
			}

			req := httptest.NewRequest(http.MethodGet,
				"/api/entries?folder_id="+intToStr(folder.ID), nil)
			req.Header.Set(folders.UnlockHeader, validUnlock)
			if !mode.authEnabled {
				req.Header.Set("Authorization", "Bearer ignored-while-auth-is-disabled")
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if mode.authEnabled {
				assert.Equal(t, http.StatusUnauthorized, rec.Code)
			} else {
				assert.Equal(t, http.StatusOK, rec.Code)
			}
		})
	}
}

func TestAdminGateResponseMatrixAcrossAuthModes(t *testing.T) {
	pool := testdb.Shared(t)
	adminID := testdb.SeedUser(t, pool, "admin@test.local", "admin")
	userID := testdb.SeedUser(t, pool, "user@test.local", "user")
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repo := auth.NewRepository(pool)
	adminHandler := auth.NewAdminHandler(repo, nil, nil, logger, "http://localhost:9088")
	adminToken, err := repo.CreateAPIToken(ctx, adminID, "admin", time.Hour)
	require.NoError(t, err)
	userToken, err := repo.CreateAPIToken(ctx, userID, "user", time.Hour)
	require.NoError(t, err)

	authOff := server.New(server.Deps{
		Pool: pool, Logger: logger, Config: config.Config{CORSOrigins: []string{"http://localhost:9088"}},
		AdminHandler: adminHandler,
	})
	middleware := auth.NewMiddleware(repo, auth.CookieOptions{}, logger, false)
	authOn := server.New(server.Deps{
		Pool: pool, Logger: logger,
		Config:       config.Config{AuthEnabled: true, CORSOrigins: []string{"http://localhost:9088"}},
		AdminHandler: adminHandler, AuthMiddleware: middleware,
	})

	for _, tc := range []struct {
		name, authorization, code string
		router                    http.Handler
		want                      int
	}{
		{name: "auth disabled bootstrap admin", router: authOff, want: http.StatusOK},
		{name: "auth enabled missing credential", router: authOn, want: http.StatusUnauthorized, code: "unauthorized"},
		{name: "non-admin API token stays hidden", router: authOn, authorization: "Bearer " + userToken.Token, want: http.StatusNotFound, code: "not_found"},
		{name: "admin API token is out of scope", router: authOn, authorization: "Bearer " + adminToken.Token, want: http.StatusForbidden, code: "token_scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
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

func TestAdminGateRechecksLiveTOTPState(t *testing.T) {
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "admin@test.local", "admin")
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repo := auth.NewRepository(pool)
	tokens, _, err := repo.IssueSession(ctx, uid, 0, auth.DefaultTTL(), "", "")
	require.NoError(t, err)
	router := server.New(server.Deps{
		Pool: pool, Logger: logger,
		Config:         config.Config{AuthEnabled: true, CORSOrigins: []string{"http://localhost:9088"}},
		AdminHandler:   auth.NewAdminHandler(repo, nil, nil, logger, "http://localhost:9088"),
		AuthMiddleware: auth.NewMiddleware(repo, auth.CookieOptions{}, logger, true),
	})
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
		req.AddCookie(&http.Cookie{Name: auth.CookieAccess, Value: tokens.Access})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	rec := request()
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "admin_2fa_required")

	_, err = pool.Exec(ctx, `
		INSERT INTO totp_secret (user_id, secret_ciphertext, secret_nonce, confirmed_at)
		VALUES ($1, $2, $3, now())`, int64(uid), []byte("ciphertext"), []byte("nonce"))
	require.NoError(t, err)

	rec = request()
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestNoteMediaRoute_AllowsAnonymousReadWithoutExposingLinkMedia(t *testing.T) {
	pool := testdb.Shared(t)
	_ = testdb.SeedUser(t, pool, "owner@test.local", "admin")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := server.New(server.Deps{
		Pool:   pool,
		Worker: nopWorker{},
		Logger: logger,
		Config: config.Config{
			AuthEnabled:  true,
			CORSOrigins:  []string{"http://localhost:9088"},
			SharedSecret: "topsecret",
		},
		AuthMiddleware: auth.NewMiddleware(auth.NewRepository(pool), auth.CookieOptions{}, logger, false),
		Screenshotter:  stubScreenshotter{},
		Storage:        stubStorage{},
		ScreenshotURL:  func(context.Context, string) bool { return true },
	})

	noteReq := httptest.NewRequest(http.MethodGet,
		"/api/files/notes/8bcb9d80-8212-4ef3-a6a8-24f9471cf90e.jpg", nil)
	noteRec := httptest.NewRecorder()
	router.ServeHTTP(noteRec, noteReq)
	assert.Equal(t, http.StatusOK, noteRec.Code)

	for _, path := range []string{
		"/api/files/images/1.jpg",
		"/api/files/screenshots/1.jpg",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "path %s must remain secret-gated", path)
	}

	for _, path := range []string{
		"/api/files/notes/not-a-uuid.jpg",
		"/api/files/notes/%2e%2e%2fimages%2f1.jpg",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code, "path %s must fail closed", path)
	}
}

type stubScreenshotter struct{}

func (stubScreenshotter) Capture(_ context.Context, _ string) ([]byte, error) { return nil, nil }

type stubStorage struct{}

func (stubStorage) Upload(_ context.Context, _ string, _ []byte, _ string) error { return nil }
func (stubStorage) GetObject(_ context.Context, _ string) ([]byte, string, error) {
	return []byte("\x89PNG\r\n\x1a\n"), "image/png", nil
}
func (stubStorage) DeleteObject(_ context.Context, _ string) error { return nil }

func TestHealthzOK(t *testing.T) {
	srv, done := newServer(t, "")
	defer done()
	resp, err := http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "ok", body["status"])
	assert.Equal(t, "ok", body["db"])
}

// TestHealthzDegradedDoesNotLeakErr locks the §4-ish leak fix: healthz is the
// only endpoint mounted BEFORE the SHARED_SECRET gate, so a degraded response
// must surface the boolean state only — the raw pool.Ping error can carry
// internal DSN/host text that an unauthenticated caller can read. Closing the
// pool before the request simulates "db unreachable" without a separate
// container misconfiguration.
func TestHealthzDegradedDoesNotLeakErr(t *testing.T) {
	pool := testdb.Shared(t)

	// server.New mounts bootstrapPrincipal, which resolves the admin itself;
	// the seed just has to exist.
	_ = testdb.SeedUser(t, pool, "owner@test.local", "admin")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := server.New(server.Deps{
		Pool:   pool,
		Worker: nopWorker{},
		Logger: logger,
		Config: config.Config{Port: "0", CORSOrigins: []string{"*"}, PreviewConcurrency: 1, PreviewTimeoutSec: 1},
	})
	srv := httptest.NewServer(router)
	defer srv.Close()

	pool.Close() // make Ping fail

	resp, err := http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "degraded", body["status"], "status must reflect db-down")
	assert.Equal(t, "unreachable", body["db"], "db field must be the fixed marker, not the raw err")

	// Defense-in-depth: scan the db field for typical DSN/err substrings that
	// pool.Ping would otherwise leak. Catches regressions even if the field
	// name were ever changed.
	for _, leak := range []string{"connection", "dsn", "foldex@", "127.0.0.1", "dial"} {
		assert.NotContains(t, body["db"], leak, "leaked substring %q in db field", leak)
	}
}

func TestFullCRUDFlow(t *testing.T) {
	srv, done := newServer(t, "")
	defer done()
	c := srv.Client()

	// Create tag
	resp, err := c.Post(srv.URL+"/api/tags", "application/json",
		bytes.NewBufferString(`{"name":"jira","color":"#1f6feb"}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var tag struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tag))
	_ = resp.Body.Close()

	// GET single tag
	resp, err = c.Get(srv.URL + "/api/tags/" + intToStr(tag.ID))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	// PATCH tag color
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/tags/"+intToStr(tag.ID),
		bytes.NewBufferString(`{"color":"#ffffff"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = c.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	// Create link with tag
	body := []byte(`{"url":"https://example.com","title":"Ex","tag_ids":[` + intToStr(tag.ID) + `]}`)
	resp, err = c.Post(srv.URL+"/api/links", "application/json", bytes.NewBuffer(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var link struct {
		ID   int64 `json:"id"`
		Tags []struct {
			Name string `json:"name"`
		} `json:"tags"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&link))
	_ = resp.Body.Close()
	require.Len(t, link.Tags, 1)
	assert.Equal(t, "jira", link.Tags[0].Name)

	// List with filters
	resp, err = c.Get(srv.URL + "/api/links?q=example&tag=" + intToStr(tag.ID) + "&sort=clicks&limit=10")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	// GET single link
	resp, err = c.Get(srv.URL + "/api/links/" + intToStr(link.ID))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	// Invalid id
	resp, err = c.Get(srv.URL + "/api/links/abc")
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	_ = resp.Body.Close()

	// PATCH link title
	req, _ = http.NewRequest(http.MethodPatch, srv.URL+"/api/links/"+intToStr(link.ID),
		bytes.NewBufferString(`{"title":"renamed"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = c.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	// Refresh preview
	resp, err = c.Post(srv.URL+"/api/links/"+intToStr(link.ID)+"/refresh-preview", "", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	_ = resp.Body.Close()

	// Refresh preview missing → 404
	resp, err = c.Post(srv.URL+"/api/links/9999/refresh-preview", "", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	_ = resp.Body.Close()

	// DELETE link
	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/api/links/"+intToStr(link.ID), nil)
	resp, err = c.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	_ = resp.Body.Close()

	// DELETE tag
	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/api/tags/"+intToStr(tag.ID), nil)
	resp, err = c.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	_ = resp.Body.Close()
}

func TestBadRequestPaths(t *testing.T) {
	srv, done := newServer(t, "")
	defer done()
	c := srv.Client()

	// Invalid JSON
	resp, err := c.Post(srv.URL+"/api/tags", "application/json", bytes.NewBufferString("{"))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Missing name
	resp, err = c.Post(srv.URL+"/api/tags", "application/json", bytes.NewBufferString(`{}`))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Bad URL on link
	resp, err = c.Post(srv.URL+"/api/links", "application/json", bytes.NewBufferString(`{"url":"ftp://x"}`))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Link not found
	resp, err = c.Get(srv.URL + "/api/links/77777")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// Tag not found
	resp, err = c.Get(srv.URL + "/api/tags/77777")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// Tag invalid id
	resp, err = c.Get(srv.URL + "/api/tags/0")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// PATCH invalid JSON
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/tags/1", bytes.NewBufferString("{"))
	req.Header.Set("Content-Type", "application/json")
	resp, err = c.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// DELETE missing tag
	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/api/tags/8888", nil)
	resp, err = c.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// DELETE missing link
	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/api/links/9999", nil)
	resp, err = c.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// PATCH missing tag
	req, _ = http.NewRequest(http.MethodPatch, srv.URL+"/api/tags/9999",
		bytes.NewBufferString(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = c.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// PATCH missing link
	req, _ = http.NewRequest(http.MethodPatch, srv.URL+"/api/links/9999",
		bytes.NewBufferString(`{"title":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = c.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// Tag invalid id (links handler shares httperr.ParseID)
	resp, err = c.Get(srv.URL + "/api/links/abc")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestImportExportThroughRouter(t *testing.T) {
	srv, done := newServer(t, "")
	defer done()

	// Export when empty
	resp, err := srv.Client().Get(srv.URL + "/api/export?format=netscape")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Export JSON
	resp, err = srv.Client().Get(srv.URL + "/api/export?format=json")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// List tags + list links happy paths
	resp, err = srv.Client().Get(srv.URL + "/api/tags")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	resp, err = srv.Client().Get(srv.URL + "/api/links?limit=20&offset=0&sort=recent")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestStatsEndpointsThroughRouter(t *testing.T) {
	srv, done := newServer(t, "")
	defer done()
	c := srv.Client()

	// All four stats endpoints respond 200 on an empty DB.
	for _, path := range []string{"/api/stats/summary", "/api/stats/daily?days=7", "/api/stats/top?limit=5", "/api/stats/tags"} {
		resp, err := c.Get(srv.URL + path)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode, "path %s", path)
		resp.Body.Close()
	}
}

func TestDuplicateAndConflict(t *testing.T) {
	srv, done := newServer(t, "")
	defer done()
	c := srv.Client()

	// First tag
	resp, err := c.Post(srv.URL+"/api/tags", "application/json",
		bytes.NewBufferString(`{"name":"duped","color":"#000"}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var first struct{ ID int64 }
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&first))
	_ = resp.Body.Close()

	// Duplicate POST → 409
	resp, err = c.Post(srv.URL+"/api/tags", "application/json",
		bytes.NewBufferString(`{"name":"duped","color":"#000"}`))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusConflict, resp.StatusCode)

	// Second tag — capture its actual id (BIGSERIAL advances even on rollback)
	resp, err = c.Post(srv.URL+"/api/tags", "application/json",
		bytes.NewBufferString(`{"name":"second","color":"#fff"}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var second struct{ ID int64 }
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&second))
	_ = resp.Body.Close()

	// PATCH second to clash with first → 409
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/tags/"+intToStr(second.ID),
		bytes.NewBufferString(`{"name":"duped"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = c.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestSharedSecretGuard(t *testing.T) {
	srv, done := newServer(t, "topsecret")
	defer done()
	c := srv.Client()

	// /healthz is outside /api and ignores the secret
	resp, err := c.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// /api requires the header
	resp, err = c.Get(srv.URL + "/api/tags")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Wrong secret is also 401 (not only missing header)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/tags", nil)
	req.Header.Set("X-Foldex-Secret", "wrong-secret")
	resp, err = c.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Contains(t, string(body), "unauthorized")

	// Correct secret allows /api
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/tags", nil)
	req.Header.Set("X-Foldex-Secret", "topsecret")
	resp, err = c.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Public share surfaces stay reachable without the secret header
	resp, err = c.Get(srv.URL + "/go/does-not-exist")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	resp, err = c.Get(srv.URL + "/n/does-not-exist")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func intToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	out := []byte{}
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	if neg {
		out = append([]byte{'-'}, out...)
	}
	return string(out)
}
