//go:build integration

package mailoutbox

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"foldex/internal/mailer"
	"foldex/internal/testdb"
)

func silent() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

// deadLetterFixture wires a real Postgres and a real broker, and leaves one
// message sitting in the outbox already marked published — the state a message
// is in once the relay has handed it to the broker.
func deadLetterFixture(t *testing.T) (*Repository, *Outbox, Topology, string, int64) {
	t.Helper()
	ctx := context.Background()
	url := startRabbit(t)
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))

	o := testOutbox(t)
	repo := NewRepository(pool)
	tp := Topology{}.WithDefaults()

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, o.EnqueueTx(ctx, tx, mailer.SessionRevokedMessage("grace@x.test"), "en"))
	require.NoError(t, tx.Commit(ctx))

	claimed, err := repo.Claim(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, repo.MarkPublished(ctx, claimed[0].ID, claimed[0].ClaimToken))

	return repo, o, tp, url, claimed[0].ID
}

func statusOf(t *testing.T, repo *Repository, id int64) (string, string) {
	t.Helper()
	var status, lastErr string
	require.NoError(t, repo.pool.QueryRow(context.Background(),
		`SELECT status, coalesce(last_error,'') FROM mail_outbox WHERE id = $1`, id).
		Scan(&status, &lastErr))
	return status, lastErr
}

// The property the watcher exists for. Handing delivery to a broker moves the
// truth about it out of the database; without this loop every row would read
// 'published' whether the message arrived or died on the last rung.
func TestDeadLetterWatcher_TurnsABrokerGiveUpBackIntoAFailedRow(t *testing.T) {
	repo, _, tp, url, id := deadLetterFixture(t)

	conn, err := amqp.Dial(url)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	pub, err := NewConfirmingChannel(conn)
	require.NoError(t, err)
	require.NoError(t, tp.Declare(pub.Raw()))

	w := NewDeadLetterWatcher(repo, AMQPConfig{URL: url, Topology: tp}, silent())
	w.Start(context.Background())
	defer w.Stop()

	body, err := encodeWire(Outgoing{ID: id, Template: mailer.TemplateSessionRevoked, Recipient: "grace@x.test"})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, tp.Republish(ctx, pub, body, 4, "send_failed", true))

	require.Eventually(t, func() bool {
		status, _ := statusOf(t, repo, id)
		return status == "failed"
	}, 30*time.Second, 100*time.Millisecond, "the dead-lettered message should settle its row")

	_, lastErr := statusOf(t, repo, id)
	require.Equal(t, "send_failed", lastErr,
		"the normalized reason travels outside the sealed payload and must reach the row")
}

// The watcher must never need the encryption key. It is the whole reason the
// reporting lives in the backend rather than in the worker: a process that can
// settle rows should not also be able to read reset links.
func TestDeadLetterWatcher_SettlesWithoutOpeningThePayload(t *testing.T) {
	repo, _, tp, url, id := deadLetterFixture(t)

	conn, err := amqp.Dial(url)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	pub, err := NewConfirmingChannel(conn)
	require.NoError(t, err)
	require.NoError(t, tp.Declare(pub.Raw()))

	// A watcher built with a repository and NO outbox cipher at all. If settling
	// ever needed to decrypt, this could not compile, let alone pass.
	w := NewDeadLetterWatcher(repo, AMQPConfig{URL: url, Topology: tp}, silent())
	w.Start(context.Background())
	defer w.Stop()

	// Ciphertext that no key on this instance can open.
	body, err := encodeWire(Outgoing{
		ID: id, Template: mailer.TemplateSessionRevoked, Recipient: "grace@x.test",
		Ciphertext: []byte("not really ciphertext"), Nonce: []byte("nope"),
	})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, tp.Republish(ctx, pub, body, 4, "delivery_exhausted", true))

	require.Eventually(t, func() bool {
		status, _ := statusOf(t, repo, id)
		return status == "failed"
	}, 30*time.Second, 100*time.Millisecond)
}

// An unreadable body carries no id to report against, so requeueing it would
// spin the loop forever on something no version of this code can act on.
func TestDeadLetterWatcher_AcksGarbageInsteadOfSpinningOnIt(t *testing.T) {
	repo, _, tp, url, id := deadLetterFixture(t)

	conn, err := amqp.Dial(url)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	pub, err := NewConfirmingChannel(conn)
	require.NoError(t, err)
	require.NoError(t, tp.Declare(pub.Raw()))

	w := NewDeadLetterWatcher(repo, AMQPConfig{URL: url, Topology: tp}, silent())
	w.Start(context.Background())
	defer w.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, tp.Republish(ctx, pub, []byte("{not json"), 4, "send_failed", true))

	// The queue drains rather than redelivering forever...
	require.Eventually(t, func() bool {
		q, err := pub.Raw().QueueInspect(tp.deadQueue())
		return err == nil && q.Messages == 0
	}, 30*time.Second, 100*time.Millisecond, "an unreadable report must not be requeued forever")

	// ...and no unrelated row was touched on the way.
	status, _ := statusOf(t, repo, id)
	require.Equal(t, "published", status)
}

// Stop must join the consume loop even while it is parked in the reconnect
// wait, or a shutdown would block for the full backoff on an unreachable broker.
func TestDeadLetterWatcher_StopIsIdempotentAndUnblocksTheReconnectWait(t *testing.T) {
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(context.Background(), pool))
	// Port 1 is closed, so consume() fails at once and the loop parks.
	w := NewDeadLetterWatcher(NewRepository(pool),
		AMQPConfig{URL: "amqp://127.0.0.1:1/"}, silent())
	w.Start(context.Background())

	done := make(chan struct{})
	go func() {
		w.Stop()
		w.Stop() // idempotent — shutdown may reach it twice
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(deadLetterReconnectDelay + 2*time.Second):
		t.Fatal("Stop waited out the reconnect delay instead of cancelling it")
	}

	// Start after Stop must not resurrect the loop. The context is NOT nil here
	// — the first Start created it — so what proves the guard held is that the
	// second Start left that same cancelled context in place instead of
	// swapping in a live one and leaking a goroutine past shutdown.
	before := w.ctx
	w.Start(context.Background())
	require.Same(t, before, w.ctx, "a second Start must not replace the context")
	require.Error(t, w.ctx.Err(), "the context must stay cancelled after Stop")
}
