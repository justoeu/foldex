package notes

import "foldex/internal/pkg/htmlsanitize"

// SanitizeBody re-derives body_html + body_text the same way Create/Update
// Normalize does. Backup restore and any path that writes notes outside the
// DTO layer MUST call this so crafted HTML cannot skip the allowlist.
func SanitizeBody(bodyHTML string) (cleanHTML, plainText string) {
	clean := htmlsanitize.Sanitize(bodyHTML)
	return clean, htmlsanitize.PlainText(clean)
}
