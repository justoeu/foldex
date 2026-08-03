// Package logsafe sanitizes untrusted strings before they enter structured
// logs (CodeQL go/log-injection). slog itself does not escape CR/LF inside
// attribute values, so a value containing "\nERROR fake" can forge lines.
//
// Prefer ObjectKey / HTTPPath over String when the value is attacker-
// influenced: CodeQL does not model arbitrary sanitizers, so those helpers
// return only non-tainted structural labels (never the raw input).
package logsafe

import (
	"strings"
	"unicode"
)

const maxLen = 256

// String strips control characters (including CR/LF) and truncates.
// Prefer ObjectKey/HTTPPath for CodeQL-sensitive sinks.
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

// ObjectKey returns a non-tainted prefix label for object-store keys
// (screenshots|images|notes|other). Never echoes the raw key.
func ObjectKey(key string) string {
	switch {
	case strings.HasPrefix(key, "screenshots/"):
		return "screenshots"
	case strings.HasPrefix(key, "images/"):
		return "images"
	case strings.HasPrefix(key, "notes/"):
		return "notes"
	case strings.HasPrefix(key, "files/"):
		return "files"
	default:
		return "other"
	}
}

// HTTPPath returns a non-tainted route class for request paths.
func HTTPPath(path string) string {
	switch {
	case path == "/healthz" || path == "/readyz":
		return "health"
	case strings.HasPrefix(path, "/api/"):
		return "api"
	case strings.HasPrefix(path, "/go/"):
		return "go"
	case strings.HasPrefix(path, "/n/"):
		return "note"
	default:
		return "other"
	}
}
