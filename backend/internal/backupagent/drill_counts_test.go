package backupagent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// queryRestoredCounts itself needs a live Postgres on a unix socket (its
// happy path runs in drill_integration_test.go). What a unit test can hold
// without that cluster is (a) the connect-failure contract of
// queryRestoredCounts and (b) collectSanityCounts — the yardstick logic the
// function hands the connection to — against a fake pgx.Row.

type fakeCountRow struct {
	value int64
	err   error
}

func (r fakeCountRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 1 {
		return errors.New("fake: want exactly one scan destination")
	}
	p, ok := dest[0].(*int64)
	if !ok {
		return errors.New("fake: scan destination is not *int64")
	}
	*p = r.value
	return nil
}

type fakeCountQuerier struct {
	byTable map[string]int64
	errOn   map[string]error // keyed by table name, or "schema_migrations"
	version int64
	queries []string
}

func (q *fakeCountQuerier) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	q.queries = append(q.queries, sql)
	for table, n := range q.byTable {
		if sql == "SELECT count(*) FROM "+table {
			if err := q.errOn[table]; err != nil {
				return fakeCountRow{err: err}
			}
			return fakeCountRow{value: n}
		}
	}
	if err := q.errOn["schema_migrations"]; err != nil {
		return fakeCountRow{err: err}
	}
	return fakeCountRow{value: q.version}
}

func fullCountQuerier() *fakeCountQuerier {
	byTable := make(map[string]int64, len(sanityTables))
	for i, table := range sanityTables {
		byTable[table] = int64(100 + i)
	}
	return &fakeCountQuerier{byTable: byTable, errOn: map[string]error{}, version: 42}
}

func TestCollectSanityCountsReadsEveryTablePlusVersion(t *testing.T) {
	q := fullCountQuerier()
	counts, version, err := collectSanityCounts(context.Background(), q)
	require.NoError(t, err)

	assert.EqualValues(t, 42, version)
	require.Len(t, counts, len(sanityTables))
	for i, table := range sanityTables {
		assert.EqualValues(t, 100+i, counts[table], "count for %s", table)
	}

	// One count query per compiled table, in list order, then the version —
	// table names come from sanityTables alone, never from data.
	require.Len(t, q.queries, len(sanityTables)+1)
	for i, table := range sanityTables {
		assert.Equal(t, "SELECT count(*) FROM "+table, q.queries[i])
	}
	assert.Equal(t, "SELECT version FROM schema_migrations", q.queries[len(sanityTables)])
}

func TestCollectSanityCountsWrapsTableErrors(t *testing.T) {
	q := fullCountQuerier()
	boom := errors.New("relation gone")
	q.errOn["link"] = boom

	counts, version, err := collectSanityCounts(context.Background(), q)
	require.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "count link")
	assert.Nil(t, counts)
	assert.Zero(t, version)
}

func TestCollectSanityCountsWrapsVersionError(t *testing.T) {
	q := fullCountQuerier()
	boom := errors.New("no schema_migrations")
	q.errOn["schema_migrations"] = boom

	counts, version, err := collectSanityCounts(context.Background(), q)
	require.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "schema version")
	assert.Nil(t, counts)
	assert.Zero(t, version)
}

func TestQueryRestoredCountsConnectFailure(t *testing.T) {
	// A socket dir that holds no Postgres socket: the connect fails before a
	// single count is read, and the caller must see which side failed.
	socketDir := filepath.Join(t.TempDir(), "no-such-cluster")
	counts, version, err := queryRestoredCounts(context.Background(), socketDir, drillDatabase, "foldex")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connect restored cluster")
	assert.Nil(t, counts)
	assert.Zero(t, version)
}

func TestQueryRestoredCountsHonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	counts, version, err := queryRestoredCounts(ctx, t.TempDir(), drillDatabase, "foldex")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connect restored cluster")
	assert.Nil(t, counts)
	assert.Zero(t, version)
}
