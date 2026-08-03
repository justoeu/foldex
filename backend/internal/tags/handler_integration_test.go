//go:build integration

package tags_test

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

	"foldex/internal/tags"
	"foldex/internal/testdb"
)

func newRouter(t *testing.T) http.Handler {
	t.Helper()
	pool := testdb.New(t)
	r := chi.NewRouter()
	r.Route("/tags", tags.NewHandler(tags.NewRepository(pool)).Mount)
	return r
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

func idStr(id int64) string { return strconv.FormatInt(id, 10) }

func TestHandler_CRUD(t *testing.T) {
	h := newRouter(t)

	rr := doJSON(t, h, http.MethodPost, "/tags/", map[string]any{
		"name":  "jira",
		"color": "#1f6feb",
		"icon":  "🪲",
	})
	require.Equal(t, http.StatusCreated, rr.Code)
	var created tags.Tag
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	assert.Equal(t, "jira", created.Name)
	assert.Equal(t, "#1f6feb", created.Color)
	require.NotNil(t, created.Icon)
	assert.Equal(t, "🪲", *created.Icon)

	rr = doJSON(t, h, http.MethodGet, "/tags/"+idStr(created.ID), nil)
	require.Equal(t, http.StatusOK, rr.Code)

	rr = doJSON(t, h, http.MethodGet, "/tags/", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	var list []tags.Tag
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	require.Len(t, list, 1)
	assert.EqualValues(t, 0, list[0].LinkCount)

	rr = doJSON(t, h, http.MethodPatch, "/tags/"+idStr(created.ID), map[string]any{
		"name":  "Jira",
		"color": "#abc",
	})
	require.Equal(t, http.StatusOK, rr.Code)
	var updated tags.Tag
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &updated))
	assert.Equal(t, "Jira", updated.Name)
	assert.Equal(t, "#abc", updated.Color)

	rr = doJSON(t, h, http.MethodDelete, "/tags/"+idStr(created.ID), nil)
	require.Equal(t, http.StatusNoContent, rr.Code)

	rr = doJSON(t, h, http.MethodGet, "/tags/"+idStr(created.ID), nil)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestHandler_Create_InvalidInput(t *testing.T) {
	h := newRouter(t)
	rr := doJSON(t, h, http.MethodPost, "/tags/", map[string]any{"name": ""})
	require.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid_input")
}

func TestHandler_Create_InvalidJSON(t *testing.T) {
	h := newRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/tags/", bytes.NewBufferString(`{not`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid_json")
}

func TestHandler_Create_DuplicateName(t *testing.T) {
	h := newRouter(t)
	rr := doJSON(t, h, http.MethodPost, "/tags/", map[string]any{"name": "docs", "color": "#fff"})
	require.Equal(t, http.StatusCreated, rr.Code)
	rr = doJSON(t, h, http.MethodPost, "/tags/", map[string]any{"name": "docs", "color": "#000"})
	require.Equal(t, http.StatusConflict, rr.Code)
	assert.Contains(t, rr.Body.String(), "tag_name_taken")
}

func TestHandler_Create_DefaultColor(t *testing.T) {
	h := newRouter(t)
	rr := doJSON(t, h, http.MethodPost, "/tags/", map[string]any{"name": "plain"})
	require.Equal(t, http.StatusCreated, rr.Code)
	var created tags.Tag
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	assert.Equal(t, "#6366F1", created.Color)
}

func TestHandler_Get_InvalidID(t *testing.T) {
	h := newRouter(t)
	rr := doJSON(t, h, http.MethodGet, "/tags/not-a-number", nil)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid_id")
}

func TestHandler_Get_NotFound(t *testing.T) {
	h := newRouter(t)
	rr := doJSON(t, h, http.MethodGet, "/tags/99999", nil)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestHandler_Update_InvalidID(t *testing.T) {
	h := newRouter(t)
	rr := doJSON(t, h, http.MethodPatch, "/tags/abc", map[string]any{"name": "x"})
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandler_Update_InvalidInput(t *testing.T) {
	h := newRouter(t)
	created := doJSON(t, h, http.MethodPost, "/tags/", map[string]any{"name": "ok", "color": "#abc"})
	var tag tags.Tag
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &tag))

	rr := doJSON(t, h, http.MethodPatch, "/tags/"+idStr(tag.ID), map[string]any{"name": ""})
	require.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid_input")
}

func TestHandler_Update_InvalidJSON(t *testing.T) {
	h := newRouter(t)
	created := doJSON(t, h, http.MethodPost, "/tags/", map[string]any{"name": "ok", "color": "#abc"})
	var tag tags.Tag
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &tag))

	req := httptest.NewRequest(http.MethodPatch, "/tags/"+idStr(tag.ID), bytes.NewBufferString(`{`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandler_Update_NotFound(t *testing.T) {
	h := newRouter(t)
	rr := doJSON(t, h, http.MethodPatch, "/tags/99999", map[string]any{"name": "ghost"})
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestHandler_Update_DuplicateName(t *testing.T) {
	h := newRouter(t)
	a := doJSON(t, h, http.MethodPost, "/tags/", map[string]any{"name": "alpha", "color": "#abc"})
	b := doJSON(t, h, http.MethodPost, "/tags/", map[string]any{"name": "beta", "color": "#def"})
	var tagA, tagB tags.Tag
	require.NoError(t, json.Unmarshal(a.Body.Bytes(), &tagA))
	require.NoError(t, json.Unmarshal(b.Body.Bytes(), &tagB))

	rr := doJSON(t, h, http.MethodPatch, "/tags/"+idStr(tagB.ID), map[string]any{"name": "alpha"})
	require.Equal(t, http.StatusConflict, rr.Code)
	assert.Contains(t, rr.Body.String(), "tag_name_taken")
}

func TestHandler_Update_EmptyPatch(t *testing.T) {
	h := newRouter(t)
	created := doJSON(t, h, http.MethodPost, "/tags/", map[string]any{"name": "keep", "color": "#abc"})
	var tag tags.Tag
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &tag))

	rr := doJSON(t, h, http.MethodPatch, "/tags/"+idStr(tag.ID), map[string]any{})
	require.Equal(t, http.StatusOK, rr.Code)
	var got tags.Tag
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	assert.Equal(t, "keep", got.Name)
}

func TestHandler_Update_IconOnly(t *testing.T) {
	h := newRouter(t)
	created := doJSON(t, h, http.MethodPost, "/tags/", map[string]any{"name": "ico", "color": "#abc"})
	var tag tags.Tag
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &tag))

	rr := doJSON(t, h, http.MethodPatch, "/tags/"+idStr(tag.ID), map[string]any{"icon": "📌"})
	require.Equal(t, http.StatusOK, rr.Code)
	var got tags.Tag
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	require.NotNil(t, got.Icon)
	assert.Equal(t, "📌", *got.Icon)
}

func TestHandler_Delete_InvalidID(t *testing.T) {
	h := newRouter(t)
	rr := doJSON(t, h, http.MethodDelete, "/tags/nope", nil)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandler_Delete_NotFound(t *testing.T) {
	h := newRouter(t)
	rr := doJSON(t, h, http.MethodDelete, "/tags/99999", nil)
	require.Equal(t, http.StatusNotFound, rr.Code)
}
