package backup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRestoreModesShareFolderAndNoteInsertSQL is DUP-ECH-006: skip and
// duplicate used to copy the folder and note INSERT statements. One helper
// each is the lock; a second literal is a drift waiting to happen.
func TestRestoreModesShareFolderAndNoteInsertSQL(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "db_restore_staged.go", nil, 0)
	require.NoError(t, err)

	folderSQL, noteSQL := 0, 0
	calls := map[string]map[string]int{
		"restoreSkipStaged":      {},
		"restoreDuplicateStaged": {},
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(n.Value)
			if err != nil {
				return true
			}
			if strings.Contains(s, "INSERT INTO folder") {
				folderSQL++
			}
			if strings.Contains(s, "INSERT INTO note") {
				noteSQL++
			}
		case *ast.FuncDecl:
			if n.Body == nil {
				return true
			}
			if _, watched := calls[n.Name.Name]; !watched {
				return true
			}
			ast.Inspect(n.Body, func(inner ast.Node) bool {
				call, ok := inner.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				calls[n.Name.Name][id.Name]++
				return true
			})
		}
		return true
	})

	require.Equal(t, 1, folderSQL,
		"AST/text finds duplicated INSERT INTO folder in two restore modes")
	require.Equal(t, 1, noteSQL,
		"AST/text finds duplicated INSERT INTO note in two restore modes")

	for _, fn := range []string{"restoreSkipStaged", "restoreDuplicateStaged"} {
		require.Greater(t, calls[fn]["insertStagedFolders"], 0,
			"%s must call insertStagedFolders", fn)
		require.Greater(t, calls[fn]["insertStagedNotes"], 0,
			"%s must call insertStagedNotes", fn)
	}
}
