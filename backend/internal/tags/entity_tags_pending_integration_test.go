//go:build integration

package tags_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/links"
	"foldex/internal/pkg/domainerr"
	"foldex/internal/tags"
	"foldex/internal/testdb"
)

func TestSetEntityTagsWithPending_CreatesAndAttaches(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	uid := testdb.SeedUser(t, pool, "pending-tags-owner@test.local", "admin")
	lrepo := links.NewRepository(pool)
	link, err := lrepo.Create(ctx, uid, links.CreateInput{
		URL: "https://pending-tags.test/a", Title: "a",
	})
	require.NoError(t, err)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	require.NoError(t, tags.SetEntityTagsWithPending(ctx, tx, uid, "link", link.ID, nil, []tags.CreateInput{
		{Name: "pending-a", Color: "#abc"},
		{Name: "pending-b", Color: "#def"},
	}))
	require.NoError(t, tx.Commit(ctx))

	chips, err := tags.TagsForEntities(ctx, pool, uid, "link", []int64{link.ID})
	require.NoError(t, err)
	require.Len(t, chips[link.ID], 2)
	names := []string{chips[link.ID][0].Name, chips[link.ID][1].Name}
	assert.ElementsMatch(t, []string{"pending-a", "pending-b"}, names)

	both, err := tags.TagsForLinkAndNote(ctx, pool, uid, []int64{link.ID}, nil)
	require.NoError(t, err)
	assert.Len(t, both["link"][link.ID], 2)
}

func TestApplyPatchTags_NoopWhenNothingChanges(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	uid := testdb.SeedUser(t, pool, "patch-tags-noop@test.local", "admin")
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	require.NoError(t, tags.ApplyPatchTags(ctx, tx, uid, "link", 1, nil, nil))
}

func TestApplyPatchTags_RejectsUnownedEntity(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	uid := testdb.SeedUser(t, pool, "patch-tags-unowned@test.local", "admin")
	ids := []int64{1}
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	err = tags.ApplyPatchTags(ctx, tx, uid, "link", 9_000_001, &ids, nil)
	assert.ErrorIs(t, err, domainerr.ErrNotFound)
}

func TestApplyPatchTags_ReplacesExistingIDs(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	uid := testdb.SeedUser(t, pool, "patch-tags-replace@test.local", "admin")
	trepo := tags.NewRepository(pool)
	lrepo := links.NewRepository(pool)
	tag, err := trepo.Create(ctx, uid, tags.CreateInput{Name: "keep", Color: "#abc"})
	require.NoError(t, err)
	link, err := lrepo.Create(ctx, uid, links.CreateInput{
		URL: "https://patch-tags.test/b", Title: "b",
	})
	require.NoError(t, err)

	ids := []int64{tag.ID}
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	require.NoError(t, tags.ApplyPatchTags(ctx, tx, uid, "link", link.ID, &ids, nil))
	require.NoError(t, tx.Commit(ctx))

	chips, err := tags.TagsForEntities(ctx, pool, uid, "link", []int64{link.ID})
	require.NoError(t, err)
	require.Len(t, chips[link.ID], 1)
	assert.Equal(t, tag.ID, chips[link.ID][0].ID)
}
