package backup

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type gatedClickWriter struct {
	encoded int
}

func (w *gatedClickWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"link_id"`)) {
		w.encoded++
	}
	return len(p), nil
}

type gatedClickRows struct {
	total      int
	current    int
	writer     *gatedClickWriter
	err        error
	maxPending int
}

func (r *gatedClickRows) Next() bool {
	if r.current > 0 && r.writer.encoded != r.current {
		r.err = errors.New("row source advanced before the prior row was encoded")
		return false
	}
	if r.current == r.total {
		return false
	}
	r.current++
	pending := r.current - r.writer.encoded
	if pending > r.maxPending {
		r.maxPending = pending
	}
	return true
}

func (r *gatedClickRows) Scan(dest ...any) error {
	*dest[0].(*int64) = int64(r.current)
	*dest[1].(*time.Time) = time.Unix(int64(r.current), 0).UTC()
	return nil
}

func (r *gatedClickRows) Close()                                       {}
func (r *gatedClickRows) Err() error                                   { return r.err }
func (r *gatedClickRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *gatedClickRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *gatedClickRows) Values() ([]any, error)                       { return nil, nil }
func (r *gatedClickRows) RawValues() [][]byte                          { return nil }
func (r *gatedClickRows) Conn() *pgx.Conn                              { return nil }

var _ pgx.Rows = (*gatedClickRows)(nil)

func TestSnapshotClickRowsAreEncodedIncrementally(t *testing.T) {
	const rows = 50_000
	w := &gatedClickWriter{}
	source := &gatedClickRows{total: rows, writer: w}
	encoder := snapshotStreamEncoder{w: w}

	count, err := encoder.writeRows(context.Background(), `,"click_logs":`, source, scanClickRow)
	require.NoError(t, err)
	assert.EqualValues(t, rows, count)
	assert.Equal(t, rows, w.encoded)
	assert.Equal(t, 1, source.maxPending, "the cursor may expose only the row currently being encoded")
}
