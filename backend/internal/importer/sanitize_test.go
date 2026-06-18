package importer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeImportColor(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty defaults to indigo", "", defaultImportColor},
		{"whitespace defaults to indigo", "   ", defaultImportColor},
		{"valid hex 3 passes", "#abc", "#abc"},
		{"valid hex 6 passes", "#aabbcc", "#aabbcc"},
		{"valid hex 8 passes", "#aabbccdd", "#aabbccdd"},
		{"valid gradient passes", "linear-gradient(135deg, #8B85FF, #6366F1)", "linear-gradient(135deg, #8B85FF, #6366F1)"},
		{"whitespace trimmed", "  #abc  ", "#abc"},
		{"tracking-pixel url() rejected", `red url("https://evil/exfil")`, defaultImportColor},
		{"named color rejected", "red", defaultImportColor},
		{"css custom property rejected", "var(--accent)", defaultImportColor},
		{"expression() rejected", "expression(alert(1))", defaultImportColor},
		{"bad hex length rejected", "#12345", defaultImportColor},
		{"multi-stop gradient rejected", "linear-gradient(135deg, #a, #b, #c)", defaultImportColor},
		{"newline-in-gradient rejected (linebreak splitter)", "linear-gradient(135deg,\n#a,\n#b)", defaultImportColor},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, sanitizeImportColor(tc.in))
		})
	}
}
