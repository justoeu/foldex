//go:build integration

package entries_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/entries"
	"foldex/internal/folders"
	"foldex/internal/links"
	"foldex/internal/notes"
	"foldex/internal/tags"
	"foldex/internal/testdb"
)

type fixture struct {
	erepo *entries.Repository
	lrepo *links.Repository
	nrepo *notes.Repository
	trepo *tags.Repository
	frepo *folders.Repository
}

func setup(t *testing.T) (context.Context, fixture) {
	t.Helper()
	pool := testdb.New(t)
	return context.Background(), fixture{
		erepo: entries.NewRepository(pool),
		lrepo: links.NewRepository(pool),
		nrepo: notes.NewRepository(pool),
		trepo: tags.NewRepository(pool),
		frepo: folders.NewRepository(pool),
	}
}

func TestList_InterleavesLinksAndNotes(t *testing.T) {
	ctx, f := setup(t)
	link, err := f.lrepo.Create(ctx, links.CreateInput{URL: "https://example.com/a", Title: "Link A"})
	require.NoError(t, err)
	note, err := f.nrepo.Create(ctx, notes.CreateInput{Title: "Note A"})
	require.NoError(t, err)

	out, err := f.erepo.List(ctx, entries.ListQuery{})
	require.NoError(t, err)
	require.Len(t, out, 2)

	kinds := map[string]bool{}
	for _, e := range out {
		kinds[e.Kind] = true
		if e.Kind == "link" {
			assert.Equal(t, link.ID, e.ID)
			require.NotNil(t, e.URL)
			assert.Equal(t, "https://example.com/a", *e.URL)
		} else {
			assert.Equal(t, note.ID, e.ID)
			assert.Nil(t, e.URL, "note entries must not carry link-only fields")
		}
	}
	assert.True(t, kinds["link"])
	assert.True(t, kinds["note"])
}

func TestList_PinnedAlwaysFirstAcrossKinds(t *testing.T) {
	ctx, f := setup(t)
	_, err := f.lrepo.Create(ctx, links.CreateInput{URL: "https://example.com/unpinned", Title: "Unpinned link"})
	require.NoError(t, err)
	pinnedNote, err := f.nrepo.Create(ctx, notes.CreateInput{Title: "Pinned note", Pinned: true})
	require.NoError(t, err)

	out, err := f.erepo.List(ctx, entries.ListQuery{Sort: "alpha"})
	require.NoError(t, err)
	require.NotEmpty(t, out)
	assert.Equal(t, "note", out[0].Kind)
	assert.Equal(t, pinnedNote.ID, out[0].ID)
}

func TestList_AlphaSortAcrossKinds(t *testing.T) {
	ctx, f := setup(t)
	_, err := f.lrepo.Create(ctx, links.CreateInput{URL: "https://example.com/z", Title: "Zebra link"})
	require.NoError(t, err)
	_, err = f.nrepo.Create(ctx, notes.CreateInput{Title: "Apple note"})
	require.NoError(t, err)

	out, err := f.erepo.List(ctx, entries.ListQuery{Sort: "alpha"})
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Equal(t, "Apple note", out[0].Title)
	assert.Equal(t, "Zebra link", out[1].Title)
}

func TestList_SearchMatchesLinkURLAndNoteBody(t *testing.T) {
	ctx, f := setup(t)
	_, err := f.lrepo.Create(ctx, links.CreateInput{URL: "https://jira.example/INV-1", Title: "Ticket"})
	require.NoError(t, err)
	_, err = f.nrepo.Create(ctx, notes.CreateInput{Title: "Shopping", BodyHTML: "<p>buy oat milk</p>"})
	require.NoError(t, err)

	byURL, err := f.erepo.List(ctx, entries.ListQuery{Q: "jira.example"})
	require.NoError(t, err)
	require.Len(t, byURL, 1)
	assert.Equal(t, "link", byURL[0].Kind)

	byBody, err := f.erepo.List(ctx, entries.ListQuery{Q: "oat milk"})
	require.NoError(t, err)
	require.Len(t, byBody, 1)
	assert.Equal(t, "note", byBody[0].Kind)
}

func TestList_TagFilterScopedPerKind(t *testing.T) {
	ctx, f := setup(t)
	tag, err := f.trepo.Create(ctx, tags.CreateInput{Name: "shared", Color: "#fff"})
	require.NoError(t, err)

	taggedLink, err := f.lrepo.Create(ctx, links.CreateInput{URL: "https://example.com/tagged", Title: "Tagged link", TagIDs: []int64{tag.ID}})
	require.NoError(t, err)
	taggedNote, err := f.nrepo.Create(ctx, notes.CreateInput{Title: "Tagged note", TagIDs: []int64{tag.ID}})
	require.NoError(t, err)
	_, err = f.lrepo.Create(ctx, links.CreateInput{URL: "https://example.com/untagged", Title: "Untagged link"})
	require.NoError(t, err)

	out, err := f.erepo.List(ctx, entries.ListQuery{TagIDs: []int64{tag.ID}})
	require.NoError(t, err)
	require.Len(t, out, 2)
	gotIDs := map[string]int64{}
	for _, e := range out {
		gotIDs[e.Kind] = e.ID
		require.Len(t, e.Tags, 1)
		assert.Equal(t, tag.ID, e.Tags[0].ID)
	}
	assert.Equal(t, taggedLink.ID, gotIDs["link"])
	assert.Equal(t, taggedNote.ID, gotIDs["note"])
}

func TestList_FolderScope(t *testing.T) {
	ctx, f := setup(t)
	folder, err := f.frepo.Create(ctx, folders.CreateInput{Name: "Reading", Color: "#abc"})
	require.NoError(t, err)

	inFolderLink, err := f.lrepo.Create(ctx, links.CreateInput{URL: "https://example.com/in", Title: "In folder link", FolderID: &folder.ID})
	require.NoError(t, err)
	inFolderNote, err := f.nrepo.Create(ctx, notes.CreateInput{Title: "In folder note", FolderID: &folder.ID})
	require.NoError(t, err)
	_, err = f.lrepo.Create(ctx, links.CreateInput{URL: "https://example.com/root", Title: "Root link"})
	require.NoError(t, err)

	out, err := f.erepo.List(ctx, entries.ListQuery{FolderID: &folder.ID})
	require.NoError(t, err)
	require.Len(t, out, 2)
	for _, e := range out {
		if e.Kind == "link" {
			assert.Equal(t, inFolderLink.ID, e.ID)
		} else {
			assert.Equal(t, inFolderNote.ID, e.ID)
		}
	}

	ungrouped, err := f.erepo.List(ctx, entries.ListQuery{Ungrouped: true})
	require.NoError(t, err)
	for _, e := range ungrouped {
		if e.Kind == "link" {
			assert.NotEqual(t, inFolderLink.ID, e.ID, "ungrouped scope must exclude the in-folder link")
		} else {
			assert.NotEqual(t, inFolderNote.ID, e.ID, "ungrouped scope must exclude the in-folder note")
		}
	}
}

func TestList_PaginationBoundarySpansBothKinds(t *testing.T) {
	ctx, f := setup(t)
	titles := []string{"item-a", "item-b", "item-c", "item-d", "item-e", "item-f"}
	for i := 0; i < 3; i++ {
		_, err := f.lrepo.Create(ctx, links.CreateInput{URL: "https://example.com/" + titles[i], Title: titles[i]})
		require.NoError(t, err)
	}
	for i := 3; i < 6; i++ {
		_, err := f.nrepo.Create(ctx, notes.CreateInput{Title: titles[i]})
		require.NoError(t, err)
	}

	all, err := f.erepo.List(ctx, entries.ListQuery{Sort: "alpha", Limit: 100})
	require.NoError(t, err)
	require.Len(t, all, 6)

	page1, err := f.erepo.List(ctx, entries.ListQuery{Sort: "alpha", Limit: 2, Offset: 0})
	require.NoError(t, err)
	page2, err := f.erepo.List(ctx, entries.ListQuery{Sort: "alpha", Limit: 2, Offset: 2})
	require.NoError(t, err)
	page3, err := f.erepo.List(ctx, entries.ListQuery{Sort: "alpha", Limit: 2, Offset: 4})
	require.NoError(t, err)

	require.Len(t, page1, 2)
	require.Len(t, page2, 2)
	require.Len(t, page3, 2)
	assert.Equal(t, all[0].ID, page1[0].ID)
	assert.Equal(t, all[0].Kind, page1[0].Kind)
	assert.Equal(t, all[2].ID, page2[0].ID)
	assert.Equal(t, all[4].ID, page3[0].ID)
}
