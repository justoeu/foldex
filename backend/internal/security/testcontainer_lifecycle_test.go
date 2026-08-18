package security_test

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

// A package that starts a shared container MUST stop it from TestMain.
//
// This is a leak with no upper bound, and it stays invisible until the machine
// is unusable. `testdb.Shared` starts one Postgres per test BINARY and holds it
// in a package-level `sync.Once` on purpose — the alternative was 171 container
// starts per run, most of whose failures ("connection refused", "unexpected EOF"
// during migrations) were never bugs in the code under test. The cost of that
// choice is that nothing inside a test can terminate it: a `t.Cleanup` on
// whichever test happened to be first would kill the container while the rest
// of the package still needed it. Only TestMain, after m.Run returns, is late
// enough.
//
// And there is NO safety net behind it. The Makefile exports
// TESTCONTAINERS_RYUK_DISABLED=true, so testcontainers' reaper — the process
// that would normally clean up when the session ends — is not running at all.
// A package that forgets the hook leaves a Postgres (and, in mailoutbox, a
// RabbitMQ) alive until the machine is rebooted.
//
// That is not hypothetical. Two packages were missing the hook, and the symptom
// was 25 orphaned containers — some seventeen hours old — a Docker daemon
// taking 28 seconds to answer `docker info`, and an integration suite whose
// failures read as hung tests. Nothing in that chain pointed at the cause.
//
// A test rather than a review checklist, for the same reason
// TestResetCoversEveryTable and TestNoUnscopedTenantQueries are tests: the next
// package to need a database will be added in a hurry, and "remember TestMain"
// is exactly the sort of thing that gets remembered nineteen times out of
// twenty.
//
// Deliberately NOT behind the `integration` build tag: it starts no container
// of its own, and the run most likely to skip tags is the one where someone is
// iterating quickly — which is precisely when the hook gets forgotten.
func TestEveryPackageUsingTestdbStopsIt(t *testing.T) {
	uses, stops := map[string]bool{}, map[string]bool{}

	require.NoError(t, walkTestFiles(func(path string, f *ast.File) {
		dir := filepath.Dir(path)
		forEachCall(f, func(pkg, fn string, inside string) {
			if pkg != "testdb" {
				return
			}
			switch fn {
			case "Shared":
				uses[dir] = true
			case "StopShared":
				stops[dir] = true
			}
		})
	}))
	require.NotEmpty(t, uses, "no package calls testdb.Shared — the walk is broken, not the tree")

	var missing []string
	for dir := range uses {
		if !stops[dir] {
			missing = append(missing, dir)
		}
	}
	require.Emptyf(t, missing,
		"these packages start a shared container and never stop it: %v\n\n"+
			"Add to one _test.go file in each:\n\n"+
			"\tfunc TestMain(m *testing.M) {\n"+
			"\t\tcode := m.Run()\n"+
			"\t\ttestdb.StopShared()\n"+
			"\t\tos.Exit(code)\n"+
			"\t}\n\n"+
			"The reaper is disabled (TESTCONTAINERS_RYUK_DISABLED=true in the Makefile), "+
			"so without this the container survives the run and they accumulate until "+
			"the Docker daemon stops answering.", missing)
}

// StopShared belongs in TestMain and nowhere else.
//
// Called from an ordinary test — or from a t.Cleanup one registers — it
// terminates the container the whole package shares, under the tests that are
// still using it. The failure that produces is a connection error in some
// unrelated test, about as far from the cause as a symptom gets.
//
// Matched on the CALL, not on the text: an earlier draft of this file compared
// printed function bodies and flagged itself, because its own failure message
// names the function. A guard that fails for the wrong reason teaches people to
// route around it.
func TestStopSharedIsOnlyCalledFromTestMain(t *testing.T) {
	var offenders []string

	require.NoError(t, walkTestFiles(func(path string, f *ast.File) {
		forEachCall(f, func(pkg, fn, inside string) {
			if pkg == "testdb" && fn == "StopShared" && inside != "TestMain" {
				offenders = append(offenders, path+":"+inside)
			}
		})
	}))
	require.Emptyf(t, offenders,
		"StopShared is called outside TestMain in %v — it terminates the container the "+
			"whole package shares, so anywhere earlier kills it under the tests that "+
			"still need it", offenders)
}

// forEachCall reports every `pkg.Fn(...)` in the file, with the name of the
// enclosing top-level function ("" at file scope).
func forEachCall(f *ast.File, visit func(pkg, fn, inside string)) {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			visit(ident.Name, sel.Sel.Name, fn.Name.Name)
			return true
		})
	}
}

// walkTestFiles parses every _test.go file under internal/.
func walkTestFiles(visit func(path string, f *ast.File)) error {
	return filepath.Walk("..", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		// Parsed WITHOUT the build tag filter on purpose: the hook lives in
		// integration-tagged files, and a walk that honoured tags would see
		// none of them and pass vacuously.
		f, perr := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		visit(path, f)
		return nil
	})
}
