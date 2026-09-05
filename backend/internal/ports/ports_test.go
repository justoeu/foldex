package ports

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInterfacesExist(t *testing.T) {
	// Compile-time assertion that interface shapes stay usable.
	var _ Uploader
	var _ Enqueuer
}

func TestIsObjectTooLarge(t *testing.T) {
	assert.True(t, IsObjectTooLarge(fmt.Errorf("adapter: %w", ErrObjectTooLarge)))
	assert.False(t, IsObjectTooLarge(fmt.Errorf("adapter: %w", errors.New("storage: object exceeds max serve size"))))
	assert.False(t, IsObjectTooLarge(errors.New("not found")))
}

func TestEnqueueSentinelsUnwrap(t *testing.T) {
	assert.ErrorIs(t, fmt.Errorf("preview admission: %w", ErrQueueFull), ErrQueueFull)
	assert.ErrorIs(t, fmt.Errorf("preview admission: %w", ErrStopped), ErrStopped)
	assert.False(t, errors.Is(ErrQueueFull, ErrStopped))
}
