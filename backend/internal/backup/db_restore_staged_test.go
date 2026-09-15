package backup

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRestoreModesShareFolderAndNoteInsertSQL is DUP-ECH-006: skip and
// duplicate used to copy the folder and note INSERT statements. One helper
// each is the lock; a second literal is a drift waiting to happen.
func TestRestoreModesShareFolderAndNoteInsertSQL(t *testing.T) {
	folderSQL, noteSQL, calls := restoreInsertSQLCharter(t)
	require.Equal(t, 1, folderSQL,
		"AST/text finds duplicated INSERT INTO folder in two restore modes")
	require.Equal(t, 1, noteSQL,
		"AST/text finds duplicated INSERT INTO note in two restore modes")

	require.Greater(t, calls["restoreSkipStaged"]["insertStagedFolders"], 0,
		"restoreSkipStaged must call insertStagedFolders")
	require.Greater(t, calls["restoreSkipStaged"]["insertStagedNotes"], 0,
		"restoreSkipStaged must call insertStagedNotes")
	require.Greater(t, calls["insertDuplicateRestore"]["insertStagedFolders"], 0,
		"insertDuplicateRestore must call insertStagedFolders")
	require.Greater(t, calls["insertDuplicateRestore"]["insertStagedNotes"], 0,
		"insertDuplicateRestore must call insertStagedNotes")
	require.Greater(t, calls["restoreDuplicateStaged"]["insertDuplicateRestore"], 0,
		"restoreDuplicateStaged must call insertDuplicateRestore")
}
