package entries

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

func TestBuildListQuery_PageBoundsOrdinarySortClickAggregation(t *testing.T) {
	t.Parallel()
	for _, sort := range []string{"", "created", "alpha", "alpha_desc"} {
		t.Run(sort, func(t *testing.T) {
			sql, args := buildListQuery(authctx.UserID(42), ListQuery{Sort: sort, Limit: 7, Offset: 11})

			candidateEnd := strings.Index(sql, "), page_clicks AS (")
			require.Positive(t, candidateEnd)
			require.Contains(t, sql[:candidateEnd], "WITH candidates AS MATERIALIZED")
			require.Contains(t, sql[:candidateEnd], "LIMIT $3 OFFSET $4")
			require.NotContains(t, sql[:candidateEnd], "click_log")
			require.Contains(t, sql[candidateEnd:], "ARRAY(SELECT c.id FROM candidates c WHERE c.kind = 'link')")
			require.Contains(t, sql[candidateEnd:], "ARRAY(SELECT c.id FROM candidates c WHERE c.kind = 'note')")
			require.NotContains(t, sql, "LATERAL")
			require.Equal(t, 2, strings.Count(sql, "FROM click_log cl"))
			require.Equal(t, []any{int64(42), int64(42), 7, 11}, args)
		})
	}
}

func TestBuildListQuery_PreAggregatesClickRankedSorts(t *testing.T) {
	t.Parallel()
	for _, sort := range []string{"clicks", "recent"} {
		t.Run(sort, func(t *testing.T) {
			sql, args := buildListQuery(authctx.UserID(9), ListQuery{Sort: sort, Limit: 5})

			require.NotContains(t, sql, "WITH candidates")
			require.NotContains(t, sql, "LATERAL")
			require.Equal(t, 2, strings.Count(sql, "FROM click_log WHERE user_id"))
			require.Less(t, strings.Index(sql, "FROM click_log"), strings.Index(sql, "LIMIT $3"))
			require.Equal(t, []any{int64(9), int64(9), 5, 0}, args)
		})
	}
}

func TestBuildListQuery_KeepsRequestValuesParameterized(t *testing.T) {
	t.Parallel()
	payload := `x%' OR true; SELECT pg_sleep(10); --`
	sql, args := buildListQuery(authctx.UserID(7), ListQuery{Q: payload, Sort: payload})

	require.NotContains(t, sql, payload)
	require.Contains(t, sql, "l.user_id = $1")
	require.Contains(t, sql, "n.user_id = $3")
	require.Equal(t, []any{int64(7), "%" + payload + "%", int64(7), "%" + payload + "%", 100, 0}, args)
}
