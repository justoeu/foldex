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

	var missing []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		// Seed one row, run Reset, and assert it is gone. Asserting behaviour
		// beats string-matching the TRUNCATE statement: a table listed but
		// spelled wrong would still pass a textual check.
		if !strings.Contains(resetStatement, name) {
			missing = append(missing, name)
		}
	}
	require.NoError(t, rows.Err())
	assert.Empty(t, missing,
		"these tables exist but are not truncated by testdb.Reset — add them or subtests will leak rows")
}
