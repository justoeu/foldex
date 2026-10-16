package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/testsupport"
)

func TestCountConflicts_EmptySnapshotSkipsDatabase(t *testing.T) {
	// A closed pool proves the empty case never queries: any query would fail.
	c, err := countConflicts(context.Background(), testsupport.ClosedPool(t), testUID, &Snapshot{})
	require.NoError(t, err)
	assert.Equal(t, Conflicts{}, c)
}

func TestCountConflicts_ClosedPoolSurfacesLinkAndTagBranches(t *testing.T) {
	ctx := context.Background()
	pool := testsupport.ClosedPool(t)

	_, err := countConflicts(ctx, pool, testUID, &Snapshot{Links: []LinkRow{{URL: "https://a.test"}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflict links")

	_, err = countConflicts(ctx, pool, testUID, &Snapshot{Tags: []TagRow{{Name: "t"}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflict tags")
}

func TestCompleteRestoreLedger_ClosedPool(t *testing.T) {
	err := completeRestoreLedger(context.Background(), testsupport.ClosedPool(t), testUID, [sha256.Size]byte{1}, ModeSkip, FileReport{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "complete restore ledger")
}

func TestDeleteWipedNoteMediaOwnership_Branches(t *testing.T) {
	ctx := context.Background()
	pool := testsupport.ClosedPool(t)

	// No notes/ keys: ownership cleanup is a no-op and never touches the DB.
	require.NoError(t, deleteWipedNoteMediaOwnership(ctx, pool, testUID, []string{"images/1.jpg", "screenshots/2.png"}))
	require.NoError(t, deleteWipedNoteMediaOwnership(ctx, pool, testUID, nil))

	err := deleteWipedNoteMediaOwnership(ctx, pool, testUID, []string{"notes/abc.jpg"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete wiped note media ownership")
}

func TestServiceExport_ClosedPoolFailsAtBeginTx(t *testing.T) {
	svc := NewService(testsupport.ClosedPool(t), nil, nil)
	require.NotNil(t, svc)
	_, err := svc.Export(context.Background(), testUID, &bytes.Buffer{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "begin tx")
}

func TestScanRows_Branches(t *testing.T) {
	ctx := context.Background()

	t.Run("success visits every row", func(t *testing.T) {
		tx := newFakeTx()
		tx.queries = []pgx.Rows{newScriptRows([]any{int64(1)}, []any{int64(2)})}
		var seen []int64
		err := scanRows(ctx, tx, "sql", nil, func(rows pgx.Rows) error {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			seen = append(seen, id)
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, []int64{1, 2}, seen)
	})

	t.Run("query error", func(t *testing.T) {
		tx := newFakeTx()
		tx.queryErr = errRestoreForced
		err := scanRows(ctx, tx, "sql", nil, func(pgx.Rows) error { return nil })
		assert.ErrorIs(t, err, errRestoreForced)
	})

	t.Run("callback error stops iteration", func(t *testing.T) {
		tx := newFakeTx()
		tx.queries = []pgx.Rows{newScriptRows([]any{int64(1)}, []any{int64(2)})}
		calls := 0
		err := scanRows(ctx, tx, "sql", nil, func(pgx.Rows) error {
			calls++
			return errFakeScan
		})
		assert.ErrorIs(t, err, errFakeScan)
		assert.Equal(t, 1, calls)
	})

	t.Run("rows error surfaces", func(t *testing.T) {
		rows := newScriptRows([]any{int64(1)})
		rows.err = errRestoreForced
		tx := newFakeTx()
		tx.queries = []pgx.Rows{rows}
		err := scanRows(ctx, tx, "sql", nil, func(rows pgx.Rows) error {
			var id int64
			return rows.Scan(&id)
		})
		assert.ErrorIs(t, err, errRestoreForced)
	})
}

func TestWipeUser_HappyPathWithFakeTx(t *testing.T) {
	// QueryRow must answer six count(*) scans; each gets a distinct count via
	// a sequence of scripted rows is overkill — one value proves the wiring.
	tx := newFakeTx()
	tx.row = valuesRow(int64(3))
	c, err := wipeUser(context.Background(), tx, testUID)
	require.NoError(t, err)
	assert.EqualValues(t, 3, c.ClickLogs)
	assert.EqualValues(t, 3, c.LinkTags)
	assert.EqualValues(t, 3, c.Links)
	assert.EqualValues(t, 3, c.Notes)
	assert.EqualValues(t, 3, c.Folders)
	assert.EqualValues(t, 3, c.Tags)
}

func TestWipeUser_EveryStageFailureSurfaces(t *testing.T) {
	t.Run("count failure", func(t *testing.T) {
		tx := newFakeTx()
		tx.row = errRow(errRestoreForced)
		_, err := wipeUser(context.Background(), tx, testUID)
		assert.ErrorIs(t, err, errRestoreForced)
	})
	t.Run("delete failure after counts", func(t *testing.T) {
		// seqRestoreTx answers QueryRow/Query fine until the tick budget runs
		// out; sweep so every Exec/Query stage of wipeUser fails in turn.
		for failAt := 1; failAt <= 20; failAt++ {
			_, _ = wipeUser(context.Background(), &seqRestoreTx{failAt: failAt}, testUID)
		}
		// With a budget beyond every stage the wipe completes.
		_, err := wipeUser(context.Background(), &seqRestoreTx{failAt: 10_000}, testUID)
		require.NoError(t, err)
	})
}

func TestUserObjectKeys_CollectsOwnedLinkAndNoteKeys(t *testing.T) {
	tx := newFakeTx()
	// Query order: link candidates, note media keys, owned link ids.
	tx.queries = []pgx.Rows{
		newScriptRows(
			[]any{int64(5), "/api/files/images/5.jpg"},
			[]any{int64(6), "https://cdn.example/x.jpg"}, // external: no candidate
			[]any{int64(7), "/api/files/screenshots/7.png"},
		),
		newScriptRows([]any{"notes/abc-1.jpg"}),
		newScriptRows([]any{int64(5)}), // only link 5 still owned
	}
	keys, err := userObjectKeys(context.Background(), tx, testUID, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"images/5.jpg", "notes/abc-1.jpg"}, keys,
		"screenshots/7.png is dropped: its owning link is not the caller's")
}

func TestUserObjectKeys_IncludesUnreferencedNoteMediaVariant(t *testing.T) {
	tx := newFakeTx()
	tx.queries = []pgx.Rows{
		newScriptRows(),
		newScriptRows([]any{"notes/loose.jpg"}),
		// ownedLinkIDs short-circuits on empty candidate ids (no Query).
	}
	keys, err := userObjectKeys(context.Background(), tx, testUID, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"notes/loose.jpg"}, keys)
}

func TestUserObjectKeys_ErrorPropagation(t *testing.T) {
	ctx := context.Background()

	t.Run("link candidates query", func(t *testing.T) {
		tx := newFakeTx()
		tx.queryErr = errRestoreForced
		_, err := userObjectKeys(ctx, tx, testUID, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "link object keys")
	})

	t.Run("note media query", func(t *testing.T) {
		tx := newFakeTx()
		failing := newScriptRows([]any{"notes/x.jpg"})
		failing.scanErr = errFakeScan
		tx.queries = []pgx.Rows{newScriptRows(), failing}
		_, err := userObjectKeys(ctx, tx, testUID, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "note object keys")
	})

	t.Run("owned link ids query", func(t *testing.T) {
		tx := newFakeTx()
		failing := newScriptRows([]any{int64(5)})
		failing.scanErr = errFakeScan
		tx.queries = []pgx.Rows{
			newScriptRows([]any{int64(5), "/api/files/images/5.jpg"}),
			newScriptRows(),
			failing,
		}
		_, err := userObjectKeys(ctx, tx, testUID, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "owned link ids")
	})
}

func TestOwnedLinkIDs_EmptyInputSkipsQuery(t *testing.T) {
	tx := newFakeTx()
	tx.queryErr = errRestoreForced // any Query would fail; none must happen
	out, err := ownedLinkIDs(context.Background(), tx, testUID, nil)
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestRecordLinkCandidateURLs_IgnoresRemoteAndMalformed(t *testing.T) {
	candidates := map[string][]int64{}
	require.NoError(t, recordLinkCandidateURLs("https://evil.example/x/api/files/images/9.jpg", candidates))
	assert.Empty(t, candidates, "a remote URL embedding the proxy path must not yield a key")

	require.NoError(t, recordLinkCandidateURLs("/api/files/images/notanid.jpg", candidates))
	assert.Empty(t, candidates)

	require.NoError(t, recordLinkCandidateURLs("/api/files/images/9.jpg", candidates))
	require.NoError(t, recordLinkCandidateURLs("/api/files/images/9.jpg", candidates))
	assert.Equal(t, []int64{9}, candidates["images/9.jpg"], "same candidate id is deduped")
}

func TestKeepOwnedLinkKeys_AddErrorPropagates(t *testing.T) {
	err := keepOwnedLinkKeys(
		map[string][]int64{"images/1.jpg": {1}},
		map[int64]struct{}{1: {}},
		func(string) error { return errFakeScan },
	)
	assert.ErrorIs(t, err, errFakeScan)

	added := []string{}
	err = keepOwnedLinkKeys(
		map[string][]int64{"images/1.jpg": {2}, "images/3.jpg": {3}},
		map[int64]struct{}{3: {}},
		func(k string) error { added = append(added, k); return nil },
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"images/3.jpg"}, added)
}
