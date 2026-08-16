// Package slug owns URL-slug allocation shared by links, notes, importer, and
// backup without coupling those domains to one another.
package slug

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// MaxUniqueAttempts caps collision suffixes (-2, -3, …) for UniqueAvailable.
const MaxUniqueAttempts = 1000

// ExistsFn reports whether candidate is already taken.
type ExistsFn func(ctx context.Context, candidate string) (exists bool, err error)

// UniqueAvailable returns base if free, else base-2, base-3, …
func UniqueAvailable(ctx context.Context, base string, exists ExistsFn) (string, error) {
	if base == "" {
		return "", fmt.Errorf("unique slug: empty base")
	}
	for attempt := 1; attempt < MaxUniqueAttempts; attempt++ {
		candidate := candidateForAttempt(base, attempt)
		taken, err := exists(ctx, candidate)
		if err != nil {
			return "", fmt.Errorf("check slug availability: %w", err)
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("unique slug: exhausted attempts for %q", base)
}

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
	return truncate(folded, MaxLen)
}

func candidateForAttempt(base string, attempt int) string {
	stem, suffix := candidateParts(base, attempt)
	candidate := stem + suffix
	if suffix == "" && onlyDigitsRE.MatchString(candidate) {
		candidate = truncate(stem, MaxLen-len("-x")) + "-x"
	}
	return candidate
}

func candidateParts(base string, attempt int) (string, string) {
	suffix := ""
	if attempt > 1 {
		suffix = fmt.Sprintf("-%d", attempt)
	}
	return truncate(base, MaxLen-len(suffix)), suffix
}

func truncate(value string, maxLen int) string {
	if len(value) <= maxLen {
		return value
	}
	cut := value[:maxLen]
	if value[maxLen] != '-' {
		if i := strings.LastIndex(cut, "-"); i > 0 {
			cut = cut[:i]
		}
	}
	return strings.Trim(cut, "-")
}

// FromTitleOrFallback slugifies title and falls back to "{prefix}-{id}" when
// the title yields an empty slug (punctuation-only titles).
func FromTitleOrFallback(title, prefix string, id int64) string {
	s := Slugify(title)
	if s == "" {
		return fmt.Sprintf("%s-%d", prefix, id)
	}
	return s
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
