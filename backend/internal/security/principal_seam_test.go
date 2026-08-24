package security

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryPrincipalSeamAnnotatesTheSpan fails when a place that establishes a
// principal does not stamp it onto the request span.
//
// This guard exists because the feature shipped wrong once and the tests said
// it was right. The identity annotation was first a chi middleware mounted on
// the /api group, with an integration test through the real router that killed
// two mutants. It still missed roughly twenty routes: /api/auth mounts its OWN
// Authenticate inside the auth handler, outside that group, so sessions,
// password change, 2FA and API tokens — the credential-management surface an
// operator most wants attributed — produced spans with no user. Nothing failed.
// No build error, no panic, an identical response.
//
// Moving the call onto the three seams that create a principal fixed the
// instance. It did not close the CLASS: a fourth seam added later (an OAuth
// callback, a public-token gate) would silently reintroduce exactly the same
// hole, and the per-seam integration tests can only prove the seams that exist
// today. This test is what refuses the fourth one.
//
// It matches on the AST call, never on file text, for the reason
// TestEveryPackageUsingTestdbStopsIt documents: a text-matching guard flags
// itself — this very comment names both functions — and a guard that fails for
// the wrong reason teaches people to route around it.
func TestEveryPrincipalSeamAnnotatesTheSpan(t *testing.T) {
	// Same convention as the sibling guards: walk the package tree relative to
	// this file rather than resolving a module root.
	root := filepath.Join("..")

	// authctxtest exists ONLY to hand a principal to handler tests; it never
	// runs inside a request and there is no span to annotate.
	skipDirs := map[string]bool{"authctxtest": true}

	type seam struct {
		file string
		line int
		fn   string
	}
	var missing []seam
	var found int

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		// Production code only: a test may construct a principal freely.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			return perr
		}

		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			// Two independent walks over the SAME function body: the seam is
			// "this function puts a principal in a context", and the obligation
			// is "this function also annotates". Scoping to the enclosing func
			// is what makes the rule mechanical — a call in a sibling function
			// is not proof the seam is covered.
			creates := callsSelector(fn.Body, "authctx", "WithPrincipal")
			if !creates {
				return true
			}
			found++
			if !callsSelector(fn.Body, "tracing", "AnnotatePrincipal") {
				missing = append(missing, seam{
					file: path,
					line: fset.Position(fn.Pos()).Line,
					fn:   fn.Name.Name,
				})
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	// A scan that finds nothing would pass silently forever — the failure mode
	// this whole file exists to refuse.
	if found == 0 {
		t.Fatal("found no authctx.WithPrincipal call sites at all — the scan is broken, not the code")
	}

	for _, m := range missing {
		t.Errorf("%s:%d: %s establishes a principal but never calls tracing.AnnotatePrincipal.\n"+
			"Every place that creates a principal must stamp it onto the request span, or that "+
			"route's traces carry no user and nothing anywhere reports it — see INV-170. Add "+
			"tracing.AnnotatePrincipal(ctx) after building the context, before passing it on.",
			m.file, m.line, m.fn)
	}
}

// callsSelector reports whether body contains a call to pkg.name.
func callsSelector(body *ast.BlockStmt, pkg, name string) bool {
	var hit bool
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != name {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == pkg {
			hit = true
			return false
		}
		return true
	})
	return hit
}
