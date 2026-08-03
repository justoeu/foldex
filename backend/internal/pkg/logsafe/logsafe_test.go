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
