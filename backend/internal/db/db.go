package db

import (
	"context"
	"fmt"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
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
		// otelpgx's own connection details are turned off and re-added by
		// connAttrs, which is the same set MINUS user.name — see there.
		otelpgx.WithDisableConnectionDetailsInAttributes(),
		otelpgx.WithTracerAttributes(connAttrs(cfg.ConnConfig)...),
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

// connAttrs is otelpgx's connection-detail set with user.name REMOVED.
//
// semconv has one `user.name` and this application has two subjects that fit
// it: the Postgres role the pool authenticates as, and the account whose
// request is being served. otelpgx stamps the first on every query span; the
// second is what internal/tracing.AnnotatePrincipal puts on the SERVER span as
// `user.id` (INV-170). In one trace the two read as a matching pair — id and
// name of the same person — and they are not. The failure is silent and
// convincing: a dashboard grouping "requests by user.name" answers
// `user_foldex` for 100% of traffic and looks like a working breakdown rather
// than a broken query, which is worse than having no such panel at all.
//
// Dropping it costs no diagnostic power. It is a deployment CONSTANT, already
// in DB_URL and in the compose file, and a query span that needed it would
// need the whole DSN anyway. server.address, server.port and db.namespace are
// kept because those do vary per deployment and do answer "which database did
// this span talk to". db.system.name is already in otelpgx's defaults and is
// deliberately not repeated here.
func connAttrs(cc *pgx.ConnConfig) []attribute.KeyValue {
	return []attribute.KeyValue{
		semconv.ServerAddress(cc.Host),
		semconv.ServerPort(int(cc.Port)),
		semconv.DBNamespace(cc.Database),
	}
}
