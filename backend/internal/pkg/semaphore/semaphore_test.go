package semaphore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSemaphore_TryAcquireHonoursCap(t *testing.T) {
	s := New(2)
	assert.Equal(t, 2, s.Cap())
	assert.True(t, s.TryAcquire())
	assert.True(t, s.TryAcquire())
	assert.False(t, s.TryAcquire())
	s.Release()
	assert.True(t, s.TryAcquire())
}

func TestSemaphore_ReleaseWithoutAcquireDoesNotBlockOrUnderflow(t *testing.T) {
	s := New(1)
	s.Release()
	assert.True(t, s.TryAcquire())
	assert.False(t, s.TryAcquire())
}

func TestSemaphore_AcquireWaitsForRelease(t *testing.T) {
	s := New(1)
	require.True(t, s.TryAcquire())
	done := make(chan error, 1)
	go func() { done <- s.Acquire(context.Background(), nil) }()
	select {
	case <-done:
		t.Fatal("Acquire returned while the only slot was held")
	case <-time.After(20 * time.Millisecond):
	}
	s.Release()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Acquire did not return after Release")
	}
}

func TestSemaphore_AcquireContextAndAbort(t *testing.T) {
	s := New(1)
	require.True(t, s.TryAcquire())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, s.Acquire(ctx, nil), context.DeadlineExceeded)

	abort := make(chan struct{})
	close(abort)
	assert.ErrorIs(t, s.Acquire(context.Background(), abort), ErrAborted)
}

func TestKeyed_PerKeyAndGlobalCaps(t *testing.T) {
	k := NewKeyed[string](2, 1)
	assert.True(t, k.TryAcquire("a"))
	assert.False(t, k.TryAcquire("a"), "per-key cap refuses even with global room")
	assert.True(t, k.TryAcquire("b"))
	assert.False(t, k.TryAcquire("c"), "global cap refuses a fresh key")

	k.Release("a")
	assert.True(t, k.TryAcquire("c"))
	k.Release("a") // releasing a non-holder is a no-op, not an underflow
	assert.False(t, k.TryAcquire("d"))
	k.Release("b")
	k.Release("c")
	assert.True(t, k.TryAcquire("d"))
}
