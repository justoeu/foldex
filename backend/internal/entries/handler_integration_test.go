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
	"foldex/internal/folders"
	"foldex/internal/links"
	"foldex/internal/notes"
	"foldex/internal/testdb"
)

// testUnlockKey is a fixed 32-byte HMAC key for tests — real deployments get
// one from folders.LoadOrGenerateFolderUnlockKey, but these tests only need
// IssueUnlockToken/CheckUnlock to agree on SOME key.
var testUnlockKey = []byte("01234567890123456789012345678901")

func TestHandler_List(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	_, err := links.NewRepository(pool).Create(ctx, links.CreateInput{URL: "https://example.com/x", Title: "A link"})
	require.NoError(t, err)
	_, err = notes.NewRepository(pool).Create(ctx, notes.CreateInput{Title: "A note"})
	require.NoError(t, err)

	foldersRepo := folders.NewRepository(pool)
	r := chi.NewRouter()
	r.Route("/entries", entries.NewHandler(entries.NewRepository(pool), foldersRepo, testUnlockKey).Mount)

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
	r.Route("/entries", entries.NewHandler(entries.NewRepository(pool), folders.NewRepository(pool), testUnlockKey).Mount)

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
	r.Route("/entries", entries.NewHandler(entries.NewRepository(pool), folders.NewRepository(pool), testUnlockKey).Mount)

	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete} {
		req := httptest.NewRequest(method, "/entries/", nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code, "entries is read-only — %s must not be routed", method)
	}
}

// TestHandler_List_FolderGate locks the content-gate on GET
// /api/entries?folder_id=X — the ONE read path that returns a protected
// folder's real links+notes (see internal/entries package doc + CLAUDE.md's
// folder-password invariant).
func TestHandler_List_FolderGate(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	foldersRepo := folders.NewRepository(pool)

	pw := "hunter22"
	protected, err := foldersRepo.Create(ctx, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)
	_, err = links.NewRepository(pool).Create(ctx, links.CreateInput{
		URL: "https://hidden.example", Title: "Hidden", FolderID: &protected.ID,
	})
	require.NoError(t, err)

	open, err := foldersRepo.Create(ctx, folders.CreateInput{Name: "Open", Color: "#def"})
	require.NoError(t, err)
	_, err = links.NewRepository(pool).Create(ctx, links.CreateInput{
		URL: "https://visible.example", Title: "Visible", FolderID: &open.ID,
	})
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Route("/entries", entries.NewHandler(entries.NewRepository(pool), foldersRepo, testUnlockKey).Mount)

	// No token at all → 403 folder_locked, no content leaked.
	req := httptest.NewRequest(http.MethodGet, "/entries/?folder_id="+strconv.FormatInt(protected.ID, 10), nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
	var body map[string]map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, "folder_locked", body["error"]["code"])

	// Wrong/garbage token → still 403.
	req = httptest.NewRequest(http.MethodGet, "/entries/?folder_id="+strconv.FormatInt(protected.ID, 10), nil)
	req.Header.Set(folders.UnlockHeader, "garbage")
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)

	// Valid token for the RIGHT folder → 200 with the real content.
	hash, err := foldersRepo.PasswordHashFor(ctx, protected.ID)
	require.NoError(t, err)
	require.NotNil(t, hash)
	token := folders.IssueUnlockToken(testUnlockKey, protected.ID, *hash)
	req = httptest.NewRequest(http.MethodGet, "/entries/?folder_id="+strconv.FormatInt(protected.ID, 10), nil)
	req.Header.Set(folders.UnlockHeader, token)
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var out []entries.Entry
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Len(t, out, 1)
	assert.Equal(t, "Hidden", out[0].Title)

	// A token minted for a DIFFERENT protected folder must not unlock this one.
	otherPW := "other-pass"
	other, err := foldersRepo.Create(ctx, folders.CreateInput{Name: "Other Secret", Color: "#123", Password: &otherPW})
	require.NoError(t, err)
	otherHash, err := foldersRepo.PasswordHashFor(ctx, other.ID)
	require.NoError(t, err)
	require.NotNil(t, otherHash)
	wrongFolderToken := folders.IssueUnlockToken(testUnlockKey, other.ID, *otherHash)
	req = httptest.NewRequest(http.MethodGet, "/entries/?folder_id="+strconv.FormatInt(protected.ID, 10), nil)
	req.Header.Set(folders.UnlockHeader, wrongFolderToken)
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)

	// An unprotected folder needs no token at all.
	req = httptest.NewRequest(http.MethodGet, "/entries/?folder_id="+strconv.FormatInt(open.ID, 10), nil)
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
}
