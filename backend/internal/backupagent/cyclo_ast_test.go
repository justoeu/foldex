package backupagent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackupAgentLoadCyclomaticIsUnder15(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "config.go", nil, 0)
	require.NoError(t, err)
	var load *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && fn.Name.Name == "Load" && fn.Recv == nil {
			load = fn
			return false
		}
		return true
	})
	require.NotNil(t, load, "Load must live in config.go")
	got := cyclomatic(load)
	assert.LessOrEqual(t, got, 15,
		"backupagent.Load cyclomatic=%d — table the required env groups and one requireNonEmpty helper", got)
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
