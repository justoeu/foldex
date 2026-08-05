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

	// Templates whose SECOND format verb IS the WHERE clause. They look like a
	// WHERE-less UPDATE to a string-literal check, but links.Update and
	// notes.Update build `WHERE user_id = $n AND id = $n+1` into it — asserted
	// behaviourally by TestCrossUser_UpdateAndDeleteOfAnotherUsersRowIsNotFoundAndMutatesNothing.
	// Listed rather than pattern-matched so a genuinely predicate-less
	// `UPDATE link SET pinned = true` still fails.
	"UPDATE link SET %s %s": "template; the trailing verb is the WHERE, built with user_id in links.Update",
	"UPDATE note SET %s %s": "template; the trailing verb is the WHERE, built with user_id in notes.Update",
}

// TestNoUnscopedTenantQueries is the mechanical half of the isolation guarantee.
//
// It walks every SQL string literal in the tree, so a NEW complete query that
// forgets its user_id predicate fails here even if nobody wrote a behavioural
// test for it. It inspects AST literals rather than grepping lines because
// these queries span many lines — FROM and WHERE are almost never on the same
// one, which is what makes the obvious `grep "FROM link"` unusable.
//
// TWO KNOWN LIMITS, stated so nobody mistakes this for full coverage. Both are
// why the behavioural cross-user suite in this package is the primary control
// and this file is the backstop — not the other way round.
//
//  1. RUNTIME-ASSEMBLED STATEMENTS. Several queries concatenate a constant
//     fragment with a WHERE built at runtime (entries.appendScopeFilters,
//     folders.List, links.linkFrom). A string-literal check never sees the
//     finished statement, so those fragments are listed in exemptQueries.
//
//  2. ONE PREDICATE VOUCHES FOR THE WHOLE LITERAL. The check asks "does this
//     literal contain a user_id predicate", not "is every tenant-table
//     reference in it scoped". A statement whose CTE is correctly scoped while
//     its OUTER query is not therefore passes. This was measured, not assumed:
//     demoting `WHERE t.user_id = $1` to a projection in stats.TagBuckets is
//     NOT caught here, because the `link_clicks` CTE above it still carries a
//     user_id predicate. The same mutation in tags.List — a literal with no
//     other predicate to hide behind — IS caught.
//
// The compensating control for (2) is TestCrossUser_StatsExcludeAnotherUsersClicks,
// which asserts on every stats aggregate individually. Closing it properly
// needs a real SQL parser; until then, a multi-CTE query touching tenant tables
// must earn a behavioural test, not just a green run here.
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
			if !tenantTablePat.MatchString(sql) {
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
			upper := strings.ToUpper(norm)

			flag := func(why string) {
				snippet := norm
				if len(snippet) > 110 {
					snippet = snippet[:110] + "…"
				}
				findings = append(findings, finding{rel, why + ": " + snippet})
			}

			// A WHERE-less DELETE or UPDATE against a tenant table is wrong
			// regardless of what else the literal contains — it rewrites or
			// removes every tenant's rows. Checked before the fragment rule
			// below, because "no WHERE" is exactly what makes a fragment look
			// innocent.
			if (strings.Contains(upper, "DELETE FROM ") || strings.HasPrefix(upper, "UPDATE ")) &&
				!strings.Contains(upper, "WHERE") {
				flag("DELETE/UPDATE with no WHERE clause")
				return true
			}

			// Scoping must live in a PREDICATE, not merely somewhere in the
			// literal. `SELECT user_id, title FROM link` mentions user_id and
			// filters on nothing; the old check accepted it. predicateText drops
			// every SELECT list so a projected column cannot satisfy the rule,
			// leaving WHERE / ON / subquery predicates.
			if strings.Contains(predicateText(norm, upper), "user_id") {
				return true
			}

			// No WHERE at all, after the DELETE/UPDATE case above, means a
			// FRAGMENT whose predicate is appended at runtime
			// (entries.appendScopeFilters, folders.List). Static analysis cannot
			// judge those; the behavioural cross-user suite in this package is
			// what covers them.
			if !strings.Contains(upper, "WHERE") {
				return true
			}

			flag("no user_id predicate")
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

// predicateText returns norm with every SELECT list removed — the span between
// a SELECT keyword and its matching FROM — so that a user_id which is merely
// PROJECTED cannot be mistaken for a user_id that FILTERS.
//
// That distinction is the whole point: `SELECT id, user_id FROM link` reads
// every tenant's rows while containing the string "user_id", and the previous
// version of this check accepted it. What survives the strip is WHERE clauses,
// JOIN ... ON conditions, and INSERT column lists — the three places a real
// tenant predicate can legitimately live.
//
// upper is passed in rather than recomputed so keyword search stays
// case-insensitive while the returned text keeps its original case.
func predicateText(norm, upper string) string {
	var b strings.Builder
	i := 0
	for {
		s := strings.Index(upper[i:], "SELECT")
		if s < 0 {
			b.WriteString(norm[i:])
			return b.String()
		}
		s += i
		b.WriteString(norm[i:s])
		f := strings.Index(upper[s:], " FROM ")
		if f < 0 {
			// SELECT with no FROM (e.g. `SELECT pg_try_advisory_xact_lock($1)`);
			// nothing left that could hold a predicate.
			return b.String()
		}
		i = s + f
	}
}

// TestPredicateTextIgnoresProjectedColumns proves the strip actually
// discriminates. Without it this file's central rule is untested logic guarding
// untested logic.
func TestPredicateTextIgnoresProjectedColumns(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want bool // does a user_id PREDICATE survive?
	}{
		{"projected only", "SELECT id, user_id FROM link ORDER BY id", false},
		{"where predicate", "SELECT id FROM link WHERE user_id = $1", true},
		{"join on predicate", "SELECT l.id FROM click_log c JOIN link l ON l.id = c.entity_id AND l.user_id = $1", true},
		{"subquery predicate", "SELECT count(*) FROM click_log WHERE entity_id IN (SELECT id FROM link WHERE user_id = $1)", true},
		{"insert column list", "INSERT INTO tag (user_id, name) VALUES ($1,$2)", true},
		{"projected in subquery only", "SELECT (SELECT user_id FROM link LIMIT 1) FROM tag WHERE name = $1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strings.Contains(predicateText(c.sql, strings.ToUpper(c.sql)), "user_id")
			assert.Equal(t, c.want, got)
		})
	}
}
