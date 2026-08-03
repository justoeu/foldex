package redirect

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/httperr"
)

type fakeResolver struct {
	byID   map[int64]string
	bySlug map[string]string
	err    error
}

func (f *fakeResolver) ClickAndResolve(_ context.Context, id int64) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if u, ok := f.byID[id]; ok {
		return u, nil
	}
	return "", httperr.ErrNotFound
}

func (f *fakeResolver) ClickAndResolveBySlug(_ context.Context, slug string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if u, ok := f.bySlug[slug]; ok {
		return u, nil
	}
	return "", httperr.ErrNotFound
}

func mount(f *fakeResolver) http.Handler {
	h := NewHandler(f)
	r := chi.NewRouter()
	h.Mount(r)
	return r
}

func TestRedirect_ByID(t *testing.T) {
	r := mount(&fakeResolver{byID: map[int64]string{42: "https://example.com/x"}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/go/42", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "https://example.com/x", rec.Header().Get("Location"))
}

func TestRedirect_BySlug(t *testing.T) {
	r := mount(&fakeResolver{bySlug: map[string]string{"my-link": "https://ex.com"}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/go/my-link", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "https://ex.com", rec.Header().Get("Location"))
}

func TestRedirect_NotFound(t *testing.T) {
	r := mount(&fakeResolver{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/go/99", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRedirect_RejectsNonHTTPScheme(t *testing.T) {
	r := mount(&fakeResolver{byID: map[int64]string{1: "javascript:alert(1)"}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/go/1", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_target")
}

func TestRedirect_RepoError(t *testing.T) {
	r := mount(&fakeResolver{err: errors.New("db down")})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/go/1", nil)
	r.ServeHTTP(rec, req)
	// generic internal via httperr
	require.NotEqual(t, http.StatusFound, rec.Code)
}

func TestRedirect_Helper(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	redirect(rec, req, "https://ok.example")
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "https://ok.example", rec.Header().Get("Location"))

	rec = httptest.NewRecorder()
	redirect(rec, req, "ftp://x")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
