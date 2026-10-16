package backup

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInsertStagedNotes_OKAndFailures(t *testing.T) {
	ctx := context.Background()

	mapping := newIDMapping()
	n, err := insertStagedNotes(ctx, newFakeTx(), testUID, mapping)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n) // fake Exec tag is INSERT 0 1

	tx := newFakeTx()
	tx.execErr = errRestoreForced
	_, err = insertStagedNotes(ctx, tx, testUID, mapping)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insert staged notes")

	tx2 := newFakeTx()
	tx2.queryErr = errRestoreForced
	_, err = insertStagedNotes(ctx, tx2, testUID, mapping)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "map staged notes")
}

func TestRestoreConflictLinks_SkipAndDuplicate(t *testing.T) {
	ctx := context.Background()
	links := []LinkRow{{ID: 1, URL: "https://a.test"}, {ID: 2, URL: "https://b.test"}}

	t.Run("skip counts skipped without warnings", func(t *testing.T) {
		mapping := newIDMapping()
		got, err := restoreConflictLinks(ctx, newFakeTx(), testUID, links, nil, mapping, ModeSkip)
		require.NoError(t, err)
		assert.EqualValues(t, 1, got.inserted) // fake tag
		assert.EqualValues(t, 1, got.skipped)  // 2 links - 1 inserted
		assert.Empty(t, got.warnings)
		assert.True(t, mapping.insertedLinks[0], "inserted set is authoritative even when empty")
	})

	t.Run("duplicate warns per pre-existing URL", func(t *testing.T) {
		existing := map[string]existingRestoreLink{"https://a.test": {slug: "a"}}
		got, err := restoreConflictLinks(ctx, newFakeTx(), testUID, links, existing, newIDMapping(), ModeDuplicate)
		require.NoError(t, err)
		require.Len(t, got.warnings, 1)
		assert.Contains(t, got.warnings[0], "https://a.test")
	})

	t.Run("insert failure is mode-labelled", func(t *testing.T) {
		_, err := restoreConflictLinks(ctx, failRestoreTx{}, testUID, links, nil, newIDMapping(), ModeDuplicate)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate")
		_, err = restoreConflictLinks(ctx, failRestoreTx{}, testUID, links, nil, newIDMapping(), ModeSkip)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "insert staged restore links")
	})
}

func TestInsertDuplicateRestore_HappyPathAndRenameWarnings(t *testing.T) {
	ctx := context.Background()
	snap := &Snapshot{
		Tags:  []TagRow{{ID: 1, Name: "go"}, {ID: 2, Name: "rust"}},
		Links: []LinkRow{{ID: 3, URL: "https://a.test"}},
	}
	prep := duplicateRestorePrep{
		tagNames:      []string{"go (2)", "rust"},
		existingLinks: map[string]existingRestoreLink{"https://a.test": {slug: "a"}},
	}
	inserted, warnings, _, err := insertDuplicateRestore(ctx, newFakeTx(), testUID, snap, prep, newIDMapping())
	require.NoError(t, err)
	assert.EqualValues(t, 1, inserted.Tags)
	assert.EqualValues(t, 1, inserted.Links)
	require.Len(t, warnings, 2) // one tag rename + one pre-existing link

	_, _, _, err = insertDuplicateRestore(ctx, failRestoreTx{}, testUID, snap, prep, newIDMapping())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insert staged duplicate tags")

	tx := newFakeTx()
	tx.queryErr = errRestoreForced
	_, _, _, err = insertDuplicateRestore(ctx, tx, testUID, snap, prep, newIDMapping())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "map staged duplicate tags")
}

func TestLoadInsertedLinkIDs_RecordsAuthoritativeSet(t *testing.T) {
	ctx := context.Background()
	tx := newFakeTx()
	tx.queries = []pgx.Rows{newScriptRows([]any{int64(11)}, []any{int64(12)})}
	mapping := newIDMapping()
	require.NoError(t, loadInsertedLinkIDs(ctx, tx, testUID, mapping))
	assert.True(t, mapping.insertedLinks[0])
	assert.True(t, mapping.insertedLinks[11])
	assert.True(t, mapping.insertedLinks[12])

	tx2 := newFakeTx()
	bad := newScriptRows([]any{int64(1)})
	bad.scanErr = errFakeScan
	tx2.queries = []pgx.Rows{bad}
	assert.ErrorIs(t, loadInsertedLinkIDs(ctx, tx2, testUID, newIDMapping()), errFakeScan)

	err := loadInsertedLinkIDs(ctx, failRestoreTx{}, testUID, newIDMapping())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load inserted staged links")
}

func TestLoadStagedIDMapping_RowsScanAndRowsErrors(t *testing.T) {
	ctx := context.Background()
	tx := newFakeTx()
	tx.queries = []pgx.Rows{newScriptRows([]any{int64(1), int64(10)}, []any{int64(2), int64(20)})}
	mapping := map[int64]int64{}
	require.NoError(t, loadStagedIDMapping(ctx, tx, "q", mapping))
	assert.Equal(t, map[int64]int64{1: 10, 2: 20}, mapping)

	tx2 := newFakeTx()
	bad := newScriptRows([]any{int64(1), int64(10)})
	bad.scanErr = errFakeScan
	tx2.queries = []pgx.Rows{bad}
	assert.ErrorIs(t, loadStagedIDMapping(ctx, tx2, "q", map[int64]int64{}), errFakeScan)

	tx3 := newFakeTx()
	rowsErr := newScriptRows([]any{int64(1), int64(10)})
	rowsErr.err = errRestoreForced
	tx3.queries = []pgx.Rows{rowsErr}
	assert.ErrorIs(t, loadStagedIDMapping(ctx, tx3, "q", map[int64]int64{}), errRestoreForced)
}

func TestAllocateDuplicateTagNames_SuffixesPastTaken(t *testing.T) {
	ctx := context.Background()

	names, err := allocateDuplicateTagNames(ctx, newFakeTx(), testUID, []TagRow{{Name: "fresh"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"fresh"}, names)

	tx := newFakeTx()
	tx.queries = []pgx.Rows{newScriptRows([]any{"go"}, []any{"go (2)"})}
	names, err = allocateDuplicateTagNames(ctx, tx, testUID, []TagRow{{Name: "go"}, {Name: "go"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"go (3)", "go (4)"}, names,
		"taken names and names allocated earlier in the same batch are both avoided")

	names, err = allocateDuplicateTagNames(ctx, newFakeTx(), testUID, nil)
	require.NoError(t, err)
	assert.Empty(t, names)

	_, err = allocateDuplicateTagNames(ctx, failRestoreTx{}, testUID, []TagRow{{Name: "go"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load duplicate tag names")
}

func TestLoadTakenDuplicateTagNames_LimitAndScanErrors(t *testing.T) {
	ctx := context.Background()

	got, err := loadTakenDuplicateTagNames(ctx, newFakeTx(), testUID, nil, 10)
	require.NoError(t, err)
	assert.Nil(t, got)

	tx := newFakeTx()
	// Production query has LIMIT limit+1; the fake ignores SQL, so returning
	// two rows against limit=1 exercises the overflow guard itself.
	tx.queries = []pgx.Rows{newScriptRows([]any{"b"}, []any{"a"})}
	_, err = loadTakenDuplicateTagNames(ctx, tx, testUID, []string{"a"}, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "more than 1 relevant collisions")

	tx2 := newFakeTx()
	tx2.queries = []pgx.Rows{newScriptRows([]any{"b"}, []any{"a"})}
	got, err = loadTakenDuplicateTagNames(ctx, tx2, testUID, []string{"a"}, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, got, "result is sorted")

	tx3 := newFakeTx()
	bad := newScriptRows([]any{"a"})
	bad.scanErr = errFakeScan
	tx3.queries = []pgx.Rows{bad}
	_, err = loadTakenDuplicateTagNames(ctx, tx3, testUID, []string{"a"}, 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scan duplicate tag name")
}

func TestLoadExistingRestoreLinks_RowsAndErrors(t *testing.T) {
	ctx := context.Background()
	links := []LinkRow{{URL: "https://a.test"}}

	got, err := loadExistingRestoreLinks(ctx, failRestoreTx{}, testUID, nil)
	require.NoError(t, err, "no links means no query")
	assert.Empty(t, got)

	tx := newFakeTx()
	tx.queries = []pgx.Rows{newScriptRows([]any{"https://a.test", "slug-a"})}
	got, err = loadExistingRestoreLinks(ctx, tx, testUID, links)
	require.NoError(t, err)
	assert.Equal(t, "slug-a", got["https://a.test"].slug)

	_, err = loadExistingRestoreLinks(ctx, failRestoreTx{}, testUID, links)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load existing restore links")

	tx2 := newFakeTx()
	bad := newScriptRows([]any{"https://a.test", "slug-a"})
	bad.scanErr = errFakeScan
	tx2.queries = []pgx.Rows{bad}
	_, err = loadExistingRestoreLinks(ctx, tx2, testUID, links)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scan existing restore link")
}

func TestAllocateRestoreSlugs_ExistingAndDuplicateURLs(t *testing.T) {
	ctx := context.Background()
	links := []LinkRow{
		{URL: "https://a.test", Slug: "ignored", Title: "A"},
		{URL: "https://b.test", Slug: "bee", Title: "B"},
		{URL: "https://b.test", Slug: "bee", Title: "B again"},
		{URL: "https://c.test", Title: "See Em Space"},
	}
	existing := map[string]existingRestoreLink{"https://a.test": {slug: "live-slug"}}
	slugs, err := allocateRestoreLinkSlugs(ctx, newFakeTx(), links, existing)
	require.NoError(t, err)
	require.Len(t, slugs, 4)
	assert.Equal(t, "live-slug", slugs[0], "pre-existing link keeps its live slug")
	assert.Equal(t, "bee", slugs[1])
	assert.Equal(t, slugs[1], slugs[2], "duplicate URL in the snapshot allocates once")
	assert.NotEmpty(t, slugs[3])

	noteSlugs, err := allocateRestoreNoteSlugs(ctx, newFakeTx(), []NoteRow{
		{Slug: "first", Title: "First"},
		{Title: "Second Note"},
	})
	require.NoError(t, err)
	assert.Equal(t, "first", noteSlugs[0])
	assert.NotEmpty(t, noteSlugs[1])
	assert.NotEqual(t, noteSlugs[0], noteSlugs[1])
}

func TestCopyRestoreStaging_HappyPathCopiesEveryCollection(t *testing.T) {
	ctx := context.Background()
	tx := newFakeTx()
	// reserveRestoreStagingIDs queries once per non-empty collection and each
	// result must carry exactly len(collection) ids.
	tx.queries = []pgx.Rows{
		newScriptRows([]any{int64(10)}), // tags
		newScriptRows([]any{int64(20)}), // folders
		newScriptRows([]any{int64(30)}), // links
		newScriptRows([]any{int64(40)}), // notes
	}
	snap := &Snapshot{
		Tags:    []TagRow{{ID: 1, Name: "t"}},
		Folders: []FolderRow{{ID: 2, Name: "f"}},
		Links:   []LinkRow{{ID: 3, URL: "https://a.test"}},
		Notes:   []NoteRow{{ID: 4, Title: "n", BodyHTML: "<p>x</p>"}},
	}
	warnings, err := copyRestoreStaging(ctx, tx, snap, []string{"t-renamed"}, []string{"a"}, []string{"n"})
	require.NoError(t, err)
	assert.Empty(t, warnings)
	assert.EqualValues(t, 4, tx.copied, "one staged row per collection")

	tx2 := newFakeTx()
	tx2.queries = []pgx.Rows{newScriptRows([]any{int64(10)})}
	tx2.copyErr = errRestoreForced
	_, err = copyRestoreStaging(ctx, tx2, &Snapshot{Tags: []TagRow{{ID: 1, Name: "t"}}}, nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "copy restore tags")
}

func TestReserveRestoreIDs_ShortScanAndRowsErrors(t *testing.T) {
	ctx := context.Background()

	tx := newFakeTx()
	tx.queries = []pgx.Rows{newScriptRows([]any{int64(1)})} // want 2
	_, err := reserveRestoreIDs(ctx, tx, "tag_id_seq", 2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "got 1, want 2")

	tx2 := newFakeTx()
	bad := newScriptRows([]any{int64(1)})
	bad.scanErr = errFakeScan
	tx2.queries = []pgx.Rows{bad}
	_, err = reserveRestoreIDs(ctx, tx2, "link_id_seq", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reserve restore ids from link_id_seq")

	tx3 := newFakeTx()
	rowsErr := newScriptRows([]any{int64(1)})
	rowsErr.err = errRestoreForced
	tx3.queries = []pgx.Rows{rowsErr}
	_, err = reserveRestoreIDs(ctx, tx3, "note_id_seq", 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, errRestoreForced)

	for _, seq := range []string{"tag_id_seq", "folder_id_seq", "link_id_seq", "note_id_seq"} {
		txN := newFakeTx()
		txN.queries = []pgx.Rows{newScriptRows([]any{int64(9)})}
		ids, err := reserveRestoreIDs(ctx, txN, seq, 1)
		require.NoError(t, err, seq)
		assert.Equal(t, []int64{9}, ids)
	}
}

func TestCopyRestoreTags_UsesAllocatedNames(t *testing.T) {
	ctx := context.Background()
	tx := newFakeTx()
	require.NoError(t, copyRestoreTags(ctx, tx, []TagRow{{ID: 1, Name: "orig"}}, []string{"renamed"}, []int64{10}))
	assert.EqualValues(t, 1, tx.copied)

	tx.copyErr = errRestoreForced
	err := copyRestoreTags(ctx, tx, []TagRow{{ID: 1, Name: "orig"}}, nil, []int64{10})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "copy restore tags")
}
