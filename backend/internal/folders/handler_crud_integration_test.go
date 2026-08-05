//go:build integration

package folders_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/folders"
)

func TestHandler_Create_HappyAndValidation(t *testing.T) {
	h, _, _ := newHandlerRouter(t)

	rr := doJSON(t, h, http.MethodPost, "/folders/", map[string]any{
		"name":  "New Folder",
		"color": "#8B85FF",
	})
	require.Equal(t, http.StatusCreated, rr.Code)
	var f folders.Folder
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &f))
	assert.Equal(t, "New Folder", f.Name)
	assert.Equal(t, "#8B85FF", f.Color)
	assert.False(t, f.HasPassword)

	rr = doJSON(t, h, http.MethodPost, "/folders/", map[string]any{
		"name":  "Bad",
		"color": `red url("https://evil")`,
	})
	require.Equal(t, http.StatusBadRequest, rr.Code)
	assertErrorCode(t, rr, "invalid_input")

	rr = doJSON(t, h, http.MethodPost, "/folders/", map[string]any{
		"name":  "   ",
		"color": "#abc",
	})
	require.Equal(t, http.StatusBadRequest, rr.Code)
	assertErrorCode(t, rr, "invalid_input")

	req := httptest.NewRequest(http.MethodPost, "/folders/", nil)
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandler_Create_WithPasswordAndParent(t *testing.T) {
	h, _, _ := newHandlerRouter(t)

	rr := doJSON(t, h, http.MethodPost, "/folders/", map[string]any{
		"name":  "Parent",
		"color": "#111111",
	})
	require.Equal(t, http.StatusCreated, rr.Code)
	var parent folders.Folder
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &parent))

	pw := "secret-pw"
	hint := "not the password"
	rr = doJSON(t, h, http.MethodPost, "/folders/", map[string]any{
		"name":          "Child",
		"color":         "#222222",
		"parent_id":     parent.ID,
		"password":      pw,
		"password_hint": hint,
	})
	require.Equal(t, http.StatusCreated, rr.Code)
	var child folders.Folder
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &child))
	assert.True(t, child.HasPassword)
	require.NotNil(t, child.ParentID)
	assert.Equal(t, parent.ID, *child.ParentID)
	require.NotNil(t, child.PasswordHint)
	assert.Equal(t, hint, *child.PasswordHint)

	rr = doJSON(t, h, http.MethodPost, "/folders/", map[string]any{
		"name":          "BadHint",
		"color":         "#333333",
		"password":      "same-thing",
		"password_hint": "same-thing",
	})
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandler_Get_OKAndNotFound(t *testing.T) {
	h, repo, uid := newHandlerRouter(t)
	ctx := context.Background()
	f, err := repo.Create(ctx, uid, folders.CreateInput{Name: "G", Color: "#abc"})
	require.NoError(t, err)

	rr := doJSON(t, h, http.MethodGet, "/folders/"+strconv.FormatInt(f.ID, 10), nil)
	require.Equal(t, http.StatusOK, rr.Code)
	var got folders.Folder
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	assert.Equal(t, "G", got.Name)

	rr = doJSON(t, h, http.MethodGet, "/folders/999999", nil)
	require.Equal(t, http.StatusNotFound, rr.Code)

	rr = doJSON(t, h, http.MethodGet, "/folders/not-a-number", nil)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandler_Update_HappyAndValidation(t *testing.T) {
	h, repo, uid := newHandlerRouter(t)
	ctx := context.Background()
	f, err := repo.Create(ctx, uid, folders.CreateInput{Name: "Old", Color: "#abc"})
	require.NoError(t, err)

	rr := doJSON(t, h, http.MethodPatch, "/folders/"+strconv.FormatInt(f.ID, 10), map[string]any{
		"name":  "Renamed",
		"color": "#defabc",
	})
	require.Equal(t, http.StatusOK, rr.Code)
	var got folders.Folder
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	assert.Equal(t, "Renamed", got.Name)

	rr = doJSON(t, h, http.MethodPatch, "/folders/"+strconv.FormatInt(f.ID, 10), map[string]any{
		"color": "not-a-color",
	})
	require.Equal(t, http.StatusBadRequest, rr.Code)
	assertErrorCode(t, rr, "invalid_input")

	rr = doJSON(t, h, http.MethodPatch, "/folders/xyz", map[string]any{"name": "x"})
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = doJSON(t, h, http.MethodPatch, "/folders/999999", map[string]any{"name": "x"})
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestHandler_Delete_DefaultAndCascade(t *testing.T) {
	h, repo, uid := newHandlerRouter(t)
	ctx := context.Background()

	f, err := repo.Create(ctx, uid, folders.CreateInput{Name: "ToDelete", Color: "#abc"})
	require.NoError(t, err)

	rr := doJSON(t, h, http.MethodDelete, "/folders/"+strconv.FormatInt(f.ID, 10), nil)
	require.Equal(t, http.StatusNoContent, rr.Code)

	rr = doJSON(t, h, http.MethodGet, "/folders/"+strconv.FormatInt(f.ID, 10), nil)
	require.Equal(t, http.StatusNotFound, rr.Code)

	parent, err := repo.Create(ctx, uid, folders.CreateInput{Name: "CascParent", Color: "#111"})
	require.NoError(t, err)
	child, err := repo.Create(ctx, uid, folders.CreateInput{Name: "CascChild", Color: "#222", ParentID: &parent.ID})
	require.NoError(t, err)

	rr = doJSON(t, h, http.MethodDelete, "/folders/"+strconv.FormatInt(parent.ID, 10)+"?cascade=1", nil)
	require.Equal(t, http.StatusNoContent, rr.Code)

	rr = doJSON(t, h, http.MethodGet, "/folders/"+strconv.FormatInt(parent.ID, 10), nil)
	require.Equal(t, http.StatusNotFound, rr.Code)
	rr = doJSON(t, h, http.MethodGet, "/folders/"+strconv.FormatInt(child.ID, 10), nil)
	require.Equal(t, http.StatusNotFound, rr.Code)

	p3, err := repo.Create(ctx, uid, folders.CreateInput{Name: "P3", Color: "#ccc"})
	require.NoError(t, err)
	rr = doJSON(t, h, http.MethodDelete, "/folders/"+strconv.FormatInt(p3.ID, 10)+"?cascade=true", nil)
	require.Equal(t, http.StatusNoContent, rr.Code)

	rr = doJSON(t, h, http.MethodDelete, "/folders/999999", nil)
	require.Equal(t, http.StatusNotFound, rr.Code)

	rr = doJSON(t, h, http.MethodDelete, "/folders/abc", nil)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandler_ResetPassword_NotFoundAndBadJSON(t *testing.T) {
	master := fakeMaster{configured: true, password: "master-ok"}
	h, _, _ := newHandlerRouterMaster(t, master)

	rr := doJSON(t, h, http.MethodPost, "/folders/999999/reset-password",
		map[string]any{"master_password": "master-ok"})
	require.Equal(t, http.StatusNotFound, rr.Code)

	rr = doJSON(t, h, http.MethodPost, "/folders/not-id/reset-password",
		map[string]any{"master_password": "master-ok"})
	require.Equal(t, http.StatusBadRequest, rr.Code)

	req := httptest.NewRequest(http.MethodPost, "/folders/1/reset-password", nil)
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandler_List_RootAndFlat(t *testing.T) {
	h, repo, uid := newHandlerRouter(t)
	ctx := context.Background()
	root, err := repo.Create(ctx, uid, folders.CreateInput{Name: "R", Color: "#aabbcc"})
	require.NoError(t, err)
	_, err = repo.Create(ctx, uid, folders.CreateInput{Name: "C", Color: "#ddeeff", ParentID: &root.ID})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/folders/?root=true", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var roots []folders.Folder
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &roots))
	require.Len(t, roots, 1)

	req = httptest.NewRequest(http.MethodGet, "/folders/", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var all []folders.Folder
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &all))
	assert.Len(t, all, 2)
}

func TestHandler_List_FieldsMinimal(t *testing.T) {
	h, repo, uid := newHandlerRouter(t)
	ctx := context.Background()
	_, err := repo.Create(ctx, uid, folders.CreateInput{Name: "Min", Color: "#112233"})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/folders/?fields=minimal", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var out []folders.Folder
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.NotEmpty(t, out)
	assert.Empty(t, out[0].Previews)
	assert.EqualValues(t, 0, out[0].LinkCount)
}

func TestHandler_Unlock_BadIDAndNotFound(t *testing.T) {
	h, _, _ := newHandlerRouter(t)

	rr := doJSON(t, h, http.MethodPost, "/folders/xyz/unlock", map[string]string{"password": "x"})
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = doJSON(t, h, http.MethodPost, "/folders/999999/unlock", map[string]string{"password": "x"})
	require.Equal(t, http.StatusNotFound, rr.Code)
}
