//go:build integration

package stats_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/links"
	"foldex/internal/stats"
	"foldex/internal/tags"
	"foldex/internal/testdb"
)

func setup(t *testing.T) (context.Context, *stats.Repository, *links.Repository, *tags.Repository) {
	t.Helper()
	pool := testdb.New(t)
	return context.Background(),
		stats.NewRepository(pool),
		links.NewRepository(pool),
		tags.NewRepository(pool)
}

func TestSummary_EmptyDB(t *testing.T) {
	ctx, srepo, _, _ := setup(t)
	s, err := srepo.Summary(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 0, s.TotalLinks)
	assert.EqualValues(t, 0, s.TotalTags)
	assert.EqualValues(t, 0, s.TotalClicks)
}

func TestSummary_AfterClicks(t *testing.T) {
	ctx, srepo, lrepo, trepo := setup(t)
	tagX, err := trepo.Create(ctx, tags.CreateInput{Name: "x", Color: "#fff"})
	require.NoError(t, err)
	link, err := lrepo.Create(ctx, links.CreateInput{URL: "https://example.com", Title: "ex", TagIDs: []int64{tagX.ID}})
	require.NoError(t, err)

	// 3 clicks → click_log has 3 rows.
	for i := 0; i < 3; i++ {
		_, err := lrepo.ClickAndResolve(ctx, link.ID)
		require.NoError(t, err)
	}

	s, err := srepo.Summary(ctx)
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
	ctx, srepo, lrepo, _ := setup(t)
	link, _ := lrepo.Create(ctx, links.CreateInput{URL: "https://a", Title: "a"})
	_, _ = lrepo.ClickAndResolve(ctx, link.ID)

	out, err := srepo.Daily(ctx, 7)
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
	ctx, srepo, _, _ := setup(t)
	out, err := srepo.Daily(ctx, 0) // 0 → default 60
	require.NoError(t, err)
	assert.Len(t, out, 60)

	out, err = srepo.Daily(ctx, 1000) // > 365 → default 60
	require.NoError(t, err)
	assert.Len(t, out, 60)
}

func TestTopLinks_OrdersByClicks(t *testing.T) {
	ctx, srepo, lrepo, _ := setup(t)
	a, _ := lrepo.Create(ctx, links.CreateInput{URL: "https://a", Title: "A"})
	b, _ := lrepo.Create(ctx, links.CreateInput{URL: "https://b", Title: "B"})
	c, _ := lrepo.Create(ctx, links.CreateInput{URL: "https://c", Title: "C"})

	for i := 0; i < 3; i++ {
		_, _ = lrepo.ClickAndResolve(ctx, b.ID)
	}
	for i := 0; i < 2; i++ {
		_, _ = lrepo.ClickAndResolve(ctx, a.ID)
	}
	_, _ = lrepo.ClickAndResolve(ctx, c.ID)

	out, err := srepo.TopLinks(ctx, 10)
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
	ctx, srepo, lrepo, _ := setup(t)
	for i := 0; i < 3; i++ {
		_, err := lrepo.Create(ctx, links.CreateInput{URL: fmt.Sprintf("https://x-%d", i), Title: "x"})
		require.NoError(t, err)
	}
	out, err := srepo.TopLinks(ctx, 0) // 0 → default 10
	require.NoError(t, err)
	assert.Len(t, out, 3)
}

func TestTagBuckets_AggregatesClicks(t *testing.T) {
	ctx, srepo, lrepo, trepo := setup(t)
	t1, _ := trepo.Create(ctx, tags.CreateInput{Name: "alpha", Color: "#a"})
	t2, _ := trepo.Create(ctx, tags.CreateInput{Name: "beta", Color: "#b"})

	la, _ := lrepo.Create(ctx, links.CreateInput{URL: "https://a", Title: "A", TagIDs: []int64{t1.ID}})
	lb, _ := lrepo.Create(ctx, links.CreateInput{URL: "https://b", Title: "B", TagIDs: []int64{t1.ID, t2.ID}})
	for i := 0; i < 5; i++ {
		_, _ = lrepo.ClickAndResolve(ctx, la.ID)
	}
	for i := 0; i < 2; i++ {
		_, _ = lrepo.ClickAndResolve(ctx, lb.ID)
	}

	out, err := srepo.TagBuckets(ctx)
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
