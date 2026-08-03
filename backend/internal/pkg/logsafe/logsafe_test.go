package logsafe

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestString_StripsControlAndTruncates(t *testing.T) {
	assert.Equal(t, "hello", String("hello"))
	assert.Equal(t, "a?b?c", String("a\nb\rc"))
	assert.Equal(t, "", String(""))
	long := strings.Repeat("x", 300)
	got := String(long)
	assert.LessOrEqual(t, len([]rune(got)), maxLen+1) // + ellipsis
	assert.True(t, strings.HasSuffix(got, "…"))
}

func TestObjectKey_AndHTTPPath(t *testing.T) {
	assert.Equal(t, "screenshots", ObjectKey("screenshots/1.jpg"))
	assert.Equal(t, "images", ObjectKey("images/2.png"))
	assert.Equal(t, "notes", ObjectKey("notes/uuid.jpg"))
	assert.Equal(t, "other", ObjectKey("../etc/passwd"))
	assert.Equal(t, "api", HTTPPath("/api/links"))
	assert.Equal(t, "go", HTTPPath("/go/42"))
	assert.Equal(t, "health", HTTPPath("/healthz"))
	assert.Equal(t, "other", HTTPPath("/evil\npath"))
}
