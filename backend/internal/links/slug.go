package links

import "foldex/internal/pkg/slug"

// Slugify is a thin re-export of `slug.Slugify` so the public surface of
// `links` stays stable. New callers (importer, backup) should import the
// shared package directly to avoid pulling the whole links domain in for
// one function.
func Slugify(title string) string { return slug.Slugify(title) }

// SlugIsValid is a thin re-export of `slug.IsValid`. Same rationale as
// Slugify above.
func SlugIsValid(s string) bool { return slug.IsValid(s) }
