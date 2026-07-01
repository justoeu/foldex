package notes

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractImageKeys(t *testing.T) {
	html := `<p>hi</p><img src="/api/files/notes/abc-123.jpg" alt="a">` +
		`<img src="/api/files/notes/def-456.png" alt="b">`
	keys := extractImageKeys(html)
	assert.ElementsMatch(t, []string{"notes/abc-123.jpg", "notes/def-456.png"}, keys)
}

func TestExtractImageKeys_DedupesRepeatedImage(t *testing.T) {
	html := `<img src="/api/files/notes/same.jpg"><img src="/api/files/notes/same.jpg">`
	keys := extractImageKeys(html)
	assert.Equal(t, []string{"notes/same.jpg"}, keys)
}

func TestExtractImageKeys_IgnoresNonNotesImages(t *testing.T) {
	html := `<img src="/api/files/images/42.jpg"><img src="https://example.com/x.jpg">`
	keys := extractImageKeys(html)
	assert.Empty(t, keys)
}

func TestExtractImageKeys_EmptyBody(t *testing.T) {
	assert.Empty(t, extractImageKeys(""))
	assert.Empty(t, extractImageKeys("<p>no images here</p>"))
}

func TestParsePositiveID(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"42", 42, true},
		{"0", 0, false},
		{"", 0, false},
		{"abc", 0, false},
		{"-5", 0, false},
		{"my-slug", 0, false},
	}
	for _, tc := range cases {
		got, ok := parsePositiveID(tc.in)
		assert.Equal(t, tc.ok, ok, tc.in)
		if tc.ok {
			assert.Equal(t, tc.want, got, tc.in)
		}
	}
}
