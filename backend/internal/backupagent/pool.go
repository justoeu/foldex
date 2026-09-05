package backupagent

import (
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxPoolConns is the agent's pgx ceiling. The backend pool is 16
// (internal/db); compose Postgres defaults to 100. Dump holds one
// REPEATABLE READ connection for the whole pg_dump, and user_zip takes
// a few more — 4 leaves headroom for the backend without matching
// GOMAXPROCS the way pgxpool.New would.
const MaxPoolConns = 4

// PoolConfig parses dsn and applies the agent pool ceiling.
func PoolConfig(dsn string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("backupagent: parse dsn: %w", err)
	}
	cfg.MaxConns = MaxPoolConns
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 10 * time.Minute
	return cfg, nil
}
