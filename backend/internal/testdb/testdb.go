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

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/pwhash"
)

// pgImage is the Postgres image tests run against. It MUST equal the image
// pinned in docker-compose.db.yml AND docker-compose.services.yml so tests
// mirror prod (a version-specific planner/default change can't hide behind an
// older test engine). TestPostgresImageMatchesCompose enforces this — bump all
// three together. See CLAUDE.md §1.
const pgImage = "postgres:18.4-alpine"

// New starts a Postgres container, applies migrations from db/migrations,
// and returns a pgxpool.Pool. The container is terminated via t.Cleanup.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	// Generous timeout: this budget covers a COLD image pull (the ~400 MB
	// postgres:18.4-alpine layer) which happens inside pgmod.Run, plus connect
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

// resetStatement is a package var so TestResetCoversEveryTable can check it
// against information_schema instead of relying on review.
var resetStatement = `TRUNCATE
	    click_log, link_tag, note, link, folder, tag,
	    push_subscription, app_setting,
	    session_used_token, session, oauth_state, api_token,
	    recovery_code, totp_secret, email_otp, auth_challenge,
	    password_reset, invite, user_identity, app_user
	    RESTART IDENTITY CASCADE`

// Reset truncates all data tables but keeps the schema. CASCADE handles FK
// dependencies inside the TRUNCATE, so order is not load-bearing — but every
// data table must appear. Missing one (as the previous list missed `folder`
// and `click_log`, and later `note`) lets stale rows leak across subtests and
// produces non-deterministic failures.
func Reset(ctx context.Context, pool *pgxpool.Pool) error {
	// app_user is listed last but CASCADE does the real work: every content and
	// auth table FKs to it. It is enumerated anyway so this stays a literal
	// inventory — the previous list silently missed folder, then click_log, then
	// note, then app_setting, each time producing cross-test leakage.
	// TestResetCoversEveryTable in drift_test.go fails if a new table is added
	// without being listed here.
	_, err := pool.Exec(ctx, resetStatement)
	return err
}

// SeedUser inserts an active app_user and returns its id.
//
// Content tests call this instead of relying on migration 000017's bootstrap
// placeholder: Reset truncates that row away, and depending on a migration
// side effect for test fixtures is exactly the kind of coupling that breaks
// silently when the migration changes.
//
// email must be unique per test (app_user_email_norm_uniq).
func SeedUser(t *testing.T, pool *pgxpool.Pool, email string, role string) authctx.UserID {
	t.Helper()
	if role == "" {
		role = string(authctx.RoleUser)
	}
	var id int64
	err := pool.QueryRow(context.Background(), `
        INSERT INTO app_user (email, email_normalized, name, role, status)
        VALUES ($1, lower(btrim($1)), $2, $3, 'active')
        RETURNING id
    `, email, email, role).Scan(&id)
	if err != nil {
		t.Fatalf("testdb: seed user %q: %v", email, err)
	}
	return authctx.UserID(id)
}

// SeedUserWithPassword is SeedUser plus a real bcrypt credential, for the auth
// tests that have to drive the login endpoint end to end.
//
// It goes through pwhash rather than a fixture hash on purpose: a hardcoded
// digest would keep passing if the cost or algorithm changed underneath it,
// which is precisely the change that ought to break a login test loudly.
func SeedUserWithPassword(t *testing.T, pool *pgxpool.Pool, email, password, role string) authctx.UserID {
	t.Helper()
	id := SeedUser(t, pool, email, role)
	hash, err := pwhash.Hash(password)
	if err != nil {
		t.Fatalf("testdb: hash password: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`UPDATE app_user SET password_hash = $2, email_verified_at = now() WHERE id = $1`,
		int64(id), hash); err != nil {
		t.Fatalf("testdb: set password for %q: %v", email, err)
	}
	return id
}

// SetUserStatus flips an account's status, so tests can exercise the disabled
// and pending paths without hand-writing SQL in each one.
func SetUserStatus(t *testing.T, pool *pgxpool.Pool, id authctx.UserID, status string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE app_user SET status = $2 WHERE id = $1`, int64(id), status); err != nil {
		t.Fatalf("testdb: set status: %v", err)
	}
}
