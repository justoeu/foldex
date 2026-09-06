package clicklog

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecord_MaintainsEntityClickStatsProjection(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("clicklog.go")
	require.NoError(t, err)
	body := string(src)

	require.Contains(t, body, "INSERT INTO entity_click_stats",
		"Record is the single production click writer; it must UPSERT the ranking projection")
	require.Contains(t, body, "ON CONFLICT")
	require.Contains(t, body, "click_count")
	require.Contains(t, body, "last_clicked_at")

	allowAt := strings.Index(body, "clickctx.Allow")
	statsAt := strings.Index(body, "INSERT INTO entity_click_stats")
	require.Greater(t, allowAt, 0)
	require.Greater(t, statsAt, allowAt,
		"the stats UPSERT must sit inside the same Allow gate as the click_log INSERT")
}
