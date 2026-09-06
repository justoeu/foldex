package clickctx_test

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

// clickLogWriters are the packages allowed to insert into click_log.
//
// The public path's INSERT lives in clicklog (INV-056 / INV-182). links and
// notes still resolve the row and must not grow their own writers. importer
// and backup write historical rows with a different shape and are out of
// this guard's scope.
var clickLogWriters = []string{"../../links", "../../notes", "../clicklog"}

const clickLogInsert = "INSERT INTO click_log"

// Every click_log INSERT in production code MUST sit inside an
// `if clickctx.Allow(...)`, and this guard is the only executed thing that says
// so.
//
// The middleware tests in internal/server prove the gate is INSTALLED and that
// the coalescer decides correctly, but they drive fake resolvers that call
// clickctx.Allow themselves. Delete the check from either repository and every
// one of those tests stays green: the fakes would still coalesce while the real
// instance recorded every hit. Only the integration suite catches that, and an
// integration suite is exactly what does not run when Docker is busy.
//
// So this reads the AST, not the file text: a guard that greps for
// "clickctx.Allow" would pass on a call in a comment, or on one placed after
// the INSERT where it decides nothing.
func TestEveryClickLogInsertIsGatedByAllow(t *testing.T) {
	t.Parallel()

	found := 0
	for _, dir := range clickLogWriters {
		for _, path := range productionFiles(t, dir) {
			found += assertGatedInserts(t, path)
		}
	}
	assert.GreaterOrEqual(t, found, 1,
		"expected the public click path to insert into click_log; "+
			"finding fewer means this guard is watching nothing")
}

func TestPublicClickInsertIsOnePrimitive(t *testing.T) {
	t.Parallel()

	found := 0
	var sites []string
	for _, dir := range clickLogWriters {
		for _, path := range productionFiles(t, dir) {
			n := countClickLogInserts(t, path)
			if n > 0 {
				found += n
				sites = append(sites, path)
			}
		}
	}
	require.Equal(t, 1, found,
		"public click+resolve must share one INSERT primitive, found %d in %v", found, sites)
}

func countClickLogInserts(t *testing.T, path string) int {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err)
	count := 0
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || !strings.Contains(lit.Value, clickLogInsert) {
			return true
		}
		count++
		return true
	})
	return count
}

// assertGatedInserts checks one file and returns how many INSERTs it saw.
func assertGatedInserts(t *testing.T, path string) int {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err)

	var gated []struct{ from, to token.Pos }
	ast.Inspect(file, func(n ast.Node) bool {
		stmt, ok := n.(*ast.IfStmt)
		if !ok || !isAllowCall(stmt.Cond) {
			return true
		}
		gated = append(gated, struct{ from, to token.Pos }{stmt.Body.Pos(), stmt.Body.End()})
		return true
	})

	count := 0
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || !strings.Contains(lit.Value, clickLogInsert) {
			return true
		}
		count++
		for _, g := range gated {
			if lit.Pos() >= g.from && lit.Pos() < g.to {
				return true
			}
		}
		t.Errorf("%s: this %q is not inside an `if clickctx.Allow(...)` — the public "+
			"click path would write a row per hit again, and the middleware tests "+
			"would not notice", fset.Position(lit.Pos()), clickLogInsert)
		return true
	})
	return count
}

// isAllowCall reports whether the expression is exactly `clickctx.Allow(...)`.
func isAllowCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Allow" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "clickctx"
}

func productionFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err, "the guard's package list has gone stale: %s", dir)

	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	require.NotEmpty(t, out, "no production files under %s", dir)
	return out
}
