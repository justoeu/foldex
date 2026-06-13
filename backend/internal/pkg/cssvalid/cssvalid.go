// Package cssvalid validates user-supplied CSS values (today: tag + folder
// colors) against a closed allowlist. The frontend renders these through
// `style={{ background: tag.color }}` and `color-mix(…, tag.color)` — without
// validation an attacker could plant `red url("https://evil/exfil")` and turn
// every chip render into a tracking pixel. The threat model is single-user,
// but defense-in-depth keeps this safe if the deployment is ever exposed.
package cssvalid

import "regexp"

// colorPattern accepts:
//   - 3, 4, 6, or 8-digit hex colors (#abc, #abcd, #aabbcc, #aabbccdd) —
//     the only valid CSS hex lengths; #12345 is illegal CSS and must be
//     rejected to keep parity with what the browser would accept anyway
//   - linear-gradient(135deg, #hex, #hex) — the only gradient form the UI
//     produces (CLAUDE.md §4: "tag.color is a plain CSS string — either a hex
//     OR a linear-gradient(135deg, c1, c2)")
//
// Whitespace is allowed inside the gradient form. Anything else (url(),
// expression(), HSL with extras, CSS custom properties, named colors,
// multi-stop gradients) is rejected.
var hexPart = `#(?:[0-9a-fA-F]{3,4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})`

// Whitespace is intentionally limited to spaces and tabs (not the broader `\s`
// which includes newlines) — a newline inside the gradient string can split
// the value across CSS declarations once concatenated, and the comment above
// says "whitespace allowed", not "linebreaks allowed".
var colorPattern = regexp.MustCompile(`^(?:` + hexPart + `|linear-gradient\([ \t]*135deg[ \t]*,[ \t]*` + hexPart + `[ \t]*,[ \t]*` + hexPart + `[ \t]*\))$`)

// IsValidColor reports whether c matches the allowed hex/gradient shapes.
// Empty strings are NOT accepted — callers should default before validating.
func IsValidColor(c string) bool {
	if c == "" || len(c) > 200 {
		return false
	}
	return colorPattern.MatchString(c)
}
