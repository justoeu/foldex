package security_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOrchestrationDoesNotImportHTTPDelivery(t *testing.T) {
	forbidden := map[string]struct{}{
		"net/http":                    {},
		"foldex/internal/pkg/httperr": {},
	}
	files := []string{
		filepath.Join("..", "backup", "restore.go"),
		filepath.Join("..", "backup", "restore_helpers.go"),
		filepath.Join("..", "backup", "restore_files.go"),
	}
	for _, path := range files {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		require.NoError(t, err)
		for _, imp := range f.Imports {
			importPath, err := strconv.Unquote(imp.Path.Value)
			require.NoError(t, err)
			if _, found := forbidden[importPath]; found {
				t.Errorf("production restore orchestration %s imports HTTP delivery package %q", filepath.ToSlash(filepath.Base(path)), importPath)
			}
		}
	}
}

func TestRepositoriesDoNotImportHTTPDelivery(t *testing.T) {
	root := ".."
	forbidden := map[string]struct{}{
		"net/http":                    {},
		"foldex/internal/pkg/httperr": {},
	}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || strings.HasSuffix(path, "_test.go") ||
			!strings.HasPrefix(filepath.Base(path), "repository") || filepath.Ext(path) != ".go" {
			return nil
		}

		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			importPath, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if _, found := forbidden[importPath]; found {
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					return relErr
				}
				t.Errorf("production repository %s imports HTTP delivery package %q", filepath.ToSlash(rel), importPath)
			}
		}
		return nil
	})
	require.NoError(t, err)
}

func TestDeliveryDoesNotImportStorageAdapters(t *testing.T) {
	// cmd/server is intentionally outside this root: concrete adapters are
	// allowed only at the application composition boundary.
	root := filepath.Join("..", "links")
	forbiddenPrefixes := []string{
		"foldex/internal/storage",
		"foldex/internal/adapters",
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || strings.HasSuffix(path, "_test.go") || filepath.Ext(path) != ".go" {
			return nil
		}

		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			importPath, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			for _, prefix := range forbiddenPrefixes {
				if importPath == prefix || strings.HasPrefix(importPath, prefix+"/") {
					rel, relErr := filepath.Rel(root, path)
					if relErr != nil {
						return relErr
					}
					t.Errorf("production links delivery file %s imports storage adapter %q", filepath.ToSlash(rel), importPath)
				}
			}
		}
		return nil
	})
	require.NoError(t, err)
}

func TestNotesDoesNotImportLinks(t *testing.T) {
	root := filepath.Join("..", "notes")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || strings.HasSuffix(path, "_test.go") || filepath.Ext(path) != ".go" {
			return nil
		}

		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			importPath, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if importPath == "foldex/internal/links" {
				t.Errorf("production notes file %s imports links", filepath.Base(path))
			}
		}
		return nil
	})
	require.NoError(t, err)
}

func TestEntriesDoesNotImportLinks(t *testing.T) {
	root := filepath.Join("..", "entries")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || strings.HasSuffix(path, "_test.go") || filepath.Ext(path) != ".go" {
			return nil
		}

		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			importPath, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if importPath == "foldex/internal/links" {
				t.Errorf("production entries file %s imports links", filepath.Base(path))
			}
		}
		return nil
	})
	require.NoError(t, err)
}

func TestImporterDoesNotImportPreview(t *testing.T) {
	root := filepath.Join("..", "importer")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || strings.HasSuffix(path, "_test.go") || filepath.Ext(path) != ".go" {
			return nil
		}

		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			importPath, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if importPath == "foldex/internal/preview" {
				t.Errorf("production importer file %s imports preview", filepath.Base(path))
			}
		}
		return nil
	})
	require.NoError(t, err)
}

func TestPasswordResetRepositoryBindsEveryTokenToACredentialEpoch(t *testing.T) {
	path := filepath.Join("..", "auth", "repository_2fa.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	require.NoError(t, err)

	var inserts, resolves, spends []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		sql, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		norm := strings.Join(strings.Fields(sql), " ")
		switch {
		case strings.Contains(norm, "INSERT INTO password_reset"):
			inserts = append(inserts, norm)
		case strings.HasPrefix(norm, "SELECT") && strings.Contains(norm, "FROM password_reset") &&
			strings.Contains(norm, "token_hash = $1"):
			resolves = append(resolves, norm)
		case strings.Contains(norm, "UPDATE password_reset SET consumed_at = now()") &&
			strings.Contains(norm, "token_hash = $1"):
			spends = append(spends, norm)
		}
		return true
	})

	require.Len(t, inserts, 2, "normal and administrator reset issuers must both be inspected")
	for _, sql := range inserts {
		require.Contains(t, sql, "token_version")
	}
	require.Len(t, resolves, 1)
	require.Contains(t, resolves[0], "token_version IS NOT NULL")
	require.Len(t, spends, 1)
	require.Contains(t, spends[0], "token_version =")
}

// Importer/exporter delivery must not hold the persistence driver: the
// composition root (mount.go) builds the repository/stager and hands the
// handler a port, mirroring every sibling feature. A handler that begins its
// own transactions blurs the §7 error-mapping contract and is invisible to
// the repository* guards because its SQL lives elsewhere.
func TestImportExportHandlersDoNotImportPersistenceDriver(t *testing.T) {
	for _, dir := range []string{"importer", "exporter"} {
		entries, err := os.ReadDir(filepath.Join("..", dir))
		require.NoError(t, err)
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || filepath.Ext(name) != ".go" ||
				strings.HasSuffix(name, "_test.go") || !strings.HasPrefix(name, "handler") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", dir, name), nil, parser.ImportsOnly)
			require.NoError(t, err)
			for _, imp := range f.Imports {
				importPath, err := strconv.Unquote(imp.Path.Value)
				require.NoError(t, err)
				if strings.HasPrefix(importPath, "github.com/jackc/pgx") {
					t.Errorf("production %s/%s imports persistence driver %q — construct the repository at composition instead", dir, name, importPath)
				}
			}
		}
	}
}

// backupstatus is the server-mounted admin surface over backup_run. The
// schedule vocabulary (job names, bounds, ScheduleRow, AgentState) lives in
// backupjobs so this package cannot pull the agent process's S3 adapters.
func TestBackupStatusDoesNotImportBackupAgent(t *testing.T) {
	root := filepath.Join("..", "backupstatus")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || strings.HasSuffix(path, "_test.go") || filepath.Ext(path) != ".go" {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			importPath, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if importPath == "foldex/internal/backupagent" {
				t.Errorf("production backupstatus file %s imports backupagent — shared vocabulary lives in backupjobs", filepath.Base(path))
			}
		}
		return nil
	})
	require.NoError(t, err)
}
