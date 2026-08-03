// Package logsafe sanitizes untrusted strings before they enter structured
// logs (CodeQL go/log-injection). slog itself does not escape CR/LF inside
// attribute values, so a value containing "\nERROR fake" can forge lines.
package logsafe

import (
	"strings"
	"unicode"
)

const maxLen = 256

// String strips control characters (including CR/LF) and truncates.
func String(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(min(len(s), maxLen))
	n := 0
	for _, r := range s {
		if r == '\uFFFD' {
			continue
		}
		if unicode.IsControl(r) {
			b.WriteByte('?')
		} else {
			b.WriteRune(r)
		}
		n++
		if n >= maxLen {
			b.WriteString("…")
			break
		}
	}
	return b.String()
}
