package backup

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWrapRestoreInsertError(t *testing.T) {
	assert.NoError(t, wrapRestoreInsertError("skip", nil))
	err := wrapRestoreInsertError("wipe", errRestoreForced)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backup: insert (wipe)")
	assert.ErrorIs(t, err, errRestoreForced)
}

func TestLockRestoreTransaction(t *testing.T) {
	ctx := context.Background()

	tx := newFakeTx()
	tx.row = valuesRow(true)
	require.NoError(t, lockRestoreTransaction(ctx, tx))

	tx2 := newFakeTx()
	tx2.row = valuesRow(false)
	assert.ErrorIs(t, lockRestoreTransaction(ctx, tx2), ErrRestoreInProgress)

	tx3 := newFakeTx()
	tx3.row = errRow(errRestoreForced)
	err := lockRestoreTransaction(ctx, tx3)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "advisory lock")
}

func TestAppendRestoreSnapshotWarnings(t *testing.T) {
	ctx := context.Background()

	t.Run("owner mismatch and legacy settings both warn", func(t *testing.T) {
		tx := newFakeTx()
		tx.row = valuesRow("current@example.com")
		report := RestoreReport{Warnings: []string{}}
		snap := &Snapshot{
			OwnerEmail:  "exporter@example.com",
			AppSettings: []AppSettingRow{{Key: "master_password_hash", Value: "x"}},
		}
		appendRestoreSnapshotWarnings(ctx, tx, testUID, snap, &report)
		require.Len(t, report.Warnings, 2)
		assert.Contains(t, report.Warnings[0], "exporter@example.com")
		assert.Contains(t, report.Warnings[1], "configurações globais")
	})

	t.Run("same owner stays silent", func(t *testing.T) {
		tx := newFakeTx()
		tx.row = valuesRow("same@example.com")
		report := RestoreReport{Warnings: []string{}}
		appendRestoreSnapshotWarnings(ctx, tx, testUID, &Snapshot{OwnerEmail: "same@example.com"}, &report)
		assert.Empty(t, report.Warnings)
	})

	t.Run("lookup failure stays silent", func(t *testing.T) {
		tx := newFakeTx()
		tx.row = errRow(errRestoreForced)
		report := RestoreReport{Warnings: []string{}}
		appendRestoreSnapshotWarnings(ctx, tx, testUID, &Snapshot{OwnerEmail: "a@b.test"}, &report)
		assert.Empty(t, report.Warnings)
	})
}

func TestApplyRestoreMode_InvalidMode(t *testing.T) {
	report := RestoreReport{}
	_, err := applyRestoreMode(context.Background(), newFakeTx(), testUID, ConflictMode("explode"), &Snapshot{}, &report)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid mode")
}

func TestApplyWipeRestore_FailsClosedBeforeWipe(t *testing.T) {
	report := RestoreReport{}
	_, err := applyWipeRestore(context.Background(), failRestoreTx{}, testUID, &Snapshot{}, &report)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clear prior restore ledgers")
}

func TestTransactionalRestoreLedger_NonSkipSkipsQuery(t *testing.T) {
	// failRestoreTx errors on every query; non-skip modes must not issue one.
	for _, mode := range []ConflictMode{ModeWipe, ModeDuplicate} {
		_, found, err := transactionalRestoreLedger(context.Background(), failRestoreTx{}, testUID, [sha256.Size]byte{}, mode)
		require.NoError(t, err)
		assert.False(t, found)
	}
}

func TestRestoreFromLedger_NonSkipSkipsQuery(t *testing.T) {
	svc := &Service{} // nil pool: any ledger query would panic
	_, found, err := svc.restoreFromLedger(context.Background(), testUID, nil, ModeWipe, [sha256.Size]byte{}, time.Now())
	require.NoError(t, err)
	assert.False(t, found)
}

func ledgerRowJSON(complete bool) fakeRow {
	return valuesRow(
		[]byte(`{"links":2}`), []byte(`{"tags":1}`),
		[]byte(`["w1"]`), []byte(`{"uploaded":3}`), complete,
	)
}

func TestLoadRestoreLedger_Branches(t *testing.T) {
	ctx := context.Background()
	digest := [sha256.Size]byte{9}

	t.Run("no rows", func(t *testing.T) {
		tx := newFakeTx()
		tx.row = errRow(pgx.ErrNoRows)
		_, found, err := loadRestoreLedger(ctx, tx, testUID, digest, ModeSkip)
		require.NoError(t, err)
		assert.False(t, found)
	})

	t.Run("query error", func(t *testing.T) {
		tx := newFakeTx()
		tx.row = errRow(errRestoreForced)
		_, _, err := loadRestoreLedger(ctx, tx, testUID, digest, ModeSkip)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "load restore ledger")
	})

	t.Run("corrupt counts JSON", func(t *testing.T) {
		tx := newFakeTx()
		tx.row = valuesRow([]byte(`{`), []byte(`{}`), []byte(`[]`), []byte(`{}`), false)
		_, _, err := loadRestoreLedger(ctx, tx, testUID, digest, ModeSkip)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode restore inserted counts")
	})

	t.Run("complete ledger skips mapping loads", func(t *testing.T) {
		tx := newFakeTx()
		tx.queryErr = errRestoreForced // any mapping Query would fail
		tx.row = ledgerRowJSON(true)
		ledger, found, err := loadRestoreLedger(ctx, tx, testUID, digest, ModeSkip)
		require.NoError(t, err)
		require.True(t, found)
		assert.True(t, ledger.complete)
		assert.EqualValues(t, 2, ledger.inserted.Links)
		assert.EqualValues(t, 3, ledger.files.Uploaded)
	})

	t.Run("incomplete ledger loads entity and file mappings", func(t *testing.T) {
		tx := newFakeTx()
		tx.row = ledgerRowJSON(false)
		tx.queries = []pgx.Rows{
			newScriptRows(
				[]any{"tag", int64(1), int64(10)},
				[]any{"folder", int64(2), int64(20)},
				[]any{"link", int64(3), int64(30)},
				[]any{"note", int64(4), int64(40)},
			),
			newScriptRows([]any{"notes/old.jpg", "notes/new.jpg"}),
		}
		ledger, found, err := loadRestoreLedger(ctx, tx, testUID, digest, ModeSkip)
		require.NoError(t, err)
		require.True(t, found)
		assert.EqualValues(t, 10, ledger.mapping.tagMap[1])
		assert.EqualValues(t, 40, ledger.mapping.noteMap[4])
		assert.Equal(t, "notes/new.jpg", ledger.mapping.noteFiles["notes/old.jpg"])
	})

	t.Run("unknown entity kind", func(t *testing.T) {
		tx := newFakeTx()
		tx.row = ledgerRowJSON(false)
		tx.queries = []pgx.Rows{newScriptRows([]any{"widget", int64(1), int64(2)})}
		_, _, err := loadRestoreLedger(ctx, tx, testUID, digest, ModeSkip)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown kind "widget"`)
	})

	t.Run("entity mapping scan and rows errors", func(t *testing.T) {
		tx := newFakeTx()
		tx.row = ledgerRowJSON(false)
		bad := newScriptRows([]any{"tag", int64(1), int64(2)})
		bad.scanErr = errFakeScan
		tx.queries = []pgx.Rows{bad}
		_, _, err := loadRestoreLedger(ctx, tx, testUID, digest, ModeSkip)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "scan restore entity mapping")

		tx2 := newFakeTx()
		tx2.row = ledgerRowJSON(false)
		rowsErr := newScriptRows([]any{"tag", int64(1), int64(2)})
		rowsErr.err = errRestoreForced
		tx2.queries = []pgx.Rows{rowsErr}
		_, _, err = loadRestoreLedger(ctx, tx2, testUID, digest, ModeSkip)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "load restore entity mappings")
	})

	t.Run("file mapping errors", func(t *testing.T) {
		tx := newFakeTx()
		tx.row = ledgerRowJSON(false)
		bad := newScriptRows([]any{"a", "b"})
		bad.scanErr = errFakeScan
		tx.queries = []pgx.Rows{newScriptRows(), bad}
		_, _, err := loadRestoreLedger(ctx, tx, testUID, digest, ModeSkip)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "scan restore file mapping")

		tx2 := newFakeTx()
		tx2.row = ledgerRowJSON(false)
		tx2.queries = []pgx.Rows{newScriptRows()} // second Query hits queryErr below
		// Queue exhausted -> default empty rows; use explicit failing second set.
		rowsErr := newScriptRows([]any{"a", "b"})
		rowsErr.err = errRestoreForced
		tx2.queries = append(tx2.queries, rowsErr)
		_, _, err = loadRestoreLedger(ctx, tx2, testUID, digest, ModeSkip)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "load restore file mappings")
	})
}

func TestSaveAndClearRestoreLedger_WithFakeTx(t *testing.T) {
	ctx := context.Background()
	digest := [sha256.Size]byte{3}
	mapping := newIDMapping()
	mapping.tagMap[1] = 10
	mapping.folderMap[2] = 20
	mapping.linkMap[3] = 30
	mapping.noteMap[4] = 40
	mapping.noteFiles["notes/old.jpg"] = "notes/new.jpg"
	report := RestoreReport{Inserted: Counts{Links: 1}, Warnings: []string{"w"}}

	tx := newFakeTx()
	require.NoError(t, saveRestoreLedger(ctx, tx, testUID, digest, ModeSkip, report, mapping))
	assert.EqualValues(t, 5, tx.copied, "4 entity mappings + 1 file mapping")

	// Empty mapping: only the ledger row itself is written.
	tx2 := newFakeTx()
	require.NoError(t, saveRestoreLedger(ctx, tx2, testUID, digest, ModeSkip, report, newIDMapping()))
	assert.Zero(t, tx2.copied)

	err := saveRestoreLedger(ctx, failRestoreTx{}, testUID, digest, ModeSkip, report, mapping)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insert restore ledger")

	tx3 := newFakeTx()
	tx3.copyErr = errRestoreForced
	err = saveRestoreLedger(ctx, tx3, testUID, digest, ModeSkip, report, mapping)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "copy restore entity mappings")

	require.NoError(t, clearRestoreLedgers(ctx, newFakeTx(), testUID))
	assert.ErrorIs(t, clearRestoreLedgers(ctx, failRestoreTx{}, testUID), errRestoreForced)
}

func TestFinalizeRestoreTransaction_Paths(t *testing.T) {
	ctx := context.Background()
	digest := [sha256.Size]byte{1}

	t.Run("duplicate mode writes no ledger", func(t *testing.T) {
		result := restoreTransactionResult{mapping: newIDMapping()}
		require.NoError(t, finalizeRestoreTransaction(ctx, newFakeTx(), testUID, ModeDuplicate, digest, &Snapshot{}, &result))
	})

	t.Run("skip mode persists the ledger", func(t *testing.T) {
		result := restoreTransactionResult{mapping: newIDMapping(), report: RestoreReport{Warnings: []string{}}}
		require.NoError(t, finalizeRestoreTransaction(ctx, newFakeTx(), testUID, ModeSkip, digest, &Snapshot{}, &result))
	})

	t.Run("realign failure surfaces first", func(t *testing.T) {
		mapping := newIDMapping()
		mapping.linkMap[1] = 2 // non-empty: realign issues an UPDATE
		result := restoreTransactionResult{mapping: mapping}
		err := finalizeRestoreTransaction(ctx, failRestoreTx{}, testUID, ModeDuplicate, digest, &Snapshot{}, &result)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "realign link image urls")
	})
}

func TestMappedRestoreTagAndClickRows(t *testing.T) {
	now := time.Now().UTC()
	mapping := newIDMapping()
	mapping.linkMap[1] = 10
	mapping.noteMap[2] = 20
	mapping.tagMap[5] = 50
	snap := &Snapshot{
		LinkTags:   []LinkTagRow{{LinkID: 1, TagID: 5}, {LinkID: 99, TagID: 5}, {LinkID: 1, TagID: 99}},
		NoteTags:   []NoteTagRow{{NoteID: 2, TagID: 5}},
		ClickLogs:  []ClickRow{{LinkID: 1, ClickedAt: now}, {LinkID: 99, ClickedAt: now}},
		NoteClicks: []NoteClickRow{{NoteID: 2, ClickedAt: now}, {NoteID: 99, ClickedAt: now}},
	}

	tagRows, unmapped := mappedRestoreTagRows(mapping, snap)
	require.Len(t, tagRows, 2)
	assert.EqualValues(t, 2, unmapped)
	assert.Equal(t, []any{"link", int64(10), int64(50)}, tagRows[0])
	assert.Equal(t, []any{"note", int64(20), int64(50)}, tagRows[1])

	var skipped Counts
	clickRows := mappedRestoreClickRows(testUID, mapping, snap, &skipped, true)
	require.Len(t, clickRows, 2)
	assert.EqualValues(t, 2, skipped.ClickLogs)
	assert.Equal(t, int64(testUID), clickRows[0][3])

	// Without skip accounting, unmapped clicks are dropped silently.
	skipped = Counts{}
	_ = mappedRestoreClickRows(testUID, mapping, snap, &skipped, false)
	assert.Equal(t, Counts{}, skipped)
	_ = mappedRestoreClickRows(testUID, mapping, snap, nil, true) // nil skipped is tolerated
}

func TestAttachPolymorphicTags_Branches(t *testing.T) {
	ctx := context.Background()
	mapping := newIDMapping()
	mapping.linkMap[1] = 10
	mapping.tagMap[5] = 50

	t.Run("no mapped rows skips the database", func(t *testing.T) {
		var inserted, skipped Counts
		snap := &Snapshot{LinkTags: []LinkTagRow{{LinkID: 99, TagID: 5}}}
		require.NoError(t, attachPolymorphicTags(ctx, failRestoreTx{}, mapping, snap, &inserted, &skipped, true))
		assert.EqualValues(t, 1, skipped.LinkTags, "unmapped row counts as skipped")
	})

	t.Run("inserted and conflict counts", func(t *testing.T) {
		var inserted, skipped Counts
		snap := &Snapshot{LinkTags: []LinkTagRow{{LinkID: 1, TagID: 5}, {LinkID: 1, TagID: 5}}}
		// Fake Exec tag reports 1 affected row for 2 submitted rows.
		require.NoError(t, attachPolymorphicTags(ctx, newFakeTx(), mapping, snap, &inserted, &skipped, true))
		assert.EqualValues(t, 1, inserted.LinkTags)
		assert.EqualValues(t, 1, skipped.LinkTags)
	})

	t.Run("nil counters tolerated", func(t *testing.T) {
		snap := &Snapshot{LinkTags: []LinkTagRow{{LinkID: 1, TagID: 5}}}
		require.NoError(t, attachPolymorphicTags(ctx, newFakeTx(), mapping, snap, nil, nil, false))
	})

	t.Run("tx failure", func(t *testing.T) {
		snap := &Snapshot{LinkTags: []LinkTagRow{{LinkID: 1, TagID: 5}}}
		err := attachPolymorphicTags(ctx, failRestoreTx{}, mapping, snap, nil, nil, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create restore link_tag temp")
	})
}

func TestInsertRestoreTagRows_StepFailures(t *testing.T) {
	ctx := context.Background()
	rows := [][]any{{"link", int64(1), int64(2)}}

	n, err := insertRestoreTagRows(ctx, newFakeTx(), rows)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)

	tx := newFakeTx()
	tx.copyErr = errRestoreForced
	_, err = insertRestoreTagRows(ctx, tx, rows)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "copy restore link_tag temp")
}

func TestCopyPolymorphicClicks_Branches(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	mapping := newIDMapping()
	mapping.linkMap[1] = 10

	t.Run("no clicks skips the database", func(t *testing.T) {
		require.NoError(t, copyPolymorphicClicks(ctx, failRestoreTx{}, testUID, mapping, &Snapshot{}, restoreClickAccounting{}))
	})

	t.Run("all unmapped skips CopyFrom", func(t *testing.T) {
		var skipped Counts
		snap := &Snapshot{ClickLogs: []ClickRow{{LinkID: 99, ClickedAt: now}}}
		require.NoError(t, copyPolymorphicClicks(ctx, failRestoreTx{}, testUID, mapping, snap,
			restoreClickAccounting{skipped: &skipped, countSkips: true}))
		assert.EqualValues(t, 1, skipped.ClickLogs)
	})

	t.Run("mapped clicks copy and count", func(t *testing.T) {
		var inserted Counts
		snap := &Snapshot{ClickLogs: []ClickRow{{LinkID: 1, ClickedAt: now}}}
		require.NoError(t, copyPolymorphicClicks(ctx, newFakeTx(), testUID, mapping, snap,
			restoreClickAccounting{inserted: &inserted}))
		assert.EqualValues(t, 1, inserted.ClickLogs)
	})

	t.Run("copy failure", func(t *testing.T) {
		snap := &Snapshot{ClickLogs: []ClickRow{{LinkID: 1, ClickedAt: now}}}
		err := copyPolymorphicClicks(ctx, failRestoreTx{}, testUID, mapping, snap, restoreClickAccounting{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "copy click_log")
	})
}

func TestRealignLinkImageURLs_Branches(t *testing.T) {
	ctx := context.Background()
	require.NoError(t, realignLinkImageURLs(ctx, failRestoreTx{}, testUID, newIDMapping()),
		"empty link map issues no UPDATE")

	mapping := newIDMapping()
	mapping.linkMap[1] = 2
	require.NoError(t, realignLinkImageURLs(ctx, newFakeTx(), testUID, mapping))
	err := realignLinkImageURLs(ctx, failRestoreTx{}, testUID, mapping)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "realign link image urls")
}

func TestRestoreNoteMediaRefs_Branches(t *testing.T) {
	ctx := context.Background()

	t.Run("no note files and no mapped notes is a no-op", func(t *testing.T) {
		snap := &Snapshot{Notes: []NoteRow{{ID: 1, BodyHTML: "<p>plain</p>"}}}
		require.NoError(t, restoreNoteMediaRefs(ctx, newFakeTx(), testUID, snap, newIDMapping()))
	})

	t.Run("owned refs are restored, foreign keys ignored", func(t *testing.T) {
		mapping := newIDMapping()
		mapping.noteMap[1] = 10
		mapping.noteFiles["notes/old-a.jpg"] = "notes/new-a.jpg"
		cover := "/api/files/notes/new-a.jpg"
		snap := &Snapshot{Notes: []NoteRow{
			{ID: 1, BodyHTML: `<img src="/api/files/notes/new-a.jpg">`, CoverURL: &cover},
			{ID: 2, BodyHTML: `<img src="/api/files/notes/new-a.jpg">`}, // unmapped note: skipped
		}}
		require.NoError(t, restoreNoteMediaRefs(ctx, newFakeTx(), testUID, snap, mapping))
	})
}

func TestZipFileEntries_FiltersByPrefix(t *testing.T) {
	zr := zipReaderWithEntries(t,
		struct {
			name string
			body []byte
		}{"files/images/1.jpg", []byte("x")},
		struct {
			name string
			body []byte
		}{"manifest.json", []byte("{}")},
		struct {
			name string
			body []byte
		}{"files/notes/a.jpg", []byte("y")},
	)
	entries := zipFileEntries(zr, filesPrefix)
	assert.Len(t, entries, 2)
	assert.Contains(t, entries, "files/images/1.jpg")
	assert.NotContains(t, entries, "manifest.json")
}

func TestNoteMediaSpool_BudgetsAndWrite(t *testing.T) {
	spool := newNoteMediaSpool()
	spool.aggregateBudget = 4

	got, err := spool.write([]byte("ab"), "image/png")
	require.NoError(t, err)
	t.Cleanup(func() {
		if spool.file != nil {
			_ = spool.file.Close()
			_ = os.Remove(spool.file.Name())
		}
	})
	assert.EqualValues(t, 0, got.offset)
	assert.EqualValues(t, 2, got.size)
	assert.Equal(t, "image/png", got.contentType)

	_, err = spool.write([]byte("abc"), "image/png")
	require.Error(t, err, "aggregate budget (4) already holds 2 bytes")
	assert.True(t, errors.Is(err, ErrInvalidBackup), "over-budget media is an invalid-backup rejection")

	// A prepared restore with no spool cleans up as a no-op.
	(&preparedNoteMediaRestore{}).cleanup()
	var nilPrepared *preparedNoteMediaRestore
	nilPrepared.cleanup()
}
