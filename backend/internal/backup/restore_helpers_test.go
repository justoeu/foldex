package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"foldex/internal/notes"
)

func TestSanitizeNoteBody(t *testing.T) {
	html, text := notes.SanitizeBody(`<p>hi<script>alert(1)</script></p>`)
	assert.NotContains(t, html, "<script>")
	assert.Contains(t, text, "hi")

	html, text = notes.SanitizeBody("")
	assert.Equal(t, "", html)
	assert.Equal(t, "", text)
}
