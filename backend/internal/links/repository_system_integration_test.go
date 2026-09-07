//go:build integration

package links_test

import (
	"context"
	"sync"
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

// RACE-HER-005 property: the entity_click_stats projection is a monotonic
// atomic increment — under any interleaving of concurrent public clicks the
// final click_count must equal click_log exactly (INV-056: click_log is the
// source of truth; no undercount, no double count).
func TestClickAndResolve_ConcurrentClicksAreCountedMonotonically(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	uid := testdb.SeedUser(t, pool, "clickmonotonic-owner@test.local", "admin")
	repo := links.NewRepository(pool)

	link, err := repo.Create(ctx, uid, links.CreateInput{URL: "https://clickmonotonic.test/a", Title: "a"})
	require.NoError(t, err)

	const goroutines, perGoroutine = 8, 10
	var wg sync.WaitGroup
	errs := make(chan error, goroutines*perGoroutine)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				if _, err := repo.ClickAndResolve(ctx, link.ID); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	var logged, projected int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM click_log WHERE entity_kind = 'link' AND entity_id = $1`, link.ID).Scan(&logged))
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT click_count FROM entity_click_stats WHERE entity_kind = 'link' AND entity_id = $1`, link.ID).Scan(&projected))
	require.EqualValues(t, goroutines*perGoroutine, logged)
	require.EqualValues(t, logged, projected,
		"entity_click_stats must equal click_log exactly under any interleaving")
}
