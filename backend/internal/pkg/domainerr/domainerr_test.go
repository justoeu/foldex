package domainerr_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/domainerr"
)

func TestInvalidInputPreservesSemanticIdentityAndMessage(t *testing.T) {
	err := fmt.Errorf("set tags: %w", domainerr.InvalidInput("unknown tag id"))
	require.ErrorIs(t, err, domainerr.ErrInvalidInput)
	message, ok := domainerr.InvalidInputMessage(err)
	assert.True(t, ok)
	assert.Equal(t, "unknown tag id", message)
	assert.False(t, errors.Is(err, domainerr.ErrNotFound))
}
