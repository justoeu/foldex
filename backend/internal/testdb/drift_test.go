//go:build integration

package testdb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/auth"
	"foldex/internal/pkg/secrets"
	"foldex/internal/pkg/slug"
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

	uid := SeedUser(t, pool, "drift@test.local", "editor")
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
	next := SeedUser(t, pool, "again@test.local", "editor")
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
	uid := SeedUser(t, pool, "legacy-challenge@test.local", "editor")
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
	uid := SeedUserWithPassword(t, pool, "legacy-reset@test.local", "a good password", "editor")
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

	raw, err := auth.NewRepository(pool).CreatePasswordReset(ctx, uid, time.Minute, "", auth.MailDraft{})
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

func TestMigration000029RepairsOverlongSlugsAndEnforcesLimit(t *testing.T) {
	ctx := context.Background()
	pool := New(t)
	dir := migrationsDir()
	down, err := os.ReadFile(filepath.Join(dir, "000029_slug_length.down.sql"))
	require.NoError(t, err)
	up, err := os.ReadFile(filepath.Join(dir, "000029_slug_length.up.sql"))
	require.NoError(t, err)

	_, err = pool.Exec(ctx, string(down))
	require.NoError(t, err, "000029 down migration must apply cleanly")
	uid := SeedUser(t, pool, "legacy-slug@test.local", "editor")
	base := strings.Repeat("a", slug.MaxLen)
	values := []string{base}
	for suffix := 2; suffix <= 100; suffix++ {
		values = append(values, fmt.Sprintf("%s-%d", base, suffix))
	}
	values = append(values,
		strings.Repeat("1", 78)+"-a-2",
		strings.Repeat("2", slug.MaxLen)+"-a",
	)
	linkIDs := make([]int64, 0, len(values))
	noteIDs := make([]int64, 0, len(values))
	for index, value := range values {
		var linkID int64
		err = pool.QueryRow(ctx, `
			INSERT INTO link (user_id, url, title, slug)
			VALUES ($1, $2, $3, $4) RETURNING id`,
			int64(uid), fmt.Sprintf("https://legacy-%d.test", index), fmt.Sprintf("legacy-%d", index), value).Scan(&linkID)
		require.NoError(t, err)
		linkIDs = append(linkIDs, linkID)
		var noteID int64
		err = pool.QueryRow(ctx, `
			INSERT INTO note (user_id, title, slug)
			VALUES ($1, $2, $3) RETURNING id`, int64(uid), fmt.Sprintf("legacy-%d", index), value).Scan(&noteID)
		require.NoError(t, err)
		noteIDs = append(noteIDs, noteID)
	}

	_, err = pool.Exec(ctx, string(up))
	require.NoError(t, err, "000029 up migration must repair legacy overlong slugs")
	for table, wantIDs := range map[string][]int64{"link": linkIDs, "note": noteIDs} {
		rows, queryErr := pool.Query(ctx, `SELECT id, title, slug FROM `+table+` ORDER BY id`)
		require.NoError(t, queryErr)
		seen := make(map[string]struct{})
		var gotIDs []int64
		for rows.Next() {
			var id int64
			var title, candidate string
			require.NoError(t, rows.Scan(&id, &title, &candidate))
			gotIDs = append(gotIDs, id)
			assert.Contains(t, title, "legacy-")
			assert.LessOrEqual(t, len(candidate), slug.MaxLen)
			assert.True(t, slug.IsValid(candidate), candidate)
			_, duplicate := seen[candidate]
			assert.False(t, duplicate, "duplicate repaired slug %q", candidate)
			seen[candidate] = struct{}{}
		}
		require.NoError(t, rows.Err())
		rows.Close()
		assert.Contains(t, seen, strings.Repeat("a", 78)+"-9")
		assert.Contains(t, seen, strings.Repeat("a", 77)+"-10")
		assert.Contains(t, seen, strings.Repeat("a", 77)+"-99")
		assert.Contains(t, seen, strings.Repeat("a", 76)+"-100")
		assert.Equal(t, wantIDs, gotIDs, "%s rows or identities changed during repair", table)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO link (user_id, url, title, slug)
		VALUES ($1, 'https://too-long.test', 'too long', $2)`, int64(uid), base+"-999")
	require.Error(t, err, "link must reject slugs longer than MaxLen")
	_, err = pool.Exec(ctx, `
		INSERT INTO note (user_id, title, slug)
		VALUES ($1, 'too long', $2)`, int64(uid), base+"-999")
	require.Error(t, err, "note must reject slugs longer than MaxLen")

	_, err = pool.Exec(ctx, string(down))
	require.NoError(t, err, "000029 must roll back after reapplying")
}

func TestMigration000030BackfillsPositivePreviewGenerationAndIsReversible(t *testing.T) {
	ctx := context.Background()
	pool := New(t)
	dir := migrationsDir()
	down, err := os.ReadFile(filepath.Join(dir, "000030_preview_generation.down.sql"))
	require.NoError(t, err)
	up, err := os.ReadFile(filepath.Join(dir, "000030_preview_generation.up.sql"))
	require.NoError(t, err)

	_, err = pool.Exec(ctx, string(down))
	require.NoError(t, err, "000030 down migration must apply cleanly")
	uid := SeedUser(t, pool, "legacy-preview@test.local", "editor")
	var linkID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO link (user_id, url, title, slug)
		VALUES ($1, 'https://legacy-preview.test', 'legacy preview', 'legacy-preview')
		RETURNING id`, int64(uid)).Scan(&linkID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, string(up))
	require.NoError(t, err, "000030 up migration must backfill existing links")
	var generation int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT preview_generation FROM link WHERE id = $1`, linkID).Scan(&generation))
	assert.EqualValues(t, 1, generation)
	_, err = pool.Exec(ctx, `UPDATE link SET preview_generation = 0 WHERE id = $1`, linkID)
	require.Error(t, err, "preview generation must remain positive")

	_, err = pool.Exec(ctx, string(down))
	require.NoError(t, err, "000030 must be reversible")
	var columnExists bool
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'link'
			  AND column_name = 'preview_generation'
		)`).Scan(&columnExists))
	assert.False(t, columnExists)
}

func TestMigration000031RepairsDuplicatesAndEnforcesOneLiveChallengeOTP(t *testing.T) {
	ctx := context.Background()
	pool := New(t)
	dir := migrationsDir()
	down, err := os.ReadFile(filepath.Join(dir, "000031_unique_live_challenge_email_otp.down.sql"))
	require.NoError(t, err)
	up, err := os.ReadFile(filepath.Join(dir, "000031_unique_live_challenge_email_otp.up.sql"))
	require.NoError(t, err)

	_, err = pool.Exec(ctx, string(down))
	require.NoError(t, err, "000031 down migration must apply cleanly")
	uid := SeedUser(t, pool, "legacy-otp@test.local", "editor")
	var challengeID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO auth_challenge (user_id, token_hash, purpose, token_version, expires_at)
		VALUES ($1, $2, 'totp', 0, now() + interval '10 minutes')
		RETURNING id`, int64(uid), []byte("legacy-otp-challenge")).Scan(&challengeID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO email_otp (user_id, challenge_id, purpose, code_hash, created_at, expires_at)
		VALUES
			($1, $2, 'login_2fa', $3, now() - interval '1 minute', now() + interval '5 minutes'),
			($1, $2, 'login_2fa', $4, now(), now() + interval '5 minutes')`,
		int64(uid), challengeID, []byte("older"), []byte("newer"))
	require.NoError(t, err, "fixture requires the pre-migration duplicate state")

	_, err = pool.Exec(ctx, string(up))
	require.NoError(t, err, "000031 must repair duplicate rows before adding the index")
	var live int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM email_otp
		WHERE challenge_id = $1 AND purpose = 'login_2fa' AND consumed_at IS NULL`, challengeID).Scan(&live))
	assert.Equal(t, 1, live)
	var survivingHash []byte
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT code_hash FROM email_otp
		WHERE challenge_id = $1 AND purpose = 'login_2fa' AND consumed_at IS NULL`, challengeID).
		Scan(&survivingHash))
	assert.Equal(t, []byte("newer"), survivingHash, "migration did not preserve the newest code")

	_, err = pool.Exec(ctx, `
		INSERT INTO email_otp (user_id, challenge_id, purpose, code_hash, expires_at)
		VALUES ($1, $2, 'login_2fa', $3, now() + interval '5 minutes')`,
		int64(uid), challengeID, []byte("second-live"))
	require.Error(t, err, "database accepted a second live login OTP for one challenge")
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "23505", pgErr.Code)
	assert.Equal(t, "email_otp_one_live_login_challenge_idx", pgErr.ConstraintName)

	_, err = pool.Exec(ctx, string(down))
	require.NoError(t, err, "000031 must be reversible")
	_, err = pool.Exec(ctx, `
		INSERT INTO email_otp (user_id, challenge_id, purpose, code_hash, expires_at)
		VALUES ($1, $2, 'login_2fa', $3, now() + interval '5 minutes')`,
		int64(uid), challengeID, []byte("allowed-after-down"))
	require.NoError(t, err, "down migration left the uniqueness index active")
}
