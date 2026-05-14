//go:build integration

package links_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
