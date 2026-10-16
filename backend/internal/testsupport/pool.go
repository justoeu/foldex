package testsupport

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// ClosedPool returns a pool that has already been closed: every operation
// fails immediately with "closed pool" and no network or container is
// involved. Repository error-path tests use it to prove each method
// surfaces infrastructure failure (wrapped, per §7) instead of panicking
// or swallowing it into a zero value. The DSN is never dialed — pgxpool
// connects lazily and the pool is closed before first use.
func ClosedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://nobody:invalid@127.0.0.1:1/none")
	require.NoError(t, err)
	pool.Close()
	return pool
}
