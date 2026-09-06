package exporter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"

	"foldex/internal/pkg/authctx/authctxtest"
)

type fakeExportRepo struct {
	links   []linkRow
	tags    []tagRow
	folders []folderRow
	err     error
}

func (f *fakeExportRepo) ListAllLinks(context.Context, authctx.UserID) ([]linkRow, error) {
	return f.links, f.err
}
func (f *fakeExportRepo) ListTags(context.Context, authctx.UserID) ([]tagRow, error) {
	return f.tags, f.err
}
func (f *fakeExportRepo) ListFolders(context.Context, authctx.UserID) ([]folderRow, error) {
	return f.folders, f.err
}

func TestExport_Netscape_OK(t *testing.T) {
	h := &Handler{repo: &fakeExportRepo{
		links: []linkRow{{URL: "https://a.com", Title: "A", Slug: "a", CreatedAt: time.Now()}},
	}}
	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(authctxtest.DefaultUser))
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
	r.Use(authctxtest.Middleware(authctxtest.DefaultUser))
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
	r.Use(authctxtest.Middleware(authctxtest.DefaultUser))
	h.Mount(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?format=xml", nil))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestExport_RepoErr(t *testing.T) {
	h := &Handler{repo: &fakeExportRepo{err: errors.New("db")}}
	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(authctxtest.DefaultUser))
	h.Mount(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.NotEqual(t, http.StatusOK, rec.Code)
}

// blockingExportRepo holds ListAllLinks until release is closed, so a test can
// fire a second GET while the first is still inside the query.
type blockingExportRepo struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (b *blockingExportRepo) ListAllLinks(context.Context, authctx.UserID) ([]linkRow, error) {
	b.calls.Add(1)
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	return []linkRow{{URL: "https://a.com", Title: "A", Slug: "a", CreatedAt: time.Unix(0, 0).UTC()}}, nil
}
func (b *blockingExportRepo) ListTags(context.Context, authctx.UserID) ([]tagRow, error) {
	return nil, nil
}
func (b *blockingExportRepo) ListFolders(context.Context, authctx.UserID) ([]folderRow, error) {
	return nil, nil
}

func mountExport(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(authctxtest.DefaultUser))
	h.Mount(r)
	return r
}

// TestExport_SecondConcurrentRequestIs429AndRowCeilingHolds is the RED for
// BP-HYD-002: GET /api/export used to materialize the whole library with no
// in-flight slot and no row cap, so two overlapping downloads both entered
// ListAllLinks and a 50_001-link tenant was fully encoded.
func TestExport_SecondConcurrentRequestIs429AndRowCeilingHolds(t *testing.T) {
	t.Run("second concurrent is 429", func(t *testing.T) {
		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		var releaseOnce sync.Once
		releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
		defer releaseAll()

		repo := &blockingExportRepo{entered: entered, release: release}
		h := mountExport(&Handler{repo: repo})

		holdCode := make(chan int, 1)
		go func() {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?format=netscape", nil))
			holdCode <- rec.Code
		}()
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("first export never entered ListAllLinks")
		}

		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?format=json", nil))
			done <- rec
		}()
		select {
		case rec := <-done:
			require.Equal(t, http.StatusTooManyRequests, rec.Code)
			assert.Equal(t, "1", rec.Header().Get("Retry-After"))
			assert.Contains(t, rec.Body.String(), "export_busy")
			assert.Equal(t, int32(1), repo.calls.Load(), "rejected export must not run ListAllLinks")
		case <-time.After(500 * time.Millisecond):
			t.Fatal("second export blocked in ListAllLinks instead of 429 before the query")
		}

		releaseAll()
		assert.Equal(t, http.StatusOK, <-holdCode)
	})

	t.Run("row ceiling holds", func(t *testing.T) {
		h := mountExport(&Handler{repo: &fakeExportRepo{
			links: make([]linkRow, maxExportLinks+1),
		}})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?format=netscape", nil))
		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		assert.Contains(t, rec.Body.String(), "export_too_large")
		assert.NotContains(t, rec.Body.String(), "<!DOCTYPE")
		assert.Less(t, rec.Body.Len(), 2048, "refusal must not encode a payload-sized buffer")

		jsonRec := httptest.NewRecorder()
		h.ServeHTTP(jsonRec, httptest.NewRequest(http.MethodGet, "/?format=json", nil))
		require.Equal(t, http.StatusRequestEntityTooLarge, jsonRec.Code)
		assert.Contains(t, jsonRec.Body.String(), "export_too_large")
		assert.NotContains(t, jsonRec.Body.String(), `"version"`)
	})
}
