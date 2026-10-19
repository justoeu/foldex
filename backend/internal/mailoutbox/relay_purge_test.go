package mailoutbox

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// purgeQueue records what Relay.purge delegates: the TTL pair and whether
// the context it received carried the 30s deadline.
type purgeQueue struct {
	purgeErr    error
	purgeN      int64
	gotPubTTL   time.Duration
	gotFailTTL  time.Duration
	hadDeadline bool
	calls       int
}

func (q *purgeQueue) Claim(context.Context, int) ([]Outgoing, error) { return nil, nil }
func (q *purgeQueue) MarkPublished(context.Context, int64, string) error {
	return nil
}
func (q *purgeQueue) MarkFailed(context.Context, int64, string, string, time.Duration, int, bool) error {
	return nil
}
func (q *purgeQueue) RequeueStuck(context.Context, time.Duration) (int64, error) { return 0, nil }

func (q *purgeQueue) Purge(ctx context.Context, publishedTTL, failedTTL time.Duration) (int64, error) {
	q.calls++
	q.gotPubTTL = publishedTTL
	q.gotFailTTL = failedTTL
	_, q.hadDeadline = ctx.Deadline()
	return q.purgeN, q.purgeErr
}

func purgeRelay(t *testing.T, q *purgeQueue, parent context.Context) (*Relay, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	rl := NewRelay(nil, nil, Options{
		PublishedTTL: 48 * time.Hour,
		FailedTTL:    30 * 24 * time.Hour,
	}, slog.New(slog.NewTextHandler(&logs, nil)))
	rl.repo = q
	rl.life.SeedContext(parent)
	t.Cleanup(func() { rl.life.Stop() })
	return rl, &logs
}

func TestRelayPurgeDelegatesTTLsWithDeadline(t *testing.T) {
	q := &purgeQueue{purgeN: 3}
	rl, logs := purgeRelay(t, q, context.Background())

	rl.purge()

	require.Equal(t, 1, q.calls)
	assert.Equal(t, 48*time.Hour, q.gotPubTTL)
	assert.Equal(t, 30*24*time.Hour, q.gotFailTTL)
	assert.True(t, q.hadDeadline, "purge runs under a bounded context, not the raw lifecycle one")
	assert.NotContains(t, logs.String(), "mail outbox purge")
}

func TestRelayPurgeLogsRepositoryError(t *testing.T) {
	q := &purgeQueue{purgeErr: errors.New("delete refused")}
	rl, logs := purgeRelay(t, q, context.Background())

	rl.purge()

	require.Equal(t, 1, q.calls)
	assert.Contains(t, logs.String(), "mail outbox purge")
	assert.Contains(t, logs.String(), "delete refused")
}

func TestRelayPurgeStaysQuietWhileShuttingDown(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel() // lifecycle context already done: a purge error here is noise
	q := &purgeQueue{purgeErr: errors.New("delete refused")}
	rl, logs := purgeRelay(t, q, parent)

	rl.purge()

	require.Equal(t, 1, q.calls, "the queue is still asked; only the log line is suppressed")
	assert.NotContains(t, logs.String(), "mail outbox purge")
}
