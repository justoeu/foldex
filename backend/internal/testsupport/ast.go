package testsupport

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ProductionFuncs parses every non-test .go file in the calling test's working
// directory (go test runs with the package dir as cwd) and returns the
// function declarations found there, body required.
func ProductionFuncs(t *testing.T) []*ast.FuncDecl {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var out []*ast.FuncDecl
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err, name)
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			out = append(out, fn)
			return true
		})
	}
	return out
}

// ProductionFuncNames is ProductionFuncs reduced to bare function names.
func ProductionFuncNames(t *testing.T) []string {
	t.Helper()
	fns := ProductionFuncs(t)
	names := make([]string, 0, len(fns))
	for _, fn := range fns {
		names = append(names, fn.Name.Name)
	}
	return names
}
