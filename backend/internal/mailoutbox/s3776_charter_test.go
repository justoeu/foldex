package mailoutbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type charterQueue struct {
	claim   []Outgoing
	err     error
	settled []int64
}

func (q *charterQueue) Claim(context.Context, int) ([]Outgoing, error) {
	if q.err != nil {
		return nil, q.err
	}
	return q.claim, nil
}
func (q *charterQueue) MarkPublished(_ context.Context, id int64, _ string) error {
	q.settled = append(q.settled, id)
	return nil
}
func (q *charterQueue) MarkFailed(context.Context, int64, string, string, time.Duration, int, bool) error {
	return nil
}
func (q *charterQueue) RequeueStuck(context.Context, time.Duration) (int64, error) { return 0, nil }
func (q *charterQueue) Purge(context.Context, time.Duration, time.Duration) (int64, error) {
	return 0, nil
}

type charterSink struct{ err error }

func (s charterSink) Deliver(context.Context, Outgoing) error { return s.err }
func (s charterSink) Name() string                            { return "charter" }

func TestS3776Charter_Drain(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	t.Run("stopped returns false", func(t *testing.T) {
		rl := &Relay{logger: logger, ctx: ctx, opts: Options{Batch: 2, Workers: 1}}
		rl.stopped.Store(true)
		assert.False(t, rl.drain())
	})

	t.Run("empty claim returns false", func(t *testing.T) {
		rl := &Relay{
			repo:   &charterQueue{},
			sink:   charterSink{},
			logger: logger,
			ctx:    ctx,
			opts:   Options{Batch: 2, Workers: 1, SendTimeout: time.Second},
		}
		assert.False(t, rl.drain())
	})

	t.Run("claim error returns false", func(t *testing.T) {
		rl := &Relay{
			repo:   &charterQueue{err: errors.New("db down")},
			sink:   charterSink{},
			logger: logger,
			ctx:    ctx,
			opts:   Options{Batch: 2, Workers: 1, SendTimeout: time.Second},
		}
		assert.False(t, rl.drain())
	})

	t.Run("full batch delivers and reports more work", func(t *testing.T) {
		q := &charterQueue{claim: []Outgoing{{ID: 1, ClaimToken: "a"}, {ID: 2, ClaimToken: "b"}}}
		rl := &Relay{
			repo:   q,
			sink:   charterSink{},
			logger: logger,
			ctx:    ctx,
			opts:   Options{Batch: 2, Workers: 1, SendTimeout: time.Second},
		}
		assert.True(t, rl.drain())
		assert.Equal(t, []int64{1, 2}, q.settled)
	})
}

func TestS3776Charter_Ping(t *testing.T) {
	t.Parallel()

	t.Run("empty URL", func(t *testing.T) {
		t.Parallel()
		err := Ping(context.Background(), AMQPConfig{})
		require.ErrorIs(t, err, ErrNoBrokerURL)
	})

	t.Run("invalid URL does not echo the secret", func(t *testing.T) {
		t.Parallel()
		err := Ping(context.Background(), AMQPConfig{URL: "amqp://user:super-secret@%zz"})
		require.EqualError(t, err, "mailoutbox: AMQP_URL is not a valid URL")
		assert.NotContains(t, err.Error(), "super-secret")
	})

	t.Run("expired context", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancel()
		time.Sleep(time.Millisecond)
		err := Ping(ctx, AMQPConfig{URL: "amqp://guest:guest@127.0.0.1:5672/"})
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "guest")
	})
}
