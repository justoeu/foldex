package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"foldex/internal/pkg/authctx"
)

var errForced = errors.New("forced tx failure")

type failTx struct{}

func (failTx) Begin(context.Context) (pgx.Tx, error) { return failTx{}, errForced }
func (failTx) Commit(context.Context) error          { return errForced }
func (failTx) Rollback(context.Context) error        { return nil }
func (failTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errForced
}
func (failTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errForced
}
func (failTx) QueryRow(context.Context, string, ...any) pgx.Row { return failRow{} }
func (failTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errForced
}
func (failTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { return nil }
func (failTx) LargeObjects() pgx.LargeObjects                         { return pgx.LargeObjects{} }
func (failTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, errForced
}
func (failTx) Conn() *pgx.Conn { return nil }

type failRow struct{}

func (failRow) Scan(...any) error { return errForced }

type okRow struct{}

func (okRow) Scan(...any) error { return nil }

type emptyRows struct{}

func (emptyRows) Close()                                       {}
func (emptyRows) TypeMap() *pgtype.Map                         { return nil }
func (emptyRows) Err() error                                   { return nil }
func (emptyRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (emptyRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (emptyRows) Next() bool                                   { return false }
func (emptyRows) Scan(...any) error                            { return nil }
func (emptyRows) Values() ([]any, error)                       { return nil, nil }
func (emptyRows) RawValues() [][]byte                          { return nil }
func (emptyRows) Conn() *pgx.Conn                              { return nil }

type seqTx struct {
	n, failAt int
	zeroRows  bool
}

func (s *seqTx) tick() bool {
	s.n++
	return s.n >= s.failAt
}

func (s *seqTx) Begin(context.Context) (pgx.Tx, error) {
	if s.tick() {
		return s, errForced
	}
	return s, nil
}
func (s *seqTx) Commit(context.Context) error {
	if s.tick() {
		return errForced
	}
	return nil
}
func (s *seqTx) Rollback(context.Context) error { return nil }
func (s *seqTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	if s.tick() {
		return pgconn.CommandTag{}, errForced
	}
	if s.zeroRows {
		return pgconn.NewCommandTag("UPDATE 0"), nil
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}
func (s *seqTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	if s.tick() {
		return nil, errForced
	}
	return emptyRows{}, nil
}
func (s *seqTx) QueryRow(context.Context, string, ...any) pgx.Row {
	if s.tick() {
		return failRow{}
	}
	return okRow{}
}
func (s *seqTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	if s.tick() {
		return 0, errForced
	}
	return 1, nil
}
func (s *seqTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { return nil }
func (s *seqTx) LargeObjects() pgx.LargeObjects                         { return pgx.LargeObjects{} }
func (s *seqTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	if s.tick() {
		return nil, errForced
	}
	return &pgconn.StatementDescription{}, nil
}
func (s *seqTx) Conn() *pgx.Conn { return nil }

type commitFailTx struct{ failTx }

func (commitFailTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("UPDATE 1"), nil
}
func (commitFailTx) Commit(context.Context) error { return errForced }

type failRows struct {
	err  error
	once bool
}

func (r *failRows) Close()                                       {}
func (r *failRows) TypeMap() *pgtype.Map                         { return nil }
func (r *failRows) Err() error                                   { return r.err }
func (r *failRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *failRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *failRows) Next() bool {
	if r.once {
		return false
	}
	r.once = true
	return true
}
func (r *failRows) Scan(...any) error      { return r.err }
func (r *failRows) Values() ([]any, error) { return nil, r.err }
func (r *failRows) RawValues() [][]byte    { return nil }
func (r *failRows) Conn() *pgx.Conn        { return nil }

func ancient() time.Time { return time.Now().Add(-48 * time.Hour) }

func dummyEnrollment() EnrollmentComplete {
	return EnrollmentComplete{UID: authctx.UserID(1), Session: LiveSession{ID: 1}}
}
