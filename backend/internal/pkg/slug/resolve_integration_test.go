//go:build integration

package slug_test

import (
	"context"
	"fmt"
	"strings"
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
	owner := testdb.SeedUser(t, pool, "slug-owner@test.local", "editor")
	other := testdb.SeedUser(t, pool, "slug-other@test.local", "editor")
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

func TestLoadTakenFindsLengthReservedSuffixes(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	owner := testdb.SeedUser(t, pool, "slug-collisions@test.local", "editor")
	base := strings.Repeat("a", slug.MaxLen)
	candidates := []string{
		base,
		strings.Repeat("a", slug.MaxLen-len("-2")) + "-2",
		strings.Repeat("a", slug.MaxLen-len("-10")) + "-10",
		strings.Repeat("a", slug.MaxLen-len("-100")) + "-100",
	}
	for i, candidate := range candidates {
		_, err := pool.Exec(ctx, `
			INSERT INTO link (user_id, url, title, slug)
			VALUES ($1, $2, 'collision', $3)`, int64(owner), fmt.Sprintf("https://collision-%d.test", i), candidate)
		require.NoError(t, err)
	}

	taken, err := slug.LoadTaken(ctx, pool, []string{base}, len(candidates))
	require.NoError(t, err)
	require.ElementsMatch(t, candidates, taken)
}
