package depstatus

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartOnNilCheckerIsANoOp(t *testing.T) {
	t.Parallel()
	var c *Checker
	assert.NotPanics(t, func() { c.Start(context.Background()) })
}

func TestStartProbesImmediatelyThenOnTTLAndStopsOnCancel(t *testing.T) {
	t.Parallel()
	var n atomic.Int32
	c := New()
	c.ttl = 5 * time.Millisecond
	c.Add(ObjectStore, func(context.Context) error {
		n.Add(1)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	c.Start(ctx)

	require.Eventually(t, func() bool { return n.Load() >= 3 },
		2*time.Second, time.Millisecond,
		"Start must probe once immediately and then again on each ttl tick")

	cancel()
	// Let an in-flight tick settle, then the count must stay put.
	time.Sleep(20 * time.Millisecond)
	stopped := n.Load()
	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, stopped, n.Load(), "no probe may run after the context is cancelled")
}

func TestStartWithCancelledContextStillProbesOnce(t *testing.T) {
	t.Parallel()
	var n atomic.Int32
	c := New()
	c.ttl = time.Hour
	c.Add(ObjectStore, func(context.Context) error {
		n.Add(1)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Start(ctx)

	require.Eventually(t, func() bool { return n.Load() == 1 },
		2*time.Second, time.Millisecond,
		"the immediate snapshot runs before the loop observes cancellation")
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, int32(1), n.Load())
}
