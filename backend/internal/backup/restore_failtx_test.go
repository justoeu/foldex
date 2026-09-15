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

type okRestoreRow struct{}

func (okRestoreRow) Scan(...any) error { return nil }

type emptyRestoreRows struct{}

func (emptyRestoreRows) Close()                                       {}
func (emptyRestoreRows) Err() error                                   { return nil }
func (emptyRestoreRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (emptyRestoreRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (emptyRestoreRows) Next() bool                                   { return false }
func (emptyRestoreRows) Scan(...any) error                            { return nil }
func (emptyRestoreRows) Values() ([]any, error)                       { return nil, nil }
func (emptyRestoreRows) RawValues() [][]byte                          { return nil }
func (emptyRestoreRows) Conn() *pgx.Conn                              { return nil }

type seqRestoreTx struct {
	n, failAt int
}

func (s *seqRestoreTx) tick() bool {
	s.n++
	return s.n >= s.failAt
}
func (s *seqRestoreTx) Begin(context.Context) (pgx.Tx, error) {
	if s.tick() {
		return s, errRestoreForced
	}
	return s, nil
}
func (s *seqRestoreTx) Commit(context.Context) error {
	if s.tick() {
		return errRestoreForced
	}
	return nil
}
func (s *seqRestoreTx) Rollback(context.Context) error { return nil }
func (s *seqRestoreTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	if s.tick() {
		return pgconn.CommandTag{}, errRestoreForced
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}
func (s *seqRestoreTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	if s.tick() {
		return nil, errRestoreForced
	}
	return emptyRestoreRows{}, nil
}
func (s *seqRestoreTx) QueryRow(context.Context, string, ...any) pgx.Row {
	if s.tick() {
		return failRestoreRow{}
	}
	return okRestoreRow{}
}
func (s *seqRestoreTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	if s.tick() {
		return 0, errRestoreForced
	}
	return 1, nil
}
func (s *seqRestoreTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { return nil }
func (s *seqRestoreTx) LargeObjects() pgx.LargeObjects                         { return pgx.LargeObjects{} }
func (s *seqRestoreTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, errRestoreForced
}
func (s *seqRestoreTx) Conn() *pgx.Conn { return nil }
