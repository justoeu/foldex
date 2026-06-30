//go:build integration

// Package testdb spins up an ephemeral Postgres container and applies the
// project's migrations. It is only compiled with `-tags=integration`.
package testdb

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	pgmod "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// pgImage is the Postgres image tests run against. It MUST equal the image
// pinned in docker-compose.db.yml AND docker-compose.services.yml so tests
// mirror prod (a version-specific planner/default change can't hide behind an
// older test engine). TestPostgresImageMatchesCompose enforces this — bump all
// three together. See CLAUDE.md §1.
const pgImage = "postgres:18.2-alpine"

// New starts a Postgres container, applies migrations from db/migrations,
// and returns a pgxpool.Pool. The container is terminated via t.Cleanup.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	// Generous timeout: this budget covers a COLD image pull (the ~400 MB
	// postgres:18.2-alpine layer) which happens inside pgmod.Run, plus connect
	// + migrations. 90s was enough only while the image stayed warm; a fresh
	// runner pulling under parallel package load (each package spins its own
	// container) blew past it. Once cached, startup is a few seconds.
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	container, err := pgmod.Run(ctx,
		pgImage,
		pgmod.WithDatabase("foldex"),
		pgmod.WithUsername("foldex"),
		pgmod.WithPassword("foldex"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("postgres container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := applyMigrations(ctx, pool); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return pool
}

func applyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	dir := migrationsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	ups := []string{}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".up.sql") {
			ups = append(ups, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(ups)
	for _, path := range ups {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			return err
		}
	}
	return nil
}

// migrationsDir locates the db/migrations folder relative to this file.
func migrationsDir() string {
	_, file, _, _ := runtime.Caller(0)
	// internal/testdb/testdb.go -> ../../db/migrations
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "db", "migrations"))
}

// Reset truncates all data tables but keeps the schema. CASCADE handles FK
// dependencies inside the TRUNCATE, so order is not load-bearing — but every
// data table must appear. Missing one (as the previous list missed `folder`
// and `click_log`, and later `note`) lets stale rows leak across subtests and
// produces non-deterministic failures.
func Reset(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `TRUNCATE click_log, link_tag, note, link, folder, tag RESTART IDENTITY CASCADE`)
	return err
}
