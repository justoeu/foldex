package backup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCognitiveComplexity_ExportAndInspectArchive(t *testing.T) {
	cases := []struct {
		file string
		name string
	}{
		{file: "service.go", name: "(*Service).Export"},
		{file: "archive.go", name: "inspectArchive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cognitOf(t, tc.file, tc.name)
			assert.LessOrEqual(t, got, 15,
				"%s cognitive=%d — extract early returns / unexported helpers, no NOSONAR", tc.name, got)
		})
	}
}

func TestCognitiveComplexity_RestoreDuplicateStagedAndCopy(t *testing.T) {
	cases := []struct {
		file string
		name string
	}{
		{file: "db_restore_staged.go", name: "restoreDuplicateStaged"},
		{file: "db_restore_staged.go", name: "copyRestoreStaging"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cognitOf(t, tc.file, tc.name)
			assert.LessOrEqual(t, got, 15,
				"%s cognitive=%d — extract early returns / unexported helpers, no NOSONAR", tc.name, got)
		})
	}
}

func TestCognitiveComplexity_LoadRestoreLedgerAndManifestIntegrity(t *testing.T) {
	cases := []struct {
		file string
		name string
	}{
		{file: "restore_ledger.go", name: "loadRestoreLedger"},
		{file: "restore_preflight.go", name: "validateManifestIntegrity"},
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
