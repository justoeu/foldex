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
	"foldex/internal/pkg/authctx"
	"foldex/internal/testdb"

	"foldex/internal/pkg/authctx/authctxtest"
	"os"
)

// TestMain owns the lifetime of this package's shared Postgres container.
//
// It cannot be a t.Cleanup: os.Exit skips deferred work, and a cleanup hung off
// whichever test ran first would tear the database down while the rest of the
// package still needed it. The Makefile disables testcontainers' reaper, so
// nothing else would collect it.
func TestMain(m *testing.M) {
	code := m.Run()
	testdb.StopShared()
	os.Exit(code)
}

// testUnlockKey is a fixed 32-byte HMAC key for tests — real deployments get
// one from folders.LoadOrGenerateFolderUnlockKey, but these tests only need
// IssueUnlockToken/CheckUnlock to agree on SOME key.
var testUnlockKey = []byte("01234567890123456789012345678901")

func TestHandler_List(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	ctx := context.Background()
	_, err := links.NewRepository(pool).Create(ctx, uid, links.CreateInput{URL: "https://example.com/x", Title: "A link"})
	require.NoError(t, err)
	_, err = notes.NewRepository(pool).Create(ctx, uid, notes.CreateInput{Title: "A note"})
	require.NoError(t, err)

	foldersRepo := folders.NewRepository(pool)
	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(uid))
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
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	ctx := context.Background()
	tag, err := func() (int64, error) {
		var id int64
		err := pool.QueryRow(ctx, `INSERT INTO tag (user_id, name, color) VALUES ($1, 't', '#fff') RETURNING id`, int64(uid)).Scan(&id)
		return id, err
	}()
	require.NoError(t, err)
	_, err = links.NewRepository(pool).Create(ctx, uid, links.CreateInput{URL: "https://example.com/x", Title: "Tagged", TagIDs: []int64{tag}})
	require.NoError(t, err)
	_, err = notes.NewRepository(pool).Create(ctx, uid, notes.CreateInput{Title: "Untagged note"})
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(uid))
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
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(uid))
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
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	ctx := context.Background()
	foldersRepo := folders.NewRepository(pool)

	pw := "hunter22"
	protected, err := foldersRepo.Create(ctx, uid, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)
	_, err = links.NewRepository(pool).Create(ctx, uid, links.CreateInput{
		URL: "https://hidden.example", Title: "Hidden", FolderID: &protected.ID,
	})
	require.NoError(t, err)

	open, err := foldersRepo.Create(ctx, uid, folders.CreateInput{Name: "Open", Color: "#def"})
	require.NoError(t, err)
	_, err = links.NewRepository(pool).Create(ctx, uid, links.CreateInput{
		URL: "https://visible.example", Title: "Visible", FolderID: &open.ID,
	})
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(uid))
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
	hash, err := foldersRepo.PasswordHashFor(ctx, uid, protected.ID)
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
	other, err := foldersRepo.Create(ctx, uid, folders.CreateInput{Name: "Other Secret", Color: "#123", Password: &otherPW})
	require.NoError(t, err)
	otherHash, err := foldersRepo.PasswordHashFor(ctx, uid, other.ID)
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

func TestHandler_PreviewStatuses_UsesTheProtectedFolderGate(t *testing.T) {
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	otherUID := testdb.SeedUser(t, pool, "other@test.local", "user")
	ctx := context.Background()
	foldersRepo := folders.NewRepository(pool)
	password := "folder-password"
	folder, err := foldersRepo.Create(ctx, uid, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &password})
	require.NoError(t, err)
	link, err := links.NewRepository(pool).Create(ctx, uid, links.CreateInput{URL: "https://secret-status.example", Title: "Secret", FolderID: &folder.ID})
	require.NoError(t, err)
	other, err := links.NewRepository(pool).Create(ctx, otherUID, links.CreateInput{URL: "https://other-status.example", Title: "Other"})
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(uid))
	r.Route("/entries", entries.NewHandler(entries.NewRepository(pool), foldersRepo, testUnlockKey).Mount)
	path := "/entries/preview-status?folder_id=" + strconv.FormatInt(folder.ID, 10) + "&id=" + strconv.FormatInt(link.ID, 10) + "&id=" + strconv.FormatInt(other.ID, 10)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.NotContains(t, rr.Body.String(), "pending")

	hash, err := foldersRepo.PasswordHashFor(ctx, uid, folder.ID)
	require.NoError(t, err)
	require.NotNil(t, hash)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set(folders.UnlockHeader, folders.IssueUnlockToken(testUnlockKey, folder.ID, *hash))
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var out []entries.PreviewStatus
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Len(t, out, 2)
	assert.True(t, out[0].Found)
	assert.False(t, out[1].Found)
}

type changingFolderLookup struct {
	hashes []*string
	calls  int
}

func (l *changingFolderLookup) PasswordHashFor(context.Context, authctx.UserID, int64) (*string, error) {
	i := l.calls
	l.calls++
	return l.hashes[i], nil
}

func TestHandler_List_FolderGateProtocolAcrossEndpoints(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	owner := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	otherOwner := testdb.SeedUser(t, pool, "other@test.local", "user")
	foldersRepo := folders.NewRepository(pool)

	password := "folder-password"
	folder, err := foldersRepo.Create(ctx, owner, folders.CreateInput{Name: "Private", Color: "#abc", Password: &password})
	require.NoError(t, err)
	link, err := links.NewRepository(pool).Create(ctx, owner, links.CreateInput{
		URL: "https://private.example", Title: "Private link", FolderID: &folder.ID,
	})
	require.NoError(t, err)
	note, err := notes.NewRepository(pool).Create(ctx, owner, notes.CreateInput{
		Title: "Private note", BodyHTML: "<p>private body</p>", FolderID: &folder.ID,
	})
	require.NoError(t, err)
	hash, err := foldersRepo.PasswordHashFor(ctx, owner, folder.ID)
	require.NoError(t, err)
	require.NotNil(t, hash)
	key := []byte("01234567890123456789012345678901")
	token := folders.IssueUnlockToken(key, folder.ID, *hash)

	otherFolder, err := foldersRepo.Create(ctx, otherOwner, folders.CreateInput{Name: "Other private", Color: "#def", Password: &password})
	require.NoError(t, err)
	otherHash, err := foldersRepo.PasswordHashFor(ctx, otherOwner, otherFolder.ID)
	require.NoError(t, err)
	require.NotNil(t, otherHash)

	endpointTests := []struct {
		name       string
		path       func(int64) string
		mount      func(folders.PasswordHashLookup) http.Handler
		assertType func(*testing.T, []byte)
	}{
		{
			name: "entries", path: func(id int64) string { return "/entries/?folder_id=" + strconv.FormatInt(id, 10) },
			mount: func(lookup folders.PasswordHashLookup) http.Handler {
				r := chi.NewRouter()
				r.Route("/entries", entries.NewHandler(entries.NewRepository(pool), lookup, key).Mount)
				return r
			},
			assertType: func(t *testing.T, body []byte) {
				var out []entries.Entry
				require.NoError(t, json.Unmarshal(body, &out))
				require.Len(t, out, 2)
				assert.ElementsMatch(t, []string{"link", "note"}, []string{out[0].Kind, out[1].Kind})
			},
		},
		{
			name: "links", path: func(id int64) string { return "/links/?folder_id=" + strconv.FormatInt(id, 10) },
			mount: func(lookup folders.PasswordHashLookup) http.Handler {
				r := chi.NewRouter()
				r.Route("/links", links.NewHandler(links.NewRepository(pool), nil).WithFolderGate(lookup, key).Mount)
				return r
			},
			assertType: func(t *testing.T, body []byte) {
				var out []links.Link
				require.NoError(t, json.Unmarshal(body, &out))
				require.Len(t, out, 1)
				assert.Equal(t, link.ID, out[0].ID)
				assert.Equal(t, link.URL, out[0].URL)
			},
		},
		{
			name: "notes", path: func(id int64) string { return "/notes/?folder_id=" + strconv.FormatInt(id, 10) },
			mount: func(lookup folders.PasswordHashLookup) http.Handler {
				r := chi.NewRouter()
				r.Route("/notes", notes.NewHandler(notes.NewRepository(pool), nil).WithFolderGate(lookup, key).Mount)
				return r
			},
			assertType: func(t *testing.T, body []byte) {
				var out []notes.Note
				require.NoError(t, json.Unmarshal(body, &out))
				require.Len(t, out, 1)
				assert.Equal(t, note.ID, out[0].ID)
				assert.Empty(t, out[0].BodyHTML, "list response must retain the note summary shape")
			},
		},
	}

	serve := func(h http.Handler, path, unlockToken string, principal authctx.Principal) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set(folders.UnlockHeader, unlockToken)
		req = req.WithContext(authctx.WithPrincipal(req.Context(), principal))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}

	for _, endpoint := range endpointTests {
		t.Run(endpoint.name, func(t *testing.T) {
			apiTokenPrincipal := authctx.Principal{UserID: owner, Role: authctx.RoleAdmin, Via: authctx.ViaAPIToken, TokenID: 1}
			rr := serve(endpoint.mount(foldersRepo), endpoint.path(folder.ID), token, apiTokenPrincipal)
			require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
			endpoint.assertType(t, rr.Body.Bytes())

			rr = serve(endpoint.mount(foldersRepo), endpoint.path(folder.ID), "", apiTokenPrincipal)
			require.Equal(t, http.StatusForbidden, rr.Code)
			assert.NotContains(t, rr.Body.String(), "Private")

			newHash := "changed-hash"
			lookup := &changingFolderLookup{hashes: []*string{hash, &newHash}}
			rr = serve(endpoint.mount(lookup), endpoint.path(folder.ID), token, apiTokenPrincipal)
			require.Equal(t, http.StatusForbidden, rr.Code)
			assert.Equal(t, 2, lookup.calls)
			assert.NotContains(t, rr.Body.String(), "Private")

			rr = serve(endpoint.mount(foldersRepo), endpoint.path(otherFolder.ID),
				folders.IssueUnlockToken(key, otherFolder.ID, *otherHash), apiTokenPrincipal)
			require.Equal(t, http.StatusNotFound, rr.Code, "folder lookup must remain owner-scoped")
		})
	}
}
