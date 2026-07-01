//go:build integration

package notes_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/notes"
	"foldex/internal/testdb"
)

func newRouter(t *testing.T) (http.Handler, *notes.Repository) {
	t.Helper()
	pool := testdb.New(t)
	repo := notes.NewRepository(pool)
	r := chi.NewRouter()
	r.Route("/notes", notes.NewHandler(repo, nil).Mount)
	return r, repo
}

func doJSON(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestHandler_CreateGetUpdateDelete(t *testing.T) {
	h, _ := newRouter(t)

	rr := doJSON(t, h, http.MethodPost, "/notes/", map[string]any{
		"title":     "Hello",
		"body_html": "<p>hi <script>alert(1)</script></p>",
	})
	require.Equal(t, http.StatusCreated, rr.Code)
	var created notes.Note
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	assert.Equal(t, "hello", created.Slug)
	assert.NotContains(t, created.BodyHTML, "<script", "handler must sanitize before persisting")

	rr = doJSON(t, h, http.MethodGet, "/notes/"+idStr(created.ID), nil)
	require.Equal(t, http.StatusOK, rr.Code)

	rr = doJSON(t, h, http.MethodPatch, "/notes/"+idStr(created.ID), map[string]any{"pinned": true})
	require.Equal(t, http.StatusOK, rr.Code)
	var updated notes.Note
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &updated))
	assert.True(t, updated.Pinned)

	rr = doJSON(t, h, http.MethodDelete, "/notes/"+idStr(created.ID), nil)
	require.Equal(t, http.StatusNoContent, rr.Code)

	rr = doJSON(t, h, http.MethodGet, "/notes/"+idStr(created.ID), nil)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestHandler_Create_InvalidInput(t *testing.T) {
	h, _ := newRouter(t)
	rr := doJSON(t, h, http.MethodPost, "/notes/", map[string]any{"title": ""})
	require.Equal(t, http.StatusBadRequest, rr.Code)
	var body map[string]map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, "invalid_input", body["error"]["code"])
}

func TestHandler_Create_SlugConflict(t *testing.T) {
	h, _ := newRouter(t)
	rr := doJSON(t, h, http.MethodPost, "/notes/", map[string]any{"title": "A", "slug": "dup"})
	require.Equal(t, http.StatusCreated, rr.Code)

	rr = doJSON(t, h, http.MethodPost, "/notes/", map[string]any{"title": "B", "slug": "dup"})
	require.Equal(t, http.StatusConflict, rr.Code)
	var body map[string]map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, "slug_taken", body["error"]["code"])
}

func TestHandler_List(t *testing.T) {
	h, _ := newRouter(t)
	doJSON(t, h, http.MethodPost, "/notes/", map[string]any{"title": "First"})
	doJSON(t, h, http.MethodPost, "/notes/", map[string]any{"title": "Second"})

	rr := doJSON(t, h, http.MethodGet, "/notes/", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	var out []notes.Note
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	assert.Len(t, out, 2)
}

func TestHandler_List_QueryParams(t *testing.T) {
	h, _ := newRouter(t)
	doJSON(t, h, http.MethodPost, "/notes/", map[string]any{"title": "Alpha", "pinned": true})
	doJSON(t, h, http.MethodPost, "/notes/", map[string]any{"title": "Beta"})

	rr := doJSON(t, h, http.MethodGet, "/notes/?q=Alpha&sort=alpha&limit=1&offset=0&ungrouped=1", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	var out []notes.Note
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Len(t, out, 1)
	assert.Equal(t, "Alpha", out[0].Title)
}

func TestHandler_List_TagAndFolderIDParams(t *testing.T) {
	h, _ := newRouter(t)
	// Malformed tag/folder_id values must be ignored, not error — same
	// permissive parsing convention as links' list handler.
	rr := doJSON(t, h, http.MethodGet, "/notes/?tag=abc&tag=-1&folder_id=abc", nil)
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestHandler_Update_InvalidInput(t *testing.T) {
	h, _ := newRouter(t)
	created := doJSON(t, h, http.MethodPost, "/notes/", map[string]any{"title": "Valid"})
	var n notes.Note
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &n))

	empty := ""
	rr := doJSON(t, h, http.MethodPatch, "/notes/"+idStr(n.ID), map[string]any{"title": empty})
	require.Equal(t, http.StatusBadRequest, rr.Code)
	var body map[string]map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, "invalid_input", body["error"]["code"])
}

func TestHandler_Update_InvalidID(t *testing.T) {
	h, _ := newRouter(t)
	rr := doJSON(t, h, http.MethodPatch, "/notes/not-a-number", map[string]any{"title": "x"})
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandler_Get_InvalidID(t *testing.T) {
	h, _ := newRouter(t)
	rr := doJSON(t, h, http.MethodGet, "/notes/not-a-number", nil)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandler_Delete_NotFound(t *testing.T) {
	h, _ := newRouter(t)
	rr := doJSON(t, h, http.MethodDelete, "/notes/999999", nil)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func idStr(id int64) string {
	return strconv.FormatInt(id, 10)
}
