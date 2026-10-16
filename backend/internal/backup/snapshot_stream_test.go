package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamSnapshotJSON_EmptyAccountWritesValidSnapshot(t *testing.T) {
	tx := newFakeTx()
	tx.row = valuesRow("owner@example.com")
	var buf bytes.Buffer
	counts, err := streamSnapshotJSON(context.Background(), tx, testUID, &buf)
	require.NoError(t, err)
	assert.Equal(t, Counts{}, counts)

	var snap Snapshot
	require.NoError(t, json.Unmarshal(buf.Bytes(), &snap))
	assert.Equal(t, DatabaseSnapshotVersion, snap.Version)
	assert.Equal(t, "owner@example.com", snap.OwnerEmail)
}

func TestStreamSnapshotJSON_CountsEveryCollection(t *testing.T) {
	now := time.Now().UTC()
	tx := newFakeTx()
	tx.row = valuesRow("owner@example.com")
	// Query order: tags, folders, links, notes, link_tags, note_tags,
	// click_logs, note_clicks.
	tx.queries = []pgx.Rows{
		newScriptRows([]any{int64(1), "tag", "#fff", nil, now}),
		newScriptRows([]any{int64(2), "folder", "#000", nil, nil, nil, now}),
		newScriptRows([]any{int64(3), "https://a.test", "A", "a", nil, nil, nil, false, "done", nil, nil, false, now, now}),
		newScriptRows([]any{int64(4), "N", "n", "<p>x</p>", "x", false, nil, nil, false, now, now}),
		newScriptRows([]any{int64(3), int64(1)}),
		newScriptRows([]any{int64(4), int64(1)}),
		newScriptRows([]any{int64(3), now}),
		newScriptRows([]any{int64(4), now}, []any{int64(4), now}),
	}
	var buf bytes.Buffer
	counts, err := streamSnapshotJSON(context.Background(), tx, testUID, &buf)
	require.NoError(t, err)
	assert.EqualValues(t, 1, counts.Tags)
	assert.EqualValues(t, 1, counts.Folders)
	assert.EqualValues(t, 1, counts.Links)
	assert.EqualValues(t, 1, counts.Notes)
	assert.EqualValues(t, 2, counts.LinkTags)
	assert.EqualValues(t, 3, counts.ClickLogs)

	var snap Snapshot
	require.NoError(t, json.Unmarshal(buf.Bytes(), &snap))
	require.Len(t, snap.Tags, 1)
	assert.Equal(t, "tag", snap.Tags[0].Name)
	require.Len(t, snap.NoteClicks, 2)
}

func TestStreamSnapshotJSON_OwnerLookupFailure(t *testing.T) {
	tx := newFakeTx()
	tx.row = errRow(errRestoreForced)
	_, err := streamSnapshotJSON(context.Background(), tx, testUID, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner email")
	assert.ErrorIs(t, err, errRestoreForced)
}

func TestStreamSnapshotJSON_WriterFailureStopsStream(t *testing.T) {
	tx := newFakeTx()
	tx.row = valuesRow("owner@example.com")
	// First raw write is `{"version":` — fail right there.
	_, err := streamSnapshotJSON(context.Background(), tx, testUID, &flakyWriter{failOn: 1})
	require.ErrorIs(t, err, errFakeWrite)

	tx2 := newFakeTx()
	tx2.row = valuesRow("owner@example.com")
	// Fail later, while streaming a collection.
	_, err = streamSnapshotJSON(context.Background(), tx2, testUID, &flakyWriter{failOn: 4})
	require.ErrorIs(t, err, errFakeWrite)
}

func TestStreamSnapshotJSON_QueryFailureIsFieldScoped(t *testing.T) {
	tx := newFakeTx()
	tx.row = valuesRow("owner@example.com")
	tx.queryErr = errRestoreForced
	_, err := streamSnapshotJSON(context.Background(), tx, testUID, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tags")
	assert.Contains(t, err.Error(), "query")
}

func TestSnapshotRowScanners(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name string
		scan func(pgx.Rows) (any, error)
		row  []any
	}{
		{"tag", scanTagRow, []any{int64(1), "t", "#fff", strPtr("icon"), now}},
		{"folder", scanFolderRow, []any{int64(2), "f", "#000", ptrInt64(1), strPtr("hash"), strPtr("hint"), now}},
		{"link", scanLinkRow, []any{int64(3), "https://a.test", "A", "a", nil, nil, nil, true, "done", nil, ptrInt64(2), true, now, now}},
		{"note", scanNoteRow, []any{int64(4), "N", "n", "<p>x</p>", "x", true, ptrInt64(2), strPtr("cover"), false, now, now}},
		{"link_tag", scanLinkTagRow, []any{int64(3), int64(1)}},
		{"note_tag", scanNoteTagRow, []any{int64(4), int64(1)}},
		{"note_click", scanNoteClickRow, []any{int64(4), now}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := newScriptRows(tc.row)
			require.True(t, rows.Next())
			got, err := tc.scan(rows)
			require.NoError(t, err)
			assert.NotNil(t, got)

			bad := newScriptRows(tc.row)
			bad.scanErr = errFakeScan
			require.True(t, bad.Next())
			_, err = tc.scan(bad)
			assert.ErrorIs(t, err, errFakeScan)
		})
	}
}

func TestSnapshotEncoderWriteRowsBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("scan error", func(t *testing.T) {
		rows := newScriptRows([]any{int64(1), time.Now()})
		rows.scanErr = errFakeScan
		enc := snapshotStreamEncoder{w: &bytes.Buffer{}}
		_, err := enc.writeRows(ctx, `,"click_logs":`, rows, scanClickRow)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "scan")
		assert.ErrorIs(t, err, errFakeScan)
	})

	t.Run("rows error after iteration", func(t *testing.T) {
		rows := newScriptRows([]any{int64(1), time.Now()})
		rows.err = errRestoreForced
		enc := snapshotStreamEncoder{w: &bytes.Buffer{}}
		count, err := enc.writeRows(ctx, `,"click_logs":`, rows, scanClickRow)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rows")
		assert.EqualValues(t, 1, count)
	})

	t.Run("encode error", func(t *testing.T) {
		rows := newScriptRows([]any{int64(1)})
		enc := snapshotStreamEncoder{w: &bytes.Buffer{}}
		_, err := enc.writeRows(ctx, `,"x":`, rows, func(pgx.Rows) (any, error) {
			return make(chan int), nil
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "encode")
	})

	t.Run("cancelled context mid-iteration", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		rows := newScriptRows([]any{int64(1), time.Now()})
		enc := snapshotStreamEncoder{w: &bytes.Buffer{}}
		_, err := enc.writeRows(cctx, `,"click_logs":`, rows, scanClickRow)
		assert.ErrorIs(t, err, context.Canceled)
	})

	t.Run("writer error on opening bracket", func(t *testing.T) {
		enc := snapshotStreamEncoder{w: &flakyWriter{failOn: 1}}
		_, err := enc.writeRows(ctx, `,"click_logs":`, newScriptRows(), scanClickRow)
		assert.ErrorIs(t, err, errFakeWrite)
	})

	t.Run("value with failing writer", func(t *testing.T) {
		enc := snapshotStreamEncoder{w: &flakyWriter{failOn: 1}}
		assert.ErrorIs(t, enc.value("x"), errFakeWrite)
		assert.ErrorIs(t, enc.raw("x"), errFakeWrite)
	})
}
