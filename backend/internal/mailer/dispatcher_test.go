package mailer

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type controlledMailer struct {
	gate    <-chan struct{}
	started chan struct{}

	mu        sync.Mutex
	active    int
	maxActive int
	sent      []Message
	failNext  error
}

func (m *controlledMailer) Driver() string { return "smtp" }

func (m *controlledMailer) Send(ctx context.Context, msg Message) error {
	m.mu.Lock()
	m.active++
	if m.active > m.maxActive {
		m.maxActive = m.active
	}
	fail := m.failNext
	m.failNext = nil
	m.mu.Unlock()

	if m.started != nil {
		select {
		case m.started <- struct{}{}:
		default:
		}
	}
	defer func() {
		m.mu.Lock()
		m.active--
		m.mu.Unlock()
	}()

	if fail != nil {
		return fail
	}
	if m.gate != nil {
		select {
		case <-m.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	m.mu.Lock()
	m.sent = append(m.sent, msg)
	m.mu.Unlock()
	return nil
}

func (m *controlledMailer) snapshot() (maxActive int, sent []Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maxActive, append([]Message(nil), m.sent...)
}

func TestDispatcherBoundsConcurrencyAndRejectsAFullQueue(t *testing.T) {
	gate := make(chan struct{})
	transport := &controlledMailer{gate: gate, started: make(chan struct{}, 2)}
	d := NewDispatcher(context.Background(), transport, DispatcherOptions{
		Workers: 2, QueueSize: 1, SendTimeout: time.Second,
	}, discardLogger())
	t.Cleanup(d.Stop)

	require.NoError(t, d.Enqueue(Message{To: "one@example.com"}, "one"))
	select {
	case <-transport.started:
	case <-time.After(time.Second):
		require.FailNow(t, "first worker did not start")
	}
	require.NoError(t, d.Enqueue(Message{To: "two@example.com"}, "two"))
	select {
	case <-transport.started:
	case <-time.After(time.Second):
		require.FailNow(t, "second worker did not start")
	}

	require.NoError(t, d.Enqueue(Message{To: "queued@example.com"}, "queued"))
	assert.ErrorIs(t, d.Enqueue(Message{To: "rejected@example.com"}, "rejected"), ErrQueueFull)

	close(gate)
	require.Eventually(t, func() bool {
		_, sent := transport.snapshot()
		return len(sent) == 3
	}, time.Second, 10*time.Millisecond)
	maxActive, sent := transport.snapshot()
	assert.LessOrEqual(t, maxActive, 2)
	assert.Len(t, sent, 3)
}

func TestDispatcherReservationOwnsQueueCapacityUntilPublishOrRelease(t *testing.T) {
	gate := make(chan struct{})
	transport := &controlledMailer{gate: gate, started: make(chan struct{}, 1)}
	d := NewDispatcher(context.Background(), transport, DispatcherOptions{
		Workers: 1, QueueSize: 1, SendTimeout: time.Second,
	}, discardLogger())
	t.Cleanup(d.Stop)

	require.NoError(t, d.Enqueue(Message{To: "active@example.com"}, "active"))
	select {
	case <-transport.started:
	case <-time.After(time.Second):
		require.FailNow(t, "worker did not start")
	}

	admission, err := d.Reserve()
	require.NoError(t, err)
	_, err = d.Reserve()
	assert.ErrorIs(t, err, ErrQueueFull)
	require.NoError(t, admission.Publish(Message{To: "reserved@example.com"}, "reserved"))
	assert.ErrorIs(t, d.Enqueue(Message{To: "still-full@example.com"}, "still full"), ErrQueueFull)

	close(gate)
	require.Eventually(t, func() bool {
		_, sent := transport.snapshot()
		return len(sent) == 2
	}, time.Second, 10*time.Millisecond)

	released, err := d.Reserve()
	require.NoError(t, err)
	released.Release()
	require.NoError(t, d.Enqueue(Message{To: "after-release@example.com"}, "after release"))
}

func TestDispatcherCancellationStopsAndJoinsWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	transport := &controlledMailer{gate: make(chan struct{}), started: make(chan struct{}, 1)}
	d := NewDispatcher(ctx, transport, DispatcherOptions{
		Workers: 1, QueueSize: 1, SendTimeout: time.Minute,
	}, discardLogger())

	require.NoError(t, d.Enqueue(Message{To: "blocked@example.com"}, "blocked"))
	select {
	case <-transport.started:
	case <-time.After(time.Second):
		require.FailNow(t, "worker did not start")
	}

	cancel()
	stopped := make(chan struct{})
	go func() {
		d.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		require.FailNow(t, "Stop did not join the cancelled worker")
	}
	assert.ErrorIs(t, d.Enqueue(Message{To: "late@example.com"}, "late"), ErrStopped)
}

func TestDispatcherLogsSendErrorsAndKeepsTheWorkerAlive(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	transport := &controlledMailer{failNext: errors.New("smtp unavailable")}
	d := NewDispatcher(context.Background(), transport, DispatcherOptions{
		Workers: 1, QueueSize: 2, SendTimeout: time.Second,
	}, logger)
	t.Cleanup(d.Stop)

	require.NoError(t, d.Enqueue(Message{To: "fails@example.com"}, "first"))
	require.NoError(t, d.Enqueue(Message{To: "works@example.com"}, "second"))
	require.Eventually(t, func() bool {
		_, sent := transport.snapshot()
		return len(sent) == 1
	}, time.Second, 10*time.Millisecond)

	_, sent := transport.snapshot()
	assert.Equal(t, "works@example.com", sent[0].To)
	assert.Contains(t, logs.String(), "smtp unavailable")
	assert.Contains(t, logs.String(), "first")
}
