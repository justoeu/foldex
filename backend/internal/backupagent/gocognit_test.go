package backupagent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCognitiveComplexity_JobRuns(t *testing.T) {
	cases := []struct {
		file string
		name string
	}{
		{file: "drill.go", name: "(*DrillJob).Run"},
		{file: "dump.go", name: "(*DumpJob).Run"},
		{file: "userzip.go", name: "(*UserZipJob).Run"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cognitOf(t, tc.file, tc.name)
			assert.LessOrEqual(t, got, 15,
				"%s cognitive=%d — extract early returns / unexported helpers, no NOSONAR", tc.name, got)
		})
	}
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
