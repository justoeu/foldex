package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasAllowedPrefix(t *testing.T) {
	cases := []struct {
		key string
		ok  bool
	}{
		{"screenshots/1.png", true},
		{"images/42.jpg", true},
		{"screenshots/", true},
		{"images/", true},
		{"other/1.png", false},
		{"", false},
		{"/screenshots/1.png", false},  // leading slash
		{"sshots/1.png", false},        // partial match
		{"screenshotsfoo.png", false},  // no trailing slash
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			assert.Equal(t, tc.ok, hasAllowedPrefix(tc.key))
		})
	}
}

func TestContentTypeFor(t *testing.T) {
	cases := []struct {
		key string
		ct  string
	}{
		{"images/1.png", "image/png"},
		{"images/1.PNG", "image/png"}, // case-insensitive ext
		{"images/1.jpg", "image/jpeg"},
		{"images/1.jpeg", "image/jpeg"},
		{"images/1.gif", "image/gif"},
		{"images/1.webp", "image/webp"},
		{"images/1.bin", "application/octet-stream"},
		{"images/no-ext", "application/octet-stream"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			assert.Equal(t, tc.ct, contentTypeFor(tc.key))
		})
	}
}
