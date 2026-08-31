//go:build integration

// Package testdb spins up an ephemeral Postgres container and applies the
// project's migrations. It is only compiled with `-tags=integration`.
package testdb

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
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
const pgImage = "postgres:18.6-alpine"

// New starts a DEDICATED Postgres container, applies the migrations and returns
// a pool. The container is terminated via t.Cleanup.
//
// Prefer Shared. This exists for the handful of tests that mutate the SCHEMA —
// `DROP TABLE recovery_code`, `ALTER TABLE app_user RENAME` — to reach the
// error branches a healthy database cannot produce. Those cannot run against a
// shared database, because the damage outlives the test that did it. Every
// other test only touches DATA, which Reset undoes.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return newPool(t, startContainer(t.Fatalf, func(fn func()) { t.Cleanup(fn) }))
}

// container is one running Postgres plus the DSN to reach it.
type container struct {
	dsn       string
	terminate func()
}

// shared is the per-BINARY container. One per package, created on first use.
//
// Sharing is safe because no integration test in this repo calls t.Parallel():
// tests inside a package run one at a time, and each harness calls Reset first,
// so the only thing that crosses a test boundary is the schema — which is what
// the migrations put there and what Reset deliberately leaves alone.
//
// The win is not just speed. Every container start is a chance to fail, and
// before this the suite was starting 171 of them; the failures that produced
// ("connection refused", "unexpected EOF" during migrations) were never bugs in
// the code under test, only in how many Postgreses were being asked for at once.
var (
	sharedOnce sync.Once
	sharedC    *container
	sharedErr  error
)

// Shared returns a CLEAN database on the test binary's shared container.
//
// It resets before handing the pool back, and that is deliberately part of the
// contract rather than the caller's job. Only one package's harness used to
// call Reset; the other fifteen relied on getting a brand-new container, so
// making the reset opt-in would have meant fifteen chances to forget — and the
// symptom is not a clear failure but a duplicate-key error in whichever test
// happens to seed the same e-mail second.
//
// Call it ONCE per test. A second call resets again, which is fine when the
// second pool is only there to be closed, and wrong when a test genuinely needs
// two INDEPENDENT databases — those must use New. There is exactly one such
// test (backup's cross-database restore) and it says so at the call site.
//
// Each caller gets its OWN pgxpool, closed by t.Cleanup. That matters: several
// tests close the pool deliberately to prove that every repository method
// surfaces a database error rather than returning a zero value, and a shared
// pool would take the rest of the package down with it.
//
// Requires StopShared from TestMain. Without it the container outlives the run:
// the Makefile disables testcontainers' reaper, so nothing else will clean up.
func Shared(t *testing.T) *pgxpool.Pool {
	t.Helper()
	sharedOnce.Do(func() {
		defer func() {
			if r := recover(); r != nil {
				sharedErr = fmt.Errorf("shared container: %v", r)
			}
		}()
		sharedC = startContainer(func(format string, args ...any) {
			panic(fmt.Sprintf(format, args...))
		}, func(func()) {})
	})
	if sharedErr != nil {
		t.Fatalf("testdb: %v", sharedErr)
	}
	pool := newPool(t, sharedC)
	if err := Reset(context.Background(), pool); err != nil {
		t.Fatalf("testdb: reset shared database: %v", err)
	}
	return pool
}

// StopShared terminates the shared container. Call it from TestMain AFTER
// m.Run() returns:
//
//	func TestMain(m *testing.M) {
//	    code := m.Run()
//	    testdb.StopShared()
//	    os.Exit(code)
//	}
//
// os.Exit skips deferred functions, which is exactly why this cannot be a
// t.Cleanup on whichever test happened to be first: that would terminate the
// container while the rest of the package still needed it.
func StopShared() {
	if sharedC != nil {
		sharedC.terminate()
		sharedC = nil
	}
}

// startContainer boots Postgres and applies the migrations once.
//
// fatalf and cleanup are injected because this runs both under a *testing.T
// (New) and outside one (Shared, from sync.Once, where there is no T to fail).
func startContainer(fatalf func(string, ...any), cleanup func(func())) *container {
	// Generous timeout: this budget covers a COLD image pull (the ~400 MB
	// postgres:18.6-alpine layer) which happens inside pgmod.Run, plus connect
	// + migrations. 90s was enough only while the image stayed warm; a fresh
	// runner pulling under parallel package load (each package spins its own
	// container) blew past it. Once cached, startup is a few seconds.
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	c, err := pgmod.Run(ctx,
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
		fatalf("postgres container: %v", err)
	}
	terminate := func() { _ = c.Terminate(context.Background()) }
	cleanup(terminate)

	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fatalf("connection string: %v", err)
	}

	// Migrations run ONCE per container. Reset undoes data between tests; the
	// schema is what survives, and that is the whole point of sharing.
	migrator, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fatalf("pgxpool: %v", err)
	}
	defer migrator.Close()
	if err := applyMigrations(ctx, migrator); err != nil {
		fatalf("apply migrations: %v", err)
	}
	return &container{dsn: dsn, terminate: terminate}
}

// newPool opens a pool over an already-migrated container, scoped to one test.
func newPool(t *testing.T, c *container) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), c.dsn)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)
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
	    backup_restore_file, backup_restore_entity, backup_restore,
	    note_media_ref, note_media,
	    click_log, link_tag, note, link, folder, tag,
	    push_subscription, app_setting, audit_log, mail_outbox,
	    session_used_token, session, oauth_state, api_token,
	    recovery_code, totp_secret, email_factor, email_otp, auth_challenge,
	    password_reset, email_change, invite, user_identity, app_user,
	    role_permission, backup_run, backup_schedule, backup_agent_state, ip_block
	    RESTART IDENTITY CASCADE`

// reseedRolePermissions restores the rows a MIGRATION put there, which
// TRUNCATE removes along with the test's own.
//
// role_permission is the only such table: it ships seeded with the compiled
// matrix (migration 000039), and a Reset that left it empty would not be a
// clean database — it would be an instance where every editable role has been
// stripped to its locked floor. A later test building a real roleperm
// repository would then watch ordinary content writes answer 403 with nothing
// pointing at the cause. Reset means "the state right after migrating", and
// for a seeded table that includes the seed.
//
// DERIVED from authctx rather than a second SQL literal. A verbatim copy of the
// migration's INSERT was written first, and it drifts the moment a permission
// is added: the migration and this list must then be edited together, nothing
// checks that they were, and the divergence surfaces as a 403 in an unrelated
// test. Locked entries are skipped for the same reason the repository never
// stores them — resolution puts them back from the compiled matrix, so a row
// here would be a second source for the one part of the matrix that must not
// have one.
func reseedRolePermissions(ctx context.Context, pool *pgxpool.Pool) error {
	compiled := authctx.DefaultGrants()
	for _, role := range authctx.AllRoles {
		if !authctx.IsRoleEditable(role) {
			continue
		}
		var perms []string
		for _, p := range authctx.AllPermissions {
			if compiled[role][p] && !authctx.IsPermissionLocked(p) {
				perms = append(perms, string(p))
			}
		}
		if len(perms) == 0 {
			continue
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO role_permission (role, permission)
			 SELECT $1, p FROM unnest($2::text[]) AS p
			 ON CONFLICT DO NOTHING`, string(role), perms); err != nil {
			return err
		}
	}
	return nil
}

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
	if _, err := pool.Exec(ctx, resetStatement); err != nil {
		return err
	}
	// See reseedRolePermissions: a truncated role_permission is not a clean
	// database.
	return reseedRolePermissions(ctx, pool)
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
		role = string(authctx.RoleEditor)
	}
	return seedUserWithHash(t, pool, email, role, unusablePasswordHash)
}

// unusablePasswordHash is a well-formed bcrypt digest of a random value that
// nothing in the suite knows. It satisfies "this active account has a
// credential" without being a password any test can present, so a test that
// accidentally relies on logging in as a seeded user fails rather than passing
// by coincidence.
var unusablePasswordHash = mustHash("seed-only-never-a-real-password")

func mustHash(pw string) string {
	h, err := pwhash.Hash(pw)
	if err != nil {
		panic("testdb: cannot hash seed password: " + err.Error())
	}
	return h
}

func seedUserWithHash(t *testing.T, pool *pgxpool.Pool, email, role, hash string) authctx.UserID {
	t.Helper()
	// An ACTIVE account must hold a credential — migration 000021 enforces that
	// with a constraint trigger, because an account with neither a password nor
	// a linked identity cannot be signed into or repaired through any UI. The
	// placeholder hash below is not a login: it is bcrypt of a value no test
	// types, and SeedUserWithPassword overwrites it when a real one is needed.
	//
	// Seeding without it used to work and now fails loudly, which is the point:
	// the fixture was manufacturing a state the product forbids.
	var id int64
	err := pool.QueryRow(context.Background(), `
        INSERT INTO app_user (email, email_normalized, name, role, status, password_hash)
        VALUES ($1, lower(btrim($1)), $2, $3, 'active', $4)
        RETURNING id
    `, email, email, role, hash).Scan(&id)
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

// ConvertToGoogleOnly models what an ADR-31 conversion leaves behind: a linked
// provider identity and NO password.
//
// It must run in ONE transaction, and the two statements cannot be split. An
// ACTIVE account is required to hold at least one credential (migration
// 000021's constraint trigger), and the trigger is DEFERRABLE — it judges the
// state at COMMIT. Nulling the password in its own autocommit statement is a
// commit with no credential, and the database refuses it. That is the same
// reason the production path does both inside one transaction.
func ConvertToGoogleOnly(t *testing.T, pool *pgxpool.Pool, id authctx.UserID, googleEmail, subject string) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("testdb: convert begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
        INSERT INTO user_identity (user_id, provider, subject, email_at_link, last_login_at)
        VALUES ($1, 'google', $2, $3, now())
    `, int64(id), subject, googleEmail); err != nil {
		t.Fatalf("testdb: link identity: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE app_user SET password_hash = NULL WHERE id = $1`, int64(id)); err != nil {
		t.Fatalf("testdb: strip password: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("testdb: convert commit: %v", err)
	}
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
