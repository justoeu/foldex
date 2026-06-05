package preview

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type fakeScreenshotter struct {
	calls   int
	payload []byte
	err     error
}

func (f *fakeScreenshotter) Capture(_ context.Context, _ string) ([]byte, error) {
	f.calls++
	return f.payload, f.err
}

type fakeUploader struct {
	calls int
	last  struct {
		key, ct string
		data    []byte
	}
	deleted []string
	err     error
}

func (f *fakeUploader) Upload(_ context.Context, key string, data []byte, ct string) error {
	f.calls++
	f.last.key, f.last.data, f.last.ct = key, data, ct
	return f.err
}

func (f *fakeUploader) DeleteObject(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	return nil
}

// These unit tests exercise the worker's branching that does not require a
// real database — channel-full Enqueue path and concurrency clamping.

func TestNewWorker_ClampsZeroConcurrency(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(nil, 0, time.Second, logger)
	assert.Equal(t, 1, w.concurrent, "concurrency below 1 must be clamped")
}

func TestWithScreenshotFallback_NilArgsIsNoop(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(nil, 1, time.Second, logger)

	w.WithScreenshotFallback(nil, &fakeUploader{})
	assert.Nil(t, w.screenshotter, "nil screenshotter must keep fallback disabled")
	assert.Nil(t, w.uploader)

	w.WithScreenshotFallback(&fakeScreenshotter{}, nil)
	assert.Nil(t, w.screenshotter)
	assert.Nil(t, w.uploader, "nil uploader must keep fallback disabled")

	sc := &fakeScreenshotter{}
	up := &fakeUploader{}
	w.WithScreenshotFallback(sc, up)
	assert.NotNil(t, w.screenshotter, "non-nil pair must enable fallback")
	assert.NotNil(t, w.uploader)
}

func TestMaybeScreenshot_DoesNothingWhenFallbackDisabled(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(nil, 1, time.Second, logger)
	// repo is nil; if we entered the screenshot path it would panic on Get.
	w.maybeScreenshot(context.Background(), 1, "http://example.com")
}

func TestWorker_EnqueueDropsWhenChannelFull(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(nil, 1, time.Second, logger)
	// Saturate the channel without starting any consumers.
	capacity := cap(w.jobs)
	for i := 0; i < capacity; i++ {
		assert.NoError(t, w.Enqueue(int64(i)), "first %d sends must succeed", capacity)
	}
	// The next one must hit the `default` branch and return ErrQueueFull
	// without blocking.
	done := make(chan error, 1)
	go func() {
		done <- w.Enqueue(99999)
	}()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, ErrQueueFull)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Enqueue blocked when channel was full")
	}
}

// TestWorker_EnqueueAfterStopReturnsErrStopped locks the Stop drain semantics:
// post-Stop sends must not silently fill the buffer (would let new HTTP
// requests succeed but never get processed). Tests Stop+Enqueue without Start
// because Start spawns requeuePending which needs a real *pgxpool.Pool.
func TestWorker_EnqueueAfterStopReturnsErrStopped(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(nil, 1, time.Second, logger)
	w.Stop() // safe without Start — cancel guard is nil-safe

	err := w.Enqueue(42)
	assert.ErrorIs(t, err, ErrStopped)
}
