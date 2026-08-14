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
