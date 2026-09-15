package backup

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var errRestoreForced = errors.New("forced restore tx failure")

type failRestoreTx struct{}

func (failRestoreTx) Begin(context.Context) (pgx.Tx, error) { return failRestoreTx{}, errRestoreForced }
func (failRestoreTx) Commit(context.Context) error          { return errRestoreForced }
func (failRestoreTx) Rollback(context.Context) error        { return nil }
func (failRestoreTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errRestoreForced
}
func (failRestoreTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errRestoreForced
}
func (failRestoreTx) QueryRow(context.Context, string, ...any) pgx.Row {
	return failRestoreRow{}
}
func (failRestoreTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errRestoreForced
}
func (failRestoreTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { return nil }
func (failRestoreTx) LargeObjects() pgx.LargeObjects                         { return pgx.LargeObjects{} }
func (failRestoreTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, errRestoreForced
}
func (failRestoreTx) Conn() *pgx.Conn { return nil }

type failRestoreRow struct{}

func (failRestoreRow) Scan(...any) error { return errRestoreForced }
