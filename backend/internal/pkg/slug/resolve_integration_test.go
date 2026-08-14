//go:build integration

package slug_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"foldex/internal/links"
	"foldex/internal/pkg/domainerr"
	"foldex/internal/pkg/slug"
	"foldex/internal/testdb"
)

func TestResolveUpdate_FallbackTitleIsOwnerScoped(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	owner := testdb.SeedUser(t, pool, "slug-owner@test.local", "user")
	other := testdb.SeedUser(t, pool, "slug-other@test.local", "user")
	repo := links.NewRepository(pool)
	foreign, err := repo.Create(ctx, other, links.CreateInput{
		URL: "https://foreign-slug-title.example", Title: "Foreign private title",
	})
	require.NoError(t, err)

	_, err = slug.ResolveUpdate(ctx, pool, owner, "link", foreign.ID, nil, nil, "link")
	require.ErrorIs(t, err, domainerr.ErrNotFound)

	_, err = repo.Update(ctx, owner, foreign.ID, links.UpdateInput{SlugSet: true})
	require.ErrorIs(t, err, domainerr.ErrNotFound)
	unchanged, err := repo.Get(ctx, other, foreign.ID)
	require.NoError(t, err)
	require.Equal(t, foreign.Title, unchanged.Title)
	require.Equal(t, foreign.Slug, unchanged.Slug)
}
