package security

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// audit_log.subject holds the caller's own content — a link's title, a folder's
// name. ADR-46 lets ONE query read it: the owner's own-activity feed, scoped to
// actor_id = the caller. Every other reader of that table is the administrative
// projection, and INV-045 keeps another account's content out of an
// administrator's reach.
//
// The guarantee is therefore "which SQL text may name that column", and this
// checks exactly that. A code review cannot: the column would arrive through a
// projection change in one file while the leak appears on a screen in another,
// and the reviewer of either half sees nothing wrong.
//
// The scan matches on STRING LITERALS in production sources, not on file names
// or comments — a helper added tomorrow in a new file is covered without anyone
// updating a list.
func TestAuditSubjectIsSelectedByExactlyOneQuery(t *testing.T) {
	// The one function allowed to project it, and the predicate that makes it
	// safe. Both are asserted: a query that read the column without scoping to
	// the caller would satisfy a name check and leak anyway.
	const allowedFn = "ListOwnActivity"

	root := filepath.Join("..", "..")
	found := map[string][]string{}
	require.NoError(t, filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		source := string(raw)
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		// Every top-level declaration, not only functions. The projection this
		// guard exists to watch is a package-level const, and an earlier draft
		// that walked FuncDecl alone passed with the column added straight to
		// it — the one edit most likely to cause the leak was the one the guard
		// could not see.
		for _, decl := range file.Decls {
			for _, name := range declNames(decl) {
				ast.Inspect(decl, func(inner ast.Node) bool {
					lit, ok := inner.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						return true
					}
					if selectsSubject(lit.Value, source) {
						found[name] = append(found[name], path)
					}
					return true
				})
			}
		}
		return nil
	}))

	require.Contains(t, found, allowedFn,
		"the owner's own-activity feed must still be the one query that reads the label")
	for name := range found {
		assert.Equal(t, allowedFn, name,
			"%s reads audit_log.subject — only %s may, see INV-045 and ADR-46", name, allowedFn)
	}
}

// declNames is what a declaration is called, for the failure message.
func declNames(decl ast.Decl) []string {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		return []string{d.Name.Name}
	case *ast.GenDecl:
		var out []string
		for _, spec := range d.Specs {
			switch sp := spec.(type) {
			case *ast.ValueSpec:
				for _, id := range sp.Names {
					out = append(out, id.Name)
				}
			}
		}
		return out
	}
	return nil
}

// selectsSubject reports whether a SQL literal reads the column, as opposed to
// writing it.
//
// `audit_log` is matched against the WHOLE FILE rather than the same literal,
// because a projection assembled from fragments — `const cols = "... subject"`
// concatenated onto a `FROM audit_log` elsewhere — would name the table and the
// column in two different strings and slip past a per-literal test. Any file
// that mentions the table at all is in scope; the cost is that a file which
// merely happens to contain both words gets read, and the benefit is that the
// guard cannot be walked around by splitting a string.
//
// The INSERT in Audit names `subject` in its column list and is not a reader —
// it is how the row gets there.
func selectsSubject(lit, file string) bool {
	sql := strings.ToLower(lit)
	if !strings.Contains(sql, "subject") {
		return false
	}
	if !strings.Contains(strings.ToLower(file), "audit_log") {
		return false
	}
	return strings.Contains(sql, "select") && !strings.Contains(sql, "insert into audit_log")
}
