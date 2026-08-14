package redirect

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/domainerr"
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
	return "", domainerr.ErrNotFound
}

func (f *fakeResolver) ClickAndResolveBySlug(_ context.Context, slug string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if u, ok := f.bySlug[slug]; ok {
		return u, nil
	}
	return "", domainerr.ErrNotFound
}

func mount(f *fakeResolver) http.Handler {
	return mountWithNumericIDs(f, true)
}

func mountWithNumericIDs(f *fakeResolver, allowNumericIDs bool) http.Handler {
	h := NewHandler(f, allowNumericIDs)
	r := chi.NewRouter()
	h.Mount(r)
	return r
}

func TestRedirect_PublicIdentifierClassification(t *testing.T) {
	const (
		maxInt64 = "9223372036854775807"
		overflow = "18446744073709551617"
	)
	tests := []struct {
		name            string
		raw             string
		allowNumericIDs bool
		byID            map[int64]string
		bySlug          map[string]string
		wantStatus      int
	}{
		{name: "max int64 feature on", raw: maxInt64, allowNumericIDs: true, byID: map[int64]string{math.MaxInt64: "https://example.com/max"}, wantStatus: http.StatusFound},
		{name: "max int64 feature off", raw: maxInt64, allowNumericIDs: false, byID: map[int64]string{math.MaxInt64: "https://example.com/max"}, wantStatus: http.StatusNotFound},
		{name: "overflow feature on is slug", raw: overflow, allowNumericIDs: true, bySlug: map[string]string{overflow: "https://example.com/overflow"}, wantStatus: http.StatusFound},
		{name: "overflow feature off is slug", raw: overflow, allowNumericIDs: false, bySlug: map[string]string{overflow: "https://example.com/overflow"}, wantStatus: http.StatusFound},
		{name: "zero feature on is slug", raw: "0", allowNumericIDs: true, bySlug: map[string]string{"0": "https://example.com/zero"}, wantStatus: http.StatusFound},
		{name: "zero feature off is slug", raw: "0", allowNumericIDs: false, bySlug: map[string]string{"0": "https://example.com/zero"}, wantStatus: http.StatusFound},
		{name: "signed feature on is slug", raw: "+42", allowNumericIDs: true, bySlug: map[string]string{"+42": "https://example.com/signed"}, wantStatus: http.StatusFound},
		{name: "signed feature off is slug", raw: "+42", allowNumericIDs: false, bySlug: map[string]string{"+42": "https://example.com/signed"}, wantStatus: http.StatusFound},
		{name: "numeric-looking slug feature on", raw: "42-notes", allowNumericIDs: true, bySlug: map[string]string{"42-notes": "https://example.com/slug"}, wantStatus: http.StatusFound},
		{name: "numeric-looking slug feature off", raw: "42-notes", allowNumericIDs: false, bySlug: map[string]string{"42-notes": "https://example.com/slug"}, wantStatus: http.StatusFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := mountWithNumericIDs(&fakeResolver{byID: tt.byID, bySlug: tt.bySlug}, tt.allowNumericIDs)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/go/"+tt.raw, nil)
			r.ServeHTTP(rec, req)
			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
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
