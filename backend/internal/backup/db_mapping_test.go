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

func TestRemapRestoreFileKey_FailsClosedOnMalformedLinkKey(t *testing.T) {
	mapping := newIDMapping()
	mapping.linkMap[123] = 456

	for _, key := range []string{
		"screenshots/7.",
		"images/9.png/nested",
		"screenshots/abc.png",
		"screenshots/0.png",
		"images/",
		"screenshots/999.png", // well-formed but not produced by this restore
	} {
		got, included := remapRestoreFileKey(key, mapping)
		assert.False(t, included, "key %q must be excluded, not written verbatim", key)
		assert.Empty(t, got, "key %q must not yield a writable object key", key)
	}

	got, included := remapRestoreFileKey("screenshots/123.png", mapping)
	assert.True(t, included)
	assert.Equal(t, "screenshots/456.png", got)
}
