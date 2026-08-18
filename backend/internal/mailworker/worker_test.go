package mailworker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"foldex/internal/mailoutbox"
	"foldex/internal/pkg/secrets"
)

func discard() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func TestNew_ClampsTheKnobsInsteadOfRefusing(t *testing.T) {
	cfg := mailoutbox.AMQPConfig{URL: "amqp://localhost:5672/"}

	w := New(nil, nil, cfg, Options{Prefetch: 0, MaxAttempts: 0, SendTimeout: 0}, nil)
	require.Equal(t, DefaultPrefetch, w.opts.Prefetch)
	require.Equal(t, DefaultMaxAttempts, w.opts.MaxAttempts)
	require.Equal(t, DefaultSendTimeout, w.opts.SendTimeout)
	require.NotNil(t, w.logger)

	// SMTP is serial I/O; an operator who asks for 5000 in flight gets the
	// ceiling rather than a worker that strands thousands of unacked messages
	// the moment it dies.
	require.Equal(t, 64, New(nil, nil, cfg, Options{Prefetch: 5000}, discard()).opts.Prefetch)
	// Nonsensical gets the default rather than the floor, the same way
	// config.normalizeMail resolves it — two clamps for one value that
	// disagreed would make the effective prefetch depend on which layer ran.
	require.Equal(t, DefaultPrefetch, New(nil, nil, cfg, Options{Prefetch: -3}, discard()).opts.Prefetch)

	// A zero Topology still names real queues.
	require.Equal(t, mailoutbox.DefaultQueue, w.cfg.Topology.QueueName())
}

// The transport's own text never reaches a log line or a database column: an
// SMTP rejection routinely quotes the envelope back, and the envelope of these
// messages is a recipient address.
func TestSendReason_NormalizesWithoutQuotingTheTransport(t *testing.T) {
	require.Equal(t, "canceled", sendReason(context.Canceled))
	require.Equal(t, "timeout", sendReason(context.DeadlineExceeded))
	require.Equal(t, "undecryptable_payload", sendReason(secrets.ErrDecrypt))

	// Anything else collapses to one token — including an error whose text
	// carries the recipient, which is exactly the leak this prevents.
	leaky := errors.New("550 5.1.1 <grace@x.test>: recipient rejected")
	require.Equal(t, "send_failed", sendReason(leaky))
	require.NotContains(t, sendReason(leaky), "grace@x.test")

	// A wrapped cancellation still reads as one, so a shutdown is not filed as
	// a delivery failure against the provider.
	require.Equal(t, "canceled", sendReason(&net.OpError{Op: "dial", Err: context.Canceled}))
}

// Stop must be safe before Start, and Start after Stop must not resurrect the
// loop — the same lifecycle contract every other worker in this codebase has,
// and the one that keeps a shutdown from racing a reconnect.
func TestLifecycle_StopIsIdempotentAndStartAfterStopIsInert(t *testing.T) {
	w := New(nil, nil, mailoutbox.AMQPConfig{URL: "amqp://127.0.0.1:1/"}, Options{}, discard())

	w.Stop()
	w.Stop()

	w.Start(context.Background())
	require.Nil(t, w.ctx, "Start after Stop must not create a context")
}

// A cancelled parent has to bring the loop down even while it is sitting in the
// reconnect delay, or shutdown would block for the full backoff on an
// unreachable broker.
func TestLifecycle_StopUnblocksTheReconnectWait(t *testing.T) {
	// Port 1 is closed, so consume() fails immediately and the loop parks in
	// its reconnect wait.
	w := New(nil, nil, mailoutbox.AMQPConfig{URL: "amqp://127.0.0.1:1/"}, Options{}, discard())
	w.Start(context.Background())

	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(reconnectDelay):
		t.Fatal("Stop waited out the reconnect delay instead of cancelling it")
	}
}
