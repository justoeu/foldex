package links

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// slugMaxLen is the upper bound the DB CHECK constraint enforces too. Long
// titles get truncated on a hyphen boundary so we don't slice through a word.
const slugMaxLen = 80

// slugFormat mirrors the DB CHECK constraint:
//
//	^[a-z0-9]+(-[a-z0-9]+)*$
//
// In words: lowercase ASCII alphanumerics joined by single hyphens, no
// leading/trailing/consecutive hyphens.
var slugFormat = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// onlyDigits guards against `/go/{n}` ambiguity with the numeric-ID path. A
// slug that's pure digits (e.g. "42", "0001") would shadow link IDs in the
// redirect handler — the DB CHECK constraint also rejects these.
var onlyDigits = regexp.MustCompile(`^[0-9]+$`)

// nonSlugChar matches any run of characters that are NOT lowercase ASCII
// alphanumeric — they all collapse to a single hyphen during slugify.
var nonSlugChar = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify converts a free-text title into a URL-safe slug.
//
//	"Jira Board — INV-1"  → "jira-board-inv-1"
//	"Conexão M2M"         → "conexao-m2m"
//	"!!!"                 → ""   (caller falls back to "link-<id>")
//
// Steps: NFD-fold accents to plain ASCII; lowercase; collapse non-alphanumerics
// to single hyphens; trim hyphens; cap to slugMaxLen on a hyphen boundary so
// words stay intact.
func Slugify(title string) string {
	// Strip accents: NFD decomposes "ã" to "a" + combining tilde, then we
	// drop the combining marks. Anything still non-ASCII becomes "-" below.
	folded, _, err := transform.String(
		transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC),
		title,
	)
	if err != nil {
		folded = title
	}
	folded = strings.ToLower(folded)
	folded = nonSlugChar.ReplaceAllString(folded, "-")
	folded = strings.Trim(folded, "-")
	if len(folded) <= slugMaxLen {
		return folded
	}
	// Cap on the last hyphen at or before the cap so we never truncate mid-word.
	cut := folded[:slugMaxLen]
	if i := strings.LastIndex(cut, "-"); i > 0 {
		cut = cut[:i]
	}
	return strings.Trim(cut, "-")
}

// SlugIsValid mirrors the DB CHECK constraint exactly. Used by the DTO
// layer before INSERT/UPDATE so user-supplied slugs are rejected with a
// clean 400 instead of a Postgres error.
func SlugIsValid(slug string) bool {
	if slug == "" || len(slug) > slugMaxLen {
		return false
	}
	if onlyDigits.MatchString(slug) {
		return false
	}
	return slugFormat.MatchString(slug)
}
