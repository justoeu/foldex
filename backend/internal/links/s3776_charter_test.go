package links

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx/authctxtest"
)

func TestS3776Charter_CaptureAndStore(t *testing.T) {
	errorCode := func(t *testing.T, rec *httptest.ResponseRecorder) string {
		t.Helper()
		var body map[string]any
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
		errBlock, _ := body["error"].(map[string]any)
		code, _ := errBlock["code"].(string)
		return code
	}

	t.Run("success", func(t *testing.T) {
		sc := &fakeScreenshotter{png: realPNG(t, 50, 50)}
		up := newFakeUploader()
		repo := newFakeRepo()
		repo.links[1] = Link{ID: 1, URL: "https://example.com"}
		r, _, _ := buildRouter(t, sc, up, repo)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/links/1/screenshot", nil))
		require.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("invalid id", func(t *testing.T) {
		r, _, _ := buildRouter(t, &fakeScreenshotter{png: realPNG(t, 50, 50)}, newFakeUploader(), newFakeRepo())
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/links/abc/screenshot", nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("non-http scheme", func(t *testing.T) {
		sc := &fakeScreenshotter{png: realPNG(t, 50, 50)}
		up := newFakeUploader()
		repo := newFakeRepo()
		repo.links[1] = Link{ID: 1, URL: "file:///etc/passwd"}
		r, _, _ := buildRouter(t, sc, up, repo)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/links/1/screenshot", nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Equal(t, "invalid_scheme", errorCode(t, rec))
		assert.Empty(t, up.uploaded)
	})

	t.Run("nil policy fails closed", func(t *testing.T) {
		sc := &fakeScreenshotter{png: realPNG(t, 50, 50)}
		up := newFakeUploader()
		repo := newFakeRepo()
		repo.links[1] = Link{ID: 1, URL: "https://example.com"}
		sh := NewScreenshotHandler(repo, sc, up, nil, newTestLogger())
		r := chi.NewRouter()
		r.Use(authctxtest.Middleware(authctxtest.DefaultUser))
		r.Post("/api/links/{id}/screenshot", sh.CaptureAndStore)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/links/1/screenshot", nil))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.Equal(t, "policy_unconfigured", errorCode(t, rec))
		assert.Empty(t, up.uploaded)
	})

	t.Run("capture failure", func(t *testing.T) {
		sc := &fakeScreenshotter{err: errors.New("chromium crashed")}
		up := newFakeUploader()
		repo := newFakeRepo()
		repo.links[1] = Link{ID: 1, URL: "https://example.com"}
		r, _, _ := buildRouter(t, sc, up, repo)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/links/1/screenshot", nil))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.Equal(t, "screenshot_failed", errorCode(t, rec))
		assert.Empty(t, up.uploaded)
	})
}
