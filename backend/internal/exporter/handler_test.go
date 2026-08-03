package exporter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
)

type fakeExportRepo struct {
	links   []linkRow
	tags    []tagRow
	folders []folderRow
	err     error
}

func (f *fakeExportRepo) ListAllLinks(context.Context) ([]linkRow, error) {
	return f.links, f.err
}
func (f *fakeExportRepo) ListTags(context.Context) ([]tagRow, error) {
	return f.tags, f.err
}
func (f *fakeExportRepo) ListFolders(context.Context) ([]folderRow, error) {
	return f.folders, f.err
}

func TestExport_Netscape_OK(t *testing.T) {
	h := &Handler{repo: &fakeExportRepo{
		links: []linkRow{{URL: "https://a.com", Title: "A", Slug: "a", CreatedAt: time.Now()}},
	}}
	r := chi.NewRouter()
	h.Mount(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?format=netscape", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	assert.Contains(t, rec.Body.String(), "https://a.com")
}

func TestExport_JSON_OK(t *testing.T) {
	h := &Handler{repo: &fakeExportRepo{
		links:   []linkRow{{URL: "https://a.com", Title: "A", Slug: "a", CreatedAt: time.Unix(0, 0).UTC()}},
		tags:    []tagRow{{Name: "t", Color: "#fff"}},
		folders: []folderRow{{Name: "f", Color: "#000"}},
	}}
	r := chi.NewRouter()
	h.Mount(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?format=json", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"version":2`)
	assert.Contains(t, rec.Body.String(), "https://a.com")
}

func TestExport_UnknownFormat(t *testing.T) {
	h := &Handler{repo: &fakeExportRepo{}}
	r := chi.NewRouter()
	h.Mount(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?format=xml", nil))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestExport_RepoErr(t *testing.T) {
	h := &Handler{repo: &fakeExportRepo{err: errors.New("db")}}
	r := chi.NewRouter()
	h.Mount(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.NotEqual(t, http.StatusOK, rec.Code)
}
