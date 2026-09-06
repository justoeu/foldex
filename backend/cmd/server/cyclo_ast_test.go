package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMainCyclomaticIsUnder20(t *testing.T) {
	fn := productionFunc(t, "main")
	got := cyclomatic(fn)
	assert.LessOrEqual(t, got, 20,
		"main cyclomatic=%d (1+If/For/Range/Case/||/&&) — extract fail-fast loaders and assemble Deps in one place", got)
}

func TestBootWiringCharter(t *testing.T) {
	for _, name := range []string{
		"loadTracing",
		"loadStorage",
		"loadVAPID",
		"loadFolderUnlock",
		"loadMail",
		"loadAuthCipher",
		"loadGrants",
		"loadAbuseCache",
		"assembleDeps",
	} {
		require.NotNil(t, productionFunc(t, name), "boot loader %s must exist", name)
	}
	require.NotNil(t, productionFunc(t, "waitForShutdown"), "waitForShutdown stays the shutdown sequencer")
}

func productionFunc(t *testing.T, name string) *ast.FuncDecl {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	fset := token.NewFileSet()
	for _, e := range entries {
		fname := e.Name()
		if e.IsDir() || !strings.HasSuffix(fname, ".go") || strings.HasSuffix(fname, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Clean(fname), nil, 0)
		require.NoError(t, err, fname)
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if ok && fn.Name.Name == name && fn.Recv == nil {
				return fn
			}
		}
	}
	t.Fatalf("func %s not found in cmd/server production files", name)
	return nil
}

func cyclomatic(fn *ast.FuncDecl) int {
	if fn == nil || fn.Body == nil {
		return 0
	}
	n := 1
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		switch x := node.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
			n++
		case *ast.CaseClause, *ast.CommClause:
			n++
		case *ast.BinaryExpr:
			if x.Op == token.LAND || x.Op == token.LOR {
				n++
			}
		}
		return true
	})
	return n
}
