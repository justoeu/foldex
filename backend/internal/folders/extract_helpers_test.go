package folders

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

var errFolderTx = errors.New("forced folder tx failure")

type failFolderTx struct{}

func (failFolderTx) Begin(context.Context) (pgx.Tx, error) { return failFolderTx{}, errFolderTx }
func (failFolderTx) Commit(context.Context) error          { return errFolderTx }
func (failFolderTx) Rollback(context.Context) error        { return nil }
func (failFolderTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errFolderTx
}
func (failFolderTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errFolderTx
}
func (failFolderTx) QueryRow(context.Context, string, ...any) pgx.Row {
	return failFolderRow{}
}
func (failFolderTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errFolderTx
}
func (failFolderTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { return nil }
func (failFolderTx) LargeObjects() pgx.LargeObjects                         { return pgx.LargeObjects{} }
func (failFolderTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, errFolderTx
}
func (failFolderTx) Conn() *pgx.Conn { return nil }

type failFolderRow struct{}

func (failFolderRow) Scan(...any) error { return errFolderTx }

func TestListWhere_ComposesParentAndRoot(t *testing.T) {
	t.Parallel()
	args := []any{int64(1)}
	assert.Equal(t, "WHERE f.user_id = $1", listWhere(ListQuery{}, &args))
	pid := int64(4)
	args = []any{int64(1)}
	assert.Contains(t, listWhere(ListQuery{ParentID: &pid}, &args), "f.parent_id = $2")
	args = []any{int64(1)}
	assert.Contains(t, listWhere(ListQuery{RootOnly: true}, &args), "f.parent_id IS NULL")
}

func TestUnmarshalFolderPreviews_EmptyAndCorrupt(t *testing.T) {
	t.Parallel()
	var f Folder
	require.NoError(t, unmarshalFolderPreviews(&f, nil, nil))
	err := unmarshalFolderPreviews(&f, []byte(`{`), nil)
	require.Error(t, err)
	err = unmarshalFolderPreviews(&f, nil, []byte(`{`))
	require.Error(t, err)
	require.NoError(t, unmarshalFolderPreviews(&f, []byte(`[]`), []byte(`[]`)))
}

func TestPrepareFolderUpdate_EmptyAndName(t *testing.T) {
	t.Parallel()
	got, err := prepareFolderUpdate(1, 2, UpdateInput{})
	require.NoError(t, err)
	assert.Nil(t, got)

	name := "Docs"
	got, err = prepareFolderUpdate(1, 2, UpdateInput{Name: &name})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Contains(t, got.q, "name = $1")

	hint := "not-the-password"
	got, err = prepareFolderUpdate(1, 2, UpdateInput{PasswordHintSet: true, PasswordHint: &hint})
	require.NoError(t, err)
	require.NotNil(t, got)

	got, err = prepareFolderUpdate(1, 2, UpdateInput{PasswordSet: true, Password: nil})
	require.NoError(t, err)
	require.NotNil(t, got)
}

func TestFolderUpdateGuards_SkipWhenNothingToDo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tx := failFolderTx{}
	a := &folderUpdate{uid: 1, id: 2, in: UpdateInput{}}
	require.NoError(t, authorizePasswordMutation(ctx, tx, a))
	require.NoError(t, authorizeHintMutation(ctx, tx, a))
	require.NoError(t, loadHintIfPasswordOnly(ctx, tx, a))
	require.NoError(t, rejectHintEqualsPassword(ctx, tx, a))
	require.NoError(t, rejectParentCycle(ctx, tx, a))
}

func TestFolderUpdateGuards_SurfaceTxFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tx := failFolderTx{}
	pw := "secret"
	parent := int64(9)
	a := &folderUpdate{
		uid: 1, id: 2,
		in: UpdateInput{
			PasswordSet: true, Password: &pw, CurrentPassword: &pw,
			PasswordHintSet: true, PasswordHint: &pw,
			ParentIDSet: true, ParentID: &parent,
		},
		cycleCheckNeeded: true,
		newPasswordHash:  &pw,
		hintToValidate:   &pw,
	}
	require.Error(t, authorizePasswordMutation(ctx, tx, a))
	require.Error(t, checkPasswordChangeAuthorized(ctx, tx, a.uid, a.id, a.in.CurrentPassword))
	require.Error(t, loadHintIfPasswordOnly(ctx, tx, &folderUpdate{
		uid: 1, id: 2, in: UpdateInput{PasswordSet: true}, newPasswordHash: &pw,
	}))
	require.Error(t, checkHintNotPassword(ctx, tx, 1, 2, false, nil, "hint"))
	require.Error(t, checkParentCycle(ctx, tx, 1, 2, 9))
	require.Error(t, authorizeFolderDelete(ctx, tx, 1, 2, nil, ""))
	require.Error(t, materializeCascadeSubtree(ctx, tx, 1, 2))
	require.Error(t, deleteCascadeContents(ctx, tx, authctx.UserID(1)))
	_, _, err := lockCascadeSubtree(ctx, tx, 1, 2)
	require.Error(t, err)
	_, err = execUpdate(ctx, tx, &folderUpdate{q: "select 1", args: nil})
	require.Error(t, err)
}

type seqFolderTx struct {
	n, failAt int
}

func (s *seqFolderTx) tick() bool {
	s.n++
	return s.n >= s.failAt
}
func (s *seqFolderTx) Begin(context.Context) (pgx.Tx, error) {
	if s.tick() {
		return s, errFolderTx
	}
	return s, nil
}
func (s *seqFolderTx) Commit(context.Context) error {
	if s.tick() {
		return errFolderTx
	}
	return nil
}
func (s *seqFolderTx) Rollback(context.Context) error { return nil }
func (s *seqFolderTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	if s.tick() {
		return pgconn.CommandTag{}, errFolderTx
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}
func (s *seqFolderTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	if s.tick() {
		return nil, errFolderTx
	}
	return failFolderRows{}, nil
}
func (s *seqFolderTx) QueryRow(context.Context, string, ...any) pgx.Row {
	if s.tick() {
		return failFolderRow{}
	}
	return failFolderRow{}
}
func (s *seqFolderTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errFolderTx
}
func (s *seqFolderTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { return nil }
func (s *seqFolderTx) LargeObjects() pgx.LargeObjects                         { return pgx.LargeObjects{} }
func (s *seqFolderTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, errFolderTx
}
func (s *seqFolderTx) Conn() *pgx.Conn { return nil }

func TestFolderHelpers_WalkEachTxStep(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	walk := func(fn func(pgx.Tx)) {
		t.Helper()
		for n := 1; n <= 8; n++ {
			fn(&seqFolderTx{failAt: n})
		}
	}
	walk(func(tx pgx.Tx) { _ = authorizeFolderDelete(ctx, tx, 1, 2, nil, "") })
	walk(func(tx pgx.Tx) { _ = materializeCascadeSubtree(ctx, tx, 1, 2) })
	walk(func(tx pgx.Tx) { _, _, _ = lockCascadeSubtree(ctx, tx, 1, 2) })
	walk(func(tx pgx.Tx) { _ = deleteCascadeContents(ctx, tx, 1) })
	walk(func(tx pgx.Tx) { _ = checkParentCycle(ctx, tx, 1, 2, 9) })
	walk(func(tx pgx.Tx) {
		_ = checkHintNotPassword(ctx, tx, 1, 2, false, nil, "hint")
	})
	pw := "secret"
	walk(func(tx pgx.Tx) {
		_ = authorizePasswordMutation(ctx, tx, &folderUpdate{
			uid: 1, id: 2,
			in:              UpdateInput{PasswordSet: true, Password: &pw, CurrentPassword: &pw},
			newPasswordHash: &pw,
		})
	})
}

func TestScanListedFolderRow_FailsOnBadRows(t *testing.T) {
	t.Parallel()
	_, _, _, err := scanListedFolderRow(failFolderRows{})
	require.Error(t, err)
}

type failFolderRows struct{}

func (failFolderRows) Close()                                       {}
func (failFolderRows) Err() error                                   { return errFolderTx }
func (failFolderRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (failFolderRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (failFolderRows) Next() bool                                   { return true }
func (failFolderRows) Scan(...any) error                            { return errFolderTx }
func (failFolderRows) Values() ([]any, error)                       { return nil, errFolderTx }
func (failFolderRows) RawValues() [][]byte                          { return nil }
func (failFolderRows) Conn() *pgx.Conn                              { return nil }
