//go:build integration

package links_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

func setup(t *testing.T) (context.Context, *links.Repository, *tags.Repository) {
	t.Helper()
	pool := testdb.New(t)
	return context.Background(), links.NewRepository(pool), tags.NewRepository(pool)
}

func TestRepository_CreateAndGetWithTags(t *testing.T) {
	ctx, lrepo, trepo := setup(t)

	tagJira, err := trepo.Create(ctx, tags.CreateInput{Name: "jira", Color: "#1f6feb"})
	require.NoError(t, err)
	tagDocs, err := trepo.Create(ctx, tags.CreateInput{Name: "docs", Color: "#a78bfa"})
	require.NoError(t, err)

	created, err := lrepo.Create(ctx, links.CreateInput{
		URL:    "https://jira.example/INV-1",
		Title:  "INV-1",
		TagIDs: []int64{tagJira.ID, tagDocs.ID},
	})
	require.NoError(t, err)
	require.NotZero(t, created.ID)
	assert.Equal(t, "pending", created.PreviewStatus)
	require.Len(t, created.Tags, 2)

	// Verify Get also returns tags
	got, err := lrepo.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Len(t, got.Tags, 2)
}

func TestRepository_ListFiltersByQAndTagAND(t *testing.T) {
	ctx, lrepo, trepo := setup(t)
	tagA, _ := trepo.Create(ctx, tags.CreateInput{Name: "a", Color: "#fff"})
	tagB, _ := trepo.Create(ctx, tags.CreateInput{Name: "b", Color: "#fff"})

	_, _ = lrepo.Create(ctx, links.CreateInput{URL: "https://example.com/alpha", Title: "Alpha", TagIDs: []int64{tagA.ID}})
	_, _ = lrepo.Create(ctx, links.CreateInput{URL: "https://example.com/beta", Title: "Beta", TagIDs: []int64{tagA.ID, tagB.ID}})
	_, _ = lrepo.Create(ctx, links.CreateInput{URL: "https://other.com/gamma", Title: "Gamma", TagIDs: []int64{tagB.ID}})

	// Text filter
	out, err := lrepo.List(ctx, links.ListQuery{Q: "example.com"})
	require.NoError(t, err)
	assert.Len(t, out, 2)

	// Tag AND filter: must have BOTH a and b
	out, err = lrepo.List(ctx, links.ListQuery{TagIDs: []int64{tagA.ID, tagB.ID}})
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "Beta", out[0].Title)
}

func TestRepository_UpdateReplacesTagSet(t *testing.T) {
	ctx, lrepo, trepo := setup(t)
	tagA, _ := trepo.Create(ctx, tags.CreateInput{Name: "a", Color: "#fff"})
	tagB, _ := trepo.Create(ctx, tags.CreateInput{Name: "b", Color: "#fff"})

	link, err := lrepo.Create(ctx, links.CreateInput{
		URL: "https://x", Title: "x", TagIDs: []int64{tagA.ID},
	})
	require.NoError(t, err)
	require.Len(t, link.Tags, 1)

	newTitle := "renamed"
	newTags := []int64{tagB.ID}
	updated, err := lrepo.Update(ctx, link.ID, links.UpdateInput{
		Title:  &newTitle,
		TagIDs: &newTags,
	})
	require.NoError(t, err)
	assert.Equal(t, "renamed", updated.Title)
	require.Len(t, updated.Tags, 1)
	assert.Equal(t, "b", updated.Tags[0].Name, "tag set must be replaced atomically")
}

func TestRepository_ClickAndResolveIsAtomic(t *testing.T) {
	ctx, lrepo, _ := setup(t)
	created, err := lrepo.Create(ctx, links.CreateInput{URL: "https://hn.example", Title: "HN"})
	require.NoError(t, err)

	url, err := lrepo.ClickAndResolve(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "https://hn.example", url)

	got, _ := lrepo.Get(ctx, created.ID)
	assert.EqualValues(t, 1, got.ClickCount)
	require.NotNil(t, got.LastClickedAt)
}

func TestRepository_ClickAndResolveNotFound(t *testing.T) {
	ctx, lrepo, _ := setup(t)
	_, err := lrepo.ClickAndResolve(ctx, 999)
	assert.ErrorIs(t, err, httperr.ErrNotFound)
}

func TestRepository_UpdatePreview(t *testing.T) {
	ctx, lrepo, _ := setup(t)
	created, _ := lrepo.Create(ctx, links.CreateInput{URL: "https://x", Title: "x"})
	fav, og, desc := "https://x/fav.ico", "https://x/og.png", "desc"
	require.NoError(t, lrepo.UpdatePreview(ctx, created.ID, links.StatusOK, &fav, &og, &desc, nil))

	got, _ := lrepo.Get(ctx, created.ID)
	assert.Equal(t, string(links.StatusOK), got.PreviewStatus)
	require.NotNil(t, got.FaviconURL)
	assert.Equal(t, fav, *got.FaviconURL)
}

func TestRepository_DeleteCascadesLinkTag(t *testing.T) {
	ctx, lrepo, trepo := setup(t)
	tag, _ := trepo.Create(ctx, tags.CreateInput{Name: "t", Color: "#fff"})
	link, _ := lrepo.Create(ctx, links.CreateInput{URL: "https://x", Title: "x", TagIDs: []int64{tag.ID}})

	require.NoError(t, lrepo.Delete(ctx, link.ID))
	_, err := lrepo.Get(ctx, link.ID)
	assert.ErrorIs(t, err, httperr.ErrNotFound)
}

// TestRepository_CreateDuplicateURLReturns409 locks the Go #3 fix. Previously
// the link_url_unique violation surfaced as a wrapped pgx error and httperr.Write
// fell through to 500. The browser extension and bulk import flows rely on a
// typed 409 url_taken to converge to a no-op.
func TestRepository_CreateDuplicateURLReturns409(t *testing.T) {
	ctx, lrepo, _ := setup(t)
	_, err := lrepo.Create(ctx, links.CreateInput{URL: "https://dup.example", Title: "first"})
	require.NoError(t, err)

	_, err = lrepo.Create(ctx, links.CreateInput{URL: "https://dup.example", Title: "second"})
	require.Error(t, err)
	var he *httperr.Error
	require.ErrorAs(t, err, &he, "duplicate URL must surface as *httperr.Error, not a raw pgx wrap")
	assert.Equal(t, 409, he.Status)
	assert.Equal(t, "url_taken", he.Code)
}

// TestHandler_CreateRejectsLargeBody locks the P2.5 fix: POST /api/links with
// a body over 64 KiB is refused with invalid_json (the MaxBytesReader trip
// surfaces as a parse failure to json.Decoder — sufficient for clients).
func TestHandler_CreateRejectsLargeBody(t *testing.T) {
	ctx, lrepo, _ := setup(t)
	_ = ctx
	h := links.NewHandler(lrepo, nopEnqueuer{})
	r := chi.NewRouter()
	r.Route("/api/links", h.Mount)

	// 64 KiB + 1 of valid-looking JSON ("description":"AAAA..."). The decoder
	// would happily accept it without the cap; MaxBytesReader trips first.
	big := make([]byte, 0, (64<<10)+1)
	big = append(big, `{"url":"https://x","title":"t","description":"`...)
	pad := make([]byte, (64<<10)+1)
	for i := range pad {
		pad[i] = 'A'
	}
	big = append(big, pad...)
	big = append(big, `"}`...)

	req := httptest.NewRequest(http.MethodPost, "/api/links/", bytes.NewReader(big))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "body over jsonBodyCap must be refused")
	// Assert the response code (not just status) — a future refactor that
	// returned 400 invalid_input from a different shape check could pass the
	// status assertion while quietly disabling the body cap.
	assert.Contains(t, w.Body.String(), `"invalid_json"`, "must surface as invalid_json (MaxBytesReader trip)")
}

// TestHandler_CreateStillReturns201WhenEnqueueFails locks the Phase 2 contract:
// link creation succeeds even if the preview worker queue is saturated. A
// failed enqueue is operational (next requeuePending picks it up), not a
// client-facing error.
func TestHandler_CreateStillReturns201WhenEnqueueFails(t *testing.T) {
	ctx, lrepo, _ := setup(t)
	_ = ctx
	h := links.NewHandler(lrepo, fullEnqueuer{})
	r := chi.NewRouter()
	r.Route("/api/links", h.Mount)

	body := `{"url":"https://example.com","title":"t"}`
	req := httptest.NewRequest(http.MethodPost, "/api/links/", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code, "ErrQueueFull from Enqueue must not surface as a client error")
}

type fullEnqueuer struct{}

func (fullEnqueuer) Enqueue(int64) error {
	// Mirror the worker's contract — ErrQueueFull is the runtime signal we
	// want the handler to swallow without affecting the response.
	return errors.New("queue full")
}

type nopEnqueuer struct{}

func (nopEnqueuer) Enqueue(int64) error { return nil }

// TestRepository_PinnedAlwaysComesFirst locks the §5 invariant: pinned links
// outrank everything else, including the selected sort. Without this test,
// dropping `l.pinned DESC` from the ORDER BY in any sort branch ships green.
func TestRepository_PinnedAlwaysComesFirst(t *testing.T) {
	ctx, lrepo, _ := setup(t)

	// Newer link first (default "created" sort) but NOT pinned.
	newer, err := lrepo.Create(ctx, links.CreateInput{URL: "https://newer", Title: "Newer"})
	require.NoError(t, err)
	// Older link, pinned — should appear FIRST despite being older.
	older, err := lrepo.Create(ctx, links.CreateInput{URL: "https://older", Title: "Older"})
	require.NoError(t, err)
	pinTrue := true
	_, err = lrepo.Update(ctx, older.ID, links.UpdateInput{Pinned: &pinTrue})
	require.NoError(t, err)

	for _, sort := range []string{"", "recent", "clicks", "alpha", "alpha_desc"} {
		t.Run("sort="+sort, func(t *testing.T) {
			out, err := lrepo.List(ctx, links.ListQuery{Sort: sort})
			require.NoError(t, err)
			require.GreaterOrEqual(t, len(out), 2)
			assert.True(t, out[0].Pinned, "pinned link must always come first under sort=%q", sort)
			assert.Equal(t, older.ID, out[0].ID)
		})
	}
	_ = newer
}

// TestRepository_UpdateDuplicateURLReturns409 mirrors the above for the Update
// path — folding a link onto another's URL via PATCH should also surface as
// 409, not 500.
func TestRepository_UpdateDuplicateURLReturns409(t *testing.T) {
	ctx, lrepo, _ := setup(t)
	a, err := lrepo.Create(ctx, links.CreateInput{URL: "https://a.example", Title: "A"})
	require.NoError(t, err)
	_, err = lrepo.Create(ctx, links.CreateInput{URL: "https://b.example", Title: "B"})
	require.NoError(t, err)

	bURL := "https://b.example"
	_, err = lrepo.Update(ctx, a.ID, links.UpdateInput{URL: &bURL})
	require.Error(t, err)
	var he *httperr.Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, 409, he.Status)
	assert.Equal(t, "url_taken", he.Code)
}

// TestRepository_UngroupedExcludesLinksInFolders locks CLAUDE.md §4: the home
// query (`?ungrouped=1`) must only surface links with `folder_id IS NULL`.
// Without this, a link in a folder would appear both in the folder card AND
// on the home grid, double-rendered.
func TestRepository_UngroupedExcludesLinksInFolders(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	frepo := folders.NewRepository(pool)
	lrepo := links.NewRepository(pool)

	f, err := frepo.Create(ctx, folders.CreateInput{Name: "Inbox", Color: "#abc"})
	require.NoError(t, err)
	_, err = lrepo.Create(ctx, links.CreateInput{URL: "https://in", Title: "InFolder", FolderID: &f.ID})
	require.NoError(t, err)
	ungrouped, err := lrepo.Create(ctx, links.CreateInput{URL: "https://out", Title: "Ungrouped"})
	require.NoError(t, err)

	got, err := lrepo.List(ctx, links.ListQuery{Ungrouped: true})
	require.NoError(t, err)
	require.Len(t, got, 1, "?ungrouped=1 must surface only links with folder_id IS NULL")
	assert.Equal(t, ungrouped.ID, got[0].ID)

	// Unscoped list returns both.
	all, err := lrepo.List(ctx, links.ListQuery{})
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

// TestRepository_ListByFolderANDTag locks the §4 composition: a folder scope
// and a tag filter must compose with AND, not OR. Inside folder F, toggling
// tag X narrows the result to links in F that also have X.
func TestRepository_ListByFolderANDTag(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	frepo := folders.NewRepository(pool)
	trepo := tags.NewRepository(pool)
	lrepo := links.NewRepository(pool)

	folder, err := frepo.Create(ctx, folders.CreateInput{Name: "Work", Color: "#abc"})
	require.NoError(t, err)
	tagX, err := trepo.Create(ctx, tags.CreateInput{Name: "x", Color: "#fff"})
	require.NoError(t, err)

	withTag, err := lrepo.Create(ctx, links.CreateInput{
		URL: "https://withtag", Title: "WithTag", FolderID: &folder.ID, TagIDs: []int64{tagX.ID},
	})
	require.NoError(t, err)
	noTag, err := lrepo.Create(ctx, links.CreateInput{
		URL: "https://notag", Title: "NoTag", FolderID: &folder.ID,
	})
	require.NoError(t, err)

	folderOnly, err := lrepo.List(ctx, links.ListQuery{FolderID: &folder.ID})
	require.NoError(t, err)
	require.Len(t, folderOnly, 2, "folder scope alone returns both")

	combined, err := lrepo.List(ctx, links.ListQuery{FolderID: &folder.ID, TagIDs: []int64{tagX.ID}})
	require.NoError(t, err)
	require.Len(t, combined, 1, "folder + tag must AND, not OR")
	assert.Equal(t, withTag.ID, combined[0].ID)
	_ = noTag
}

// TestRepository_GoEndpointIsOnlyClickInserter locks CLAUDE.md §4: `click_log`
// is the single source of truth for clicks AND `/go/:id` is the only path
// that inserts into it. Reading a link via Get/List/GetBySlug must NOT bump
// the count.
func TestRepository_GoEndpointIsOnlyClickInserter(t *testing.T) {
	ctx, lrepo, _ := setup(t)
	link, err := lrepo.Create(ctx, links.CreateInput{URL: "https://only-go", Title: "OnlyGo"})
	require.NoError(t, err)

	// Exercise every read path that is NOT /go/:id.
	_, _ = lrepo.Get(ctx, link.ID)
	_, _ = lrepo.GetBySlug(ctx, link.Slug)
	_, _ = lrepo.List(ctx, links.ListQuery{})

	got, err := lrepo.Get(ctx, link.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 0, got.ClickCount, "no read path may write click_log")

	// Now exercise the /go/:id atomic path and confirm count moves to 1.
	_, err = lrepo.ClickAndResolve(ctx, link.ID)
	require.NoError(t, err)
	got, err = lrepo.Get(ctx, link.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, got.ClickCount)
}

// TestSchema_NoCachedClickColumns locks migration 000006: link.click_count and
// link.last_clicked_at were dropped — derived from click_log via LATERAL on
// every SELECT. A future migration that adds them back would silently keep
// the LATERAL but also re-introduce the denormalization drift; this test
// surfaces it at boot.
func TestSchema_NoCachedClickColumns(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	rows, err := pool.Query(ctx, `
        SELECT column_name FROM information_schema.columns
        WHERE table_name = 'link' AND column_name IN ('click_count','last_clicked_at')
    `)
	require.NoError(t, err)
	defer rows.Close()
	var found []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		found = append(found, n)
	}
	require.NoError(t, rows.Err())
	assert.Empty(t, found, "click_count/last_clicked_at must NOT exist on link — they are derived from click_log")
}

func TestRepository_SortByClicks(t *testing.T) {
	ctx, lrepo, _ := setup(t)
	a, _ := lrepo.Create(ctx, links.CreateInput{URL: "https://a", Title: "A"})
	b, _ := lrepo.Create(ctx, links.CreateInput{URL: "https://b", Title: "B"})

	// Bump b twice, a once.
	_, _ = lrepo.ClickAndResolve(ctx, b.ID)
	_, _ = lrepo.ClickAndResolve(ctx, b.ID)
	_, _ = lrepo.ClickAndResolve(ctx, a.ID)

	out, err := lrepo.List(ctx, links.ListQuery{Sort: "clicks"})
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Equal(t, "B", out[0].Title, "highest click_count first")
}
