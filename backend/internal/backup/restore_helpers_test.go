package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"foldex/internal/pkg/htmlsanitize"
)

func TestSanitizeNoteBody(t *testing.T) {
	html, text := htmlsanitize.SanitizeAndPlain(`<p>hi<script>alert(1)</script></p>`)
	assert.NotContains(t, html, "<script>")
	assert.Contains(t, text, "hi")

	html, text = htmlsanitize.SanitizeAndPlain("")
	assert.Equal(t, "", html)
	assert.Equal(t, "", text)
}
