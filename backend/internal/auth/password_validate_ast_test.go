package auth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPasswordValidationHasOneOwner is the DUP-ECH-004 twin of DUP-ECH-002:
// two methods independently rune-count a password against max(policy, 8).
// A third bound added to one writer of credentials would miss admin-create
// (or vice versa). The scan looks for the envelope code, not the identifier,
// because renaming the helper is how a second copy would hide.
func TestPasswordValidationHasOneOwner(t *testing.T) {
	var owners []string
	fset := token.NewFileSet()

	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			if !emitsPasswordTooShort(fn.Body) {
				return true
			}
			name := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				name = recvName(fn.Recv.List[0].Type) + "." + name
			}
			owners = append(owners, filepath.ToSlash(path)+":"+name)
			return true
		})
		return nil
	})
	require.NoError(t, err)
	require.Len(t, owners, 1,
		"password_too_short must have one owner, found %v", owners)
}

func emitsPasswordTooShort(body *ast.BlockStmt) bool {
	var hit bool
	ast.Inspect(body, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok {
			return true
		}
		if strings.Contains(lit.Value, "password_too_short") {
			hit = true
			return false
		}
		return true
	})
	return hit
}

func recvName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return recvName(t.X)
	case *ast.Ident:
		return t.Name
	default:
		return "?"
	}
}
