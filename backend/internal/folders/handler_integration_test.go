//go:build integration

package folders_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/folders"
	"foldex/internal/testdb"
)

// testUnlockKey is a fixed 32-byte HMAC key — real deployments get one from
// folders.LoadOrGenerateFolderUnlockKey, but these tests only need
// IssueUnlockToken/CheckUnlock (exercised indirectly via the handler) to
// agree on SOME key.
var testUnlockKey = []byte("01234567890123456789012345678901")

func newHandlerRouter(t *testing.T) (http.Handler, *folders.Repository) {
	t.Helper()
	pool := testdb.New(t)
	repo := folders.NewRepository(pool)
	r := chi.NewRouter()
	r.Route("/folders", folders.NewHandler(repo, testUnlockKey).Mount)
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

// TestHandler_Unlock_HappyPath_WrongPassword_NotProtected covers all three
// /unlock outcomes end-to-end through the real HTTP handler.
func TestHandler_Unlock_HappyPath_WrongPassword_NotProtected(t *testing.T) {
	h, repo := newHandlerRouter(t)
	ctx := context.Background()

	pw := "correct-horse"
	protected, err := repo.Create(ctx, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)
	open, err := repo.Create(ctx, folders.CreateInput{Name: "Open", Color: "#def"})
	require.NoError(t, err)

	// Wrong password → 401 wrong_password.
	rr := doJSON(t, h, http.MethodPost, "/folders/"+strconv.FormatInt(protected.ID, 10)+"/unlock",
		map[string]string{"password": "nope"})
	require.Equal(t, http.StatusUnauthorized, rr.Code)
	assertErrorCode(t, rr, "wrong_password")

	// Unlocking a folder with no password set → 400 not_protected.
	rr = doJSON(t, h, http.MethodPost, "/folders/"+strconv.FormatInt(open.ID, 10)+"/unlock",
		map[string]string{"password": "anything"})
	require.Equal(t, http.StatusBadRequest, rr.Code)
	assertErrorCode(t, rr, "not_protected")

	// Correct password → 200 with a usable token.
	rr = doJSON(t, h, http.MethodPost, "/folders/"+strconv.FormatInt(protected.ID, 10)+"/unlock",
		map[string]string{"password": pw})
	require.Equal(t, http.StatusOK, rr.Code)
	var out struct {
		UnlockToken string `json:"unlock_token"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	assert.NotEmpty(t, out.UnlockToken)

	hash, err := repo.PasswordHashFor(ctx, protected.ID)
	require.NoError(t, err)
	require.NotNil(t, hash)
	assert.True(t, folders.VerifyUnlockToken(testUnlockKey, protected.ID, *hash, out.UnlockToken))
}

// TestHandler_List_ParentIDGate mirrors internal/entries' folder_id gate
// test — listing a protected folder's CHILDREN is just as much a content
// read as listing its links, so GET /api/folders?parent_id=X needs the same
// unlock-token proof.
func TestHandler_List_ParentIDGate(t *testing.T) {
	h, repo := newHandlerRouter(t)
	ctx := context.Background()

	pw := "hunter22"
	protected, err := repo.Create(ctx, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)
	_, err = repo.Create(ctx, folders.CreateInput{Name: "Hidden Child", Color: "#def", ParentID: &protected.ID})
	require.NoError(t, err)

	// No token → 403 folder_locked, child names never leave the server.
	req := httptest.NewRequest(http.MethodGet, "/folders/?parent_id="+strconv.FormatInt(protected.ID, 10), nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assertErrorCode(t, rr, "folder_locked")

	// Valid token → 200 with the real children.
	hash, err := repo.PasswordHashFor(ctx, protected.ID)
	require.NoError(t, err)
	require.NotNil(t, hash)
	token := folders.IssueUnlockToken(testUnlockKey, protected.ID, *hash)
	req = httptest.NewRequest(http.MethodGet, "/folders/?parent_id="+strconv.FormatInt(protected.ID, 10), nil)
	req.Header.Set(folders.UnlockHeader, token)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var out []folders.Folder
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Len(t, out, 1)
	assert.Equal(t, "Hidden Child", out[0].Name)

	// Root listing (no parent_id) is never gated — the protected folder
	// itself is visible, just with its previews redacted (locked in the
	// repository-level test).
	req = httptest.NewRequest(http.MethodGet, "/folders/?root=1", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
}

// TestHandler_Update_PasswordChange_WrongCurrentPassword_Returns401 locks
// the HTTP-level surfacing of the repository's typed wrong_password error —
// generic httperr.Write already round-trips it, this just confirms the
// wiring end-to-end.
func TestHandler_Update_PasswordChange_WrongCurrentPassword_Returns401(t *testing.T) {
	h, repo := newHandlerRouter(t)
	ctx := context.Background()

	oldPW := "old-pass1"
	f, err := repo.Create(ctx, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &oldPW})
	require.NoError(t, err)

	rr := doJSON(t, h, http.MethodPatch, "/folders/"+strconv.FormatInt(f.ID, 10), map[string]any{
		"password":         "new-pass1",
		"current_password": "wrong",
	})
	require.Equal(t, http.StatusUnauthorized, rr.Code)
	assertErrorCode(t, rr, "wrong_password")
}

func assertErrorCode(t *testing.T, rr *httptest.ResponseRecorder, code string) {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, code, body.Error.Code)
}
