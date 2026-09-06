package security

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

// TestServerDepsCarriesTheLiveGrants fails when cmd/server builds server.Deps
// without wiring the configurable RBAC matrix.
//
// `Deps.Grants` is deliberately optional — nil means the compiled matrix, which
// is what every router test wants and what an instance behaved like before
// ADR-42. That default is also exactly what let the field be FORGOTTEN: the
// compiler was satisfied, every mount site had its parameter, and the content,
// import and backup-restore gates quietly kept enforcing the compiled matrix.
// An owner's revocation committed, was audited, rendered as unticked on the
// matrix screen, and changed nothing on those routes — while the gates wired
// by hand (admin, folders, policy) DID honour it, so the revocation looked
// partially applied, which reads as flakiness rather than as a bug.
//
// A unit test cannot see this: the defect is one absent struct field in a
// composite literal that compiles, and no behaviour inside any package is
// wrong. This walks cmd/server's AST instead, the same shape as the other
// guards here. The literal used to live in main.go; after the boot extract it
// lives next to loadGrants. Walking only main.go is how this guard went silent.
func TestServerDepsCarriesTheLiveGrants(t *testing.T) {
	files := parseServerCmd(t)

	var found, wired bool
	var grantsValue ast.Expr
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Deps" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "server" {
				return true
			}
			found = true
			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "Grants" {
					continue
				}
				// The VALUE, not merely the presence of the field.
				//
				// The first version of this guard asserted only that `Grants:`
				// appeared, and `Grants: nil` passed it — reproducing verbatim the
				// defect its own message describes. `roleperm.Default()` passed
				// too. What has to be true is that the field carries a live store:
				// a local ident (not `nil`) or `h.grants` filled by loadGrants.
				// A call expression (`roleperm.Default()`) is neither.
				if liveGrantsValue(kv.Value) {
					wired = true
					grantsValue = kv.Value
				}
			}
			return true
		})
	}

	// Both halves are asserted. Without the first, a rename of Deps makes the
	// whole test pass while checking nothing.
	require.True(t, found, "fixture precondition: cmd/server builds a server.Deps literal")
	require.True(t, wired,
		"cmd/server builds server.Deps with no `Grants`, or with a value "+
			"that is not a live store (`nil`, or a compiled matrix). The router "+
			"then enforces the COMPILED matrix on every gate mounted there — "+
			"/links, /notes, /tags, /import, /backup/restore — so an owner's "+
			"revocation commits, audits, renders as unticked and changes nothing "+
			"on those routes. Set `Grants: grantsRepo` or `Grants: h.grants` from loadGrants.")

	require.True(t, grantsValueComesFromNewRepository(files, grantsValue),
		"`Grants` is wired, but the value is not a roleperm.NewRepository — the field "+
			"has to carry the live store, not merely be present")
}

func parseServerCmd(t *testing.T) []*ast.File {
	t.Helper()
	dir := filepath.Join("..", "..", "cmd", "server")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		require.NoError(t, err, name)
		files = append(files, file)
	}
	require.NotEmpty(t, files, "cmd/server production files")
	return files
}

func liveGrantsValue(v ast.Expr) bool {
	switch x := v.(type) {
	case *ast.Ident:
		return x.Name != "nil"
	case *ast.SelectorExpr:
		recv, ok := x.X.(*ast.Ident)
		return ok && recv.Name == "h" && x.Sel.Name == "grants"
	default:
		return false
	}
}

func grantsValueComesFromNewRepository(files []*ast.File, v ast.Expr) bool {
	switch x := v.(type) {
	case *ast.Ident:
		return identAssignedByRolepermNewRepository(files, x.Name)
	case *ast.SelectorExpr:
		return selectorFilledByLoadGrants(files) && funcCallsRolepermNewRepository(files, "loadGrants")
	default:
		return false
	}
}

func identAssignedByRolepermNewRepository(files []*ast.File, name string) bool {
	for _, file := range files {
		found := false
		ast.Inspect(file, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
				return true
			}
			lhs, ok := as.Lhs[0].(*ast.Ident)
			if !ok || lhs.Name != name {
				return true
			}
			if isRolepermNewRepository(as.Rhs[0]) {
				found = true
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

func selectorFilledByLoadGrants(files []*ast.File) bool {
	for _, file := range files {
		found := false
		ast.Inspect(file, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
				return true
			}
			sel, ok := as.Lhs[0].(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "grants" {
				return true
			}
			recv, ok := sel.X.(*ast.Ident)
			if !ok || recv.Name != "h" {
				return true
			}
			call, ok := as.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			fn, ok := call.Fun.(*ast.Ident)
			if ok && fn.Name == "loadGrants" {
				found = true
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

func funcCallsRolepermNewRepository(files []*ast.File, name string) bool {
	for _, file := range files {
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Name.Name != name || fn.Recv != nil || fn.Body == nil {
				continue
			}
			found := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if isRolepermNewRepository(n) {
					found = true
				}
				return true
			})
			return found
		}
	}
	return false
}

func isRolepermNewRepository(n ast.Node) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "NewRepository" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "roleperm"
}
