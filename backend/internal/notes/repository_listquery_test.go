package notes

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

func TestBuildNoteListQuery_ClickSortUsesStatsProjection(t *testing.T) {
	t.Parallel()
	for _, sort := range []string{"clicks", "recent"} {
		sql, _ := buildListQuery(authctx.UserID(42), ListQuery{Sort: sort, Limit: 7})
		assert.Contains(t, sql, "entity_click_stats", sort)
		assert.NotContains(t, sql, "FROM click_log", sort)
	}
}

func TestBuildNoteListQuery_OrdinarySortBoundsClickAggregationToPage(t *testing.T) {
	t.Parallel()
	for _, sort := range []string{"", "alpha", "alpha_desc"} {
		sql, _ := buildListQuery(authctx.UserID(42), ListQuery{Sort: sort, Limit: 7, Offset: 11})
		limitIdx := strings.Index(sql, "LIMIT")
		lateralIdx := strings.Index(sql, "LATERAL")
		require.Positive(t, limitIdx, sort)
		require.Positive(t, lateralIdx, sort)
		assert.Less(t, limitIdx, lateralIdx,
			"sort %q must paginate before aggregating clicks: %s", sort, sql)
		assert.Contains(t, sql, "user_id = n.user_id", sort)
	}
}
