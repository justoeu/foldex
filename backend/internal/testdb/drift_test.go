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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
