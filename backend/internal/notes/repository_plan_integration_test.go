//go:build integration

package notes

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/testdb"
)

func countClickLogRowsReadNotes(t *testing.T, pool *pgxpool.Pool, sql string, args []any) float64 {
	t.Helper()
	var plan []map[string]any
	require.NoError(t, pool.QueryRow(context.Background(), "EXPLAIN (ANALYZE, FORMAT JSON) "+sql, args...).Scan(&plan))
	var total float64
	var walk func(map[string]any)
	walk = func(node map[string]any) {
		if rel, _ := node["Relation Name"].(string); rel == "click_log" {
			rows, _ := node["Actual Rows"].(float64)
			loops, _ := node["Actual Loops"].(float64)
			if loops == 0 {
				loops = 1
			}
			total += rows * loops
		}
		if kids, ok := node["Plans"].([]any); ok {
			for _, k := range kids {
				if m, ok := k.(map[string]any); ok {
					walk(m)
				}
			}
		}
	}
	for _, p := range plan {
		if top, ok := p["Plan"].(map[string]any); ok {
			walk(top)
		}
	}
	return total
}

func TestListPlanBoundsClicksOffPage(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	uid := testdb.SeedUser(t, pool, "plan@test.local", "admin")
	repo := NewRepository(pool)
	const total = 60
	ids := make([]int64, 0, total)
	for i := range total {
		n, err := repo.Create(ctx, uid, CreateInput{
			Title:    "N" + strings.Repeat("x", i+1),
			BodyHTML: "<p>x</p>",
		})
		require.NoError(t, err)
		ids = append(ids, n.ID)
	}
	for _, id := range ids {
		_, err := pool.Exec(ctx, `
			INSERT INTO click_log (user_id, entity_id, entity_kind, clicked_at)
			SELECT $1, $2, 'note', now() - (g * interval '1 minute')
			FROM generate_series(1, 50) AS g`, int64(uid), id)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `
			INSERT INTO entity_click_stats (user_id, entity_kind, entity_id, click_count, last_clicked_at)
			VALUES ($1, 'note', $2, 50, now())`, int64(uid), id)
		require.NoError(t, err)
	}
	_, err := pool.Exec(ctx, `ANALYZE click_log`)
	require.NoError(t, err)

	for _, sort := range []string{"", "alpha", "clicks"} {
		sql, args := buildListQuery(uid, ListQuery{Sort: sort, Limit: 5})
		rowsRead := countClickLogRowsReadNotes(t, pool, sql, args)
		assert.LessOrEqualf(t, rowsRead, float64(1000),
			"sort=%q read %.0f click_log rows; the corpus is 3000 and the page is 5", sort, rowsRead)
	}

	notes, err := repo.List(ctx, uid, ListQuery{Sort: "clicks", Limit: 5})
	require.NoError(t, err)
	require.Len(t, notes, 5)
	assert.EqualValues(t, 50, notes[0].ClickCount)
}
