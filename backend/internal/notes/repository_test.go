package notes

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"foldex/internal/notemedia"
)

func TestExtractImageKeys(t *testing.T) {
	html := `<p>hi</p><img src="/api/files/notes/abc-123.jpg" alt="a">` +
		`<img src="/api/files/notes/def-456.png" alt="b">`
	keys := notemedia.Keys(html)
	assert.ElementsMatch(t, []string{"notes/abc-123.jpg", "notes/def-456.png"}, keys)
}

func TestExtractImageKeys_DedupesRepeatedImage(t *testing.T) {
	html := `<img src="/api/files/notes/same.jpg"><img src="/api/files/notes/same.jpg">`
	keys := notemedia.Keys(html)
	assert.Equal(t, []string{"notes/same.jpg"}, keys)
}

func TestExtractImageKeys_IgnoresNonNotesImages(t *testing.T) {
	html := `<img src="/api/files/images/42.jpg"><img src="https://example.com/x.jpg">`
	keys := notemedia.Keys(html)
	assert.Empty(t, keys)
}

func TestExtractImageKeys_EmptyBody(t *testing.T) {
	assert.Empty(t, notemedia.Keys(""))
	assert.Empty(t, notemedia.Keys("<p>no images here</p>"))
}
