// Package htmlsanitize is the server-side trust boundary for note bodies.
// Notes accept rich HTML from a Tiptap editor; the client is never trusted to
// sanitize its own output (a malicious API client could send anything), so
// every note write runs through Sanitize before it touches the DB or is ever
// rendered back as raw HTML (CLAUDE.md threat model: hostile-by-default for
// any user-supplied markup, mirroring cssvalid's posture for tag/folder
// colors).
package htmlsanitize

import (
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
)

// numericAttr restricts img width/height to plain digits — defense in depth
// against anything riding along in those attribute values.
var numericAttr = regexp.MustCompile(`^[0-9]+$`)

// policy is an explicit allowlist matching exactly what Tiptap's StarterKit
// (plus the Image and Link extensions) emits — built with bluemonday.NewPolicy()
// rather than a preset (UGCPolicy/StrictPolicy) so nothing is silently
// over-permitted. Notably absent: <table> (no table extension wired into the
// editor yet — keep the allowlist closed rather than speculatively widen it)
// and the `data:` URL scheme (forces every inline image through the upload
// endpoint instead of an embedded base64 blob).
var policy = newPolicy()

// stripPolicy removes every tag, used by PlainText to derive a search-index
// column from sanitized HTML.
var stripPolicy = bluemonday.StrictPolicy()

func newPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()

	p.AllowElements(
		"p", "br", "hr",
		"strong", "em", "u", "s", "code", "pre", "blockquote",
		"ul", "ol", "li",
		"h1", "h2", "h3", "h4", "h5", "h6",
	)

	// Links: only href is user-controlled. rel/target are derived by
	// bluemonday's Require/Add helpers below, never taken from the input —
	// allowing a user-supplied rel/target could defeat the nofollow/noopener
	// protections those helpers exist to enforce.
	p.AllowAttrs("href").OnElements("a")
	p.RequireNoFollowOnLinks(true)
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	p.RequireParseableURLs(true)

	// Images: src goes through the same URL-scheme allowlist as href (no
	// data: — see package doc). width/height are restricted to plain digits.
	p.AllowAttrs("src").OnElements("img")
	p.AllowAttrs("alt").OnElements("img")
	p.AllowAttrs("width", "height").Matching(numericAttr).OnElements("img")

	// Applies to every URL-bearing attribute the policy recognizes (href,
	// src, ...) — deliberately excludes "data" and "javascript".
	p.AllowURLSchemes("http", "https", "mailto")
	// Note inline images are served from this backend via relative paths
	// (/api/files/notes/<uuid>.jpg, returned by the image-upload endpoint) —
	// bluemonday rejects schemeless URLs unless relative URLs are explicitly
	// allowed.
	p.AllowRelativeURLs(true)

	return p
}

// Sanitize strips everything outside the Tiptap-StarterKit allowlist:
// script tags, event handler attributes, javascript:/data: URLs, and any
// element/attribute not explicitly allowed above.
func Sanitize(html string) string {
	return policy.Sanitize(html)
}

// PlainText strips all markup, returning trimmed plain text suitable for the
// note.body_text search column (ILIKE/trigram). Always derived server-side
// from the already-sanitized HTML — never accepted from the client — so the
// search index can't drift from what's actually stored/rendered.
func PlainText(html string) string {
	return strings.TrimSpace(stripPolicy.Sanitize(html))
}
