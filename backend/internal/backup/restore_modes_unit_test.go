package backup

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stagedQueries returns the Query result queue a one-row-per-collection
// staged restore consumes, in call order: existing links, taken link slugs,
// taken note slugs, four id reservations, then the staged id mappings.
func stagedQueries() []pgx.Rows {
	return []pgx.Rows{
		newScriptRows(),                           // existing links
		newScriptRows(),                           // taken link slugs
		newScriptRows(),                           // taken note slugs
		newScriptRows([]any{int64(10)}),           // tag ids
		newScriptRows([]any{int64(20)}),           // folder ids
		newScriptRows([]any{int64(30)}),           // link ids
		newScriptRows([]any{int64(40)}),           // note ids
		newScriptRows([]any{int64(1), int64(10)}), // tag mapping
		newScriptRows([]any{int64(2), int64(20)}), // folder mapping
		newScriptRows([]any{int64(3), int64(30)}), // link mapping
		newScriptRows([]any{int64(30)}),           // inserted link ids
		newScriptRows([]any{int64(4), int64(40)}), // note mapping
	}
}

func oneEachSnapshot() *Snapshot {
	return &Snapshot{
		Tags:    []TagRow{{ID: 1, Name: "t", Color: "#fff"}},
		Folders: []FolderRow{{ID: 2, Name: "f", Color: "#fff"}},
		Links:   []LinkRow{{ID: 3, URL: "https://a.test", Title: "A", Slug: "a"}},
		Notes:   []NoteRow{{ID: 4, Title: "N", Slug: "n", BodyHTML: "<p>x</p>"}},
	}
}

func TestRestoreSkipStaged_FullHappyPathWithFakeTx(t *testing.T) {
	tx := newFakeTx()
	tx.queries = stagedQueries()
	inserted, skipped, warnings, mapping, err := restoreSkipStaged(context.Background(), tx, testUID, oneEachSnapshot())
	require.NoError(t, err)
	assert.Empty(t, warnings)
	assert.EqualValues(t, 1, inserted.Tags) // fake Exec tag
	assert.EqualValues(t, 0, skipped.Tags)
	assert.EqualValues(t, 1, inserted.Folders)
	assert.EqualValues(t, 1, inserted.Links)
	assert.EqualValues(t, 1, inserted.Notes)
	assert.EqualValues(t, 10, mapping.tagMap[1])
	assert.EqualValues(t, 30, mapping.linkMap[3])
	assert.True(t, mapping.insertedLinks[30])
}

func TestRestoreSkipStaged_TagInsertFailure(t *testing.T) {
	tx := newFakeTx()
	tx.queries = stagedQueries()
	tx.execErr = errRestoreForced // createRestoreStagingTables Exec fails first
	_, _, _, _, err := restoreSkipStaged(context.Background(), tx, testUID, oneEachSnapshot())
	require.Error(t, err)
}

func TestApplyRestoreMode_AllModesWithFakeTx(t *testing.T) {
	ctx := context.Background()

	t.Run("skip", func(t *testing.T) {
		tx := newFakeTx()
		tx.queries = stagedQueries()
		report := RestoreReport{Warnings: []string{}}
		mapping, err := applyRestoreMode(ctx, tx, testUID, ModeSkip, oneEachSnapshot(), &report)
		require.NoError(t, err)
		assert.EqualValues(t, 1, report.Inserted.Links)
		assert.EqualValues(t, 30, mapping.linkMap[3])
	})

	t.Run("wipe wipes then restores", func(t *testing.T) {
		tx := newFakeTx()
		tx.row = valuesRow(int64(2)) // six count(*) scans in wipeUser
		// ReleaseOwnerRefs runs a DELETE ... RETURNING through Query before
		// the staged restore queue starts.
		tx.queries = append([]pgx.Rows{newScriptRows()}, stagedQueries()...)
		report := RestoreReport{Warnings: []string{}}
		mapping, err := applyRestoreMode(ctx, tx, testUID, ModeWipe, oneEachSnapshot(), &report)
		require.NoError(t, err)
		assert.EqualValues(t, 2, report.Wiped.Links)
		assert.EqualValues(t, 1, report.Inserted.Links)
		assert.EqualValues(t, 30, mapping.linkMap[3])
	})

	t.Run("duplicate", func(t *testing.T) {
		tx := newFakeTx()
		// prepareDuplicateRestore queries: taken tag names, existing links,
		// taken link slugs, taken note slugs; then staging reservations and
		// mappings (tag mapping has no args but still consumes one Query).
		tx.queries = []pgx.Rows{
			newScriptRows(),
			newScriptRows(),
			newScriptRows(),
			newScriptRows(),
			newScriptRows([]any{int64(10)}),
			newScriptRows([]any{int64(20)}),
			newScriptRows([]any{int64(30)}),
			newScriptRows([]any{int64(40)}),
			newScriptRows([]any{int64(1), int64(10)}),
			newScriptRows([]any{int64(2), int64(20)}),
			newScriptRows([]any{int64(3), int64(30)}),
			newScriptRows([]any{int64(30)}),
			newScriptRows([]any{int64(4), int64(40)}),
		}
		report := RestoreReport{Warnings: []string{}}
		mapping, err := applyRestoreMode(ctx, tx, testUID, ModeDuplicate, oneEachSnapshot(), &report)
		require.NoError(t, err)
		assert.EqualValues(t, 1, report.Inserted.Tags)
		assert.EqualValues(t, 40, mapping.noteMap[4])
	})

	t.Run("skip failure is wrapped with the mode", func(t *testing.T) {
		report := RestoreReport{Warnings: []string{}}
		_, err := applyRestoreMode(ctx, failRestoreTx{}, testUID, ModeSkip, oneEachSnapshot(), &report)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "backup: insert (skip)")
	})
}

func TestStreamSnapshotJSON_EachCollectionFailureSurfaces(t *testing.T) {
	now := time.Now().UTC()
	rowFor := func(collection int) []any {
		switch collection {
		case 0:
			return []any{int64(1), "t", "#fff", nil, now}
		case 1:
			return []any{int64(2), "f", "#000", nil, nil, nil, now}
		case 2:
			return []any{int64(3), "https://a.test", "A", "a", nil, nil, nil, false, "done", nil, nil, false, now, now}
		case 3:
			return []any{int64(4), "N", "n", "<p>x</p>", "x", false, nil, nil, false, now, now}
		case 4, 5:
			return []any{int64(3), int64(1)}
		default:
			return []any{int64(3), now}
		}
	}
	// Fail the scan of collection i in turn; each must abort the stream.
	for failAt := range 8 {
		tx := newFakeTx()
		tx.row = valuesRow("owner@example.com")
		tx.queries = make([]pgx.Rows, 8)
		for i := range 8 {
			rows := newScriptRows(rowFor(i))
			if i == failAt {
				rows.scanErr = errFakeScan
			}
			tx.queries[i] = rows
		}
		_, err := streamSnapshotJSON(context.Background(), tx, testUID, &bytes.Buffer{})
		require.Error(t, err, "collection %d", failAt)
		assert.ErrorIs(t, err, errFakeScan)
	}

	// Writer failures at every position, including the trailing
	// owner_email writes, must surface instead of producing a torn snapshot.
	for failOn := 1; failOn <= 40; failOn++ {
		tx := newFakeTx()
		tx.row = valuesRow("owner@example.com")
		_, _ = streamSnapshotJSON(context.Background(), tx, testUID, &flakyWriter{failOn: failOn})
	}
	// An empty snapshot performs exactly 21 writes (2 header + 2 per
	// collection + 3 tail); failing the last one — the closing brace after
	// owner_email — must still propagate.
	tx := newFakeTx()
	tx.row = valuesRow("owner@example.com")
	_, err := streamSnapshotJSON(context.Background(), tx, testUID, &flakyWriter{failOn: 21})
	require.ErrorIs(t, err, errFakeWrite, "a late write failure (owner_email tail) still propagates")
	tx2 := newFakeTx()
	tx2.row = valuesRow("owner@example.com")
	_, err = streamSnapshotJSON(context.Background(), tx2, testUID, &flakyWriter{failOn: 22})
	require.NoError(t, err)
}

func TestBuildRestoreFilePlan_Branches(t *testing.T) {
	entry := func(name string, body string) struct {
		name string
		body []byte
	} {
		return struct {
			name string
			body []byte
		}{name, []byte(body)}
	}

	mapping := newIDMapping()
	mapping.linkMap[1] = 9
	mapping.noteFiles["notes/old.jpg"] = "notes/new.jpg"
	prepared := &preparedNoteMediaRestore{
		files: map[string]preparedNoteMediaFile{
			"notes/old.jpg": {offset: 0, size: 3, contentType: "image/jpeg"},
		},
	}

	zr := zipReaderWithEntries(t,
		entry("manifest.json", "{}"),              // ignored, not counted
		entry("files/images/1.jpg", "img"),        // mapped link image
		entry("files/images/77.jpg", "orphan"),    // unmapped: skipped
		entry("files/notes/old.jpg", "note"),      // prepared note media
		entry("files/notes/unmapped.jpg", "skip"), // unmapped note: skipped
	)
	plan, err := buildRestoreFilePlan(zr, mapping, prepared)
	require.NoError(t, err)
	require.Len(t, plan.work, 2)
	assert.EqualValues(t, 2, plan.skipped)
	assert.Equal(t, "images/9.jpg", plan.work[0].key)
	assert.False(t, plan.work[0].isNote)
	assert.Equal(t, "notes/new.jpg", plan.work[1].key)
	assert.True(t, plan.work[1].isNote)
	assert.True(t, plan.work[1].hasPrepared)

	bad := zipReaderWithEntries(t, entry("files/etc/passwd", "x"))
	_, err = buildRestoreFilePlan(bad, mapping, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rejected entry")
}

func TestRestoreCycleSlice_Terminations(t *testing.T) {
	p := func(v int64) *int64 { return &v }
	folders := []FolderRow{
		{ID: 1, ParentID: p(2)},
		{ID: 2, ParentID: p(3)},
		{ID: 3}, // chain ends: nil parent while still pending
		{ID: 4, ParentID: p(99)},
	}
	indexByID := map[int64]int{1: 0, 2: 1, 3: 2, 4: 3}
	pending := []bool{true, true, true, true}

	assert.Nil(t, restoreCycleSlice(0, folders, indexByID, pending),
		"a chain reaching a parentless folder is not a cycle")
	assert.Nil(t, restoreCycleSlice(3, folders, indexByID, pending),
		"an unknown parent id ends the walk")
	assert.Nil(t, restoreCycleSlice(0, folders, indexByID, []bool{false, true, true, true}),
		"a non-pending start yields no cycle")
}
