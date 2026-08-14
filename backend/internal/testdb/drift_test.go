//go:build integration

package testdb

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/auth"
	"foldex/internal/pkg/secrets"
)

var composePostgresImage = regexp.MustCompile(`(?m)^\s*image:\s*(postgres:\S+)`)

// TestPostgresImageMatchesCompose fails if the test-container image drifts from
// the image pinned in the compose files — the exact silent mismatch that let
// the suite run on PG16 while prod (compose) ran PG18. Pure file read, no
// container; cheap enough to ride along the integration suite.
func TestPostgresImageMatchesCompose(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	// backend/internal/testdb/<this file> -> repo root
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))

	for _, name := range []string{"docker-compose.db.yml", "docker-compose.services.yml"} {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		m := composePostgresImage.FindSubmatch(body)
		if m == nil {
			t.Fatalf("%s: no `image: postgres:...` line found", name)
		}
		if got := string(m[1]); got != pgImage {
			t.Errorf("%s pins %q but testdb.pgImage is %q — bump them together (CLAUDE.md §1)", name, got, pgImage)
		}
	}
}

// TestResetCoversEveryTable fails when a migration adds a table that Reset does
// not truncate.
//
// Reset's list has silently missed a table three times already (folder, then
// click_log, then note, then app_setting), each time producing rows that leaked
// across subtests and non-deterministic failures. Migration 000017 added twelve
// tables at once, so the list is now far too long to keep correct by eye.
func TestResetCoversEveryTable(t *testing.T) {
	ctx := context.Background()
	pool := New(t)

	rows, err := pool.Query(ctx, `
        SELECT table_name FROM information_schema.tables
        WHERE table_schema = 'public' AND table_type = 'BASE TABLE'
          AND table_name <> 'schema_migrations'
        ORDER BY table_name`)
	require.NoError(t, err)
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		tables = append(tables, name)
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, tables, "fixture precondition: migrations created tables")

	// Word-boundary match, not strings.Contains.
	//
	// The comment above this test used to say "asserting behaviour beats
	// string-matching the TRUNCATE statement" while the code did exactly the
	// string match it disclaimed — and the failure mode is specific: a new
	// table whose name is a SUBSTRING of a listed one passes without ever being
	// truncated. `user` inside `app_user`, `session` inside
	// `session_used_token`, `token` inside `api_token` are all live examples.
	//
	// That was one leaky test before; since the suite moved to one shared
	// database per package, Reset is the ONLY isolation there is, so a missed
	// table leaks rows into every later test in the package.
	var missing []string
	for _, name := range tables {
		if !truncatesTable(resetStatement, name) {
			missing = append(missing, name)
		}
	}
	assert.Empty(t, missing,
		"these tables exist but are not truncated by testdb.Reset — add them or every "+
			"later test in the package inherits their rows")
}

// truncatesTable reports whether stmt names table as a whole word.
func truncatesTable(stmt, table string) bool {
	for _, field := range strings.FieldsFunc(stmt, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t'
	}) {
		if field == table {
			return true
		}
	}
	return false
}

// Reset is the only thing standing between one test's rows and the next test's
// assertions now that a package shares one database. This proves it actually
// empties them rather than merely naming them.
func TestResetActuallyEmptiesTheTables(t *testing.T) {
	ctx := context.Background()
	pool := New(t)

	uid := SeedUser(t, pool, "drift@test.local", "user")
	_, err := pool.Exec(ctx,
		`INSERT INTO link (user_id, url, title, slug) VALUES ($1, 'https://x.test', 'x', 'x')`,
		int64(uid))
	require.NoError(t, err)

	require.NoError(t, Reset(ctx, pool))

	for _, table := range []string{"app_user", "link"} {
		var n int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n))
		assert.Zero(t, n, "%s still holds rows after Reset", table)
	}

	// RESTART IDENTITY is load-bearing too: tests assert on uid = 1.
	next := SeedUser(t, pool, "again@test.local", "user")
	assert.EqualValues(t, 1, next, "Reset must restart the identity sequences")
}

func TestMigration000022IsReversible(t *testing.T) {
	ctx := context.Background()
	pool := New(t)
	dir := migrationsDir()
	down, err := os.ReadFile(filepath.Join(dir, "000022_note_media_ownership.down.sql"))
	require.NoError(t, err)
	up, err := os.ReadFile(filepath.Join(dir, "000022_note_media_ownership.up.sql"))
	require.NoError(t, err)

	_, err = pool.Exec(ctx, string(down))
	require.NoError(t, err, "000022 down migration must apply cleanly")
	var removed *string
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass('public.note_media')::text`).Scan(&removed))
	assert.Nil(t, removed)

	_, err = pool.Exec(ctx, string(up))
	require.NoError(t, err, "000022 up migration must reapply after down")
	var restored string
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass('public.note_media')::text`).Scan(&restored))
	assert.Equal(t, "note_media", restored)
}

func TestMigration000025IsReversibleAndLegacyChallengesFailClosed(t *testing.T) {
	ctx := context.Background()
	pool := New(t)
	dir := migrationsDir()
	down, err := os.ReadFile(filepath.Join(dir, "000025_auth_challenge_credential_epoch.down.sql"))
	require.NoError(t, err)
	up, err := os.ReadFile(filepath.Join(dir, "000025_auth_challenge_credential_epoch.up.sql"))
	require.NoError(t, err)

	_, err = pool.Exec(ctx, string(down))
	require.NoError(t, err, "000025 down migration must apply cleanly")
	uid := SeedUser(t, pool, "legacy-challenge@test.local", "user")
	legacyRaw := "legacy-challenge-token"
	_, err = pool.Exec(ctx, `
		INSERT INTO auth_challenge (user_id, token_hash, purpose, expires_at)
		VALUES ($1, $2, 'totp', now() + interval '10 minutes')`, int64(uid), secrets.Hash(legacyRaw))
	require.NoError(t, err)

	_, err = pool.Exec(ctx, string(up))
	require.NoError(t, err, "000025 up migration must preserve legacy challenges")
	var legacyVersion *int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT token_version FROM auth_challenge WHERE token_hash = $1`, secrets.Hash(legacyRaw)).Scan(&legacyVersion))
	assert.Nil(t, legacyVersion, "migration invented a credential epoch for an old proof")
	_, err = auth.NewRepository(pool).ResolveChallenge(ctx, legacyRaw, auth.PurposeTOTP)
	assert.ErrorIs(t, err, auth.ErrChallengeInvalid, "a pre-migration proof remained usable")
	_, err = pool.Exec(ctx, `
		UPDATE auth_challenge SET consumed_at = now()
		WHERE token_hash = $1 AND token_version IS NOT NULL`, secrets.Hash(legacyRaw))
	require.NoError(t, err, "runtime superseding must skip fail-closed legacy rows")

	_, err = pool.Exec(ctx, `
		INSERT INTO auth_challenge (user_id, token_hash, purpose, expires_at)
		VALUES ($1, $2, 'totp', now() + interval '10 minutes')`, int64(uid), []byte("new-without-epoch"))
	require.Error(t, err, "new challenges without a credential epoch must be refused")

	_, err = pool.Exec(ctx, string(down))
	require.NoError(t, err, "000025 must roll back after reapplying")
	var columnExists bool
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'auth_challenge'
			  AND column_name = 'token_version'
		)`).Scan(&columnExists))
	assert.False(t, columnExists)
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'totp_secret'
			  AND column_name = 'enrollment_token_version'
		)`).Scan(&columnExists))
	assert.False(t, columnExists)
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'totp_secret'
			  AND column_name = 'enrollment_session_id'
		)`).Scan(&columnExists))
	assert.False(t, columnExists)
}

func TestMigration000026PendingPreviewIndexIsReversible(t *testing.T) {
	ctx := context.Background()
	pool := New(t)
	dir := migrationsDir()
	down, err := os.ReadFile(filepath.Join(dir, "000026_pending_preview_index.down.sql"))
	require.NoError(t, err)
	up, err := os.ReadFile(filepath.Join(dir, "000026_pending_preview_index.up.sql"))
	require.NoError(t, err)

	_, err = pool.Exec(ctx, string(down))
	require.NoError(t, err)
	var index *string
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass('public.link_preview_pending_idx')::text`).Scan(&index))
	assert.Nil(t, index)

	_, err = pool.Exec(ctx, string(up))
	require.NoError(t, err)
	var restored string
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass('public.link_preview_pending_idx')::text`).Scan(&restored))
	assert.Equal(t, "link_preview_pending_idx", restored)
}

func TestMigration000027BackupRestoreLedgerIsReversible(t *testing.T) {
	ctx := context.Background()
	pool := New(t)
	dir := migrationsDir()
	down, err := os.ReadFile(filepath.Join(dir, "000027_backup_restore_ledger.down.sql"))
	require.NoError(t, err)
	up, err := os.ReadFile(filepath.Join(dir, "000027_backup_restore_ledger.up.sql"))
	require.NoError(t, err)

	_, err = pool.Exec(ctx, string(down))
	require.NoError(t, err)
	var removed *string
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass('public.backup_restore')::text`).Scan(&removed))
	assert.Nil(t, removed)

	_, err = pool.Exec(ctx, string(up))
	require.NoError(t, err)
	for _, table := range []string{"backup_restore", "backup_restore_entity", "backup_restore_file"} {
		var restored string
		require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass('public.' || $1)::text`, table).Scan(&restored))
		assert.Equal(t, table, restored)
	}
}

func TestMigration000028IsReversibleAndLegacyPasswordResetsFailClosed(t *testing.T) {
	ctx := context.Background()
	pool := New(t)
	dir := migrationsDir()
	down, err := os.ReadFile(filepath.Join(dir, "000028_password_reset_credential_epoch.down.sql"))
	require.NoError(t, err)
	up, err := os.ReadFile(filepath.Join(dir, "000028_password_reset_credential_epoch.up.sql"))
	require.NoError(t, err)

	_, err = pool.Exec(ctx, string(down))
	require.NoError(t, err, "000028 down migration must apply cleanly")
	uid := SeedUserWithPassword(t, pool, "legacy-reset@test.local", "a good password", "user")
	legacyRaw := "legacy-password-reset-token"
	_, err = pool.Exec(ctx, `
		INSERT INTO password_reset (user_id, token_hash, expires_at)
		VALUES ($1, $2, now() + interval '10 minutes')`, int64(uid), secrets.Hash(legacyRaw))
	require.NoError(t, err)

	var hashBefore string
	var epochBefore int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT password_hash, token_version FROM app_user WHERE id = $1`, int64(uid)).
		Scan(&hashBefore, &epochBefore))
	_, err = pool.Exec(ctx, string(up))
	require.NoError(t, err, "000028 up migration must preserve legacy password resets")
	var legacyVersion *int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT token_version FROM password_reset WHERE token_hash = $1`, secrets.Hash(legacyRaw)).Scan(&legacyVersion))
	assert.Nil(t, legacyVersion, "migration invented a credential epoch for an old reset link")

	_, err = auth.NewRepository(pool).ConsumePasswordReset(ctx, legacyRaw, "a replacement password")
	assert.ErrorIs(t, err, auth.ErrResetInvalid, "a pre-migration reset link remained usable")
	var hashAfter string
	var epochAfter int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT password_hash, token_version FROM app_user WHERE id = $1`, int64(uid)).
		Scan(&hashAfter, &epochAfter))
	assert.Equal(t, hashBefore, hashAfter)
	assert.Equal(t, epochBefore, epochAfter)

	_, err = pool.Exec(ctx, `
		INSERT INTO password_reset (user_id, token_hash, expires_at)
		VALUES ($1, $2, now() + interval '10 minutes')`, int64(uid), []byte("new-without-epoch"))
	require.Error(t, err, "new password resets without a credential epoch must be refused")

	raw, err := auth.NewRepository(pool).CreatePasswordReset(ctx, uid, time.Minute, "")
	require.NoError(t, err)
	var resetVersion, liveVersion int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT r.token_version, u.token_version
		FROM password_reset r JOIN app_user u ON u.id = r.user_id
		WHERE r.token_hash = $1`, secrets.Hash(raw)).Scan(&resetVersion, &liveVersion))
	assert.Equal(t, liveVersion, resetVersion)

	_, err = pool.Exec(ctx, string(down))
	require.NoError(t, err, "000028 must roll back after reapplying")
	var columnExists bool
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'password_reset'
			  AND column_name = 'token_version'
		)`).Scan(&columnExists))
	assert.False(t, columnExists)
}
