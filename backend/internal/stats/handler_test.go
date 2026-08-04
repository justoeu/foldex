package stats

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"

	"foldex/internal/pkg/authctx"

	"foldex/internal/pkg/authctx/authctxtest"
)

type fakeRepo struct {
	summary Summary
	daily   []DailyPoint
	top     []TopLink
	tags    []TagBucket
	err     error
}

func (f *fakeRepo) Summary(context.Context, authctx.UserID) (Summary, error) { return f.summary, f.err }
func (f *fakeRepo) Daily(context.Context, authctx.UserID, int) ([]DailyPoint, error) {
	return f.daily, f.err
}
func (f *fakeRepo) TopLinks(context.Context, authctx.UserID, int) ([]TopLink, error) {
	return f.top, f.err
}
func (f *fakeRepo) TagBuckets(context.Context, authctx.UserID) ([]TagBucket, error) {
	return f.tags, f.err
}

type fakeStorage struct {
	s   StorageStats
	err error
}

func (f *fakeStorage) Stats(context.Context) (StorageStats, error) { return f.s, f.err }

func mount(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(authctxtest.DefaultUser))
	h.Mount(r)
	return r
}

func TestHandler_Summary_OK(t *testing.T) {
	r := mount(NewHandler(&fakeRepo{summary: Summary{TotalLinks: 3, TotalClicks: 10}}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/summary", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"total_links":3`)
}

func TestHandler_Summary_Err(t *testing.T) {
	r := mount(NewHandler(&fakeRepo{err: errors.New("db")}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/summary", nil))
	assert.NotEqual(t, http.StatusOK, rec.Code)
}

func TestHandler_Daily_OK(t *testing.T) {
	r := mount(NewHandler(&fakeRepo{daily: []DailyPoint{{Clicks: 1}}}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/daily?days=99999", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHandler_Top_OK(t *testing.T) {
	r := mount(NewHandler(&fakeRepo{top: []TopLink{{ID: 1, Title: "a", Clicks: 5}}}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/top?limit=5", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"title":"a"`)
}

func TestHandler_Tags_OK(t *testing.T) {
	r := mount(NewHandler(&fakeRepo{tags: []TagBucket{{Name: "t", Clicks: 2}}}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tags", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHandler_Storage_OK(t *testing.T) {
	h := NewHandler(&fakeRepo{}).WithStorage(&fakeStorage{s: StorageStats{Objects: 4, TotalBytes: 100}})
	r := mount(h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/storage", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"objects":4`)
}

func TestHandler_Storage_Err(t *testing.T) {
	h := NewHandler(&fakeRepo{}).WithStorage(&fakeStorage{err: errors.New("object store")})
	r := mount(h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/storage", nil))
	assert.NotEqual(t, http.StatusOK, rec.Code)
}

func TestHandler_Daily_Err(t *testing.T) {
	r := mount(NewHandler(&fakeRepo{err: errors.New("x")}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/daily", nil))
	assert.NotEqual(t, http.StatusOK, rec.Code)
}
