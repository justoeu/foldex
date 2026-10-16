//go:build integration

package importer

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/testdb"
)

// Two same-user imports racing on identical titles must both land: the
// FOR NO KEY UPDATE row lock serializes staging and slug allocation.
// (Stager level: the HTTP layer already rejects concurrent applies.)
func TestApply_ConcurrentSameTitleImportsBothLand(t *testing.T) {
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(context.Background(), pool))
	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	stager := NewStager(pool)

	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			items := []Item{{URL: fmt.Sprintf("https://race-%d.example", idx), Title: "Same Title"}}
			_, _, _, _, _, err := stager.ApplyWithMode(context.Background(), uid, items, modeSkip, nil)
			errs[idx] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "concurrent import %d", i)
	}

	var slugs int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(DISTINCT slug) FROM link WHERE user_id = $1`, int64(uid)).Scan(&slugs))
	assert.Equal(t, 2, slugs)
}
