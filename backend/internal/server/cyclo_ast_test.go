package server

import (
	"go/ast"
	"go/token"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/testsupport"
)

func TestServerNewCyclomaticIsUnder20(t *testing.T) {
	fn := productionFunc(t, "New")
	got := cyclomatic(fn)
	assert.LessOrEqual(t, got, 20,
		"server.New cyclomatic=%d (1+If/For/Range/Case/||/&&, nested funcs included) — extract named mounters so New only sequences them", got)
}

func TestRouterMountCharter(t *testing.T) {
	t.Run("named mounters exist", func(t *testing.T) {
		for _, name := range []string{
			"publicShareRoutes",
			"principalStack",
			"adminSurface",
			"contentCRUD",
			"storageOrUnavailable",
		} {
			require.NotNil(t, productionFunc(t, name), "mounter %s must exist", name)
		}
	})

	decls := testsupport.ProductionFuncs(t)
	t.Run("writeGate wraps content groups", func(t *testing.T) {
		for _, route := range []string{"/tags", "/settings", "/links", "/notes"} {
			fn := funcRouting(t, decls, route)
			assert.Truef(t, fnUses(fn, "writeGate"),
				"%s routes %s but does not Use/With writeGate", fn.Name.Name, route)
		}
	})
	t.Run("RejectAPIToken wraps account surfaces", func(t *testing.T) {
		for _, route := range []string{"/activity", "/status", "/settings", "/admin"} {
			fn := funcRouting(t, decls, route)
			assert.Truef(t, fnUses(fn, "RejectAPIToken"),
				"%s routes %s but does not Use/With RejectAPIToken", fn.Name.Name, route)
		}
	})
	t.Run("RequireAdmin wraps /admin before RejectAPIToken", func(t *testing.T) {
		fn := funcRouting(t, decls, "/admin")
		admin := routeCallback(fn, "/admin")
		require.NotNil(t, admin, "/admin must mount via Route callback")
		require.True(t, litUses(admin, "RequireAdmin"), "/admin must Use RequireAdmin")
		require.True(t, litUses(admin, "RejectAPIToken"), "/admin must Use RejectAPIToken")
		assert.Less(t, identPosIn(admin, "RequireAdmin"), identPosIn(admin, "RejectAPIToken"),
			"RequireAdmin must wrap /admin before RejectAPIToken — reversing them answers 403 to a token holder and confirms the admin surface exists")
	})
}

func productionFunc(t *testing.T, name string) *ast.FuncDecl {
	t.Helper()
	for _, fn := range testsupport.ProductionFuncs(t) {
		if fn.Name.Name == name && fn.Recv == nil {
			return fn
		}
	}
	t.Fatalf("func %s not found in server production files", name)
	return nil
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

func funcRouting(t *testing.T, decls []*ast.FuncDecl, route string) *ast.FuncDecl {
	t.Helper()
	for _, fn := range decls {
		if fnHasRoute(fn, route) {
			return fn
		}
	}
	t.Fatalf("no production function routes %s", route)
	return nil
}

func fnHasRoute(fn *ast.FuncDecl, route string) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Route", "Get", "Post", "Put", "Patch", "Delete", "Method":
		default:
			return true
		}
		for _, arg := range call.Args {
			lit, ok := arg.(*ast.BasicLit)
			if ok && lit.Kind == token.STRING && strings.Trim(lit.Value, `"`) == route {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func fnUses(fn *ast.FuncDecl, ident string) bool {
	return litUses(fn, ident)
}

func routeCallback(fn *ast.FuncDecl, route string) *ast.FuncLit {
	var lit *ast.FuncLit
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Route" {
			return true
		}
		arg0, ok := call.Args[0].(*ast.BasicLit)
		if !ok || strings.Trim(arg0.Value, `"`) != route {
			return true
		}
		if fl, ok := call.Args[1].(*ast.FuncLit); ok {
			lit = fl
			return false
		}
		return true
	})
	return lit
}

func litUses(n ast.Node, ident string) bool {
	found := false
	ast.Inspect(n, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Use" && sel.Sel.Name != "With") {
			return true
		}
		for _, arg := range call.Args {
			if exprNamed(arg, ident) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func identPosIn(n ast.Node, ident string) int {
	pos := int(^uint(0) >> 1)
	ast.Inspect(n, func(node ast.Node) bool {
		if exprNamed(node, ident) && int(node.Pos()) < pos {
			pos = int(node.Pos())
		}
		return true
	})
	return pos
}

func exprNamed(n ast.Node, ident string) bool {
	switch x := n.(type) {
	case *ast.Ident:
		return x.Name == ident
	case *ast.SelectorExpr:
		return x.Sel.Name == ident
	}
	return false
}
