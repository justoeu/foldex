//go:build integration

package tags_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/httperr"
	"foldex/internal/tags"
	"foldex/internal/testdb"
)

func TestRepository_CRUD(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	repo := tags.NewRepository(pool)

	icon := "🪲"
	created, err := repo.Create(ctx, tags.CreateInput{Name: "jira", Color: "#1f6feb", Icon: &icon})
	require.NoError(t, err)
	assert.Equal(t, "jira", created.Name)
	assert.NotZero(t, created.ID)

	// Get
	got, err := repo.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "jira", got.Name)

	// List shows the row with link_count=0
	all, err := repo.List(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.EqualValues(t, 0, all[0].LinkCount)

	// Update
	newName := "Jira"
	upd, err := repo.Update(ctx, created.ID, tags.UpdateInput{Name: &newName})
	require.NoError(t, err)
	assert.Equal(t, "Jira", upd.Name)

	// Delete
	require.NoError(t, repo.Delete(ctx, created.ID))
	_, err = repo.Get(ctx, created.ID)
	assert.ErrorIs(t, err, httperr.ErrNotFound)
}

func TestRepository_CreateDuplicateNameConflict(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	repo := tags.NewRepository(pool)

	_, err := repo.Create(ctx, tags.CreateInput{Name: "docs", Color: "#fff"})
	require.NoError(t, err)
	_, err = repo.Create(ctx, tags.CreateInput{Name: "docs", Color: "#000"})
	require.Error(t, err)
	var he *httperr.Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, "tag_name_taken", he.Code)
}

func TestRepository_DeleteMissing(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	repo := tags.NewRepository(pool)
	err := repo.Delete(ctx, 999)
	assert.ErrorIs(t, err, httperr.ErrNotFound)
}

func TestRepository_UpdateEmptyPatchReturnsCurrent(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	repo := tags.NewRepository(pool)

	created, err := repo.Create(ctx, tags.CreateInput{Name: "x", Color: "#abc"})
	require.NoError(t, err)
	got, err := repo.Update(ctx, created.ID, tags.UpdateInput{})
	require.NoError(t, err)
	assert.Equal(t, "x", got.Name)
}
