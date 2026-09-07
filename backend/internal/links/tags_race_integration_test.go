//go:build integration

package links_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/links"
	"foldex/internal/pkg/domainerr"
	"foldex/internal/tags"
	"foldex/internal/testdb"
)

func waitForBlockedLockCount(t *testing.T, pool *pgxpool.Pool, want int) {
	t.Helper()
	require.Eventually(t, func() bool {
		var blocked int
		err := pool.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&blocked)
		return err == nil && blocked >= want
	}, 3*time.Second, 10*time.Millisecond,
		"%d queries did not block on the expected row locks", want)
}

func tagIDsFor(t *testing.T, pool *pgxpool.Pool, kind string, entityID int64) []int64 {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT tag_id FROM link_tag WHERE entity_kind = $1 AND entity_id = $2 ORDER BY tag_id`, kind, entityID)
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

// RACE-HER-001: two tag-only PATCHes on the same entity used to run their
// DELETE+CopyFrom without any parent-row lock. The loser's statement snapshot
// predating the winner's inserts, its CopyFrom hit the link_tag PK and the
// request surfaced as an unmapped 500 instead of a clean last-writer-wins.
func TestUpdate_ConcurrentTagPatches_SerializeOnTheParentRow(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	uid := testdb.SeedUser(t, pool, "tagrace-owner@test.local", "admin")
	lrepo := links.NewRepository(pool)
	trepo := tags.NewRepository(pool)

	mk := func(name string) int64 {
		tag, err := trepo.Create(ctx, uid, tags.CreateInput{Name: name, Color: "#abc"})
		require.NoError(t, err)
		return tag.ID
	}
	t1, t2, t3 := mk("alpha"), mk("beta"), mk("gamma")

	link, err := lrepo.Create(ctx, uid, links.CreateInput{
		URL: "https://tagrace.test/a", Title: "a", TagIDs: []int64{t1, t2},
	})
	require.NoError(t, err)

	// Hold the existing link_tag rows so both PATCHes park mid-transaction
	// before either commits.
	blocker, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(ctx) }()
	_, err = blocker.Exec(ctx,
		`SELECT 1 FROM link_tag WHERE entity_kind = 'link' AND entity_id = $1 FOR UPDATE`, link.ID)
	require.NoError(t, err)

	setA := []int64{t1, t3}
	setB := []int64{t2, t3}
	resA := make(chan error, 1)
	resB := make(chan error, 1)
	go func() {
		_, err := lrepo.Update(ctx, uid, link.ID, links.UpdateInput{TagIDs: &setA})
		resA <- err
	}()
	go func() {
		_, err := lrepo.Update(ctx, uid, link.ID, links.UpdateInput{TagIDs: &setB})
		resB <- err
	}()
	waitForBlockedLockCount(t, pool, 2)

	require.NoError(t, blocker.Commit(ctx))
	assert.NoError(t, <-resA, "the losing PATCH must not surface a raw unique-violation (unmapped 500)")
	assert.NoError(t, <-resB, "the losing PATCH must not surface a raw unique-violation (unmapped 500)")

	final := tagIDsFor(t, pool, "link", link.ID)
	require.Len(t, final, 2, "exactly one writer's full set may survive, no duplicates")
	if final[0] == t1 {
		assert.ElementsMatch(t, []int64{t1, t3}, final)
	} else {
		assert.ElementsMatch(t, []int64{t2, t3}, final)
	}
}

// RACE-HER-002: a tag-only PATCH interleaved between a delete's ownership
// check and its link_tag purge could insert rows for the now-dead link id —
// link_tag has had no FK since the 000014 polymorphization and nothing swept
// the orphans.
func TestDelete_RacingTagPatch_LeavesNoOrphanTagRows(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	uid := testdb.SeedUser(t, pool, "tagdel-owner@test.local", "admin")
	lrepo := links.NewRepository(pool)
	trepo := tags.NewRepository(pool)

	mk := func(name string) int64 {
		tag, err := trepo.Create(ctx, uid, tags.CreateInput{Name: name, Color: "#abc"})
		require.NoError(t, err)
		return tag.ID
	}
	t1, t2, t3 := mk("one"), mk("two"), mk("three")

	link, err := lrepo.Create(ctx, uid, links.CreateInput{
		URL: "https://tagdel.test/a", Title: "a", TagIDs: []int64{t1, t2},
	})
	require.NoError(t, err)

	// Tempo-control: park the delete mid-transaction by holding the link row.
	blocker, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(ctx) }()
	_, err = blocker.Exec(ctx,
		`SELECT id FROM link WHERE user_id = $1 AND id = $2 FOR UPDATE`, int64(uid), link.ID)
	require.NoError(t, err)

	delRes := make(chan error, 1)
	go func() { delRes <- lrepo.Delete(ctx, uid, link.ID) }()
	waitForBlockedLockCount(t, pool, 1)

	setC := []int64{t3}
	patchRes := make(chan error, 1)
	go func() {
		_, err := lrepo.Update(ctx, uid, link.ID, links.UpdateInput{TagIDs: &setC})
		patchRes <- err
	}()
	waitForBlockedLockCount(t, pool, 2)

	require.NoError(t, blocker.Commit(ctx))
	require.NoError(t, <-delRes)
	// The PATCH lost the race either way; a 404 (not a 500, not orphans) is
	// the legitimate outcome.
	assert.ErrorIs(t, <-patchRes, domainerr.ErrNotFound)

	assert.Empty(t, tagIDsFor(t, pool, "link", link.ID),
		"a delete that raced a tag PATCH must not leave orphan link_tag rows behind")
}
