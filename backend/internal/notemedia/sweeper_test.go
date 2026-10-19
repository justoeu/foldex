package notemedia

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/testsupport"
)

type sweeperFakeDeleter struct{ deleted [][]string }

func (d *sweeperFakeDeleter) DeleteObject(_ context.Context, key string) error {
	d.deleted = append(d.deleted, []string{key})
	return nil
}

func (d *sweeperFakeDeleter) DeleteObjects(_ context.Context, keys []string) error {
	d.deleted = append(d.deleted, keys)
	return nil
}

// lockedBuffer serializes the sweeper goroutine's log writes against the
// test goroutine's reads; a bare strings.Builder is a data race there.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func sweeperTestLogger(buf *lockedBuffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// waitWithin fails instead of hanging the suite when the sweep goroutine
// never exits after cancellation.
func waitWithin(t *testing.T, s *Sweeper) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		s.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after context cancellation")
	}
}

func TestNewSweeperAppliesDefaults(t *testing.T) {
	t.Parallel()
	var buf lockedBuffer
	logger := sweeperTestLogger(&buf)
	deleter := &sweeperFakeDeleter{}

	s := NewSweeper(nil, deleter, logger)
	require.NotNil(t, s)
	assert.Nil(t, s.pool)
	assert.Same(t, deleter, s.storage)
	assert.Same(t, logger, s.logger)
	assert.Equal(t, defaultSweepInterval, s.interval)
	assert.Equal(t, DeleteBatchMax, s.limit)
	assert.NotNil(t, s.done)
	select {
	case <-s.done:
		t.Fatal("done must be open before Start runs the goroutine to exit")
	default:
	}
}

func TestSweeperStartExitsOnCancelAndWaitReturns(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var buf lockedBuffer
	// Nil storage: SystemSweepExpired short-circuits without touching the
	// (nil) pool, so ticks are harmless no-ops.
	s := NewSweeper(nil, nil, sweeperTestLogger(&buf))
	s.interval = time.Millisecond
	s.Start(ctx)
	waitWithin(t, s)
	assert.NotContains(t, buf.String(), "note media sweep failed")
}

func TestSweeperTicksWithoutStorageAndStops(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())

	var buf lockedBuffer
	s := NewSweeper(nil, nil, sweeperTestLogger(&buf))
	s.interval = time.Millisecond
	s.Start(ctx)
	// Several ticks elapse while the context is live; none may log a failure.
	time.Sleep(15 * time.Millisecond)
	cancel()
	waitWithin(t, s)
	assert.NotContains(t, buf.String(), "note media sweep failed")
	assert.NotContains(t, buf.String(), "expired note media removed")
}

func TestSweeperLogsSweepFailureAndKeepsRunning(t *testing.T) {
	t.Parallel()
	pool := testsupport.ClosedPool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var buf lockedBuffer
	s := NewSweeper(pool, &sweeperFakeDeleter{}, sweeperTestLogger(&buf))
	s.interval = time.Millisecond
	s.Start(ctx)

	require.Eventually(t, func() bool {
		return strings.Contains(buf.String(), "note media sweep failed")
	}, 2*time.Second, time.Millisecond,
		"a closed pool must surface as a logged sweep failure, not a panic or silent death")

	cancel()
	waitWithin(t, s)
}
