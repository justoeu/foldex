//go:build integration

package entries_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/entries"
	"foldex/internal/folders"
	"foldex/internal/links"
	"foldex/internal/notes"
	"foldex/internal/pkg/listquery"
	"foldex/internal/tags"
	"foldex/internal/testdb"

	"foldex/internal/pkg/authctx"
)

type fixture struct {
	pool  *pgxpool.Pool
	erepo *entries.Repository
	lrepo *links.Repository
	nrepo *notes.Repository
	trepo *tags.Repository
	frepo *folders.Repository
}

func setup(t *testing.T) (context.Context, authctx.UserID, fixture) {
	t.Helper()
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	return context.Background(), uid, fixture{
		pool:  pool,
		erepo: entries.NewRepository(pool),
		lrepo: links.NewRepository(pool),
		nrepo: notes.NewRepository(pool),
		trepo: tags.NewRepository(pool),
		frepo: folders.NewRepository(pool),
	}
}

func TestList_InterleavesLinksAndNotes(t *testing.T) {
	ctx, uid, f := setup(t)
	link, err := f.lrepo.Create(ctx, uid, links.CreateInput{URL: "https://example.com/a", Title: "Link A"})
	require.NoError(t, err)
	note, err := f.nrepo.Create(ctx, uid, notes.CreateInput{Title: "Note A"})
	require.NoError(t, err)

	out, err := f.erepo.List(ctx, uid, entries.ListQuery{})
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
	ctx, uid, f := setup(t)
	_, err := f.lrepo.Create(ctx, uid, links.CreateInput{URL: "https://example.com/unpinned", Title: "Unpinned link"})
	require.NoError(t, err)
	pinnedNote, err := f.nrepo.Create(ctx, uid, notes.CreateInput{Title: "Pinned note", Pinned: true})
	require.NoError(t, err)

	out, err := f.erepo.List(ctx, uid, entries.ListQuery{Sort: "alpha"})
	require.NoError(t, err)
	require.NotEmpty(t, out)
	assert.Equal(t, "note", out[0].Kind)
	assert.Equal(t, pinnedNote.ID, out[0].ID)
}

func TestList_AlphaSortAcrossKinds(t *testing.T) {
	ctx, uid, f := setup(t)
	_, err := f.lrepo.Create(ctx, uid, links.CreateInput{URL: "https://example.com/z", Title: "Zebra link"})
	require.NoError(t, err)
	_, err = f.nrepo.Create(ctx, uid, notes.CreateInput{Title: "Apple note"})
	require.NoError(t, err)

	out, err := f.erepo.List(ctx, uid, entries.ListQuery{Sort: "alpha"})
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Equal(t, "Apple note", out[0].Title)
	assert.Equal(t, "Zebra link", out[1].Title)
}

func TestList_SearchMatchesLinkURLAndNoteBody(t *testing.T) {
	ctx, uid, f := setup(t)
	_, err := f.lrepo.Create(ctx, uid, links.CreateInput{URL: "https://jira.example/INV-1", Title: "Ticket"})
	require.NoError(t, err)
	_, err = f.nrepo.Create(ctx, uid, notes.CreateInput{Title: "Shopping", BodyHTML: "<p>buy oat milk</p>"})
	require.NoError(t, err)

	byURL, err := f.erepo.List(ctx, uid, entries.ListQuery{Q: "jira.example"})
	require.NoError(t, err)
	require.Len(t, byURL, 1)
	assert.Equal(t, "link", byURL[0].Kind)

	byBody, err := f.erepo.List(ctx, uid, entries.ListQuery{Q: "oat milk"})
	require.NoError(t, err)
	require.Len(t, byBody, 1)
	assert.Equal(t, "note", byBody[0].Kind)
}

func TestList_TagFilterScopedPerKind(t *testing.T) {
	ctx, uid, f := setup(t)
	tag, err := f.trepo.Create(ctx, uid, tags.CreateInput{Name: "shared", Color: "#fff"})
	require.NoError(t, err)

	taggedLink, err := f.lrepo.Create(ctx, uid, links.CreateInput{URL: "https://example.com/tagged", Title: "Tagged link", TagIDs: []int64{tag.ID}})
	require.NoError(t, err)
	taggedNote, err := f.nrepo.Create(ctx, uid, notes.CreateInput{Title: "Tagged note", TagIDs: []int64{tag.ID}})
	require.NoError(t, err)
	_, err = f.lrepo.Create(ctx, uid, links.CreateInput{URL: "https://example.com/untagged", Title: "Untagged link"})
	require.NoError(t, err)

	out, err := f.erepo.List(ctx, uid, entries.ListQuery{TagIDs: []int64{tag.ID}})
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
	ctx, uid, f := setup(t)
	folder, err := f.frepo.Create(ctx, uid, folders.CreateInput{Name: "Reading", Color: "#abc"})
	require.NoError(t, err)

	inFolderLink, err := f.lrepo.Create(ctx, uid, links.CreateInput{URL: "https://example.com/in", Title: "In folder link", FolderID: &folder.ID})
	require.NoError(t, err)
	inFolderNote, err := f.nrepo.Create(ctx, uid, notes.CreateInput{Title: "In folder note", FolderID: &folder.ID})
	require.NoError(t, err)
	_, err = f.lrepo.Create(ctx, uid, links.CreateInput{URL: "https://example.com/root", Title: "Root link"})
	require.NoError(t, err)

	out, err := f.erepo.List(ctx, uid, entries.ListQuery{FolderID: &folder.ID})
	require.NoError(t, err)
	require.Len(t, out, 2)
	for _, e := range out {
		if e.Kind == "link" {
			assert.Equal(t, inFolderLink.ID, e.ID)
		} else {
			assert.Equal(t, inFolderNote.ID, e.ID)
		}
	}

	ungrouped, err := f.erepo.List(ctx, uid, entries.ListQuery{Ungrouped: true})
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
	ctx, uid, f := setup(t)
	titles := []string{"item-a", "item-b", "item-c", "item-d", "item-e", "item-f"}
	for i := 0; i < 3; i++ {
		_, err := f.lrepo.Create(ctx, uid, links.CreateInput{URL: "https://example.com/" + titles[i], Title: titles[i]})
		require.NoError(t, err)
	}
	for i := 3; i < 6; i++ {
		_, err := f.nrepo.Create(ctx, uid, notes.CreateInput{Title: titles[i]})
		require.NoError(t, err)
	}

	all, err := f.erepo.List(ctx, uid, entries.ListQuery{Sort: "alpha", Limit: 100})
	require.NoError(t, err)
	require.Len(t, all, 6)

	page1, err := f.erepo.List(ctx, uid, entries.ListQuery{Sort: "alpha", Limit: 2, Offset: 0})
	require.NoError(t, err)
	page2, err := f.erepo.List(ctx, uid, entries.ListQuery{Sort: "alpha", Limit: 2, Offset: 2})
	require.NoError(t, err)
	page3, err := f.erepo.List(ctx, uid, entries.ListQuery{Sort: "alpha", Limit: 2, Offset: 4})
	require.NoError(t, err)

	require.Len(t, page1, 2)
	require.Len(t, page2, 2)
	require.Len(t, page3, 2)
	assert.Equal(t, all[0].ID, page1[0].ID)
	assert.Equal(t, all[0].Kind, page1[0].Kind)
	assert.Equal(t, all[2].ID, page2[0].ID)
	assert.Equal(t, all[4].ID, page3[0].ID)
}

func TestList_QueryParityWithLinkAndNoteRepositories(t *testing.T) {
	ctx, uid, f := setup(t)
	tagA, err := f.trepo.Create(ctx, uid, tags.CreateInput{Name: "parity-a", Color: "#fff"})
	require.NoError(t, err)
	tagB, err := f.trepo.Create(ctx, uid, tags.CreateInput{Name: "parity-b", Color: "#fff"})
	require.NoError(t, err)
	folder, err := f.frepo.Create(ctx, uid, folders.CreateInput{Name: "Parity", Color: "#abc"})
	require.NoError(t, err)

	rootLink, err := f.lrepo.Create(ctx, uid, links.CreateInput{
		URL: "https://needle.example/page-link-a", Title: "Zulu link", Description: stringPtr("shared needle"),
		Pinned: true, TagIDs: []int64{tagA.ID, tagB.ID},
	})
	require.NoError(t, err)
	_, err = f.lrepo.Create(ctx, uid, links.CreateInput{
		URL: "https://example.com/page-link-b", Title: "Alpha link", FolderID: &folder.ID, TagIDs: []int64{tagA.ID},
	})
	require.NoError(t, err)
	rootNote, err := f.nrepo.Create(ctx, uid, notes.CreateInput{
		Title: "Yankee note", BodyHTML: "<p>shared needle page-note-a</p>", TagIDs: []int64{tagA.ID, tagB.ID},
	})
	require.NoError(t, err)
	_, err = f.nrepo.Create(ctx, uid, notes.CreateInput{
		Title: "Beta note", BodyHTML: "<p>page-note-b</p>", FolderID: &folder.ID, TagIDs: []int64{tagA.ID},
	})
	require.NoError(t, err)
	_, err = f.lrepo.ClickAndResolve(ctx, rootLink.ID)
	require.NoError(t, err)
	_, err = f.nrepo.SystemViewAndResolve(ctx, rootNote.Slug)
	require.NoError(t, err)

	tests := []struct {
		name string
		q    listquery.Params
	}{
		{name: "default", q: listquery.Params{Limit: 500}},
		{name: "search both entity shapes", q: listquery.Params{Q: "shared needle", Limit: 500}},
		{name: "tag AND", q: listquery.Params{TagIDs: []int64{tagA.ID, tagB.ID}, Limit: 500}},
		{name: "folder and tag", q: listquery.Params{FolderID: &folder.ID, TagIDs: []int64{tagA.ID}, Limit: 500}},
		{name: "ungrouped", q: listquery.Params{Ungrouped: true, Limit: 500}},
		{name: "folder wins over ungrouped", q: listquery.Params{FolderID: &folder.ID, Ungrouped: true, Limit: 500}},
		{name: "clicks", q: listquery.Params{Sort: "clicks", Limit: 500}},
		{name: "recent", q: listquery.Params{Sort: "recent", Limit: 500}},
		{name: "alpha", q: listquery.Params{Sort: "alpha", Limit: 500}},
		{name: "alpha desc", q: listquery.Params{Sort: "alpha_desc", Limit: 500}},
		{name: "link-only pagination", q: listquery.Params{Q: "page-link", Sort: "alpha", Limit: 1, Offset: 1}},
		{name: "note-only pagination", q: listquery.Params{Q: "page-note", Sort: "alpha", Limit: 1, Offset: 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			linkRows, err := f.lrepo.List(ctx, uid, tc.q)
			require.NoError(t, err)
			noteRows, err := f.nrepo.List(ctx, uid, tc.q)
			require.NoError(t, err)
			entryRows, err := f.erepo.List(ctx, uid, tc.q)
			require.NoError(t, err)

			require.Equal(t, linkIDs(linkRows), entryIDs(entryRows, "link"), "link UNION arm drifted")
			require.Equal(t, noteIDs(noteRows), entryIDs(entryRows, "note"), "note UNION arm drifted")
		})
	}
}

func TestList_QueryCountIsConstant(t *testing.T) {
	ctx, uid, f := setup(t)
	tag, err := f.trepo.Create(ctx, uid, tags.CreateInput{Name: "batched", Color: "#fff"})
	require.NoError(t, err)
	for i := range 20 {
		_, err := f.lrepo.Create(ctx, uid, links.CreateInput{
			URL: fmt.Sprintf("https://query-count.example/%d", i), Title: fmt.Sprintf("Link %02d", i), TagIDs: []int64{tag.ID},
		})
		require.NoError(t, err)
		_, err = f.nrepo.Create(ctx, uid, notes.CreateInput{Title: fmt.Sprintf("Note %02d", i), TagIDs: []int64{tag.ID}})
		require.NoError(t, err)
	}

	tests := []struct {
		name string
		list func() error
	}{
		{name: "links", list: func() error {
			_, err := f.lrepo.List(ctx, uid, links.ListQuery{Limit: 500})
			return err
		}},
		{name: "notes", list: func() error {
			_, err := f.nrepo.List(ctx, uid, notes.ListQuery{Limit: 500})
			return err
		}},
		{name: "entries", list: func() error {
			_, err := f.erepo.List(ctx, uid, entries.ListQuery{Limit: 500})
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := f.pool.Stat().AcquireCount()
			require.NoError(t, tc.list())
			require.EqualValues(t, 2, f.pool.Stat().AcquireCount()-before,
				"list query plus one batched tag query must stay constant as row count grows")
		})
	}
}

func stringPtr(v string) *string { return &v }

func linkIDs(rows []links.Link) []int64 {
	out := make([]int64, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

func noteIDs(rows []notes.Note) []int64 {
	out := make([]int64, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

func entryIDs(rows []entries.Entry, kind string) []int64 {
	out := make([]int64, 0, len(rows))
	for _, row := range rows {
		if row.Kind == kind {
			out = append(out, row.ID)
		}
	}
	return out
}
