//go:build integration

package testdb

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
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
