//go:build integration

package links_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"foldex/internal/links"
	"foldex/internal/testdb"
)

func TestSystemReleaseCheckClaims_RestoresDueImmediately(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	uid := testdb.SeedUser(t, pool, "cc-release-owner@test.local", "admin")
	repo := links.NewRepository(pool)

	weekly := "weekly"
	link, err := repo.Create(ctx, uid, links.CreateInput{
		URL: "https://cc-release.test/a", Title: "a", CheckInterval: &weekly,
	})
	require.NoError(t, err)

	due, err := repo.SystemClaimDueForCheck(ctx, 10)
	require.NoError(t, err)
	require.Len(t, due, 1)
	require.Equal(t, link.ID, due[0].ID)

	again, err := repo.SystemClaimDueForCheck(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, again, "a claimed link must not be due again within its interval")

	require.NoError(t, repo.SystemReleaseCheckClaims(ctx, []int64{link.ID}))

	reclaimed, err := repo.SystemClaimDueForCheck(ctx, 10)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1, "a released claim must be immediately due again")
	require.Equal(t, link.ID, reclaimed[0].ID)
}
