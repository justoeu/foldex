// Package slug owns the URL-slug helpers shared by links, importer, and
// backup. Lives outside `internal/links` so the importer and backup packages
// can use it without pulling the whole links domain (which would be a
// circular-ish coupling — backup ALREADY imports links for one function;
// this package breaks that).
package slug

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// MaxLen is the upper bound the DB CHECK constraint enforces too. Long titles
// get truncated on a hyphen boundary so we don't slice through a word.
const MaxLen = 80

// formatRE mirrors the DB CHECK constraint:
//
//	^[a-z0-9]+(-[a-z0-9]+)*$
//
// Lowercase ASCII alphanumerics joined by single hyphens, no leading/
// trailing/consecutive hyphens.
var formatRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// onlyDigitsRE guards against `/go/{n}` ambiguity with the numeric-ID path.
// A slug that's pure digits would shadow link IDs in the redirect handler —
// the DB CHECK constraint also rejects these.
var onlyDigitsRE = regexp.MustCompile(`^[0-9]+$`)

// nonSlugCharRE matches any run of characters that are NOT lowercase ASCII
// alphanumeric — they all collapse to a single hyphen during Slugify.
var nonSlugCharRE = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify converts a free-text title into a URL-safe slug.
//
//	"Jira Board — INV-1"  → "jira-board-inv-1"
//	"Conexão M2M"         → "conexao-m2m"
//	"!!!"                 → ""   (caller falls back to "link-<id>")
//
// Steps: NFD-fold accents to plain ASCII; lowercase; collapse non-alphanumerics
// to single hyphens; trim hyphens; cap to MaxLen on a hyphen boundary so
// words stay intact.
func Slugify(title string) string {
	folded, _, err := transform.String(
		transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC),
		title,
	)
	if err != nil {
		folded = title
	}
	folded = strings.ToLower(folded)
	folded = nonSlugCharRE.ReplaceAllString(folded, "-")
	folded = strings.Trim(folded, "-")
	if len(folded) <= MaxLen {
		return folded
	}
	cut := folded[:MaxLen]
	if i := strings.LastIndex(cut, "-"); i > 0 {
		cut = cut[:i]
	}
	return strings.Trim(cut, "-")
}

// IsValid mirrors the DB CHECK constraint exactly. Used by the DTO layer
// before INSERT/UPDATE so user-supplied slugs are rejected with a clean 400
// instead of a Postgres error.
func IsValid(slug string) bool {
	if slug == "" || len(slug) > MaxLen {
		return false
	}
	if onlyDigitsRE.MatchString(slug) {
		return false
	}
	return formatRE.MatchString(slug)
}
