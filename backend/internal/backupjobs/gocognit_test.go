package backupjobs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCognitiveComplexity_ValidateJobConfig(t *testing.T) {
	got := cognitOf(t, "schedule.go", "ValidateJobConfig")
	assert.LessOrEqual(t, got, 15,
		"ValidateJobConfig cognitive=%d — extract early returns / unexported helpers, no NOSONAR", got)
}

func cognitOf(t *testing.T, file, want string) int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	require.NoError(t, err)
	var found *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && cognitFuncName(fn) == want {
			found = fn
			return false
		}
		return true
	})
	require.NotNil(t, found, "%s must live in %s", want, file)
	return cognitComplexity(found)
}
