package notemedia

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKeysOnlyReturnsUniqueLocalNoteMedia(t *testing.T) {
	got := Keys(
		`<img src="/api/files/notes/one.jpg"><img src='/api/files/notes/one.jpg'>`,
		`<img src="/api/files/notes/two.png">`,
		`<img src="https://evil.example/x/api/files/notes/foreign.jpg">`,
		`/api/files/images/42.jpg`,
	)
	assert.Equal(t, []string{"notes/one.jpg", "notes/two.png"}, got)
}

func TestRewriteChangesMappedKeyOnly(t *testing.T) {
	value := `<img src="/api/files/notes/old.jpg"><img src="/api/files/notes/legacy.jpg">` +
		`<img src="https://evil.example/api/files/notes/old.jpg">`
	got := Rewrite(value, map[string]string{"notes/old.jpg": "notes/new.jpg"})
	assert.Equal(t,
		`<img src="/api/files/notes/new.jpg"><img src="/api/files/notes/legacy.jpg">`+
			`<img src="https://evil.example/api/files/notes/old.jpg">`, got)
}
