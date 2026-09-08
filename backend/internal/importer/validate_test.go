package importer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyConflicts_DuplicateURLCountsEveryFolder(t *testing.T) {
	t.Parallel()
	work := "Work"
	home := "Home"
	rep, agg := aggregateItems([]Item{
		{URL: "https://dup.example", Title: "A", Folder: &work},
		{URL: "https://dup.example", Title: "B", Folder: &home},
	})
	require.Equal(t, 2, rep.Counts.Links)
	require.Equal(t, 1, agg.folderAggregates["Work"].Count)
	require.Equal(t, 1, agg.folderAggregates["Home"].Count)

	applyConflicts(&rep, &agg, []string{"https://dup.example"})
	assert.Equal(t, 1, rep.Conflicts.Links)
	assert.Equal(t, 1, agg.folderAggregates["Work"].Conflicts, "last-write-wins must not drop the first folder")
	assert.Equal(t, 1, agg.folderAggregates["Home"].Conflicts)
}
