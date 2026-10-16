package backup

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

func TestRestoreSlugBase(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "kept", restoreSlugBase("kept", "Hello", "fb"))
	assert.NotEmpty(t, restoreSlugBase("", "Hello World", "fb"))
	assert.Equal(t, "fb", restoreSlugBase("", "!!!", "fb"))
}

func TestReserveRestoreIDs_EmptyAndUnknown(t *testing.T) {
	t.Parallel()
	ids, err := reserveRestoreIDs(context.Background(), failRestoreTx{}, "tag_id_seq", 0)
	require.NoError(t, err)
	assert.Nil(t, ids)

	_, err = reserveRestoreIDs(context.Background(), failRestoreTx{}, "nope_seq", 3)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown sequence")

	_, err = reserveRestoreIDs(context.Background(), failRestoreTx{}, "tag_id_seq", 2)
	require.Error(t, err)
}

func TestCopyRestore_EmptySlicesAndTxFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tx := failRestoreTx{}
	require.NoError(t, copyRestoreTags(ctx, tx, nil, nil, nil))
	_, foldersErr := copyRestoreFolders(ctx, tx, nil, nil)
	require.NoError(t, foldersErr)
	require.NoError(t, copyRestoreLinks(ctx, tx, nil, nil, nil))
	require.NoError(t, copyRestoreNotes(ctx, tx, nil, nil, nil))

	require.Error(t, copyRestoreTags(ctx, tx, []TagRow{{ID: 1, Name: "a"}}, []string{"a"}, []int64{1}))
	_, foldersErr = copyRestoreFolders(ctx, tx, []FolderRow{{ID: 1, Name: "f"}}, []int64{1})
	require.Error(t, foldersErr)
	require.Error(t, copyRestoreLinks(ctx, tx, []LinkRow{{ID: 1, URL: "https://x", Title: "t"}}, []string{"s"}, []int64{1}))
	require.Error(t, copyRestoreNotes(ctx, tx, []NoteRow{{ID: 1, Title: "n", BodyHTML: "<p>x</p>"}}, []string{"s"}, []int64{1}))
	require.Error(t, createRestoreStagingTables(ctx, tx))
	require.Error(t, loadStagedIDMapping(ctx, tx, "select 1", map[int64]int64{}))
}

func TestNormalizeRestoreFolderParents_TreeDuplicateAndCycle(t *testing.T) {
	t.Parallel()
	root := int64(1)
	childParent := int64(2)
	folders := []FolderRow{
		{ID: 1, Name: "root"},
		{ID: 2, Name: "child", ParentID: &root},
		{ID: 2, Name: "dup"},
		{ID: 3, Name: "orphan", ParentID: ptrInt64(99)},
	}
	parents, warnings := normalizeRestoreFolderParents(folders)
	require.Len(t, parents, 4)
	assert.Nil(t, parents[0])
	require.NotNil(t, parents[1])
	assert.EqualValues(t, 1, *parents[1])
	assert.Nil(t, parents[3], "unknown parent is dropped")
	assert.NotEmpty(t, warnings)

	a, b := int64(2), int64(1)
	cycled := []FolderRow{
		{ID: 1, Name: "a", ParentID: &a},
		{ID: 2, Name: "b", ParentID: &b},
	}
	cycledParents, _ := normalizeRestoreFolderParents(cycled)
	assert.Nil(t, cycledParents[0])
	assert.Nil(t, cycledParents[1])
	_ = childParent
}

type countingCopyTx struct {
	seqRestoreTx
	rows [][]any
}

func (c *countingCopyTx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, src pgx.CopyFromSource) (int64, error) {
	for src.Next() {
		values, err := src.Values()
		if err != nil {
			return 0, err
		}
		c.rows = append(c.rows, values)
	}
	return int64(len(c.rows)), src.Err()
}

// A hand-edited archive repeating a folder id must stage the folder once —
// twice violates the staging PK and aborts the whole restore.
func TestCopyRestoreFolders_SkipsDuplicateFolderIDs(t *testing.T) {
	t.Parallel()
	tx := &countingCopyTx{}
	folders := []FolderRow{
		{ID: 5, Name: "first"},
		{ID: 5, Name: "duplicate"},
		{ID: 6, Name: "other"},
	}
	warnings, err := copyRestoreFolders(context.Background(), tx, folders, []int64{50, 51, 60})
	require.NoError(t, err)
	require.Len(t, tx.rows, 2)
	require.Equal(t, "first", tx.rows[0][3], "the first occurrence wins")
	require.NotEmpty(t, warnings, "the duplicate is surfaced, not silenced")
}

func TestCopyRestoreStaging_FailsWhenReserveFails(t *testing.T) {
	t.Parallel()
	_, err := copyRestoreStaging(context.Background(), failRestoreTx{}, &Snapshot{Tags: []TagRow{{ID: 1}}}, []string{"a"}, nil, nil)
	require.Error(t, err)
}

func TestRestoreSkipAndDuplicate_FailClosedOnBadTx(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	uid := authctx.UserID(1)
	snap := &Snapshot{Tags: []TagRow{{ID: 1, Name: "t"}}}
	_, _, _, _, err := restoreSkipStaged(ctx, failRestoreTx{}, uid, snap)
	require.Error(t, err)
	_, _, _, err = restoreDuplicateStaged(ctx, failRestoreTx{}, uid, snap)
	require.Error(t, err)
}

func TestRestoreStaged_WalkEachTxStep(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	uid := authctx.UserID(1)
	snap := &Snapshot{
		Tags:    []TagRow{{ID: 1, Name: "t", Color: "#abc"}},
		Folders: []FolderRow{{ID: 2, Name: "f"}},
		Links:   []LinkRow{{ID: 3, URL: "https://ex.test", Title: "L", Slug: "l"}},
		Notes:   []NoteRow{{ID: 4, Title: "N", Slug: "n", BodyHTML: "<p>x</p>"}},
	}
	for n := 1; n <= 16; n++ {
		_, _, _, _, _ = restoreSkipStaged(ctx, &seqRestoreTx{failAt: n}, uid, snap)
		_, _, _, _ = restoreDuplicateStaged(ctx, &seqRestoreTx{failAt: n}, uid, snap)
		_, _ = copyRestoreStaging(ctx, &seqRestoreTx{failAt: n}, snap, []string{"a"}, []string{"l"}, []string{"n"})
		_, _ = insertStagedFolders(ctx, &seqRestoreTx{failAt: n}, uid, newIDMapping())
		_, _ = loadExistingRestoreLinks(ctx, &seqRestoreTx{failAt: n}, uid, snap.Links)
	}
}

func ptrInt64(v int64) *int64 { return &v }
