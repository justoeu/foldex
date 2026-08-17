//go:build integration

package stats_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/folders"
	"foldex/internal/links"
	"foldex/internal/stats"
	"foldex/internal/tags"
	"foldex/internal/testdb"

	"foldex/internal/pkg/authctx"
)

func setup(t *testing.T) (context.Context, authctx.UserID, *stats.Repository, *links.Repository, *tags.Repository) {
	t.Helper()
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	return context.Background(), uid,
		stats.NewRepository(pool),
		links.NewRepository(pool),
		tags.NewRepository(pool)
}

func TestSummary_EmptyDB(t *testing.T) {
	ctx, uid, srepo, _, _ := setup(t)
	s, err := srepo.Summary(ctx, uid)
	require.NoError(t, err)
	assert.EqualValues(t, 0, s.TotalLinks)
	assert.EqualValues(t, 0, s.TotalTags)
	assert.EqualValues(t, 0, s.TotalClicks)
}

func TestSummary_AfterClicks(t *testing.T) {
	ctx, uid, srepo, lrepo, trepo := setup(t)
	tagX, err := trepo.Create(ctx, uid, tags.CreateInput{Name: "x", Color: "#fff"})
	require.NoError(t, err)
	link, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://example.com", Title: "ex", TagIDs: []int64{tagX.ID}})
	require.NoError(t, err)

	// 3 clicks → click_log has 3 rows.
	for i := 0; i < 3; i++ {
		_, err := lrepo.ClickAndResolve(ctx, link.ID)
		require.NoError(t, err)
	}

	s, err := srepo.Summary(ctx, uid)
	require.NoError(t, err)
	assert.EqualValues(t, 1, s.TotalLinks)
	assert.EqualValues(t, 1, s.TotalTags)
	assert.EqualValues(t, 3, s.TotalClicks)
	assert.EqualValues(t, 3, s.ClicksLast30d)
	assert.EqualValues(t, 0, s.ClicksPrev30d)
	assert.EqualValues(t, 1, s.NewLinksLast30)
	assert.Equal(t, "example.com", s.TopHost)
}

func TestDaily_BackfillsEmptyDays(t *testing.T) {
	ctx, uid, srepo, lrepo, _ := setup(t)
	link, _ := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://a", Title: "a"})
	_, _ = lrepo.ClickAndResolve(ctx, link.ID)

	out, err := srepo.Daily(ctx, uid, 7)
	require.NoError(t, err)
	require.Len(t, out, 7, "must emit one bucket per day even with zero clicks")

	// All dates ascending, exactly one day apart.
	for i := 1; i < len(out); i++ {
		gap := out[i].Date.Sub(out[i-1].Date)
		assert.Equal(t, 24*time.Hour, gap, "buckets must be 1 day apart")
	}
	// The most recent bucket should contain at least 1 click (we just inserted).
	last := out[len(out)-1]
	assert.GreaterOrEqual(t, last.Clicks, int64(1))
}

func TestDaily_ClampsBadInput(t *testing.T) {
	ctx, uid, srepo, _, _ := setup(t)
	out, err := srepo.Daily(ctx, uid, 0) // 0 → default 60
	require.NoError(t, err)
	assert.Len(t, out, 60)

	out, err = srepo.Daily(ctx, uid, 1000) // > 365 → default 60
	require.NoError(t, err)
	assert.Len(t, out, 60)
}

func TestTopLinks_OrdersByClicks(t *testing.T) {
	ctx, uid, srepo, lrepo, _ := setup(t)
	a, _ := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://a", Title: "A"})
	b, _ := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://b", Title: "B"})
	c, _ := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://c", Title: "C"})

	for i := 0; i < 3; i++ {
		_, _ = lrepo.ClickAndResolve(ctx, b.ID)
	}
	for i := 0; i < 2; i++ {
		_, _ = lrepo.ClickAndResolve(ctx, a.ID)
	}
	_, _ = lrepo.ClickAndResolve(ctx, c.ID)

	out, err := srepo.TopLinks(ctx, uid, 10)
	require.NoError(t, err)
	require.Len(t, out, 3)
	assert.Equal(t, "B", out[0].Title)
	assert.Equal(t, int64(3), out[0].Clicks)
	assert.Equal(t, int64(3), out[0].Clicks30d, "all clicks just inserted → in 30d window")
	assert.Equal(t, "b", out[0].Host)
	assert.Equal(t, "A", out[1].Title)
	assert.Equal(t, "C", out[2].Title)
}

func TestTopLinks_ClampsBadLimit(t *testing.T) {
	ctx, uid, srepo, lrepo, _ := setup(t)
	for i := 0; i < 3; i++ {
		_, err := lrepo.Create(ctx, uid, links.CreateInput{URL: fmt.Sprintf("https://x-%d", i), Title: "x"})
		require.NoError(t, err)
	}
	out, err := srepo.TopLinks(ctx, uid, 0) // 0 → default 10
	require.NoError(t, err)
	assert.Len(t, out, 3)
}

func TestTagBuckets_AggregatesClicks(t *testing.T) {
	ctx, uid, srepo, lrepo, trepo := setup(t)
	t1, _ := trepo.Create(ctx, uid, tags.CreateInput{Name: "alpha", Color: "#a"})
	t2, _ := trepo.Create(ctx, uid, tags.CreateInput{Name: "beta", Color: "#b"})

	la, _ := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://a", Title: "A", TagIDs: []int64{t1.ID}})
	lb, _ := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://b", Title: "B", TagIDs: []int64{t1.ID, t2.ID}})
	for i := 0; i < 5; i++ {
		_, _ = lrepo.ClickAndResolve(ctx, la.ID)
	}
	for i := 0; i < 2; i++ {
		_, _ = lrepo.ClickAndResolve(ctx, lb.ID)
	}

	out, err := srepo.TagBuckets(ctx, uid)
	require.NoError(t, err)
	require.Len(t, out, 2)

	// alpha has both links (5+2=7), beta has just lb (2).
	byName := map[string]int64{}
	links := map[string]int64{}
	for _, b := range out {
		byName[b.Name] = b.Clicks
		links[b.Name] = b.Links
	}
	assert.EqualValues(t, 7, byName["alpha"])
	assert.EqualValues(t, 2, byName["beta"])
	assert.EqualValues(t, 2, links["alpha"])
	assert.EqualValues(t, 1, links["beta"])
}

func TestDashboard_IsOwnerScopedAndUsesOneDatabaseRoundTrip(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	otherUID := testdb.SeedUser(t, pool, "other@test.local", "editor")
	lrepo := links.NewRepository(pool)
	trepo := tags.NewRepository(pool)
	tag, err := trepo.Create(ctx, uid, tags.CreateInput{Name: "owner-tag", Color: "#abc"})
	require.NoError(t, err)
	ownerLink, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://owner.example", Title: "Owner", TagIDs: []int64{tag.ID}})
	require.NoError(t, err)
	password := "folder-password"
	protectedFolder, err := folders.NewRepository(pool).Create(ctx, uid, folders.CreateInput{Name: "Protected", Color: "#def", Password: &password})
	require.NoError(t, err)
	protectedLink, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://protected.example", Title: "Protected", FolderID: &protectedFolder.ID, TagIDs: []int64{tag.ID}})
	require.NoError(t, err)
	otherLink, err := lrepo.Create(ctx, otherUID, links.CreateInput{URL: "https://other.example", Title: "Other"})
	require.NoError(t, err)
	for range 2 {
		_, err = lrepo.ClickAndResolve(ctx, ownerLink.ID)
		require.NoError(t, err)
	}
	for range 3 {
		_, err = lrepo.ClickAndResolve(ctx, otherLink.ID)
		require.NoError(t, err)
	}
	// The entity join succeeds, but the denormalized owner must still exclude
	// these rows from the requested tenant's click snapshot.
	_, err = pool.Exec(ctx, `
		INSERT INTO click_log (entity_kind, entity_id, user_id)
		SELECT 'link', $1, $2 FROM generate_series(1, 5)
	`, ownerLink.ID, int64(otherUID))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO click_log (entity_kind, entity_id, user_id)
		SELECT 'link', $1, $2 FROM generate_series(1, 4)
	`, protectedLink.ID, int64(uid))
	require.NoError(t, err)
	// The owner predicate succeeds, but the owned-link join must discard an
	// orphan whose polymorphic target no longer exists.
	_, err = pool.Exec(ctx, `
		INSERT INTO click_log (entity_kind, entity_id, user_id)
		VALUES ('link', 9223372036854775807, $1)
	`, int64(uid))
	require.NoError(t, err)

	repo := stats.NewRepository(pool)
	before := pool.Stat().AcquireCount()
	dashboard, err := repo.Dashboard(ctx, uid, 7, 5)
	require.NoError(t, err)
	require.EqualValues(t, 1, pool.Stat().AcquireCount()-before, "dashboard must execute as one database round trip")
	assert.EqualValues(t, 1, dashboard.Summary.TotalLinks)
	assert.EqualValues(t, 2, dashboard.Summary.TotalClicks)
	assert.Equal(t, "owner.example", dashboard.Summary.TopHost)
	require.Len(t, dashboard.Daily, 7)
	var dailyClicks int64
	for _, point := range dashboard.Daily {
		dailyClicks += point.Clicks
	}
	assert.EqualValues(t, 2, dailyClicks)
	require.Len(t, dashboard.Top, 1)
	assert.Equal(t, ownerLink.ID, dashboard.Top[0].ID)
	require.Len(t, dashboard.Tags, 1)
	assert.EqualValues(t, 2, dashboard.Tags[0].Clicks)

	summary, err := repo.Summary(ctx, uid)
	require.NoError(t, err)
	assert.EqualValues(t, 1, summary.TotalLinks)
	assert.EqualValues(t, 2, summary.TotalClicks)
	assert.Equal(t, "owner.example", summary.TopHost)
	daily, err := repo.Daily(ctx, uid, 7)
	require.NoError(t, err)
	var legacyDailyClicks int64
	for _, point := range daily {
		legacyDailyClicks += point.Clicks
	}
	assert.EqualValues(t, 2, legacyDailyClicks)
	top, err := repo.TopLinks(ctx, uid, 5)
	require.NoError(t, err)
	require.Len(t, top, 1)
	assert.Equal(t, ownerLink.ID, top[0].ID)
	buckets, err := repo.TagBuckets(ctx, uid)
	require.NoError(t, err)
	require.Len(t, buckets, 1)
	assert.EqualValues(t, 2, buckets[0].Clicks)
	assert.EqualValues(t, 1, buckets[0].Links)
}

func TestDashboard_BaseClicksCanUseOwnerEntityIndex(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	lrepo := links.NewRepository(pool)
	link, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://owner.example", Title: "Owner"})
	require.NoError(t, err)
	_, err = lrepo.ClickAndResolve(ctx, link.ID)
	require.NoError(t, err)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	_, err = tx.Exec(ctx, `SET LOCAL enable_seqscan = off`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `SET LOCAL enable_nestloop = off`)
	require.NoError(t, err)
	rows, err := tx.Query(ctx, `
		EXPLAIN (COSTS OFF)
		WITH owned_links AS MATERIALIZED (
			SELECT l.id
			FROM link l
			WHERE l.user_id = $1
		)
		SELECT c.entity_id, c.clicked_at
		FROM click_log c
		JOIN owned_links l ON l.id = c.entity_id
		WHERE c.user_id = $1
		  AND c.entity_kind = 'link'
	`, int64(uid))
	require.NoError(t, err)
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	require.NoError(t, rows.Err())
	assert.Contains(t, plan.String(), "click_log_user_entity_idx")
}
