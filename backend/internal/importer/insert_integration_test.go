//go:build integration

package importer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/links"
	"foldex/internal/tags"
	"foldex/internal/testdb"
)

// TestInsertLinkInTx_WipeFirstDoesNotOrphanTagsOrClicks is the regression
// lock for the gap migration 000014 introduced: link_tag/click_log lost
// their ON DELETE CASCADE FK to link(id) when those tables were
// polymorphized, so insertLinkInTx's wipeFirst DELETE FROM link alone would
// silently leave dangling link_tag/click_log rows behind unless the import
// path purges them itself before re-inserting the replacement row.
func TestInsertLinkInTx_WipeFirstDoesNotOrphanTagsOrClicks(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	lrepo := links.NewRepository(pool)
	trepo := tags.NewRepository(pool)

	tag, err := trepo.Create(ctx, tags.CreateInput{Name: "work", Color: "#fff"})
	require.NoError(t, err)
	original, err := lrepo.Create(ctx, links.CreateInput{
		URL: "https://wipe-target.example", Title: "Original", TagIDs: []int64{tag.ID},
	})
	require.NoError(t, err)
	_, err = lrepo.ClickAndResolve(ctx, original.ID)
	require.NoError(t, err)

	newID, dup, wiped, err := insertLinkIfNew(ctx, pool, "https://wipe-target.example", "Replacement", nil, nil, nil, 0, nil, true)
	require.NoError(t, err)
	assert.False(t, dup)
	assert.True(t, wiped)
	assert.NotEqual(t, original.ID, newID, "wipeFirst must replace with a fresh row, not reuse the old id")

	var tagRows, clickRows int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM link_tag WHERE entity_kind = 'link' AND entity_id = $1`, original.ID).Scan(&tagRows))
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM click_log WHERE entity_kind = 'link' AND entity_id = $1`, original.ID).Scan(&clickRows))
	assert.EqualValues(t, 0, tagRows, "wipeFirst must not leave an orphaned link_tag row for the replaced link")
	assert.EqualValues(t, 0, clickRows, "wipeFirst must not leave an orphaned click_log row for the replaced link")
}
