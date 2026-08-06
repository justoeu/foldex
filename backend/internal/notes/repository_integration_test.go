//go:build integration

package notes_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/folders"
	"foldex/internal/links"
	"foldex/internal/notes"
	"foldex/internal/pkg/httperr"
	"foldex/internal/tags"
	"foldex/internal/testdb"

	"foldex/internal/pkg/authctx"
)

func setup(t *testing.T) (context.Context, authctx.UserID, *notes.Repository, *tags.Repository, *folders.Repository) {
	t.Helper()
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	return context.Background(), uid, notes.NewRepository(pool), tags.NewRepository(pool), folders.NewRepository(pool)
}

func TestRepository_CreateAndGetWithTags(t *testing.T) {
	ctx, uid, nrepo, trepo, _ := setup(t)

	tagA, err := trepo.Create(ctx, uid, tags.CreateInput{Name: "snippets", Color: "#1f6feb"})
	require.NoError(t, err)

	created, err := nrepo.Create(ctx, uid, notes.CreateInput{
		Title:    "My Note",
		BodyHTML: "<p>hello <strong>world</strong></p>",
		TagIDs:   []int64{tagA.ID},
	})
	require.NoError(t, err)
	require.NotZero(t, created.ID)
	assert.Equal(t, "my-note", created.Slug)
	require.Len(t, created.Tags, 1)
	assert.Equal(t, "<p>hello <strong>world</strong></p>", created.BodyHTML)
	assert.EqualValues(t, 0, created.ClickCount)

	got, err := nrepo.Get(ctx, uid, created.ID)
	require.NoError(t, err)
	assert.Len(t, got.Tags, 1)
}

func TestRepository_Create_SanitizesBodyHTML(t *testing.T) {
	// Repository.Create does not call Normalize() itself — that's the DTO
	// layer's job, exercised at the handler boundary. This test locks that
	// the repository persists whatever BodyHTML it's given verbatim (so a
	// caller that skips Normalize is a caller bug, not a repository one) —
	// the real safety net is in dto_test.go's sanitize tests plus the
	// handler always calling Normalize before Create.
	ctx, uid, nrepo, _, _ := setup(t)
	created, err := nrepo.Create(ctx, uid, notes.CreateInput{Title: "x", BodyHTML: "<p>plain</p>"})
	require.NoError(t, err)
	assert.Equal(t, "<p>plain</p>", created.BodyHTML)
}

func TestRepository_Create_AutoSlugCollisionSuffix(t *testing.T) {
	ctx, uid, nrepo, _, _ := setup(t)
	a, err := nrepo.Create(ctx, uid, notes.CreateInput{Title: "Same Title"})
	require.NoError(t, err)
	b, err := nrepo.Create(ctx, uid, notes.CreateInput{Title: "Same Title"})
	require.NoError(t, err)
	assert.Equal(t, "same-title", a.Slug)
	assert.Equal(t, "same-title-2", b.Slug)
}

func TestRepository_Create_UserSuppliedSlugConflict(t *testing.T) {
	ctx, uid, nrepo, _, _ := setup(t)
	slug := "taken-slug"
	_, err := nrepo.Create(ctx, uid, notes.CreateInput{Title: "A", Slug: &slug})
	require.NoError(t, err)

	_, err = nrepo.Create(ctx, uid, notes.CreateInput{Title: "B", Slug: &slug})
	require.Error(t, err)
	var herr *httperr.Error
	require.ErrorAs(t, err, &herr)
	assert.Equal(t, "slug_taken", herr.Code)
	assert.Equal(t, 409, herr.Status)
}

func TestRepository_GetBySlug(t *testing.T) {
	ctx, uid, nrepo, _, _ := setup(t)
	created, err := nrepo.Create(ctx, uid, notes.CreateInput{Title: "Findable"})
	require.NoError(t, err)
	got, err := nrepo.GetBySlug(ctx, uid, created.Slug)
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)
}

func TestRepository_Get_NotFound(t *testing.T) {
	ctx, uid, nrepo, _, _ := setup(t)
	_, err := nrepo.Get(ctx, uid, 999999)
	require.ErrorIs(t, err, httperr.ErrNotFound)
}

func TestRepository_List_SearchTitleAndBody(t *testing.T) {
	ctx, uid, nrepo, _, _ := setup(t)
	_, err := nrepo.Create(ctx, uid, notes.CreateInput{Title: "Recipe for cake", BodyHTML: "<p>flour and sugar</p>"})
	require.NoError(t, err)
	_, err = nrepo.Create(ctx, uid, notes.CreateInput{Title: "Shopping list", BodyHTML: "<p>milk and eggs</p>"})
	require.NoError(t, err)

	byTitle, err := nrepo.List(ctx, uid, notes.ListQuery{Q: "cake"})
	require.NoError(t, err)
	require.Len(t, byTitle, 1)
	assert.Equal(t, "Recipe for cake", byTitle[0].Title)

	byBody, err := nrepo.List(ctx, uid, notes.ListQuery{Q: "eggs"})
	require.NoError(t, err)
	require.Len(t, byBody, 1)
	assert.Equal(t, "Shopping list", byBody[0].Title)
}

func TestRepository_List_TagANDFilter(t *testing.T) {
	ctx, uid, nrepo, trepo, _ := setup(t)
	tagA, _ := trepo.Create(ctx, uid, tags.CreateInput{Name: "a", Color: "#fff"})
	tagB, _ := trepo.Create(ctx, uid, tags.CreateInput{Name: "b", Color: "#fff"})

	_, _ = nrepo.Create(ctx, uid, notes.CreateInput{Title: "OnlyA", TagIDs: []int64{tagA.ID}})
	both, _ := nrepo.Create(ctx, uid, notes.CreateInput{Title: "Both", TagIDs: []int64{tagA.ID, tagB.ID}})

	out, err := nrepo.List(ctx, uid, notes.ListQuery{TagIDs: []int64{tagA.ID, tagB.ID}})
	require.NoError(t, err)
	require.Len(t, out, 1, "tag filter must AND, not OR")
	assert.Equal(t, both.ID, out[0].ID)
}

func TestRepository_List_FolderScope(t *testing.T) {
	ctx, uid, nrepo, _, frepo := setup(t)
	folder, err := frepo.Create(ctx, uid, folders.CreateInput{Name: "Reading", Color: "#abc"})
	require.NoError(t, err)

	inFolder, err := nrepo.Create(ctx, uid, notes.CreateInput{Title: "In folder", FolderID: &folder.ID})
	require.NoError(t, err)
	_, err = nrepo.Create(ctx, uid, notes.CreateInput{Title: "Root note"})
	require.NoError(t, err)

	scoped, err := nrepo.List(ctx, uid, notes.ListQuery{FolderID: &folder.ID})
	require.NoError(t, err)
	require.Len(t, scoped, 1)
	assert.Equal(t, inFolder.ID, scoped[0].ID)

	ungrouped, err := nrepo.List(ctx, uid, notes.ListQuery{Ungrouped: true})
	require.NoError(t, err)
	for _, n := range ungrouped {
		assert.NotEqual(t, inFolder.ID, n.ID)
	}
}

func TestRepository_List_PinnedAlwaysFirst(t *testing.T) {
	ctx, uid, nrepo, _, _ := setup(t)
	_, err := nrepo.Create(ctx, uid, notes.CreateInput{Title: "Unpinned"})
	require.NoError(t, err)
	pinned, err := nrepo.Create(ctx, uid, notes.CreateInput{Title: "Pinned", Pinned: true})
	require.NoError(t, err)

	out, err := nrepo.List(ctx, uid, notes.ListQuery{Sort: "alpha"})
	require.NoError(t, err)
	require.NotEmpty(t, out)
	assert.Equal(t, pinned.ID, out[0].ID, "pinned must sort first regardless of requested sort")
}

func TestRepository_List_AlphaSort(t *testing.T) {
	ctx, uid, nrepo, _, _ := setup(t)
	_, _ = nrepo.Create(ctx, uid, notes.CreateInput{Title: "Banana"})
	_, _ = nrepo.Create(ctx, uid, notes.CreateInput{Title: "Apple"})

	out, err := nrepo.List(ctx, uid, notes.ListQuery{Sort: "alpha"})
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Equal(t, "Apple", out[0].Title)
	assert.Equal(t, "Banana", out[1].Title)
}

func TestRepository_Update_TriStateFolderID(t *testing.T) {
	ctx, uid, nrepo, _, frepo := setup(t)
	folder, err := frepo.Create(ctx, uid, folders.CreateInput{Name: "F", Color: "#abc"})
	require.NoError(t, err)
	created, err := nrepo.Create(ctx, uid, notes.CreateInput{Title: "Note"})
	require.NoError(t, err)

	updated, err := nrepo.Update(ctx, uid, created.ID, notes.UpdateInput{FolderID: &folder.ID, FolderIDSet: true})
	require.NoError(t, err)
	require.NotNil(t, updated.FolderID)
	assert.Equal(t, folder.ID, *updated.FolderID)

	cleared, err := nrepo.Update(ctx, uid, created.ID, notes.UpdateInput{FolderIDSet: true})
	require.NoError(t, err)
	assert.Nil(t, cleared.FolderID)
}

func TestRepository_Update_SlugNullRegeneratesFromTitle(t *testing.T) {
	ctx, uid, nrepo, _, _ := setup(t)
	custom := "custom-slug"
	created, err := nrepo.Create(ctx, uid, notes.CreateInput{Title: "Original Title", Slug: &custom})
	require.NoError(t, err)
	assert.Equal(t, "custom-slug", created.Slug)

	updated, err := nrepo.Update(ctx, uid, created.ID, notes.UpdateInput{SlugSet: true})
	require.NoError(t, err)
	assert.Equal(t, "original-title", updated.Slug)
}

func TestRepository_Update_BodyHTMLNotTriState(t *testing.T) {
	ctx, uid, nrepo, _, _ := setup(t)
	created, err := nrepo.Create(ctx, uid, notes.CreateInput{Title: "x", BodyHTML: "<p>a</p>"})
	require.NoError(t, err)

	// nil BodyHTML = don't touch.
	same, err := nrepo.Update(ctx, uid, created.ID, notes.UpdateInput{})
	require.NoError(t, err)
	assert.Equal(t, "<p>a</p>", same.BodyHTML)

	// explicit empty string = legal cleared body.
	empty := ""
	cleared, err := nrepo.Update(ctx, uid, created.ID, notes.UpdateInput{BodyHTML: &empty})
	require.NoError(t, err)
	assert.Equal(t, "", cleared.BodyHTML)
}

func TestRepository_Update_TagIDs(t *testing.T) {
	ctx, uid, nrepo, trepo, _ := setup(t)
	tagA, _ := trepo.Create(ctx, uid, tags.CreateInput{Name: "a", Color: "#fff"})
	created, err := nrepo.Create(ctx, uid, notes.CreateInput{Title: "x"})
	require.NoError(t, err)
	require.Empty(t, created.Tags)

	updated, err := nrepo.Update(ctx, uid, created.ID, notes.UpdateInput{TagIDs: &[]int64{tagA.ID}})
	require.NoError(t, err)
	require.Len(t, updated.Tags, 1)
	assert.Equal(t, tagA.ID, updated.Tags[0].ID)
}

// TestNoteUpdate_ConflictOnStaleUpdatedAt locks RACE-HER-012 optional CAS.
func TestNoteUpdate_ConflictOnStaleUpdatedAt(t *testing.T) {
	ctx, uid, nrepo, _, _ := setup(t)
	created, err := nrepo.Create(ctx, uid, notes.CreateInput{Title: "v1"})
	require.NoError(t, err)
	seen := created.UpdatedAt

	title := "v2"
	updated, err := nrepo.Update(ctx, uid, created.ID, notes.UpdateInput{Title: &title})
	require.NoError(t, err)
	assert.Equal(t, "v2", updated.Title)

	stale := "stale-loser"
	_, err = nrepo.Update(ctx, uid, created.ID, notes.UpdateInput{
		Title:            &stale,
		IfMatchUpdatedAt: &seen,
	})
	require.Error(t, err)
	var he *httperr.Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, 409, he.Status)
	assert.Equal(t, "conflict", he.Code)

	// Fresh match still works.
	fresh := "v3"
	ok, err := nrepo.Update(ctx, uid, created.ID, notes.UpdateInput{
		Title:            &fresh,
		IfMatchUpdatedAt: &updated.UpdatedAt,
	})
	require.NoError(t, err)
	assert.Equal(t, "v3", ok.Title)
}

func TestRepository_Update_NotFound(t *testing.T) {
	ctx, uid, nrepo, _, _ := setup(t)
	title := "x"
	_, err := nrepo.Update(ctx, uid, 999999, notes.UpdateInput{Title: &title})
	require.ErrorIs(t, err, httperr.ErrNotFound)
}

func TestRepository_Delete_CascadesTagsAndClicks(t *testing.T) {
	ctx, uid, nrepo, trepo, _ := setup(t)
	tagA, _ := trepo.Create(ctx, uid, tags.CreateInput{Name: "a", Color: "#fff"})
	created, err := nrepo.Create(ctx, uid, notes.CreateInput{Title: "Doomed", TagIDs: []int64{tagA.ID}})
	require.NoError(t, err)
	_, err = nrepo.SystemViewAndResolve(ctx, created.Slug)
	require.NoError(t, err)

	require.NoError(t, nrepo.Delete(ctx, uid, created.ID, nil))

	_, err = nrepo.Get(ctx, uid, created.ID)
	require.ErrorIs(t, err, httperr.ErrNotFound)

	// Re-create a note and confirm the deleted note's tag/click rows didn't
	// leave dangling entity_kind='note' rows that could surface elsewhere.
	out, err := nrepo.List(ctx, uid, notes.ListQuery{TagIDs: []int64{tagA.ID}})
	require.NoError(t, err)
	for _, n := range out {
		assert.NotEqual(t, created.ID, n.ID, "deleted note's link_tag row must be gone")
	}
}

func TestRepository_Delete_NotFound(t *testing.T) {
	ctx, uid, nrepo, _, _ := setup(t)
	err := nrepo.Delete(ctx, uid, 999999, nil)
	require.ErrorIs(t, err, httperr.ErrNotFound)
}

func TestRepository_ViewAndResolve_LogsClickByIDAndSlug(t *testing.T) {
	ctx, uid, nrepo, _, _ := setup(t)
	created, err := nrepo.Create(ctx, uid, notes.CreateInput{Title: "Viewable"})
	require.NoError(t, err)

	got, err := nrepo.Get(ctx, uid, created.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 0, got.ClickCount, "Get must never write click_log")

	_, err = nrepo.SystemViewAndResolve(ctx, created.Slug)
	require.NoError(t, err)
	got, err = nrepo.Get(ctx, uid, created.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, got.ClickCount)

	_, err = nrepo.SystemViewAndResolve(ctx, strconv.FormatInt(created.ID, 10))
	require.NoError(t, err)
	got, err = nrepo.Get(ctx, uid, created.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, got.ClickCount)
}

func TestRepository_ViewAndResolve_NotFound(t *testing.T) {
	ctx, _, nrepo, _, _ := setup(t)
	_, err := nrepo.SystemViewAndResolve(ctx, "does-not-exist")
	require.ErrorIs(t, err, httperr.ErrNotFound)
}

// TestCrossContamination_LinkAndNoteRowsDoNotLeak is the regression guard for
// the highest-risk consequence of polymorphizing link_tag/click_log: a tag
// or click attached to a note (or link) must never surface when querying the
// other entity kind, even when the two share the same numeric id.
func TestCrossContamination_LinkAndNoteRowsDoNotLeak(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	nrepo := notes.NewRepository(pool)
	lrepo := links.NewRepository(pool)
	trepo := tags.NewRepository(pool)
	tag, err := trepo.Create(ctx, uid, tags.CreateInput{Name: "shared", Color: "#fff"})
	require.NoError(t, err)

	link, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://example.com/x", Title: "L", TagIDs: []int64{tag.ID}})
	require.NoError(t, err)
	note, err := nrepo.Create(ctx, uid, notes.CreateInput{Title: "N", TagIDs: []int64{tag.ID}})
	require.NoError(t, err)
	// The whole point of this test is that link.ID and note.ID collide (a
	// fresh testdb gives both BIGSERIALs the same starting value) — without
	// this, entity_id alone can't be ambiguous and the test would pass for a
	// trivial reason instead of proving cross-kind isolation. Assert it
	// explicitly so a future fixture-ordering change that breaks the
	// collision fails loudly here instead of silently testing nothing.
	require.Equal(t, link.ID, note.ID, "test premise: link and note must share the same numeric id")

	_, err = lrepo.ClickAndResolve(ctx, link.ID)
	require.NoError(t, err)
	_, err = nrepo.SystemViewAndResolve(ctx, note.Slug)
	require.NoError(t, err)

	gotLink, err := lrepo.Get(ctx, uid, link.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, gotLink.ClickCount, "link click_count must not include note views")

	gotNote, err := nrepo.Get(ctx, uid, note.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, gotNote.ClickCount, "note click_count must not include link clicks")
}
