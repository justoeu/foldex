package security_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFolderScopedListHandlersDelegateContentGateProtocol(t *testing.T) {
	for _, packageName := range []string{"entries", "links", "notes"} {
		t.Run(packageName, func(t *testing.T) {
			path := filepath.Join("..", packageName, "handler.go")
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			require.NoError(t, err)

			var list *ast.FuncDecl
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if ok && fn.Recv != nil && fn.Name.Name == "list" {
					list = fn
					break
				}
			}
			require.NotNil(t, list, "%s handler must define list", packageName)

			sharedCalls := 0
			repositoryListCalls := 0
			var directChecks []token.Position
			var folderBranches []token.Position
			ast.Inspect(list.Body, func(node ast.Node) bool {
				if branch, ok := node.(*ast.IfStmt); ok {
					ast.Inspect(branch.Cond, func(conditionNode ast.Node) bool {
						sel, ok := conditionNode.(*ast.SelectorExpr)
						if ok && sel.Sel.Name == "FolderID" {
							folderBranches = append(folderBranches, fset.Position(sel.Pos()))
						}
						return true
					})
				}
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				receiver, receiverOK := sel.X.(*ast.SelectorExpr)
				if receiverOK && receiver.Sel.Name == "folderGate" && sel.Sel.Name == "Check" {
					directChecks = append(directChecks, fset.Position(sel.Pos()))
				}
				if receiverOK && receiver.Sel.Name == "repo" && sel.Sel.Name == "List" {
					repositoryListCalls++
				}
				pkg, ok := sel.X.(*ast.Ident)
				if ok && pkg.Name == "folders" && sel.Sel.Name == "ListWithContentGate" {
					sharedCalls++
				}
				return true
			})

			require.Empty(t, directChecks,
				"%s list must not own ContentGate.Check calls; checks belong to folders.ListWithContentGate: %v",
				packageName, directChecks)
			require.Equal(t, 1, sharedCalls,
				"%s list must delegate exactly one complete gate/List/gate sequence", packageName)
			require.Empty(t, folderBranches,
				"%s list must not branch on FolderID; optional-folder behavior belongs to folders.ListWithContentGate: %v",
				packageName, folderBranches)
			require.Equal(t, 1, repositoryListCalls,
				"%s list must supply exactly one typed repository List closure", packageName)
		})
	}
}
