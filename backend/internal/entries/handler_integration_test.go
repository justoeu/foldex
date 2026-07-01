//go:build integration

package entries_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/entries"
	"foldex/internal/links"
	"foldex/internal/notes"
	"foldex/internal/testdb"
)

func TestHandler_List(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	_, err := links.NewRepository(pool).Create(ctx, links.CreateInput{URL: "https://example.com/x", Title: "A link"})
	require.NoError(t, err)
	_, err = notes.NewRepository(pool).Create(ctx, notes.CreateInput{Title: "A note"})
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Route("/entries", entries.NewHandler(entries.NewRepository(pool)).Mount)

	req := httptest.NewRequest(http.MethodGet, "/entries/?sort=alpha", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var out []entries.Entry
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Len(t, out, 2)
	assert.Equal(t, "A link", out[0].Title)
	assert.Equal(t, "A note", out[1].Title)
}

func TestHandler_List_QueryParams(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	tag, err := func() (int64, error) {
		var id int64
		err := pool.QueryRow(ctx, `INSERT INTO tag (name, color) VALUES ('t', '#fff') RETURNING id`).Scan(&id)
		return id, err
	}()
	require.NoError(t, err)
	_, err = links.NewRepository(pool).Create(ctx, links.CreateInput{URL: "https://example.com/x", Title: "Tagged", TagIDs: []int64{tag}})
	require.NoError(t, err)
	_, err = notes.NewRepository(pool).Create(ctx, notes.CreateInput{Title: "Untagged note"})
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Route("/entries", entries.NewHandler(entries.NewRepository(pool)).Mount)

	req := httptest.NewRequest(http.MethodGet, "/entries/?q=Tagged&tag="+strconv.FormatInt(tag, 10)+"&limit=5&offset=0&ungrouped=1&sort=clicks", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var out []entries.Entry
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Len(t, out, 1)
	assert.Equal(t, "Tagged", out[0].Title)

	// Malformed numeric params must be ignored, not error.
	req2 := httptest.NewRequest(http.MethodGet, "/entries/?tag=abc&folder_id=abc", nil)
	rr2 := httptest.NewRecorder()
	r.ServeHTTP(rr2, req2)
	assert.Equal(t, http.StatusOK, rr2.Code)
}

func TestHandler_List_NoMutationRoutes(t *testing.T) {
	pool := testdb.New(t)
	r := chi.NewRouter()
	r.Route("/entries", entries.NewHandler(entries.NewRepository(pool)).Mount)

	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete} {
		req := httptest.NewRequest(method, "/entries/", nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code, "entries is read-only — %s must not be routed", method)
	}
}
