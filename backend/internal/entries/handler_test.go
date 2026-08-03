package entries

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"

	"foldex/internal/folders"
	"foldex/internal/pkg/httperr"
)

type fakeList struct {
	out []Entry
	err error
	q   ListQuery
}

func (f *fakeList) List(_ context.Context, q ListQuery) ([]Entry, error) {
	f.q = q
	return f.out, f.err
}

type fakeFolder struct {
	hash *string
	err  error
	// calls lets tests simulate a password-hash flip between CheckUnlock and
	// the post-List re-verify (RACE-HER-005).
	calls     int
	hash2     *string
	flipAfter int
}

func (f *fakeFolder) PasswordHashFor(context.Context, int64) (*string, error) {
	f.calls++
	if f.flipAfter > 0 && f.calls > f.flipAfter {
		return f.hash2, f.err
	}
	return f.hash, f.err
}

func mount(h *Handler) http.Handler {
	r := chi.NewRouter()
	h.Mount(r)
	return r
}

func TestList_OK(t *testing.T) {
	fl := &fakeList{out: []Entry{{Kind: "link", ID: 1, Title: "a"}}}
	h := NewHandler(fl, &fakeFolder{}, nil)
	rec := httptest.NewRecorder()
	mount(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?q=hi&sort=alpha&limit=10", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"title":"a"`)
	assert.Equal(t, "hi", fl.q.Q)
	assert.Equal(t, "alpha", fl.q.Sort)
}

func TestList_FolderLocked(t *testing.T) {
	hash := "x"
	h := NewHandler(&fakeList{}, &fakeFolder{hash: &hash}, []byte("secret-key-at-least-32-bytes-long!!"))
	rec := httptest.NewRecorder()
	mount(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?folder_id=9", nil))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "folder_locked")
}

func TestList_FolderLookupErr(t *testing.T) {
	h := NewHandler(&fakeList{}, &fakeFolder{err: httperr.ErrNotFound}, nil)
	rec := httptest.NewRecorder()
	mount(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?folder_id=1", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestList_RepoErr(t *testing.T) {
	h := NewHandler(&fakeList{err: errors.New("db")}, &fakeFolder{}, nil)
	rec := httptest.NewRecorder()
	mount(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.NotEqual(t, http.StatusOK, rec.Code)
}

func TestList_Ungrouped(t *testing.T) {
	fl := &fakeList{out: []Entry{}}
	h := NewHandler(fl, nil, nil)
	rec := httptest.NewRecorder()
	mount(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?ungrouped=1", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, fl.q.Ungrouped)
}

// TestList_RejectsWhenPasswordChangesMidRequest locks RACE-HER-005: token is
// valid against the hash at gate-in, but password changes before return → 403.
func TestList_RejectsWhenPasswordChangesMidRequest(t *testing.T) {
	secret := []byte("secret-key-at-least-32-bytes-long!!")
	oldHash := "old-bcrypt-hash"
	newHash := "new-bcrypt-hash-after-change"
	token := folders.IssueUnlockToken(secret, 9, oldHash)
	ff := &fakeFolder{hash: &oldHash, hash2: &newHash, flipAfter: 1}
	fl := &fakeList{out: []Entry{{Kind: "link", ID: 1, Title: "secret"}}}
	h := NewHandler(fl, ff, secret)
	req := httptest.NewRequest(http.MethodGet, "/?folder_id=9", nil)
	req.Header.Set(folders.UnlockHeader, token)
	rec := httptest.NewRecorder()
	mount(h).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "folder_locked")
	assert.NotContains(t, rec.Body.String(), "secret")
	assert.GreaterOrEqual(t, ff.calls, 2)
}

// silence unused import if CheckUnlock path needs folders package
var _ = folders.UnlockHeader
