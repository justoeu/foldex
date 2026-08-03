//go:build integration

package links_test

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
	"foldex/internal/links"
	"foldex/internal/pkg/httperr"
	"foldex/internal/tags"
	"foldex/internal/testdb"
)

type recordingEnqueuer struct{ ids []int64 }

func (e *recordingEnqueuer) Enqueue(id int64) error {
	e.ids = append(e.ids, id)
	return nil
}

type stubFolderLookup struct {
	hash *string
	err  error
}

func (s stubFolderLookup) PasswordHashFor(context.Context, int64) (*string, error) {
	return s.hash, s.err
}

func newLinksRouter(t *testing.T, worker links.Enqueuer, lookup links.FolderPasswordLookup, key []byte) (http.Handler, *links.Repository) {
	t.Helper()
	pool := testdb.New(t)
	repo := links.NewRepository(pool)
	h := links.NewHandler(repo, worker)
	if lookup != nil {
		h = h.WithFolderGate(lookup, key)
	}
	r := chi.NewRouter()
	r.Route("/links", h.Mount)
	return r, repo
}

func doLinkJSON(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
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

func linkID(id int64) string { return strconv.FormatInt(id, 10) }

func TestHandler_CRUD(t *testing.T) {
	enq := &recordingEnqueuer{}
	h, _ := newLinksRouter(t, enq, nil, nil)

	rr := doLinkJSON(t, h, http.MethodPost, "/links/", map[string]any{
		"url":   "https://example.com/a",
		"title": "Alpha",
	})
	require.Equal(t, http.StatusCreated, rr.Code)
	var created links.Link
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	assert.Equal(t, "Alpha", created.Title)
	assert.Equal(t, []int64{created.ID}, enq.ids)

	rr = doLinkJSON(t, h, http.MethodGet, "/links/"+linkID(created.ID), nil)
	require.Equal(t, http.StatusOK, rr.Code)

	rr = doLinkJSON(t, h, http.MethodGet, "/links/?q=Alpha&sort=alpha&limit=10&offset=0&ungrouped=1&tag=abc&tag=-1", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	var list []links.Link
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	require.Len(t, list, 1)

	rr = doLinkJSON(t, h, http.MethodPatch, "/links/"+linkID(created.ID), map[string]any{
		"title":  "Alpha Renamed",
		"pinned": true,
	})
	require.Equal(t, http.StatusOK, rr.Code)
	var updated links.Link
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &updated))
	assert.Equal(t, "Alpha Renamed", updated.Title)
	assert.True(t, updated.Pinned)

	rr = doLinkJSON(t, h, http.MethodDelete, "/links/"+linkID(created.ID), nil)
	require.Equal(t, http.StatusNoContent, rr.Code)

	rr = doLinkJSON(t, h, http.MethodGet, "/links/"+linkID(created.ID), nil)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestHandler_Create_InvalidInput(t *testing.T) {
	h, _ := newLinksRouter(t, nil, nil, nil)
	rr := doLinkJSON(t, h, http.MethodPost, "/links/", map[string]any{"url": "not-a-url", "title": "x"})
	require.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid_input")
}

func TestHandler_Create_InvalidJSON(t *testing.T) {
	h, _ := newLinksRouter(t, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/links/", bytes.NewBufferString(`{`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid_json")
}

func TestHandler_Create_DuplicateURL(t *testing.T) {
	h, _ := newLinksRouter(t, nil, nil, nil)
	body := map[string]any{"url": "https://dup.example", "title": "a"}
	require.Equal(t, http.StatusCreated, doLinkJSON(t, h, http.MethodPost, "/links/", body).Code)
	rr := doLinkJSON(t, h, http.MethodPost, "/links/", body)
	require.Equal(t, http.StatusConflict, rr.Code)
	assert.Contains(t, rr.Body.String(), "url_taken")
}

func TestHandler_Get_InvalidID(t *testing.T) {
	h, _ := newLinksRouter(t, nil, nil, nil)
	rr := doLinkJSON(t, h, http.MethodGet, "/links/nope", nil)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandler_Update_InvalidID(t *testing.T) {
	h, _ := newLinksRouter(t, nil, nil, nil)
	rr := doLinkJSON(t, h, http.MethodPatch, "/links/x", map[string]any{"title": "y"})
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandler_Update_InvalidInput(t *testing.T) {
	h, _ := newLinksRouter(t, nil, nil, nil)
	created := doLinkJSON(t, h, http.MethodPost, "/links/", map[string]any{"url": "https://u.example", "title": "u"})
	var l links.Link
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &l))
	rr := doLinkJSON(t, h, http.MethodPatch, "/links/"+linkID(l.ID), map[string]any{"title": ""})
	require.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid_input")
}

func TestHandler_Update_NotFound(t *testing.T) {
	h, _ := newLinksRouter(t, nil, nil, nil)
	rr := doLinkJSON(t, h, http.MethodPatch, "/links/99999", map[string]any{"title": "ghost"})
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestHandler_Delete_InvalidID(t *testing.T) {
	h, _ := newLinksRouter(t, nil, nil, nil)
	rr := doLinkJSON(t, h, http.MethodDelete, "/links/abc", nil)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandler_Delete_NotFound(t *testing.T) {
	h, _ := newLinksRouter(t, nil, nil, nil)
	rr := doLinkJSON(t, h, http.MethodDelete, "/links/99999", nil)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestHandler_RefreshPreview(t *testing.T) {
	enq := &recordingEnqueuer{}
	h, _ := newLinksRouter(t, enq, nil, nil)
	created := doLinkJSON(t, h, http.MethodPost, "/links/", map[string]any{"url": "https://rp.example", "title": "rp"})
	var l links.Link
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &l))
	enq.ids = nil

	rr := doLinkJSON(t, h, http.MethodPost, "/links/"+linkID(l.ID)+"/refresh-preview", nil)
	require.Equal(t, http.StatusAccepted, rr.Code)
	assert.Equal(t, []int64{l.ID}, enq.ids)

	got := doLinkJSON(t, h, http.MethodGet, "/links/"+linkID(l.ID), nil)
	var refreshed links.Link
	require.NoError(t, json.Unmarshal(got.Body.Bytes(), &refreshed))
	assert.Equal(t, "pending", refreshed.PreviewStatus)
}

func TestHandler_RefreshPreview_NotFound(t *testing.T) {
	h, _ := newLinksRouter(t, &recordingEnqueuer{}, nil, nil)
	rr := doLinkJSON(t, h, http.MethodPost, "/links/99999/refresh-preview", nil)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestHandler_RefreshPreview_InvalidID(t *testing.T) {
	h, _ := newLinksRouter(t, nil, nil, nil)
	rr := doLinkJSON(t, h, http.MethodPost, "/links/bad/refresh-preview", nil)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandler_SeenChange(t *testing.T) {
	h, repo := newLinksRouter(t, nil, nil, nil)
	ctx := context.Background()
	l, err := repo.Create(ctx, links.CreateInput{URL: "https://sc.example", Title: "sc"})
	require.NoError(t, err)
	di := "daily"
	_, err = repo.Update(ctx, l.ID, links.UpdateInput{CheckInterval: &di, CheckIntervalSet: true})
	require.NoError(t, err)
	require.NoError(t, repo.RecordCheckResult(ctx, l.ID, links.CheckResult{Fingerprint: "a"}))
	require.NoError(t, repo.RecordCheckResult(ctx, l.ID, links.CheckResult{Fingerprint: "b", Changed: true}))

	rr := doLinkJSON(t, h, http.MethodPost, "/links/"+linkID(l.ID)+"/seen-change", nil)
	require.Equal(t, http.StatusNoContent, rr.Code)

	rr = doLinkJSON(t, h, http.MethodPost, "/links/99999/seen-change", nil)
	require.Equal(t, http.StatusNotFound, rr.Code)

	rr = doLinkJSON(t, h, http.MethodPost, "/links/bad/seen-change", nil)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandler_ListRecentChanges(t *testing.T) {
	h, repo := newLinksRouter(t, nil, nil, nil)
	ctx := context.Background()
	l, err := repo.Create(ctx, links.CreateInput{URL: "https://rc.example", Title: "rc"})
	require.NoError(t, err)
	di := "daily"
	_, err = repo.Update(ctx, l.ID, links.UpdateInput{CheckInterval: &di, CheckIntervalSet: true})
	require.NoError(t, err)
	require.NoError(t, repo.RecordCheckResult(ctx, l.ID, links.CheckResult{Fingerprint: "a"}))
	require.NoError(t, repo.RecordCheckResult(ctx, l.ID, links.CheckResult{Fingerprint: "b", Changed: true}))

	rr := doLinkJSON(t, h, http.MethodGet, "/links/recent-changes?days=7&limit=10", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	var out []links.Link
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Len(t, out, 1)
	assert.Equal(t, l.ID, out[0].ID)
}

func TestHandler_List_FolderGate(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	frepo := folders.NewRepository(pool)
	lrepo := links.NewRepository(pool)
	f, err := frepo.Create(ctx, folders.CreateInput{Name: "Locked", Color: "#abc"})
	require.NoError(t, err)
	_, err = lrepo.Create(ctx, links.CreateInput{URL: "https://in-locked", Title: "In", FolderID: &f.ID})
	require.NoError(t, err)

	hash := "bcrypt-hash-placeholder"
	h := links.NewHandler(lrepo, nil).WithFolderGate(stubFolderLookup{hash: &hash}, []byte("secret-key-at-least-32-bytes-long!!"))
	r := chi.NewRouter()
	r.Route("/links", h.Mount)

	rr := doLinkJSON(t, r, http.MethodGet, "/links/?folder_id="+linkID(f.ID), nil)
	require.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "folder_locked")
}

func TestHandler_List_FolderLookupErr(t *testing.T) {
	h, _ := newLinksRouter(t, nil, stubFolderLookup{err: assert.AnError}, nil)
	rr := doLinkJSON(t, h, http.MethodGet, "/links/?folder_id=1", nil)
	require.NotEqual(t, http.StatusOK, rr.Code)
}

func TestHandler_WithMetadataFetcher_Chains(t *testing.T) {
	h := links.NewHandler(nil, nil).WithMetadataFetcher(nil)
	assert.NotNil(t, h)
}

func TestRepository_UpdateOGImageAndClear(t *testing.T) {
	ctx, lrepo, _ := setup(t)
	l, err := lrepo.Create(ctx, links.CreateInput{URL: "https://og.example", Title: "og"})
	require.NoError(t, err)

	require.NoError(t, lrepo.UpdateOGImage(ctx, l.ID, "/api/files/og/1.jpg"))
	got, err := lrepo.Get(ctx, l.ID)
	require.NoError(t, err)
	require.NotNil(t, got.OGImageURL)
	assert.Equal(t, "/api/files/og/1.jpg", *got.OGImageURL)
	assert.Equal(t, "ok", got.PreviewStatus)

	require.NoError(t, lrepo.ClearOGImage(ctx, l.ID))
	got, err = lrepo.Get(ctx, l.ID)
	require.NoError(t, err)
	assert.Nil(t, got.OGImageURL)

	assert.ErrorIs(t, lrepo.UpdateOGImage(ctx, 99999, "/x"), httperr.ErrNotFound)
	assert.ErrorIs(t, lrepo.ClearOGImage(ctx, 99999), httperr.ErrNotFound)
}

func TestRepository_ClickAndResolveBySlug(t *testing.T) {
	ctx, lrepo, _ := setup(t)
	l, err := lrepo.Create(ctx, links.CreateInput{URL: "https://slug-go.example", Title: "Slug Go"})
	require.NoError(t, err)

	url, err := lrepo.ClickAndResolveBySlug(ctx, l.Slug)
	require.NoError(t, err)
	assert.Equal(t, "https://slug-go.example", url)

	got, err := lrepo.Get(ctx, l.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, got.ClickCount)

	_, err = lrepo.ClickAndResolveBySlug(ctx, "no-such-slug")
	require.Error(t, err)
}

func TestRepository_Create_UserSlugTaken(t *testing.T) {
	ctx, lrepo, _ := setup(t)
	slug := "my-slug"
	_, err := lrepo.Create(ctx, links.CreateInput{URL: "https://s1.example", Title: "A", Slug: &slug})
	require.NoError(t, err)
	_, err = lrepo.Create(ctx, links.CreateInput{URL: "https://s2.example", Title: "B", Slug: &slug})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "slug")
}

func TestRepository_Create_AutoSlugCollision(t *testing.T) {
	ctx, lrepo, _ := setup(t)
	a, err := lrepo.Create(ctx, links.CreateInput{URL: "https://c1.example", Title: "Same Title"})
	require.NoError(t, err)
	b, err := lrepo.Create(ctx, links.CreateInput{URL: "https://c2.example", Title: "Same Title"})
	require.NoError(t, err)
	assert.NotEqual(t, a.Slug, b.Slug)
	assert.Contains(t, b.Slug, a.Slug)
}

func TestRepository_Delete_NotFound(t *testing.T) {
	ctx, lrepo, _ := setup(t)
	err := lrepo.Delete(ctx, 99999)
	require.Error(t, err)
}

func TestRepository_GetBySlug_NotFound(t *testing.T) {
	ctx, lrepo, _ := setup(t)
	_, err := lrepo.GetBySlug(ctx, "missing")
	require.Error(t, err)
}

func TestRepository_Create_WithTagsAndFolder(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	lrepo := links.NewRepository(pool)
	trepo := tags.NewRepository(pool)
	frepo := folders.NewRepository(pool)

	tag, err := trepo.Create(ctx, tags.CreateInput{Name: "t", Color: "#abc"})
	require.NoError(t, err)
	f, err := frepo.Create(ctx, folders.CreateInput{Name: "F", Color: "#def"})
	require.NoError(t, err)

	l, err := lrepo.Create(ctx, links.CreateInput{
		URL: "https://tf.example", Title: "TF", TagIDs: []int64{tag.ID}, FolderID: &f.ID,
	})
	require.NoError(t, err)
	require.Len(t, l.Tags, 1)
	require.NotNil(t, l.FolderID)
	assert.Equal(t, f.ID, *l.FolderID)
}

func TestRepository_Update_FullFieldCoverage(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	lrepo := links.NewRepository(pool)
	trepo := tags.NewRepository(pool)
	frepo := folders.NewRepository(pool)

	tag, err := trepo.Create(ctx, tags.CreateInput{Name: "u", Color: "#abc"})
	require.NoError(t, err)
	f, err := frepo.Create(ctx, folders.CreateInput{Name: "UF", Color: "#def"})
	require.NoError(t, err)
	l, err := lrepo.Create(ctx, links.CreateInput{URL: "https://full.example", Title: "Full"})
	require.NoError(t, err)

	newURL := "https://full-renamed.example"
	newTitle := "Full Renamed"
	desc := "a description"
	slug := "custom-full-slug"
	tags := []int64{tag.ID}
	updated, err := lrepo.Update(ctx, l.ID, links.UpdateInput{
		URL:         &newURL,
		Title:       &newTitle,
		Description: &desc,
		Slug:        &slug,
		SlugSet:     true,
		FolderID:    &f.ID,
		FolderIDSet: true,
		TagIDs:      &tags,
	})
	require.NoError(t, err)
	assert.Equal(t, newURL, updated.URL)
	assert.Equal(t, newTitle, updated.Title)
	require.NotNil(t, updated.Description)
	assert.Equal(t, desc, *updated.Description)
	assert.Equal(t, slug, updated.Slug)
	require.NotNil(t, updated.FolderID)
	assert.Equal(t, f.ID, *updated.FolderID)
	require.Len(t, updated.Tags, 1)

	// Clear folder + regenerate slug from title.
	cleared, err := lrepo.Update(ctx, l.ID, links.UpdateInput{
		FolderIDSet: true,
		FolderID:    nil,
		SlugSet:     true,
		Slug:        nil,
	})
	require.NoError(t, err)
	assert.Nil(t, cleared.FolderID)
	assert.NotEmpty(t, cleared.Slug)

	// Empty patch still returns the row.
	same, err := lrepo.Update(ctx, l.ID, links.UpdateInput{})
	require.NoError(t, err)
	assert.Equal(t, cleared.ID, same.ID)
}

func TestRepository_Update_SlugTaken(t *testing.T) {
	ctx, lrepo, _ := setup(t)
	slugA := "taken-slug"
	_, err := lrepo.Create(ctx, links.CreateInput{URL: "https://a1.example", Title: "A", Slug: &slugA})
	require.NoError(t, err)
	b, err := lrepo.Create(ctx, links.CreateInput{URL: "https://b1.example", Title: "B"})
	require.NoError(t, err)

	_, err = lrepo.Update(ctx, b.ID, links.UpdateInput{Slug: &slugA, SlugSet: true})
	require.Error(t, err)
	var he *httperr.Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, "slug_taken", he.Code)
}

func TestRepository_UpdatePreview_InvalidStatus(t *testing.T) {
	ctx, lrepo, _ := setup(t)
	l, err := lrepo.Create(ctx, links.CreateInput{URL: "https://pv.example", Title: "pv"})
	require.NoError(t, err)
	err = lrepo.UpdatePreview(ctx, l.ID, links.PreviewStatus("nope"), nil, nil, nil, nil)
	require.Error(t, err)
}

func TestHandler_Update_InvalidJSON(t *testing.T) {
	h, _ := newLinksRouter(t, nil, nil, nil)
	created := doLinkJSON(t, h, http.MethodPost, "/links/", map[string]any{"url": "https://ij.example", "title": "ij"})
	var l links.Link
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &l))
	req := httptest.NewRequest(http.MethodPatch, "/links/"+linkID(l.ID), bytes.NewBufferString(`{`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandler_Update_SlugViaJSON(t *testing.T) {
	h, _ := newLinksRouter(t, nil, nil, nil)
	created := doLinkJSON(t, h, http.MethodPost, "/links/", map[string]any{"url": "https://sj.example", "title": "SJ"})
	var l links.Link
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &l))
	rr := doLinkJSON(t, h, http.MethodPatch, "/links/"+linkID(l.ID), map[string]any{
		"slug":        "handler-slug",
		"description": "via handler",
		"folder_id":   nil,
	})
	require.Equal(t, http.StatusOK, rr.Code)
	var updated links.Link
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &updated))
	assert.Equal(t, "handler-slug", updated.Slug)
}
