package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLinkObjectID_SuffixGrammarBlocksTraversal(t *testing.T) {
	// The suffix lands verbatim in an object KEY that the operational mirror
	// later copies off-host; anything beyond a plain extension is an escape
	// vector, not a file type.
	for _, key := range []string{
		"screenshots/123.x/../../backups/db/evil",
		"images/9.png/nested",
		"screenshots/5..png",
		"screenshots/7.",
	} {
		_, _, _, ok := linkObjectID(key)
		assert.False(t, ok, "key %q must not parse", key)
	}
	prefix, id, suffix, ok := linkObjectID("screenshots/123.webp")
	assert.True(t, ok)
	assert.Equal(t, "screenshots/", prefix)
	assert.EqualValues(t, 123, id)
	assert.Equal(t, ".webp", suffix)

	// The screenshot shape — UUID segment then extension — must keep parsing:
	// the grammar exists to block traversal, not the application's own keys.
	_, id, suffix, ok = linkObjectID("screenshots/123.550e8400-e29b-41d4-a716-446655440000.jpg")
	assert.True(t, ok)
	assert.EqualValues(t, 123, id)
	assert.Equal(t, ".550e8400-e29b-41d4-a716-446655440000.jpg", suffix)
}
