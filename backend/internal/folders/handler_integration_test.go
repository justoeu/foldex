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

// fakeMaster is a test double for folders.MasterPasswordVerifier. When
// configured is false it reports "no master set"; otherwise it matches against
// password (constant-time-ish compare is irrelevant in tests).
type fakeMaster struct {
	configured bool
	password   string
}

func (f fakeMaster) VerifyMaster(_ context.Context, plain string) (ok bool, configured bool, err error) {
	if !f.configured {
		return false, false, nil
	}
	return plain == f.password, true, nil
}

func newHandlerRouter(t *testing.T) (http.Handler, *folders.Repository) {
	return newHandlerRouterMaster(t, fakeMaster{})
}

func newHandlerRouterMaster(t *testing.T, master folders.MasterPasswordVerifier) (http.Handler, *folders.Repository) {
	t.Helper()
	pool := testdb.New(t)
	repo := folders.NewRepository(pool)
	r := chi.NewRouter()
	r.Route("/folders", folders.NewHandler(repo, testUnlockKey, master).Mount)
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
	h, repo := newHandlerRouterMaster(t, master)
	ctx := context.Background()

	pw := "folder-pass"
	hint := "a clue"
	f, err := repo.Create(ctx, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw, PasswordHint: &hint})
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
	h, repo := newHandlerRouterMaster(t, fakeMaster{configured: false})
	ctx := context.Background()

	pw := "folder-pass"
	f, err := repo.Create(ctx, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)

	rr := doJSON(t, h, http.MethodPost, "/folders/"+strconv.FormatInt(f.ID, 10)+"/reset-password",
		map[string]any{"master_password": "anything"})
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "master_not_configured")
}

func TestHandler_Update_HintEqualsExistingPassword(t *testing.T) {
	h, repo := newHandlerRouter(t)
	ctx := context.Background()

	pw := "folder-pass"
	f, err := repo.Create(ctx, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)

	// Setting a hint equal to the (unchanged) password must be rejected by the
	// repository's bcrypt equality check inside the tx.
	rr := doJSON(t, h, http.MethodPatch, "/folders/"+strconv.FormatInt(f.ID, 10),
		map[string]any{"password_hint": "folder-pass"})
	assert.Equal(t, http.StatusBadRequest, rr.Code)

	// A distinct hint succeeds and round-trips.
	rr = doJSON(t, h, http.MethodPatch, "/folders/"+strconv.FormatInt(f.ID, 10),
		map[string]any{"password_hint": "rhymes with holder"})
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
	h, repo := newHandlerRouter(t)
	ctx := context.Background()

	f, err := repo.Create(ctx, folders.CreateInput{Name: "Open", Color: "#abc"})
	require.NoError(t, err)

	rr := doJSON(t, h, http.MethodPatch, "/folders/"+strconv.FormatInt(f.ID, 10),
		map[string]any{"password_hint": "a hint with no password"})
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// ── unlock rate limiting (ADR-28) ──────────────────────────────────────────

func TestHandler_Unlock_LocksOutAfterFiveWrongAttempts(t *testing.T) {
	h, repo := newHandlerRouter(t)
	ctx := context.Background()

	pw := "correct-horse"
	f, err := repo.Create(ctx, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
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
	h, repo := newHandlerRouter(t)
	ctx := context.Background()

	pw := "correct-horse"
	f, err := repo.Create(ctx, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
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
