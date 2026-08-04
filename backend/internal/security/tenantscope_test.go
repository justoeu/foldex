package security_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tenantTables are the tables that gained user_id in migration 000017. A query
// reading from one of them without a user_id predicate is a cross-tenant leak
// unless it lives in a file that declares itself system-scoped.
// JOIN is in the alternation because leaving it out is exactly how a real leak
// got past this check: stats.Summary's top-host query reads `FROM click_log`
// and reaches the tenant table only via `JOIN link l`, so the pattern never
// inspected it and the query shipped with no user_id at all.
var tenantTablePat = regexp.MustCompile(`(?i)\b(?:FROM|INTO|UPDATE|JOIN)\s+(link|note|folder|tag|push_subscription)\b`)

// exemptFiles are allowed to query tenant tables unscoped. Each is either a
// public session-less route or a background worker that sweeps every tenant by
// design (docs/SDD-AUTH-RBAC.md §8.2). Adding a file here should be a conscious,
// reviewed act — that is the entire point of the list.
var exemptFiles = map[string]string{
	"links/repository_system.go": "public /go/{id-or-slug} + preview/change-check workers",
	"notes/repository_system.go": "public /n/{id-or-slug}",
	"preview/worker.go":          "requeuePending sweeps every tenant's pending previews on boot",
	"backup/db_slugs.go":         "slug uniqueness is GLOBAL by design — /go/ and /n/ have no session",
	"server/router.go":           "bootstrapPrincipal resolves the admin before a principal exists",
	"testdb/testdb.go":           "test fixture helper",
}

// exemptQueries are individual statements that are correct while looking wrong.
// Keyed by a distinctive substring. Each needs a reason; the list is short on
// purpose — anything longer means the rule is wrong, not the code.
var exemptQueries = map[string]string{
	"EXISTS(SELECT 1 FROM link WHERE slug =":            "slug is globally unique (public /go/{slug})",
	"EXISTS(SELECT 1 FROM note WHERE slug =":            "slug is globally unique (public /n/{slug})",
	"SELECT id FROM link WHERE slug = $1":               "slug is globally unique",
	"FROM folder _lf":                                   "lock-filter fragment; the caller supplies the tenant predicate",
	"FROM _cascade_subtree":                             "subtree table is materialized owner-scoped by DeleteCascade",
	"FROM _restore_link_tags":                           "temp table built from an owner-scoped id mapping",
	"FROM _restore_clicks":                              "temp table built from an owner-scoped id mapping",
	"DELETE FROM push_subscription WHERE endpoint = $1": "sender-only RFC 8030 404/410 cleanup; the user-facing unsubscribe uses DeleteByEndpointForUser",
	"ON CONFLICT (endpoint) DO UPDATE":                  "endpoint is a physical browser channel and stays globally unique (migration 000017 §8)",
	"UPDATE push_subscription SET last_used_at":         "id comes straight from the owner-scoped List(ctx, uid) the sender just ran",

	// The five below are FRAGMENTS: named constants and UNION arms whose OUTER
	// WHERE is appended by the caller (links.linkFrom, notes.noteFrom, the two
	// entries arms, folders.List). Each contains an INNER where from a LATERAL
	// subquery, which is why the "has WHERE ⇒ complete statement" heuristic
	// below cannot skip them automatically. Their scoping is proven
	// behaviourally by the cross-user suite in this package.
	"FROM link l LEFT JOIN LATERAL":      "fragment; List/Get append the tenant predicate",
	"FROM note n LEFT JOIN LATERAL":      "fragment; List/Get append the tenant predicate",
	"SELECT 'link' AS kind":              "entries UNION arm; appendScopeFilters supplies user_id",
	"SELECT 'note' AS kind":              "entries UNION arm; appendScopeFilters supplies user_id",
	"COALESCE(pf.previews, '[]'::jsonb)": "folders.List fragment; the WHERE is built above it",
}

// TestNoUnscopedTenantQueries is the mechanical half of the isolation guarantee.
//
// It walks every SQL string literal in the tree, so a NEW complete query that
// forgets its user_id predicate fails here even if nobody wrote a behavioural
// test for it. It inspects AST literals rather than grepping lines because
// these queries span many lines — FROM and WHERE are almost never on the same
// one, which is what makes the obvious `grep "FROM link"` unusable.
//
// KNOWN LIMIT, stated so nobody mistakes this for full coverage: foldex builds
// several queries by concatenating a constant fragment with a WHERE assembled
// at runtime (entries.appendScopeFilters, folders.List, links.linkFrom). A
// string-literal check cannot see the finished statement, so those fragments
// are listed in exemptQueries and their scoping is proven BEHAVIOURALLY by the
// cross-user suite in this package instead. Deleting a user_id predicate from
// one of them passes this test and fails TestCrossUser_ListsReturnOnlyOwnRows —
// which is why both exist.
func TestNoUnscopedTenantQueries(t *testing.T) {
	root := ".."
	type finding struct{ file, snippet string }
	var findings []finding

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
		for exempt := range exemptFiles {
			if strings.HasSuffix(rel, exempt) {
				return nil
			}
		}
		// Test files legitimately assert against raw tables.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			sql := lit.Value
			if !strings.Contains(sql, "SELECT") && !strings.Contains(sql, "INSERT") &&
				!strings.Contains(sql, "UPDATE") && !strings.Contains(sql, "DELETE") {
				return true
			}
			if !tenantTablePat.MatchString(sql) || strings.Contains(sql, "user_id") {
				return true
			}
			// A literal with no WHERE is a FRAGMENT whose predicate is appended
			// at runtime (entries.appendScopeFilters, folders.List). Static
			// analysis cannot judge those; the behavioural cross-user suite in
			// this package is what covers them.
			if !strings.Contains(strings.ToUpper(sql), "WHERE") {
				return true
			}
			// Compare against the NORMALIZED form: these literals are raw
			// backticked strings full of newlines and alignment spaces, so a
			// substring written naturally would never match.
			norm := strings.Join(strings.Fields(strings.Trim(sql, "`\"")), " ")
			for frag := range exemptQueries {
				if strings.Contains(norm, frag) {
					return true
				}
			}
			snippet := norm
			if len(snippet) > 110 {
				snippet = snippet[:110] + "…"
			}
			findings = append(findings, finding{rel, snippet})
			return true
		})
		return nil
	})
	require.NoError(t, err)

	for _, fd := range findings {
		t.Errorf("unscoped tenant query in %s:\n  %s\n"+
			"  Add a user_id predicate, or move it to repository_system.go and list the file in exemptFiles with a reason.",
			fd.file, fd.snippet)
	}
	assert.Empty(t, findings)
}
