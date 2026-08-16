//go:build integration

package entries

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/links"
	"foldex/internal/notes"
	"foldex/internal/testdb"
)

func TestList_OrdinarySortPlanBoundsClickScanToCandidatePage(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	uid := testdb.SeedUser(t, pool, "page-plan@test.local", "admin")
	lrepo := links.NewRepository(pool)
	nrepo := notes.NewRepository(pool)

	history, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://history.example", Title: "Zulu history"})
	require.NoError(t, err)
	pageNote, err := nrepo.Create(ctx, uid, notes.CreateInput{Title: "Alpha page"})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO click_log (entity_kind, entity_id, user_id, clicked_at)
		SELECT 'link', $1, $2, now() - interval '1 day'
		FROM generate_series(1, 50000)
	`, history.ID, int64(uid))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO click_log (entity_kind, entity_id, user_id, clicked_at)
		SELECT 'note', $1, $2, now() - interval '1 hour'
		FROM generate_series(1, 3)
	`, pageNote.ID, int64(uid))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `ANALYZE click_log`)
	require.NoError(t, err)

	sql, args := buildListQuery(uid, ListQuery{Sort: "alpha", Limit: 1})
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `SET LOCAL enable_seqscan = off`)
	require.NoError(t, err)
	var raw json.RawMessage
	err = tx.QueryRow(ctx, "EXPLAIN (ANALYZE, COSTS OFF, FORMAT JSON) "+sql, args...).Scan(&raw)
	require.NoError(t, err)

	var plan []map[string]any
	require.NoError(t, json.Unmarshal(raw, &plan))
	require.Len(t, plan, 1)
	stats := clickPlanStats{}
	collectClickPlanStats(plan[0]["Plan"], &stats)
	executionMS, _ := plan[0]["Execution Time"].(float64)
	t.Logf("EXPLAIN read %.0f click rows through %d scan nodes in %.3f ms", stats.rowsRead, stats.scanNodes, executionMS)
	assert.True(t, stats.usesCandidateIDIndex, "click scan must be indexed by selected entity IDs: %s", raw)
	assert.True(t, stats.ownerScoped, "click scan must retain its user_id predicate: %s", raw)
	assert.False(t, stats.hasSequentialScan, "click_log must not be sequentially scanned: %s", raw)
	assert.Positive(t, stats.scanNodes)
	assert.LessOrEqual(t, stats.rowsRead, float64(3), "the 50,000 off-page clicks must not be read: %s", raw)

	out, err := NewRepository(pool).List(ctx, uid, ListQuery{Sort: "alpha", Limit: 1})
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "note", out[0].Kind)
	assert.Equal(t, pageNote.ID, out[0].ID)
	assert.EqualValues(t, 3, out[0].ClickCount)
}

type clickPlanStats struct {
	rowsRead             float64
	scanNodes            int
	usesCandidateIDIndex bool
	ownerScoped          bool
	hasSequentialScan    bool
}

func collectClickPlanStats(value any, stats *clickPlanStats) {
	node, ok := value.(map[string]any)
	if !ok {
		return
	}
	if node["Relation Name"] == "click_log" {
		stats.scanNodes++
		rows, _ := node["Actual Rows"].(float64)
		loops, _ := node["Actual Loops"].(float64)
		stats.rowsRead += rows * loops
		nodeType, _ := node["Node Type"].(string)
		indexCond, _ := node["Index Cond"].(string)
		filter, _ := node["Filter"].(string)
		stats.usesCandidateIDIndex = stats.usesCandidateIDIndex || strings.Contains(indexCond, "entity_id = ANY")
		stats.ownerScoped = stats.ownerScoped || strings.Contains(indexCond+filter, "user_id")
		stats.hasSequentialScan = stats.hasSequentialScan || nodeType == "Seq Scan"
	}
	children, _ := node["Plans"].([]any)
	for _, child := range children {
		collectClickPlanStats(child, stats)
	}
}
