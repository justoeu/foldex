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

// Value allowlists for the three inline-style properties the Tiptap toolbar
// emits (TextAlign / Color / FontFamily). Every style value is matched against
// one of these before it survives sanitization, so no url(), expression(),
// behavior, or arbitrary CSS can ride in through the style attribute — the
// property NAMES are already restricted by AllowStyles; these pin the VALUES.
var (
	textAlignValue = regexp.MustCompile(`^(?:left|right|center|justify)$`)
	// Hex (#abc / #aabbcc) or rgb()/rgba() with integer channels — exactly what
	// Tiptap's Color extension serializes. No named colors / url() / var().
	colorValue = regexp.MustCompile(`^(?:#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})|rgb\(\s*\d{1,3}\s*,\s*\d{1,3}\s*,\s*\d{1,3}\s*\)|rgba\(\s*\d{1,3}\s*,\s*\d{1,3}\s*,\s*\d{1,3}\s*,\s*(?:0|1|0?\.\d+)\s*\))$`)
	// Font stacks are a comma-separated list of family names — letters, digits,
	// spaces, hyphens and quotes only. Blocks parentheses (url(), expression()).
	fontFamilyValue = regexp.MustCompile(`^[a-zA-Z0-9 ,"'\-]+$`)
)

// policy is an explicit allowlist matching exactly what the Tiptap editor
// emits: StarterKit (plus the Image and Link extensions) AND the formatting
// toolbar's TextAlign / Color / FontFamily (rendered as text-align styles on
// block elements and a <span style="color|font-family"> TextStyle mark). Built
// with bluemonday.NewPolicy() rather than a preset (UGCPolicy/StrictPolicy) so
// nothing is silently over-permitted. Notably absent: <table> (no table
// extension wired into the editor — keep the allowlist closed rather than
// speculatively widen it) and the `data:` URL scheme (forces every inline
// image through the upload endpoint instead of an embedded base64 blob).
// Inline-style VALUES are regexp-pinned (see textAlignValue/colorValue/
// fontFamilyValue) so the style attribute can't smuggle url()/expression().
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
		// span carries the TextStyle mark (Color / FontFamily) — inline only,
		// no attributes beyond the style allowlist below.
		"span",
	)

	// Inline rich-text styling from the Tiptap toolbar (TextAlign on block
	// elements; Color / FontFamily via the TextStyle <span> mark). Property
	// names are restricted here and every value is regexp-matched (see the
	// *Value patterns) so the style attribute can't smuggle url()/expression().
	blockEls := []string{"p", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote", "li"}
	p.AllowStyles("text-align").Matching(textAlignValue).OnElements(blockEls...)
	p.AllowStyles("color").Matching(colorValue).Globally()
	p.AllowStyles("font-family").Matching(fontFamilyValue).Globally()

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
