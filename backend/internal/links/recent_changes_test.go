package links

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListRecentChanges_SelectsOnlySidebarFields(t *testing.T) {
	sql := recentChangesSQL()
	for _, extra := range []string{
		"click_log",
		"last_fingerprint",
		"last_check_error",
		"og_image_url",
		"preview_status",
		"l.description",
		"favicon_url",
	} {
		assert.NotContainsf(t, sql, extra, "sidebar query must not select %s", extra)
	}
	require.Contains(t, sql, "l.id")
	require.Contains(t, sql, "l.title")
	require.Contains(t, sql, "l.slug")
	require.Contains(t, sql, "last_change_detected_at")
	require.Contains(t, sql, "l.user_id = $1")
}
