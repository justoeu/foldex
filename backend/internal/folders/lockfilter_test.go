package folders

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSQLNotInLockedFolder(t *testing.T) {
	s := SQLNotInLockedFolder("l")
	assert.Contains(t, s, "l.folder_id IS NULL")
	assert.Contains(t, s, "password_hash IS NOT NULL")
	assert.True(t, strings.Contains(s, "_lf.id = l.folder_id"))
	// notes alias
	n := SQLNotInLockedFolder("n")
	assert.Contains(t, n, "n.folder_id")
}
