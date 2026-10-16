package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

var errFakeScan = errors.New("fake scan failure")

// scriptRows is a pgx.Rows over preset value rows. Scan assigns each
// destination from the current row by concrete destination type, so one
// fake serves every scan*Row shape in the snapshot/ledger/staging code.
type scriptRows struct {
	rows    [][]any
	idx     int
	scanErr error
	err     error
}

func newScriptRows(rows ...[]any) *scriptRows { return &scriptRows{rows: rows, idx: -1} }

func (r *scriptRows) Close()                                       {}
func (r *scriptRows) TypeMap() *pgtype.Map                         { return nil }
func (r *scriptRows) Err() error                                   { return r.err }
func (r *scriptRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *scriptRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *scriptRows) Values() ([]any, error)                       { return nil, nil }
func (r *scriptRows) RawValues() [][]byte                          { return nil }
func (r *scriptRows) Conn() *pgx.Conn                              { return nil }

func (r *scriptRows) Next() bool {
	r.idx++
	return r.idx < len(r.rows)
}

func (r *scriptRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	return assignScanDests(dest, r.rows[r.idx])
}

func assignScanDests(dest []any, values []any) error {
	if len(dest) != len(values) {
		return fmt.Errorf("fake scan: %d dests, %d values", len(dest), len(values))
	}
	for i, d := range dest {
		if err := assignScanDest(d, values[i]); err != nil {
			return err
		}
	}
	return nil
}

func assignScanDest(dest, value any) error {
	switch d := dest.(type) {
	case *int64:
		v, ok := value.(int64)
		if !ok {
			return fmt.Errorf("fake scan: %T into *int64", value)
		}
		*d = v
	case *string:
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("fake scan: %T into *string", value)
		}
		*d = v
	case **string:
		switch v := value.(type) {
		case nil:
			*d = nil
		case *string:
			*d = v
		case string:
			*d = &v
		default:
			return fmt.Errorf("fake scan: %T into **string", value)
		}
	case **int64:
		switch v := value.(type) {
		case nil:
			*d = nil
		case *int64:
			*d = v
		case int64:
			*d = &v
		default:
			return fmt.Errorf("fake scan: %T into **int64", value)
		}
	case *bool:
		v, ok := value.(bool)
		if !ok {
			return fmt.Errorf("fake scan: %T into *bool", value)
		}
		*d = v
	case *time.Time:
		v, ok := value.(time.Time)
		if !ok {
			return fmt.Errorf("fake scan: %T into *time.Time", value)
		}
		*d = v
	case *[]byte:
		switch v := value.(type) {
		case nil:
			*d = nil
		case []byte:
			*d = v
		case string:
			*d = []byte(v)
		default:
			return fmt.Errorf("fake scan: %T into *[]byte", value)
		}
	default:
		return fmt.Errorf("fake scan: unsupported dest %T", dest)
	}
	return nil
}

type fakeRow struct{ scan func(dest ...any) error }

func (r fakeRow) Scan(dest ...any) error { return r.scan(dest...) }

func valuesRow(values ...any) fakeRow {
	return fakeRow{scan: func(dest ...any) error { return assignScanDests(dest, values) }}
}

func errRow(err error) fakeRow {
	return fakeRow{scan: func(...any) error { return err }}
}

// fakeTx is a pgx.Tx whose Query pops scripted row sets in call order,
// whose QueryRow serves one scripted row, and whose Exec/CopyFrom succeed
// unless an error is planted. CopyFrom drains its source so CopyFromSlice
// callbacks run (they build the staged row values).
type fakeTx struct {
	queries   []pgx.Rows
	queryErr  error
	row       pgx.Row
	execErr   error
	execTag   pgconn.CommandTag
	copyErr   error
	commitErr error
	copied    int64
}

func newFakeTx() *fakeTx {
	return &fakeTx{row: valuesRow(), execTag: pgconn.NewCommandTag("INSERT 0 1")}
}

func (f *fakeTx) Begin(context.Context) (pgx.Tx, error) { return f, nil }
func (f *fakeTx) Commit(context.Context) error          { return f.commitErr }
func (f *fakeTx) Rollback(context.Context) error        { return nil }
func (f *fakeTx) Conn() *pgx.Conn                       { return nil }
func (f *fakeTx) LargeObjects() pgx.LargeObjects        { return pgx.LargeObjects{} }
func (f *fakeTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	return nil
}
func (f *fakeTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, errRestoreForced
}

func (f *fakeTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	if f.execErr != nil {
		return pgconn.CommandTag{}, f.execErr
	}
	return f.execTag, nil
}

func (f *fakeTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	if len(f.queries) == 0 {
		return newScriptRows(), nil
	}
	rows := f.queries[0]
	f.queries = f.queries[1:]
	return rows, nil
}

func (f *fakeTx) QueryRow(context.Context, string, ...any) pgx.Row { return f.row }

func (f *fakeTx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, src pgx.CopyFromSource) (int64, error) {
	if f.copyErr != nil {
		return 0, f.copyErr
	}
	var n int64
	for src.Next() {
		if _, err := src.Values(); err != nil {
			return n, err
		}
		n++
	}
	f.copied += n
	return n, src.Err()
}

var (
	_ pgx.Tx               = (*fakeTx)(nil)
	_ pgx.Rows             = (*scriptRows)(nil)
	_ restoreLedgerQuerier = (*fakeTx)(nil)
)

var errFakeWrite = errors.New("fake write failure")

// flakyWriter fails on the failOn-th Write (1-based); failOn <= 0 never fails.
type flakyWriter struct {
	writes int
	failOn int
}

func (w *flakyWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.failOn > 0 && w.writes >= w.failOn {
		return 0, errFakeWrite
	}
	return len(p), nil
}

const testUID = authctx.UserID(7)

func strPtr(s string) *string { return &s }

// validBackupZipBytes builds a structurally valid backup archive: a manifest
// whose checksums match database.json (and any extra entries), so preflight
// reaches the snapshot checks instead of stopping at integrity.
func validBackupReader(t *testing.T, databaseJSON []byte, extra ...struct {
	name string
	body []byte
}) (*zip.Reader, Manifest) {
	t.Helper()
	checksums := map[string]string{}
	digest := sha256.Sum256(databaseJSON)
	checksums[snapshotDBName] = sha256Prefix + hex.EncodeToString(digest[:])
	for _, e := range extra {
		d := sha256.Sum256(e.body)
		checksums[e.name] = sha256Prefix + hex.EncodeToString(d[:])
	}
	manifest := Manifest{
		Kind: ManifestKind, Version: ManifestVersion,
		SchemaVersion: CurrentSchemaVersion, Checksums: checksums,
	}
	manifestBytes, err := json.Marshal(manifest)
	require.NoError(t, err)
	entries := []struct {
		name string
		body []byte
	}{{"manifest.json", manifestBytes}, {snapshotDBName, databaseJSON}}
	entries = append(entries, extra...)
	return zipReaderWithEntries(t, entries...), manifest
}
