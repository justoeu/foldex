package entries

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"

	"foldex/internal/folders"
	"foldex/internal/pkg/httperr"

	"foldex/internal/pkg/authctx"

	"foldex/internal/pkg/authctx/authctxtest"
)

type fakeList struct {
	out             []Entry
	err             error
	q               ListQuery
	calls           int
	counts          EntryCounts
	countsErr       error
	countsUID       authctx.UserID
	countsCalls     int
	previewOut      []PreviewStatus
	previewErr      error
	previewIDs      []int64
	previewFolderID *int64
	previewUID      authctx.UserID
	previewCalls    int
}

func (f *fakeList) Counts(_ context.Context, uid authctx.UserID) (EntryCounts, error) {
	f.countsCalls++
	f.countsUID = uid
	return f.counts, f.countsErr
}

func (f *fakeList) List(_ context.Context, _ authctx.UserID, q ListQuery) ([]Entry, error) {
	f.calls++
	f.q = q
	return f.out, f.err
}

func (f *fakeList) PreviewStatuses(_ context.Context, uid authctx.UserID, ids []int64, folderID *int64) ([]PreviewStatus, error) {
	f.previewCalls++
	f.previewUID = uid
	f.previewIDs = ids
	f.previewFolderID = folderID
	return f.previewOut, f.previewErr
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

func (f *fakeFolder) PasswordHashFor(context.Context, authctx.UserID, int64) (*string, error) {
	f.calls++
	if f.flipAfter > 0 && f.calls > f.flipAfter {
		return f.hash2, f.err
	}
	return f.hash, f.err
}

func mount(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(authctxtest.DefaultUser))
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

func TestList_FolderLookupMissingFailsClosed(t *testing.T) {
	fl := &fakeList{out: []Entry{{Kind: "note", ID: 1, Title: "secret"}}}
	h := NewHandler(fl, nil, nil)
	rec := httptest.NewRecorder()

	mount(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?folder_id=1", nil))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "secret")
	assert.Zero(t, fl.calls, "the repository must not run without the folder gate")
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

func TestCounts_OK(t *testing.T) {
	fl := &fakeList{counts: EntryCounts{Links: 17, Notes: 4}}
	rec := httptest.NewRecorder()
	mount(NewHandler(fl, &fakeFolder{}, nil)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/counts", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"links":17,"notes":4}`, rec.Body.String())
	assert.Equal(t, authctxtest.DefaultUser, fl.countsUID)
	assert.Equal(t, 1, fl.countsCalls)
}

func TestCounts_RepoErr(t *testing.T) {
	rec := httptest.NewRecorder()
	mount(NewHandler(&fakeList{countsErr: errors.New("db")}, &fakeFolder{}, nil)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/counts", nil))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestPreviewStatuses_OK(t *testing.T) {
	status := "ok"
	fl := &fakeList{previewOut: []PreviewStatus{{ID: 7, Found: true, Status: &status}, {ID: 99}}}
	rec := httptest.NewRecorder()
	mount(NewHandler(fl, &fakeFolder{}, nil)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/preview-status?id=7&id=99&folder_id=12", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, authctxtest.DefaultUser, fl.previewUID)
	assert.Equal(t, []int64{7, 99}, fl.previewIDs)
	if assert.NotNil(t, fl.previewFolderID) {
		assert.EqualValues(t, 12, *fl.previewFolderID)
	}
	assert.Contains(t, rec.Body.String(), `"preview_status":"ok"`)
	assert.Contains(t, rec.Body.String(), `"found":false`)
}

func TestPreviewStatuses_RejectsInvalidOrOversizedBatches(t *testing.T) {
	for _, path := range []string{
		"/preview-status",
		"/preview-status?id=0",
		"/preview-status?id=abc",
		"/preview-status?" + repeatedPreviewIDs(PreviewStatusMaxIDs+1),
	} {
		t.Run(path, func(t *testing.T) {
			fl := &fakeList{}
			rec := httptest.NewRecorder()
			mount(NewHandler(fl, &fakeFolder{}, nil)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Zero(t, fl.previewCalls)
		})
	}
}

func TestPreviewStatuses_RepoErr(t *testing.T) {
	rec := httptest.NewRecorder()
	mount(NewHandler(&fakeList{previewErr: errors.New("db")}, &fakeFolder{}, nil)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/preview-status?id=1", nil))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
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

func repeatedPreviewIDs(n int) string {
	q := ""
	for i := 1; i <= n; i++ {
		if q != "" {
			q += "&"
		}
		q += "id=" + strconv.Itoa(i)
	}
	return q
}
