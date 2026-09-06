package links

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLinkSlugResolutionHasOneReachableProductionMethod(t *testing.T) {
	decls := productionFuncNames(t)

	require.NotContains(t, decls, "Repository.GetBySlug",
		"tenant-scoped GetBySlug is dead; public slug resolution is ClickAndResolveBySlug")
	require.Contains(t, decls, "Repository.ClickAndResolveBySlug",
		"ClickAndResolveBySlug must remain the reachable production slug resolver")

	src, err := os.ReadFile("repository.go")
	require.NoError(t, err)
	require.NotContains(t, string(src), "GetBySlug",
		"scanLink's comment still lists GetBySlug as a live Scan sibling")
}

func TestScreenshotHandlerConstructorAcceptsTheNarrowRepo(t *testing.T) {
	fn := productionFunc(t, "NewScreenshotHandler")
	require.NotNil(t, fn, "NewScreenshotHandler missing")
	params := namedParamTypes(fn)
	require.NotEmpty(t, params)
	require.Equal(t, "screenshotRepo", params[0],
		"NewScreenshotHandler must accept the narrow screenshotRepo, not *Repository")
}

func TestSlugUpdateIsNotALocalTrampoline(t *testing.T) {
	require.NotContains(t, productionFuncNames(t), "resolveUpdateSlug",
		"resolveUpdateSlug only forwards to slug.ResolveUpdate; call that helper at the Update site")
}

func TestLinkTagAttachmentUsesCanonicalHelper(t *testing.T) {
	require.NotContains(t, productionFuncNames(t), "setLinkTags",
		"setLinkTags only binds the kind string; call tags.SetEntityTagsWithPending directly")
}

func productionFuncNames(t *testing.T) []string {
	t.Helper()
	var names []string
	for _, fn := range productionFuncs(t) {
		names = append(names, funcIdent(fn))
	}
	return names
}

func productionFunc(t *testing.T, name string) *ast.FuncDecl {
	t.Helper()
	for _, fn := range productionFuncs(t) {
		if fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

func productionFuncs(t *testing.T) []*ast.FuncDecl {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var out []*ast.FuncDecl
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err, name)
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			out = append(out, fn)
			return true
		})
	}
	return out
}

func funcIdent(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return recvIdent(fn.Recv.List[0].Type) + "." + fn.Name.Name
}

func recvIdent(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return recvIdent(t.X)
	case *ast.Ident:
		return t.Name
	default:
		return "?"
	}
}

func namedParamTypes(fn *ast.FuncDecl) []string {
	var out []string
	if fn.Type.Params == nil {
		return out
	}
	for _, f := range fn.Type.Params.List {
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			out = append(out, typeIdent(f.Type))
		}
	}
	return out
}

func typeIdent(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + typeIdent(t.X)
	case *ast.SelectorExpr:
		return typeIdent(t.X) + "." + t.Sel.Name
	default:
		return "?"
	}
}
