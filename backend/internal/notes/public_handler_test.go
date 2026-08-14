//go:build integration

package notes_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/notes"
	"foldex/internal/testdb"
)

func TestPublicHandler_RendersSanitizedHTML(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := notes.NewRepository(pool)
	created, err := repo.Create(context.Background(), uid, notes.CreateInput{
		Title:    "<b>Bold</b> Title",
		BodyHTML: "<p>hello <strong>world</strong></p>",
	})
	require.NoError(t, err)

	r := chi.NewRouter()
	notes.NewPublicHandler(repo, true).Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/n/"+created.Slug, nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	body, err := io.ReadAll(rr.Body)
	require.NoError(t, err)
	html := string(body)
	assert.Contains(t, html, "<strong>world</strong>")
	// Title is plain text rendered through {{.Title}} — must be HTML-escaped,
	// not interpreted as markup.
	assert.Contains(t, html, "&lt;b&gt;Bold&lt;/b&gt; Title")
	assert.NotContains(t, html, "<b>Bold</b> Title")

	got, err := repo.Get(context.Background(), uid, created.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, got.ClickCount, "viewing the public page must log a click")
}

func TestPublicHandler_ByID(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := notes.NewRepository(pool)
	created, err := repo.Create(context.Background(), uid, notes.CreateInput{Title: "ById", BodyHTML: "<p>x</p>"})
	require.NoError(t, err)

	r := chi.NewRouter()
	notes.NewPublicHandler(repo, true).Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/n/"+idStr(created.ID), nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestPublicHandler_NotFound(t *testing.T) {
	pool := testdb.Shared(t)

	_ = testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := notes.NewRepository(pool)
	r := chi.NewRouter()
	notes.NewPublicHandler(repo, true).Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/n/does-not-exist", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestPublicHandler_PublicNumericIDsFeatureFlag(t *testing.T) {
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := notes.NewRepository(pool)
	created, err := repo.Create(context.Background(), uid, notes.CreateInput{Title: "Flag Note", BodyHTML: "<p>x</p>"})
	require.NoError(t, err)

	tests := []struct {
		name            string
		target          string
		allowNumericIDs bool
		wantStatus      int
	}{
		{name: "numeric on", target: idStr(created.ID), allowNumericIDs: true, wantStatus: http.StatusOK},
		{name: "numeric off", target: idStr(created.ID), allowNumericIDs: false, wantStatus: http.StatusNotFound},
		{name: "slug while numeric off", target: created.Slug, allowNumericIDs: false, wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := chi.NewRouter()
			notes.NewPublicHandler(repo, tt.allowNumericIDs).Mount(r)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/n/"+tt.target, nil)
			r.ServeHTTP(rec, req)
			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}
