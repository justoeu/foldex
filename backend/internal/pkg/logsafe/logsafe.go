// Package logsafe sanitizes untrusted strings before they enter structured
// logs (CodeQL go/log-injection).
//
// The three long-running binaries all install slog's JSON handler, which
// escapes control characters, so forging a log LINE is not reachable at
// today's sinks. Two things still are, and they are why this package exists:
// the text handler does not escape (rustfs-bootstrap uses it, and a handler is
// one line to swap), and an unbounded attacker-chosen string is unbounded log
// volume regardless of encoding — hence the truncation in String.
//
// String clears CodeQL's go/log-injection at the sinks tried so far (the
// artifact bridge's four, PR #123) — stated as a measurement, not a promise:
// the analyzer's sanitizer model is not ours to depend on, and a future
// version may well flag a String'd value again.
//
// Prefer HTTPPath when a structural label is enough. It returns a class rather
// than the input, so it is safe by construction instead of by the analyzer
// agreeing with us — and it keeps attacker-chosen text out of the log
// entirely, which String only bounds.
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
