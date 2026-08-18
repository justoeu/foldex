//go:build integration

package mailoutbox_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/mailer"
	"foldex/internal/mailoutbox"
	"foldex/internal/pkg/secrets"
	"foldex/internal/testdb"
)

func testOutbox(t *testing.T) (*pgxpool.Pool, *mailoutbox.Outbox, *mailoutbox.Repository) {
	t.Helper()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(context.Background(), pool))
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 3)
	}
	c, err := secrets.NewCipher(key)
	require.NoError(t, err)
	o, err := mailoutbox.New(c)
	require.NoError(t, err)
	return pool, o, mailoutbox.NewRepository(pool)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// recorder is a mailer that counts and can be made to fail.
type recorder struct {
	mu   sync.Mutex
	sent []mailer.Message
	err  error
}

func (r *recorder) Driver() string { return "smtp" }
func (r *recorder) Send(_ context.Context, m mailer.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.sent = append(r.sent, m)
	return nil
}
func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent)
}
func (r *recorder) setErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

func countOutbox(t *testing.T, pool *pgxpool.Pool, where string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT count(*) FROM mail_outbox WHERE "+where, args...).Scan(&n))
	return n
}

// The property the whole package exists for: the message shares the caller's
// transaction, so a rollback leaves nothing behind and a commit leaves exactly
// one row.
func TestEnqueueParticipatesInTheCallersTransaction(t *testing.T) {
	pool, o, _ := testOutbox(t)
	ctx := context.Background()
	env := mailer.PasswordResetMessage("a@b.c", "https://foldex.test/#reset=T", 30)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, o.EnqueueTx(ctx, tx, env, "pt"))
	require.NoError(t, tx.Rollback(ctx))
	assert.Zero(t, countOutbox(t, pool, "TRUE"), "a rolled-back credential left its e-mail behind")

	tx, err = pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, o.EnqueueTx(ctx, tx, env, "pt"))
	require.NoError(t, tx.Commit(ctx))
	assert.Equal(t, 1, countOutbox(t, pool, "TRUE"))

	var locale, template, recipient string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT locale, template, recipient FROM mail_outbox`).Scan(&locale, &template, &recipient))
	assert.Equal(t, "pt", locale)
	assert.Equal(t, mailer.TemplatePasswordReset, template)
	assert.Equal(t, "a@b.c", recipient)
}

// Two relays against one database must not both deliver the same message.
// SKIP LOCKED is what makes that true without either blocking on the other.
func TestConcurrentClaimsNeverOverlap(t *testing.T) {
	pool, o, repo := testOutbox(t)
	ctx := context.Background()
	const total = 24
	for range total {
		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		require.NoError(t, o.EnqueueTx(ctx, tx,
			mailer.LoginCodeMessage("a@b.c", "123456", 5), "en"))
		require.NoError(t, tx.Commit(ctx))
	}

	var mu sync.Mutex
	seen := map[int64]int{}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				msgs, err := repo.Claim(ctx, 5)
				if err != nil || len(msgs) == 0 {
					return
				}
				mu.Lock()
				for _, m := range msgs {
					seen[m.ID]++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	assert.Len(t, seen, total, "some rows were never claimed")
	for id, n := range seen {
		assert.Equal(t, 1, n, "row %d was claimed by more than one relay", id)
	}
}

// A relay killed between the claim and the result would otherwise strand its
// rows in 'publishing' forever — and the point of a durable outbox is that a
// restart does not lose a reset link.
func TestStuckClaimsAreRequeuedWithoutRefundingTheAttempt(t *testing.T) {
	pool, o, repo := testOutbox(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, o.EnqueueTx(ctx, tx, mailer.SessionRevokedMessage("a@b.c"), "en"))
	require.NoError(t, tx.Commit(ctx))

	claimed, err := repo.Claim(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	assert.Equal(t, 1, claimed[0].Attempts)

	// Too young to reclaim.
	n, err := repo.RequeueStuck(ctx, time.Hour)
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Equal(t, 1, countOutbox(t, pool, "status = 'publishing'"))

	_, err = pool.Exec(ctx, `UPDATE mail_outbox SET claimed_at = now() - interval '10 minutes'`)
	require.NoError(t, err)
	n, err = repo.RequeueStuck(ctx, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	again, err := repo.Claim(ctx, 10)
	require.NoError(t, err)
	require.Len(t, again, 1)
	assert.Equal(t, 2, again[0].Attempts,
		"the spent attempt was refunded — a message that kills its worker would cycle forever")
}

// The CAS on claim_token is what stops a straggler from reporting success for
// work another relay already redid.
func TestSettlingRequiresTheExactClaimToken(t *testing.T) {
	pool, o, repo := testOutbox(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, o.EnqueueTx(ctx, tx, mailer.SessionRevokedMessage("a@b.c"), "en"))
	require.NoError(t, tx.Commit(ctx))

	claimed, err := repo.Claim(ctx, 1)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	stale := "00000000-0000-0000-0000-000000000000"
	require.NoError(t, repo.MarkPublished(ctx, claimed[0].ID, stale))
	assert.Equal(t, 1, countOutbox(t, pool, "status = 'publishing'"),
		"a stale claim token settled the row")

	require.NoError(t, repo.MarkPublished(ctx, claimed[0].ID, claimed[0].ClaimToken))
	assert.Equal(t, 1, countOutbox(t, pool, "status = 'published' AND published_at IS NOT NULL"))
}

func TestMarkFailedReschedulesUntilTheBudgetRunsOut(t *testing.T) {
	pool, o, repo := testOutbox(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, o.EnqueueTx(ctx, tx, mailer.SessionRevokedMessage("a@b.c"), "en"))
	require.NoError(t, tx.Commit(ctx))

	for attempt := 1; attempt <= 3; attempt++ {
		_, err := pool.Exec(ctx, `UPDATE mail_outbox SET next_attempt_at = now()`)
		require.NoError(t, err)
		claimed, err := repo.Claim(ctx, 1)
		require.NoError(t, err)
		require.Len(t, claimed, 1, "attempt %d", attempt)
		require.NoError(t, repo.MarkFailed(ctx, claimed[0].ID, claimed[0].ClaimToken,
			"send_failed", time.Minute, 3, false))
	}
	assert.Equal(t, 1, countOutbox(t, pool, "status = 'failed' AND last_error = 'send_failed'"))
}

// A template that no longer exists cannot start working on the fourth try, and
// retrying only delays the operator finding out.
func TestAPermanentFailureSettlesImmediately(t *testing.T) {
	pool, o, repo := testOutbox(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, o.EnqueueTx(ctx, tx, mailer.SessionRevokedMessage("a@b.c"), "en"))
	require.NoError(t, tx.Commit(ctx))

	claimed, err := repo.Claim(ctx, 1)
	require.NoError(t, err)
	require.NoError(t, repo.MarkFailed(ctx, claimed[0].ID, claimed[0].ClaimToken,
		"unknown_template", time.Minute, 6, true))
	assert.Equal(t, 1, countOutbox(t, pool, "status = 'failed'"))
}

// Published rows are transient bookkeeping; failed ones are evidence and keep
// the longer window.
func TestPurgeKeepsFailedRowsLongerThanPublishedOnes(t *testing.T) {
	pool, o, repo := testOutbox(t)
	ctx := context.Background()
	for range 2 {
		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		require.NoError(t, o.EnqueueTx(ctx, tx, mailer.SessionRevokedMessage("a@b.c"), "en"))
		require.NoError(t, tx.Commit(ctx))
	}
	_, err := pool.Exec(ctx, `
		UPDATE mail_outbox SET status = 'published', created_at = now() - interval '30 days'
		WHERE id = (SELECT min(id) FROM mail_outbox)`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		UPDATE mail_outbox SET status = 'failed', created_at = now() - interval '30 days'
		WHERE id = (SELECT max(id) FROM mail_outbox)`)
	require.NoError(t, err)

	n, err := repo.Purge(ctx, 7*24*time.Hour, 90*24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	assert.Equal(t, 1, countOutbox(t, pool, "status = 'failed'"))
}

// End to end through the relay: a transient failure is retried and eventually
// delivered, which is the promise the outbox makes and the dispatcher could
// not.
func TestRelayRetriesUntilTheTransportRecovers(t *testing.T) {
	pool, o, _ := testOutbox(t)
	ctx := context.Background()
	rec := &recorder{}
	rec.setErr(errors.New("smtp is down"))

	relay := mailoutbox.NewRelay(mailoutbox.NewRepository(pool),
		mailoutbox.NewInprocSink(o, rec),
		mailoutbox.Options{PollInterval: 5 * time.Millisecond, Workers: 1}, discardLogger())
	relay.Start(ctx)
	t.Cleanup(relay.Stop)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, o.EnqueueTx(ctx, tx,
		mailer.PasswordResetMessage("a@b.c", "https://foldex.test/#reset=T", 30), "en"))
	require.NoError(t, tx.Commit(ctx))

	require.Eventually(t, func() bool {
		return countOutbox(t, pool, "attempts > 0 AND status = 'pending'") == 1
	}, 5*time.Second, 10*time.Millisecond, "the failed send did not stay queued")
	assert.Zero(t, rec.count())

	rec.setErr(nil)
	_, err = pool.Exec(ctx, `UPDATE mail_outbox SET next_attempt_at = now()`)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return rec.count() == 1 && countOutbox(t, pool, "status = 'published'") == 1
	}, 5*time.Second, 10*time.Millisecond, "the message was never delivered after recovery")
}

// A relay killed mid-send leaves rows in flight. Start sweeps them at boot
// rather than waiting a full claim TTL — which is the difference between a
// sign-in code arriving now and arriving in two minutes, on the restart that
// caused the problem.
func TestRelayReclaimsAbandonedWorkAtBoot(t *testing.T) {
	pool, o, _ := testOutbox(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, o.EnqueueTx(ctx, tx,
		mailer.LoginCodeMessage("a@b.c", "246810", 5), "en"))
	require.NoError(t, tx.Commit(ctx))

	// Exactly the state a killed relay leaves behind: claimed, never settled.
	_, err = pool.Exec(ctx, `
		UPDATE mail_outbox SET status = 'publishing', claim_token = gen_random_uuid(),
		       claimed_at = now() - interval '10 minutes', attempts = 1`)
	require.NoError(t, err)

	rec := &recorder{}
	relay := mailoutbox.NewRelay(mailoutbox.NewRepository(pool),
		mailoutbox.NewInprocSink(o, rec),
		mailoutbox.Options{
			PollInterval: 5 * time.Millisecond, Workers: 1,
			// Comfortably shorter than the row's age, so the sweep is what
			// releases it — and long enough that nothing else could have.
			ClaimTTL: time.Minute,
		}, discardLogger())
	relay.Start(ctx)
	t.Cleanup(relay.Stop)

	require.Eventually(t, func() bool {
		return rec.count() == 1 && countOutbox(t, pool, "status = 'published'") == 1
	}, 5*time.Second, 10*time.Millisecond, "the abandoned message was never redelivered")

	var attempts int
	require.NoError(t, pool.QueryRow(ctx, `SELECT attempts FROM mail_outbox`).Scan(&attempts))
	assert.Equal(t, 2, attempts, "the attempt spent by the dead relay was refunded")
}

// A row naming a template nobody ships must settle as failed rather than
// consume its whole budget first.
func TestRelaySettlesAnUnknownTemplateWithoutRetrying(t *testing.T) {
	pool, o, _ := testOutbox(t)
	ctx := context.Background()
	rec := &recorder{}
	relay := mailoutbox.NewRelay(mailoutbox.NewRepository(pool),
		mailoutbox.NewInprocSink(o, rec),
		mailoutbox.Options{PollInterval: 5 * time.Millisecond, Workers: 1}, discardLogger())
	relay.Start(ctx)
	t.Cleanup(relay.Stop)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, o.EnqueueTx(ctx, tx,
		mailer.Envelope{Template: "retired_template", To: "a@b.c"}, "en"))
	require.NoError(t, tx.Commit(ctx))

	require.Eventually(t, func() bool {
		return countOutbox(t, pool, "status = 'failed' AND last_error = 'unknown_template'") == 1
	}, 5*time.Second, 10*time.Millisecond)
	var attempts int
	require.NoError(t, pool.QueryRow(ctx, `SELECT attempts FROM mail_outbox`).Scan(&attempts))
	assert.Equal(t, 1, attempts, "a permanent failure burned more than one attempt")
	assert.Zero(t, rec.count())
}
