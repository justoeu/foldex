package backupagent

import (
	"context"
	"runtime"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackupAgentPoolMaxConnsIsCapped(t *testing.T) {
	raw, err := pgxpool.ParseConfig("postgres://u:p@127.0.0.1:5432/foldex?sslmode=disable")
	require.NoError(t, err)

	cfg, err := PoolConfig("postgres://u:p@127.0.0.1:5432/foldex?sslmode=disable")
	require.NoError(t, err)
	require.LessOrEqual(t, cfg.MaxConns, int32(MaxPoolConns),
		"uncapped pgxpool.New would use MaxConns=%d (GOMAXPROCS=%d)", raw.MaxConns, runtime.GOMAXPROCS(0))
	assert.Equal(t, int32(MaxPoolConns), cfg.MaxConns)

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	assert.Equal(t, int32(MaxPoolConns), pool.Stat().MaxConns())
}
