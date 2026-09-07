package crudupdate

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSetBuilder_PositionsNeverRenumber(t *testing.T) {
	var b SetBuilder
	assert.True(t, b.Empty())

	first := b.Set("url", "https://x.test")
	second := b.Set("title", "t")
	b.Raw("updated_at = now()")
	assert.False(t, b.Empty())

	assert.Equal(t, 1, first)
	assert.Equal(t, 2, second)
	assert.Equal(t, []string{
		"url = $1",
		"title = $2",
		"updated_at = now()",
	}, b.sets)
	assert.Equal(t, []any{"https://x.test", "t"}, b.args)

	// Companion expressions (the reset conditions) must be able to reference
	// the position of the value they compare against.
	reset := fmt.Sprintf("url IS DISTINCT FROM $%d", first)
	assert.True(t, strings.Contains(reset, "$1"))
}
