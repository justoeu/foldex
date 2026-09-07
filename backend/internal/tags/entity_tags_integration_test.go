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

// SetEntityTags is the polymorphic replace primitive every tag write funnels
// through; these pin its serial contract (full-set replacement, clearing,
// ownership rejection). The interleaved races on top of it live in
// links/tags_race_integration_test.go.
func TestSetEntityTags_ReplacesTheWholeSet(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	uid := testdb.SeedUser(t, pool, "setentitytags-owner@test.local", "admin")
	trepo := tags.NewRepository(pool)
	lrepo := links.NewRepository(pool)

	mk := func(name string) int64 {
		tag, err := trepo.Create(ctx, uid, tags.CreateInput{Name: name, Color: "#abc"})
		require.NoError(t, err)
		return tag.ID
	}
	t1, t2, t3 := mk("alpha"), mk("beta"), mk("gamma")

	link, err := lrepo.Create(ctx, uid, links.CreateInput{
		URL: "https://setentitytags.test/a", Title: "a", TagIDs: []int64{t1, t2},
	})
	require.NoError(t, err)

	replace := func(ids []int64) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(ctx) }()
		require.NoError(t, tags.SetEntityTags(ctx, tx, uid, "link", link.ID, ids))
		require.NoError(t, tx.Commit(ctx))
	}
	idsFor := func() []int64 {
		t.Helper()
		rows, err := pool.Query(ctx,
			`SELECT tag_id FROM link_tag WHERE entity_kind = 'link' AND entity_id = $1 ORDER BY tag_id`, link.ID)
		require.NoError(t, err)
		defer rows.Close()
		var out []int64
		for rows.Next() {
			var id int64
			require.NoError(t, rows.Scan(&id))
			out = append(out, id)
		}
		require.NoError(t, rows.Err())
		return out
	}

	replace([]int64{t2, t3})
	assert.Equal(t, []int64{t2, t3}, idsFor(), "replacement must drop tags absent from the new set")

	replace(nil)
	assert.Empty(t, idsFor(), "an empty set clears every row")

	// A foreign tag id is rejected wholesale — no partial application.
	other := testdb.SeedUser(t, pool, "setentitytags-other@test.local", "editor")
	foreign, err := trepo.Create(ctx, other, tags.CreateInput{Name: "foreign", Color: "#abc"})
	require.NoError(t, err)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	err = tags.SetEntityTags(ctx, tx, uid, "link", link.ID, []int64{t1, foreign.ID})
	assert.ErrorIs(t, err, domainerr.ErrInvalidInput)
	require.NoError(t, tx.Rollback(ctx))
	assert.Empty(t, idsFor(), "a rejected replacement must not partially apply")
}
