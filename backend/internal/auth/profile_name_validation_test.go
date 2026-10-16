package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/httperr"
)

func TestValidateProfileName(t *testing.T) {
	t.Parallel()

	got, err := validateProfileName("  Ada Lovelace  ")
	require.NoError(t, err)
	assert.Equal(t, "Ada Lovelace", got, "the trimmed name is what callers store")

	atCap := strings.Repeat("é", maxProfileNameRunes)
	got, err = validateProfileName(atCap)
	require.NoError(t, err)
	assert.Equal(t, atCap, got, "the cap counts runes, not bytes")

	_, err = validateProfileName(strings.Repeat("é", maxProfileNameRunes+1))
	var he *httperr.Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, "invalid_name", he.Code)
}
