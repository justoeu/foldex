package mailoutbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lifecycleDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestLifecycle_DoubleStartKeepsFirstContext(t *testing.T) {
	var life Lifecycle
	started := make(chan context.Context, 2)
	release := make(chan struct{})
	life.Start(context.Background(), func(ctx context.Context) {
		started <- ctx
		<-release
	})
	first := <-started
	life.Start(context.Background(), func(ctx context.Context) { started <- ctx })
	select {
	case <-started:
		t.Fatal("second Start launched a second loop")
	case <-time.After(50 * time.Millisecond):
	}
	assert.Same(t, first, life.Ctx())
	close(release)
	life.Stop()
}

func TestLifecycle_StartAfterStopIsInert(t *testing.T) {
	var life Lifecycle
	life.Stop()
	life.Start(context.Background(), func(context.Context) {
		t.Error("loop must not run after Stop")
	})
	assert.Nil(t, life.Ctx(), "Start after Stop must not create a context")
	life.Stop() // idempotent
}

func TestReconnectLoop_RetriesUntilStopped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		ReconnectLoop(ctx, func() bool { return false }, lifecycleDiscardLogger(), "test", false, time.Millisecond, func() error {
			if calls.Add(1) >= 3 {
				cancel()
			}
			return errors.New("broker down")
		})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ReconnectLoop did not exit on cancel")
	}
	assert.GreaterOrEqual(t, calls.Load(), int32(3))
}

func TestReconnectLoop_StopsWhenFlagSet(t *testing.T) {
	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		ReconnectLoop(context.Background(), func() bool { return calls.Load() >= 2 }, lifecycleDiscardLogger(), "test", true, time.Millisecond, func() error {
			calls.Add(1)
			return nil
		})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ReconnectLoop ignored the stopped flag")
	}
	require.Equal(t, int32(2), calls.Load())
}
