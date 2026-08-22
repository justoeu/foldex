package db

import (
	"context"
	"fmt"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
)

func New(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	// CLIENT spans per query against the GLOBAL tracer provider: a no-op
	// unless tracing.Setup installed a real one (OTEL_EXPORTER_OTLP_ENDPOINT
	// set). Span names are the statement's first keyword, and the full SQL
	// text attribute is disabled outright — schema, WHERE shapes and any
	// future string-built statement must not cross the wire to the collector
	// (which may be plaintext on the LAN). Parameters are never included.
	// Queries outside a request (healthz Ping, workers) produce no trace at
	// all: the root sampler only samples SERVER spans (see internal/tracing).
	cfg.ConnConfig.Tracer = otelpgx.NewTracer(
		otelpgx.WithTrimSQLInSpanName(),
		otelpgx.WithDisableSQLStatementInAttributes(),
	)
	cfg.MaxConns = 16
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 10 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}
