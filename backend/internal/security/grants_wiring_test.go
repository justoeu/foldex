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
	w := analyzeGrantsWiring(parseServerCmd(t))
	// Both halves are asserted. Without the first, a rename of Deps makes the
	// whole test pass while checking nothing.
	require.True(t, w.found, "fixture precondition: cmd/server builds a server.Deps literal")
	require.True(t, w.wired,
		"cmd/server builds server.Deps with no `Grants`, or with a value "+
			"that is not a live store (`nil`, or a compiled matrix). The router "+
			"then enforces the COMPILED matrix on every gate mounted there — "+
			"/links, /notes, /tags, /import, /backup/restore — so an owner's "+
			"revocation commits, audits, renders as unticked and changes nothing "+
			"on those routes. Set `Grants: grantsRepo` or `Grants: h.grants` from loadGrants.")
	require.True(t, w.liveStore,
		"`Grants` is wired, but the value is not a roleperm.NewRepository — the field "+
			"has to carry the live store, not merely be present")
}

type grantsWiring struct {
	found     bool
	wired     bool
	liveStore bool
}

func analyzeGrantsWiring(files []*ast.File) grantsWiring {
	var out grantsWiring
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
			out.found = true
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
					out.wired = true
					grantsValue = kv.Value
				}
			}
			return true
		})
	}
	if out.wired {
		out.liveStore = grantsValueComesFromNewRepository(files, grantsValue)
	}
	return out
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

func parseSnippet(t *testing.T, src string) []*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "snippet.go", src, 0)
	require.NoError(t, err)
	return []*ast.File{file}
}

func TestGrantsWiringSnippets(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		found     bool
		wired     bool
		liveStore bool
	}{
		{
			name: "current-good loadGrants",
			src: `package p
func f() {
	h.grants = loadGrants(h)
	_ = server.Deps{Grants: h.grants}
}
func loadGrants(h *handles) *roleperm.Repository {
	grantsRepo := roleperm.NewRepository(h.pool)
	return grantsRepo
}
`,
			found: true, wired: true, liveStore: true,
		},
		{
			name: "missing Grants field",
			src: `package p
func f() {
	_ = server.Deps{Pool: p}
}
`,
			found: true, wired: false, liveStore: false,
		},
		{
			name: "Grants nil",
			src: `package p
func f() {
	_ = server.Deps{Grants: nil}
}
`,
			found: true, wired: false, liveStore: false,
		},
		{
			name: "Grants Default",
			src: `package p
func f() {
	_ = server.Deps{Grants: roleperm.Default()}
}
`,
			found: true, wired: false, liveStore: false,
		},
		{
			name: "Grants other ident",
			src: `package p
func f() {
	other := somethingElse()
	_ = server.Deps{Grants: other}
}
`,
			found: true, wired: true, liveStore: false,
		},
		{
			// The call in the body is not the store. Returning Default after
			// discarding NewRepository used to pass because the helper only
			// asked whether the call appeared.
			name: "loadGrants discards NewRepository",
			src: `package p
func f() {
	h.grants = loadGrants(h)
	_ = server.Deps{Grants: h.grants}
}
func loadGrants(h *handles) *roleperm.Repository {
	_ = roleperm.NewRepository(p)
	return roleperm.Default()
}
`,
			found: true, wired: true, liveStore: false,
		},
		{
			name: "old inline NewRepository",
			src: `package p
func f() {
	grantsRepo := roleperm.NewRepository(p)
	_ = server.Deps{Grants: grantsRepo}
}
`,
			found: true, wired: true, liveStore: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := analyzeGrantsWiring(parseSnippet(t, tc.src))
			require.Equal(t, tc.found, w.found, "found")
			require.Equal(t, tc.wired, w.wired, "wired")
			require.Equal(t, tc.liveStore, w.liveStore, "liveStore")
		})
	}
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
		return selectorFilledByLoadGrants(files) && loadGrantsReturnsNewRepository(files)
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

// loadGrantsReturnsNewRepository is true only when the last return of
// loadGrants is roleperm.NewRepository itself, or an ident assigned from
// that call. A NewRepository in the body that is discarded (and Default
// returned) is the hole this used to miss.
func loadGrantsReturnsNewRepository(files []*ast.File) bool {
	for _, file := range files {
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "loadGrants" || fn.Recv != nil || fn.Body == nil {
				continue
			}
			fromNewRepo := map[string]bool{}
			var lastReturn ast.Expr
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.AssignStmt:
					if len(x.Lhs) != 1 || len(x.Rhs) != 1 {
						return true
					}
					id, ok := x.Lhs[0].(*ast.Ident)
					if !ok || id.Name == "_" {
						return true
					}
					if isRolepermNewRepository(x.Rhs[0]) {
						fromNewRepo[id.Name] = true
					}
				case *ast.ReturnStmt:
					if len(x.Results) == 1 {
						lastReturn = x.Results[0]
					}
				}
				return true
			})
			if lastReturn == nil {
				return false
			}
			if isRolepermNewRepository(lastReturn) {
				return true
			}
			id, ok := lastReturn.(*ast.Ident)
			return ok && fromNewRepo[id.Name]
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
