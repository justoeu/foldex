//go:build integration

package folders_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/folders"
	"foldex/internal/pkg/domainerr"
	"foldex/internal/testdb"

	"foldex/internal/pkg/authctx"

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

// testUnlockKey is a fixed 32-byte HMAC key — real deployments get one from
// folders.LoadOrGenerateFolderUnlockKey, but these tests only need
// IssueUnlockToken/CheckUnlock (exercised indirectly via the handler) to
// agree on SOME key.
var testUnlockKey = []byte("01234567890123456789012345678901")

// fakeMaster is a test double for folders.MasterPasswordVerifier. When
// configured is false it reports "no master set"; otherwise it matches against
// password (constant-time-ish compare is irrelevant in tests).
type fakeMaster struct {
	configured bool
	password   string
}

func (f fakeMaster) VerifyMaster(_ context.Context, _ authctx.UserID, plain string) (ok bool, configured bool, err error) {
	if !f.configured {
		return false, false, nil
	}
	return plain == f.password, true, nil
}

func newHandlerRouter(t *testing.T) (http.Handler, *folders.Repository, authctx.UserID) {
	return newHandlerRouterMaster(t, fakeMaster{})
}

func newHandlerRouterMaster(t *testing.T, master folders.MasterPasswordVerifier) (http.Handler, *folders.Repository, authctx.UserID) {
	t.Helper()
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := folders.NewRepository(pool)
	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(uid))
	r.Route("/folders", folders.NewHandler(repo, testUnlockKey, master).Mount)
	return r, repo, uid
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
	h, repo, uid := newHandlerRouter(t)
	ctx := context.Background()

	pw := "correct-horse"
	protected, err := repo.Create(ctx, uid, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)
	open, err := repo.Create(ctx, uid, folders.CreateInput{Name: "Open", Color: "#def"})
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

	hash, err := repo.PasswordHashFor(ctx, uid, protected.ID)
	require.NoError(t, err)
	require.NotNil(t, hash)
	assert.True(t, folders.VerifyUnlockToken(testUnlockKey, protected.ID, *hash, out.UnlockToken))
}

// TestHandler_List_ParentIDGate mirrors internal/entries' folder_id gate
// test — listing a protected folder's CHILDREN is just as much a content
// read as listing its links, so GET /api/folders?parent_id=X needs the same
// unlock-token proof.
func TestHandler_List_ParentIDGate(t *testing.T) {
	h, repo, uid := newHandlerRouter(t)
	ctx := context.Background()

	pw := "hunter22"
	protected, err := repo.Create(ctx, uid, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)
	_, err = repo.Create(ctx, uid, folders.CreateInput{Name: "Hidden Child", Color: "#def", ParentID: &protected.ID})
	require.NoError(t, err)

	// No token → 403 folder_locked, child names never leave the server.
	req := httptest.NewRequest(http.MethodGet, "/folders/?parent_id="+strconv.FormatInt(protected.ID, 10), nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assertErrorCode(t, rr, "folder_locked")

	// Valid token → 200 with the real children.
	hash, err := repo.PasswordHashFor(ctx, uid, protected.ID)
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
	h, repo, uid := newHandlerRouter(t)
	ctx := context.Background()

	oldPW := "old-pass1"
	f, err := repo.Create(ctx, uid, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &oldPW})
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

// ── master-password reset + hint (ADR-29) ─────────────────────────────────

// folderResp is the subset of the folder JSON these tests assert on.
type folderResp struct {
	ID           int64   `json:"id"`
	HasPassword  bool    `json:"has_password"`
	PasswordHint *string `json:"password_hint"`
}

func getFolder(t *testing.T, h http.Handler, id int64) folderResp {
	t.Helper()
	rr := doJSON(t, h, http.MethodGet, "/folders/"+strconv.FormatInt(id, 10), nil)
	require.Equal(t, http.StatusOK, rr.Code)
	var f folderResp
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &f))
	return f
}

func TestHandler_ResetPassword_Master(t *testing.T) {
	master := fakeMaster{configured: true, password: "the-master-pass"}
	h, repo, uid := newHandlerRouterMaster(t, master)
	ctx := context.Background()

	pw := "folder-pass"
	hint := "a clue"
	f, err := repo.Create(ctx, uid, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw, PasswordHint: &hint})
	require.NoError(t, err)
	require.True(t, f.HasPassword)

	// Wrong master → 401.
	rr := doJSON(t, h, http.MethodPost, "/folders/"+strconv.FormatInt(f.ID, 10)+"/reset-password",
		map[string]any{"master_password": "nope"})
	assert.Equal(t, http.StatusUnauthorized, rr.Code)

	// Still locked.
	assert.True(t, getFolder(t, h, f.ID).HasPassword)

	// Correct master → 204, folder unprotected + hint cleared.
	rr = doJSON(t, h, http.MethodPost, "/folders/"+strconv.FormatInt(f.ID, 10)+"/reset-password",
		map[string]any{"master_password": "the-master-pass"})
	assert.Equal(t, http.StatusNoContent, rr.Code)

	got := getFolder(t, h, f.ID)
	assert.False(t, got.HasPassword)
	assert.Nil(t, got.PasswordHint)
}

func TestHandler_ResetPassword_MasterNotConfigured(t *testing.T) {
	h, repo, uid := newHandlerRouterMaster(t, fakeMaster{configured: false})
	ctx := context.Background()

	pw := "folder-pass"
	f, err := repo.Create(ctx, uid, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)

	rr := doJSON(t, h, http.MethodPost, "/folders/"+strconv.FormatInt(f.ID, 10)+"/reset-password",
		map[string]any{"master_password": "anything"})
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "master_not_configured")
}

func TestHandler_Update_HintEqualsExistingPassword(t *testing.T) {
	h, repo, uid := newHandlerRouter(t)
	ctx := context.Background()

	pw := "folder-pass"
	f, err := repo.Create(ctx, uid, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)

	// Hint mutation on a protected folder requires current_password (oracle fix).
	rr := doJSON(t, h, http.MethodPatch, "/folders/"+strconv.FormatInt(f.ID, 10),
		map[string]any{"password_hint": "folder-pass"})
	assert.Equal(t, http.StatusUnauthorized, rr.Code, "hint change without current_password must 401")

	// Setting a hint equal to the (unchanged) password must be rejected by the
	// repository's bcrypt equality check inside the tx.
	rr = doJSON(t, h, http.MethodPatch, "/folders/"+strconv.FormatInt(f.ID, 10),
		map[string]any{"password_hint": "folder-pass", "current_password": pw})
	assert.Equal(t, http.StatusBadRequest, rr.Code)

	// A distinct hint succeeds and round-trips.
	rr = doJSON(t, h, http.MethodPatch, "/folders/"+strconv.FormatInt(f.ID, 10),
		map[string]any{"password_hint": "rhymes with holder", "current_password": pw})
	assert.Equal(t, http.StatusOK, rr.Code)
	got := getFolder(t, h, f.ID)
	require.NotNil(t, got.PasswordHint)
	assert.Equal(t, "rhymes with holder", *got.PasswordHint)

	// Removing the password (with the required current password) also clears
	// the hint — a hint for a nonexistent password is dead data.
	rr = doJSON(t, h, http.MethodPatch, "/folders/"+strconv.FormatInt(f.ID, 10),
		map[string]any{"password": nil, "current_password": pw})
	assert.Equal(t, http.StatusOK, rr.Code)
	got = getFolder(t, h, f.ID)
	assert.False(t, got.HasPassword)
	assert.Nil(t, got.PasswordHint)
}

func TestHandler_HintOnUnprotectedFolder_Rejected(t *testing.T) {
	h, repo, uid := newHandlerRouter(t)
	ctx := context.Background()

	f, err := repo.Create(ctx, uid, folders.CreateInput{Name: "Open", Color: "#abc"})
	require.NoError(t, err)

	rr := doJSON(t, h, http.MethodPatch, "/folders/"+strconv.FormatInt(f.ID, 10),
		map[string]any{"password_hint": "a hint with no password"})
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// ── unlock rate limiting (ADR-28) ──────────────────────────────────────────

func TestHandler_Unlock_LocksOutAfterFiveWrongAttempts(t *testing.T) {
	h, repo, uid := newHandlerRouter(t)
	ctx := context.Background()

	pw := "correct-horse"
	f, err := repo.Create(ctx, uid, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)
	path := "/folders/" + strconv.FormatInt(f.ID, 10) + "/unlock"

	// 5 wrong attempts: first 4 are 401 wrong_password, the 5th trips the lock.
	for i := 1; i <= 4; i++ {
		rr := doJSON(t, h, http.MethodPost, path, map[string]string{"password": "nope"})
		require.Equal(t, http.StatusUnauthorized, rr.Code, "attempt %d", i)
		assertErrorCode(t, rr, "wrong_password")
		var body struct {
			FailedAttempts    int `json:"failed_attempts"`
			AttemptsRemaining int `json:"attempts_remaining"`
		}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
		assert.Equal(t, i, body.FailedAttempts)
		assert.Equal(t, 5-i, body.AttemptsRemaining)
	}

	rr := doJSON(t, h, http.MethodPost, path, map[string]string{"password": "nope"})
	require.Equal(t, http.StatusTooManyRequests, rr.Code)
	assertErrorCode(t, rr, "too_many_attempts")

	// While locked, even the CORRECT password is rejected with 429 (and the
	// Retry-After header is set).
	rr = doJSON(t, h, http.MethodPost, path, map[string]string{"password": pw})
	require.Equal(t, http.StatusTooManyRequests, rr.Code)
	assert.NotEmpty(t, rr.Header().Get("Retry-After"))
}

func TestHandler_Unlock_SuccessResetsAttemptCounter(t *testing.T) {
	h, repo, uid := newHandlerRouter(t)
	ctx := context.Background()

	pw := "correct-horse"
	f, err := repo.Create(ctx, uid, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)
	path := "/folders/" + strconv.FormatInt(f.ID, 10) + "/unlock"

	// 4 wrong, then a correct one resets the counter.
	for i := 0; i < 4; i++ {
		doJSON(t, h, http.MethodPost, path, map[string]string{"password": "nope"})
	}
	rr := doJSON(t, h, http.MethodPost, path, map[string]string{"password": pw})
	require.Equal(t, http.StatusOK, rr.Code)

	// A fresh wrong attempt now reports failed_attempts=1 (counter was reset).
	rr = doJSON(t, h, http.MethodPost, path, map[string]string{"password": "nope"})
	require.Equal(t, http.StatusUnauthorized, rr.Code)
	var body struct {
		FailedAttempts int `json:"failed_attempts"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, 1, body.FailedAttempts)
}

// TestHandler_Unlock_ForeignAttemptsCannotLockTheOwnerOut locks the ordering
// of the attempt reservation against the ownership check.
//
// The limiter is keyed by folder id, and folder ids are globally unique — so
// user B can address user A's folder. If the slot were reserved BEFORE the
// uid-scoped lookup that rejects B (as it was until this test landed), each of
// B's in-flight requests would hold a slot against A's budget for the duration
// of a database round-trip, and enough parallel ones would trip an hour-long
// lockout on a folder B cannot even read. Reserving only after ownership is
// proven makes B's requests cost A nothing.
func TestHandler_Unlock_ForeignAttemptsCannotLockTheOwnerOut(t *testing.T) {
	pool := testdb.Shared(t)
	alice := testdb.SeedUser(t, pool, "alice@test.local", "user")
	bob := testdb.SeedUser(t, pool, "bob@test.local", "user")

	repo := folders.NewRepository(pool)
	// ONE handler — the limiter lives on it, so both routers share the state
	// an attacker would be trying to poison.
	h := folders.NewHandler(repo, testUnlockKey, fakeMaster{})
	routerFor := func(uid authctx.UserID) http.Handler {
		r := chi.NewRouter()
		r.Use(authctxtest.Middleware(uid))
		r.Route("/folders", h.Mount)
		return r
	}

	pw := "correct-horse"
	f, err := repo.Create(context.Background(), alice, folders.CreateInput{Name: "Alice secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)
	path := "/folders/" + strconv.FormatInt(f.ID, 10) + "/unlock"

	// Bob hammers Alice's folder id in parallel. Every one of these must 404
	// (the folder is not his) — the point is what they leave behind.
	const burst = 50
	var wg sync.WaitGroup
	wg.Add(burst)
	codes := make([]int, burst)
	bobRouter := routerFor(bob)
	for i := range burst {
		go func() {
			defer wg.Done()
			rr := doJSON(t, bobRouter, http.MethodPost, path, map[string]string{"password": "guess"})
			codes[i] = rr.Code
		}()
	}
	wg.Wait()
	for i, c := range codes {
		require.Equal(t, http.StatusNotFound, c, "bob's request %d should be 404", i)
	}

	// Alice's budget must be untouched: the correct password still unlocks.
	rr := doJSON(t, routerFor(alice), http.MethodPost, path, map[string]string{"password": pw})
	require.Equal(t, http.StatusOK, rr.Code, "alice must not be locked out by bob's attempts")
}

func TestHandler_DeleteProtectedRequiresCurrentUnlockToken(t *testing.T) {
	h, repo, uid := newHandlerRouter(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		query string
	}{
		{name: "keep_contents"},
		{name: "cascade", query: "?cascade=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			password := "delete-secret"
			folder, err := repo.Create(ctx, uid, folders.CreateInput{
				Name: "Protected " + tc.name, Color: "#abc", Password: &password,
			})
			require.NoError(t, err)
			path := "/folders/" + strconv.FormatInt(folder.ID, 10) + tc.query

			rr := doJSON(t, h, http.MethodDelete, path, nil)
			require.Equal(t, http.StatusForbidden, rr.Code)
			assertErrorCode(t, rr, "folder_locked")
			_, err = repo.Get(ctx, uid, folder.ID)
			require.NoError(t, err, "missing proof must not delete the folder")

			req := httptest.NewRequest(http.MethodDelete, path, nil)
			req.Header.Set(folders.UnlockHeader, "invalid-token")
			rr = httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			require.Equal(t, http.StatusForbidden, rr.Code)
			assertErrorCode(t, rr, "folder_locked")

			hash, err := repo.PasswordHashFor(ctx, uid, folder.ID)
			require.NoError(t, err)
			require.NotNil(t, hash)
			req = httptest.NewRequest(http.MethodDelete, path, nil)
			req.Header.Set(folders.UnlockHeader, folders.IssueUnlockToken(testUnlockKey, folder.ID, *hash))
			rr = httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			require.Equal(t, http.StatusNoContent, rr.Code)
			_, err = repo.Get(ctx, uid, folder.ID)
			assert.ErrorIs(t, err, domainerr.ErrNotFound)
		})
	}
}

func TestHandler_DeleteCascadeRejectsProtectedDescendants(t *testing.T) {
	h, repo, uid := newHandlerRouter(t)
	ctx := context.Background()
	root, err := repo.Create(ctx, uid, folders.CreateInput{Name: "Open root", Color: "#abc"})
	require.NoError(t, err)
	password := "child-secret"
	child, err := repo.Create(ctx, uid, folders.CreateInput{
		Name: "Protected child", Color: "#def", ParentID: &root.ID, Password: &password,
	})
	require.NoError(t, err)

	rr := doJSON(t, h, http.MethodDelete,
		"/folders/"+strconv.FormatInt(root.ID, 10)+"?cascade=1", nil)
	require.Equal(t, http.StatusConflict, rr.Code)
	var out struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Count int64 `json:"count"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	assert.Equal(t, "descendant_protected", out.Error.Code)
	assert.EqualValues(t, 1, out.Count)

	_, err = repo.Get(ctx, uid, root.ID)
	require.NoError(t, err, "a rejected cascade must keep the root")
	_, err = repo.Get(ctx, uid, child.ID)
	require.NoError(t, err, "a rejected cascade must keep protected descendants")
}

func TestHandler_DeleteRejectsAPIToken(t *testing.T) {
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := folders.NewRepository(pool)
	password := "delete-secret"
	folder, err := repo.Create(context.Background(), uid, folders.CreateInput{
		Name: "Protected", Color: "#abc", Password: &password,
	})
	require.NoError(t, err)
	hash, err := repo.PasswordHashFor(context.Background(), uid, folder.ID)
	require.NoError(t, err)
	require.NotNil(t, hash)

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			principal := authctx.Principal{UserID: uid, Role: authctx.RoleAdmin, Via: authctx.ViaAPIToken}
			next.ServeHTTP(w, req.WithContext(authctx.WithPrincipal(req.Context(), principal)))
		})
	})
	r.Route("/folders", folders.NewHandler(repo, testUnlockKey, fakeMaster{}).Mount)

	for _, query := range []string{"", "?cascade=1"} {
		req := httptest.NewRequest(http.MethodDelete,
			"/folders/"+strconv.FormatInt(folder.ID, 10)+query, nil)
		req.Header.Set(folders.UnlockHeader, folders.IssueUnlockToken(testUnlockKey, folder.ID, *hash))
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		require.Equal(t, http.StatusForbidden, rr.Code)
		assertErrorCode(t, rr, "token_scope")
		_, err = repo.Get(context.Background(), uid, folder.ID)
		require.NoError(t, err, "API token rejection must happen before deletion")
	}
}

func TestHandler_DeleteCrossUserIsNotFoundAndOpenFolderStillDeletes(t *testing.T) {
	pool := testdb.Shared(t)
	alice := testdb.SeedUser(t, pool, "alice@test.local", "user")
	bob := testdb.SeedUser(t, pool, "bob@test.local", "user")
	repo := folders.NewRepository(pool)
	folder, err := repo.Create(context.Background(), alice, folders.CreateInput{Name: "Open", Color: "#abc"})
	require.NoError(t, err)
	h := folders.NewHandler(repo, testUnlockKey, fakeMaster{})
	routerFor := func(uid authctx.UserID) http.Handler {
		r := chi.NewRouter()
		r.Use(authctxtest.Middleware(uid))
		r.Route("/folders", h.Mount)
		return r
	}

	path := "/folders/" + strconv.FormatInt(folder.ID, 10)
	rr := doJSON(t, routerFor(bob), http.MethodDelete, path, nil)
	require.Equal(t, http.StatusNotFound, rr.Code)
	_, err = repo.Get(context.Background(), alice, folder.ID)
	require.NoError(t, err, "cross-user DELETE must not mutate the owner's folder")

	rr = doJSON(t, routerFor(bob), http.MethodDelete, path+"?cascade=1", nil)
	require.Equal(t, http.StatusNotFound, rr.Code)
	_, err = repo.Get(context.Background(), alice, folder.ID)
	require.NoError(t, err, "cross-user cascade DELETE must not mutate the owner's folder")

	rr = doJSON(t, routerFor(alice), http.MethodDelete, path, nil)
	require.Equal(t, http.StatusNoContent, rr.Code)
}

func TestHandler_DeletePasswordCheckAndMutationAreAtomic(t *testing.T) {
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := folders.NewRepository(pool)
	oldPassword := "old-password"
	folder, err := repo.Create(context.Background(), uid, folders.CreateInput{
		Name: "Protected", Color: "#abc", Password: &oldPassword,
	})
	require.NoError(t, err)
	oldHash, err := repo.PasswordHashFor(context.Background(), uid, folder.ID)
	require.NoError(t, err)
	require.NotNil(t, oldHash)
	oldToken := folders.IssueUnlockToken(testUnlockKey, folder.ID, *oldHash)
	newHash, err := folders.HashPassword("new-password")
	require.NoError(t, err)

	changeTx, err := pool.Begin(context.Background())
	require.NoError(t, err)
	defer changeTx.Rollback(context.Background())
	_, err = changeTx.Exec(context.Background(),
		`UPDATE folder SET password_hash = $3 WHERE user_id = $1 AND id = $2`, int64(uid), folder.ID, newHash)
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(uid))
	r.Route("/folders", folders.NewHandler(repo, testUnlockKey, fakeMaster{}).Mount)
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodDelete, "/folders/"+strconv.FormatInt(folder.ID, 10), nil)
		req.Header.Set(folders.UnlockHeader, oldToken)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		result <- rr
	}()

	require.Eventually(t, func() bool {
		var waiting bool
		err := pool.QueryRow(context.Background(), `
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE datname = current_database()
				  AND pid <> pg_backend_pid()
				  AND wait_event_type = 'Lock'
				  AND query ILIKE '%folder%'
			)`).Scan(&waiting)
		return err == nil && waiting
	}, 2*time.Second, 10*time.Millisecond, "DELETE should wait on the password-change row lock")
	require.NoError(t, changeTx.Commit(context.Background()))

	select {
	case rr := <-result:
		require.Equal(t, http.StatusForbidden, rr.Code)
		assertErrorCode(t, rr, "folder_locked")
	case <-time.After(2 * time.Second):
		t.Fatal("DELETE did not finish after password change committed")
	}
	_, err = repo.Get(context.Background(), uid, folder.ID)
	require.NoError(t, err, "stale unlock token must not delete after a concurrent password change")
}
