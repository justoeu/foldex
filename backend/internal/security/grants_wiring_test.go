package security

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestServerDepsCarriesTheLiveGrants fails when main.go builds server.Deps
// without wiring the configurable RBAC matrix.
//
// `Deps.Grants` is deliberately optional — nil means the compiled matrix, which
// is what every router test wants and what an instance behaved like before
// ADR-42. That default is also exactly what let the field be FORGOTTEN in
// main.go: the compiler was satisfied, every mount site had its parameter, and
// the content, import and backup-restore gates quietly kept enforcing the
// compiled matrix. An owner's revocation committed, was audited, rendered as
// unticked on the matrix screen, and changed nothing on those routes — while
// the gates main wires by hand (admin, folders, policy) DID honour it, so the
// revocation looked partially applied, which reads as flakiness rather than as
// a bug.
//
// A unit test cannot see this: the defect is one absent struct field in a
// composite literal that compiles, and no behaviour inside any package is
// wrong. This walks main's AST instead, the same shape as the other guards
// here.
func TestServerDepsCarriesTheLiveGrants(t *testing.T) {
	path := filepath.Join("..", "..", "cmd", "server", "main.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err, "parse main.go")

	var found, wired bool
	var valueName string
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
			// too. What has to be true is that the field carries a live store,
			// so the value must be a plain identifier: `nil` is an identifier
			// as well, hence the explicit exclusion, and a call expression
			// (`roleperm.Default()`) is not an identifier at all.
			ident, isIdent := kv.Value.(*ast.Ident)
			if isIdent && ident.Name != "nil" {
				wired = true
				valueName = ident.Name
			}
		}
		return true
	})

	// Both halves are asserted. Without the first, a rename of Deps makes the
	// whole test pass while checking nothing.
	require.True(t, found, "fixture precondition: main.go builds a server.Deps literal")
	require.True(t, wired,
		"cmd/server/main.go builds server.Deps with no `Grants`, or with a value "+
			"that is not a live store (`nil`, or a compiled matrix). The router "+
			"then enforces the COMPILED matrix on every gate mounted there — "+
			"/links, /notes, /tags, /import, /backup/restore — so an owner's "+
			"revocation commits, audits, renders as unticked and changes nothing "+
			"on those routes. Set `Grants: grantsRepo`.")

	// And that identifier is the roleperm repository, not some other variable
	// that happens to satisfy the interface. Without this the guard passes on
	// `Grants: someUnrelatedThing`.
	assignedByNewRepository := false
	ast.Inspect(file, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		lhs, ok := as.Lhs[0].(*ast.Ident)
		if !ok || lhs.Name != valueName {
			return true
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "NewRepository" {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "roleperm" {
				assignedByNewRepository = true
			}
		}
		return true
	})
	require.True(t, assignedByNewRepository,
		"`Grants: %s` is wired, but %s is not a roleperm.NewRepository — the field "+
			"has to carry the live store, not merely be present", valueName, valueName)
}
