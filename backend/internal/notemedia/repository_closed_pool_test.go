package notemedia

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
	"foldex/internal/testsupport"
)

var errMediaForced = errors.New("forced tx failure")

const neverFail = 1 << 30

type seqMediaTx struct {
	n, failAt int
	withKey   bool
}

func (s *seqMediaTx) tick() bool {
	s.n++
	return s.n >= s.failAt
}

func (s *seqMediaTx) Begin(context.Context) (pgx.Tx, error) {
	if s.tick() {
		return s, errMediaForced
	}
	return s, nil
}
func (s *seqMediaTx) Commit(context.Context) error {
	if s.tick() {
		return errMediaForced
	}
	return nil
}
func (s *seqMediaTx) Rollback(context.Context) error { return nil }
func (s *seqMediaTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	if s.tick() {
		return pgconn.CommandTag{}, errMediaForced
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}
func (s *seqMediaTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	if s.tick() {
		return nil, errMediaForced
	}
	if s.withKey {
		return &keyMediaRows{}, nil
	}
	return emptyMediaRows{}, nil
}
func (s *seqMediaTx) QueryRow(context.Context, string, ...any) pgx.Row {
	if s.tick() {
		return failMediaRow{}
	}
	return okMediaRow{}
}
func (s *seqMediaTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	if s.tick() {
		return 0, errMediaForced
	}
	return 1, nil
}
func (s *seqMediaTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { return nil }
func (s *seqMediaTx) LargeObjects() pgx.LargeObjects                         { return pgx.LargeObjects{} }
func (s *seqMediaTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	if s.tick() {
		return nil, errMediaForced
	}
	return &pgconn.StatementDescription{}, nil
}
func (s *seqMediaTx) Conn() *pgx.Conn { return nil }

type failMediaRow struct{}

func (failMediaRow) Scan(...any) error { return errMediaForced }

type okMediaRow struct{}

func (okMediaRow) Scan(...any) error { return nil }

type emptyMediaRows struct{}

func (emptyMediaRows) Close()                                       {}
func (emptyMediaRows) TypeMap() *pgtype.Map                         { return nil }
func (emptyMediaRows) Err() error                                   { return nil }
func (emptyMediaRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (emptyMediaRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (emptyMediaRows) Next() bool                                   { return false }
func (emptyMediaRows) Scan(...any) error                            { return nil }
func (emptyMediaRows) Values() ([]any, error)                       { return nil, nil }
func (emptyMediaRows) RawValues() [][]byte                          { return nil }
func (emptyMediaRows) Conn() *pgx.Conn                              { return nil }

type keyMediaRows struct{ once bool }

func (r *keyMediaRows) Close()                                       {}
func (r *keyMediaRows) TypeMap() *pgtype.Map                         { return nil }
func (r *keyMediaRows) Err() error                                   { return nil }
func (r *keyMediaRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *keyMediaRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *keyMediaRows) Next() bool {
	if r.once {
		return false
	}
	r.once = true
	return true
}
func (r *keyMediaRows) Scan(dest ...any) error {
	*(dest[0].(*string)) = "notes/released.jpg"
	return nil
}
func (r *keyMediaRows) Values() ([]any, error) { return nil, nil }
func (r *keyMediaRows) RawValues() [][]byte    { return nil }
func (r *keyMediaRows) Conn() *pgx.Conn        { return nil }

type errMediaRows struct {
	scanErr error
	err     error
	once    bool
}

func (r *errMediaRows) Close()                                       {}
func (r *errMediaRows) TypeMap() *pgtype.Map                         { return nil }
func (r *errMediaRows) Err() error                                   { return r.err }
func (r *errMediaRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *errMediaRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *errMediaRows) Next() bool                                   { return false }
func (r *errMediaRows) Scan(...any) error                            { return r.scanErr }
func (r *errMediaRows) Values() ([]any, error)                       { return nil, nil }
func (r *errMediaRows) RawValues() [][]byte                          { return nil }
func (r *errMediaRows) Conn() *pgx.Conn                              { return nil }

type scanFailMediaRows struct{ once bool }

func (r *scanFailMediaRows) Close()                                       {}
func (r *scanFailMediaRows) TypeMap() *pgtype.Map                         { return nil }
func (r *scanFailMediaRows) Err() error                                   { return nil }
func (r *scanFailMediaRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *scanFailMediaRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *scanFailMediaRows) Next() bool {
	if r.once {
		return false
	}
	r.once = true
	return true
}
func (r *scanFailMediaRows) Scan(...any) error      { return errMediaForced }
func (r *scanFailMediaRows) Values() ([]any, error) { return nil, nil }
func (r *scanFailMediaRows) RawValues() [][]byte    { return nil }
func (r *scanFailMediaRows) Conn() *pgx.Conn        { return nil }

type closedPoolDeleter struct {
	err   error
	calls int
}

func (d *closedPoolDeleter) DeleteObject(context.Context, string) error { return d.err }
func (d *closedPoolDeleter) DeleteObjects(context.Context, []string) error {
	d.calls++
	return d.err
}

func TestPoolFunctionsClosedPool_Errors(t *testing.T) {
	t.Parallel()
	pool := testsupport.ClosedPool(t)
	ctx := context.Background()
	uid := authctx.UserID(1)

	require.Error(t, RegisterLease(ctx, pool, uid, "notes/a.jpg"), "RegisterLease")
	require.Error(t, ForgetLease(ctx, pool, uid, "notes/a.jpg"), "ForgetLease")

	deleter := &closedPoolDeleter{}
	_, err := DeleteOwnedUnreferenced(ctx, pool, uid, []string{"notes/a.jpg"}, deleter)
	require.Error(t, err, "DeleteOwnedUnreferenced fails at Begin")

	many := make([]string, DeleteBatchMax+5)
	for i := range many {
		many[i] = fmt.Sprintf("notes/%d.jpg", i)
	}
	_, err = DeleteOwnedUnreferenced(ctx, pool, uid, many, deleter)
	require.Error(t, err, "DeleteOwnedUnreferenced over-cap batch still reaches Begin")

	_, err = SystemSweepExpired(ctx, pool, deleter, 10)
	require.Error(t, err, "SystemSweepExpired fails at Begin")

	_, err = SystemSweepExpired(ctx, pool, deleter, 0)
	require.Error(t, err, "SystemSweepExpired clamps limit then fails at Begin")
}

func TestPoolFunctions_EarlyReturnsByDesign(t *testing.T) {
	t.Parallel()
	pool := testsupport.ClosedPool(t)
	ctx := context.Background()
	uid := authctx.UserID(1)

	n, err := DeleteOwnedUnreferenced(ctx, pool, uid, []string{"notes/a.jpg"}, nil)
	require.NoError(t, err, "nil storage returns before the DB")
	assert.Zero(t, n)

	n, err = DeleteOwnedUnreferenced(ctx, pool, uid, nil, &closedPoolDeleter{})
	require.NoError(t, err, "no candidates returns before the DB")
	assert.Zero(t, n)

	n, err = SystemSweepExpired(ctx, pool, nil, 10)
	require.NoError(t, err, "nil storage returns before the DB")
	assert.Zero(t, n)
}

func TestSyncRefs_EachStepSurfacesTxError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	uid := authctx.UserID(1)
	keys := []string{"notes/a.jpg"}

	_, err := SyncRefs(ctx, &seqMediaTx{failAt: 1}, uid, 1, keys)
	require.ErrorIs(t, err, errMediaForced, "delete stale refs")

	_, err = SyncRefs(ctx, &seqMediaTx{failAt: 2}, uid, 1, keys)
	require.ErrorIs(t, err, errMediaForced, "insert refs")

	_, err = SyncRefs(ctx, &seqMediaTx{failAt: 3}, uid, 1, keys)
	require.ErrorIs(t, err, errMediaForced, "claim refs")

	_, err = SyncRefs(ctx, &seqMediaTx{failAt: 2, withKey: true}, uid, 1, nil)
	require.ErrorIs(t, err, errMediaForced, "expire released after stale delete returns a key")

	released, err := SyncRefs(ctx, &seqMediaTx{failAt: neverFail}, uid, 1, keys)
	require.NoError(t, err, "fully working tx")
	assert.Nil(t, released)
}

func TestReleaseRefs_EachStepSurfacesTxError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	uid := authctx.UserID(1)

	_, err := ReleaseNoteRefs(ctx, &seqMediaTx{failAt: 1}, uid, 1)
	require.ErrorIs(t, err, errMediaForced, "ReleaseNoteRefs delete")

	_, err = ReleaseNoteRefs(ctx, &seqMediaTx{failAt: 2, withKey: true}, uid, 1)
	require.ErrorIs(t, err, errMediaForced, "ReleaseNoteRefs expire")

	_, err = ReleaseNoteRefs(ctx, &seqMediaTx{failAt: neverFail}, uid, 1)
	require.NoError(t, err, "ReleaseNoteRefs working tx")

	require.ErrorIs(t, ReleaseFolderSubtreeRefs(ctx, &seqMediaTx{failAt: 1}, uid),
		errMediaForced, "ReleaseFolderSubtreeRefs delete")
	require.ErrorIs(t, ReleaseFolderSubtreeRefs(ctx, &seqMediaTx{failAt: 2, withKey: true}, uid),
		errMediaForced, "ReleaseFolderSubtreeRefs expire")
	require.NoError(t, ReleaseFolderSubtreeRefs(ctx, &seqMediaTx{failAt: neverFail}, uid))

	require.ErrorIs(t, ReleaseOwnerRefs(ctx, &seqMediaTx{failAt: 1}, uid),
		errMediaForced, "ReleaseOwnerRefs delete")
	require.ErrorIs(t, ReleaseOwnerRefs(ctx, &seqMediaTx{failAt: 2, withKey: true}, uid),
		errMediaForced, "ReleaseOwnerRefs expire")
	require.NoError(t, ReleaseOwnerRefs(ctx, &seqMediaTx{failAt: neverFail}, uid))
}

func TestRestoreRefs_EachStepSurfacesTxError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	uid := authctx.UserID(1)
	keys := []string{"notes/a.jpg"}
	refs := []RestoredRef{{NoteID: 1, ObjectKey: "notes/a.jpg"}}

	require.ErrorIs(t, RestoreRefs(ctx, &seqMediaTx{failAt: 1}, uid, keys, refs),
		errMediaForced, "insert restored media")
	require.ErrorIs(t, RestoreRefs(ctx, &seqMediaTx{failAt: 2}, uid, keys, refs),
		errMediaForced, "create temp table")
	require.ErrorIs(t, RestoreRefs(ctx, &seqMediaTx{failAt: 3}, uid, keys, refs),
		errMediaForced, "copy refs")
	require.ErrorIs(t, RestoreRefs(ctx, &seqMediaTx{failAt: 4}, uid, keys, refs),
		errMediaForced, "insert refs")
	require.ErrorIs(t, RestoreRefs(ctx, &seqMediaTx{failAt: 5}, uid, keys, refs),
		errMediaForced, "claim restored")

	require.NoError(t, RestoreRefs(ctx, &seqMediaTx{failAt: neverFail}, uid, keys, refs))
	require.NoError(t, RestoreRefs(ctx, &seqMediaTx{failAt: neverFail}, uid, keys, nil),
		"keys only, no refs")
	require.NoError(t, RestoreRefs(ctx, &seqMediaTx{failAt: 1}, uid, nil, nil),
		"empty keys and refs return before touching the tx")
}

func TestExpireReleasedAndScanKeys_ErrorPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	uid := authctx.UserID(1)

	got, err := expireReleased(ctx, &seqMediaTx{failAt: 1}, uid, nil)
	require.NoError(t, err, "empty keys return before the DB")
	assert.Nil(t, got)

	_, err = expireReleased(ctx, &seqMediaTx{failAt: 1}, uid, []string{"notes/a.jpg"})
	require.ErrorIs(t, err, errMediaForced, "expire query")

	got, err = expireReleased(ctx, &seqMediaTx{failAt: neverFail, withKey: true}, uid, []string{"notes/a.jpg"})
	require.NoError(t, err)
	assert.Equal(t, []string{"notes/released.jpg"}, got)

	_, err = scanKeys(&scanFailMediaRows{})
	require.ErrorIs(t, err, errMediaForced, "scan failure")

	_, err = scanKeys(&errMediaRows{err: errMediaForced})
	require.ErrorIs(t, err, errMediaForced, "rows.Err after iteration")

	got, err = scanKeys(emptyMediaRows{})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestDeleteLockedObjects_ErrorPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	keys := []string{"notes/a.jpg", "notes/b.jpg"}

	got, err := deleteLockedObjects(ctx, &closedPoolDeleter{}, nil)
	require.NoError(t, err, "no keys is a no-op")
	assert.Nil(t, got)

	_, err = deleteLockedObjects(ctx, &closedPoolDeleter{err: errMediaForced}, keys)
	require.ErrorIs(t, err, errMediaForced)

	got, err = deleteLockedObjects(ctx, &closedPoolDeleter{}, keys)
	require.NoError(t, err)
	assert.Equal(t, keys, got)

	many := make([]string, objectDeleteMax+1)
	for i := range many {
		many[i] = fmt.Sprintf("notes/%d.jpg", i)
	}
	deleter := &closedPoolDeleter{}
	require.NoError(t, deleteStorageObjects(ctx, deleter, many))
	assert.Equal(t, 2, deleter.calls, "deletes are batched at objectDeleteMax")
}
