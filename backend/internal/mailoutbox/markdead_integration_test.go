//go:build integration

package mailoutbox_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"foldex/internal/mailer"
)

// MarkDead is how a broker's give-up gets back into the database. Under AMQP
// the relay only knows the message was handed over, so without this the row
// would read 'published' forever for a reset link nobody received.
func TestMarkDead_SettlesAPublishedRowAndReportsThatItDid(t *testing.T) {
	ctx := context.Background()
	pool, o, repo := testOutbox(t)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, o.EnqueueTx(ctx, tx, mailer.SessionRevokedMessage("a@b.c"), "en"))
	require.NoError(t, tx.Commit(ctx))

	claimed, err := repo.Claim(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, repo.MarkPublished(ctx, claimed[0].ID, claimed[0].ClaimToken))

	settled, err := repo.MarkDead(ctx, claimed[0].ID, "delivery_exhausted")
	require.NoError(t, err)
	require.True(t, settled)

	var status, lastErr string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status, coalesce(last_error,'') FROM mail_outbox WHERE id = $1`, claimed[0].ID).
		Scan(&status, &lastErr))
	require.Equal(t, "failed", status)
	require.Equal(t, "delivery_exhausted", lastErr)
}

// The status predicate is not ceremony. A dead-letter delivery can arrive late,
// after a stuck-claim sweep already returned the row to 'pending' for a genuine
// retry — settling it then would bury a message that is about to be sent.
func TestMarkDead_LeavesARowThatIsNoLongerPublishedAlone(t *testing.T) {
	ctx := context.Background()
	pool, o, repo := testOutbox(t)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, o.EnqueueTx(ctx, tx, mailer.SessionRevokedMessage("a@b.c"), "en"))
	require.NoError(t, tx.Commit(ctx))

	claimed, err := repo.Claim(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	// Still 'publishing' — never published.
	settled, err := repo.MarkDead(ctx, claimed[0].ID, "delivery_exhausted")
	require.NoError(t, err)
	require.False(t, settled, "an in-flight row must not be settled by a stale report")

	var status string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status FROM mail_outbox WHERE id = $1`, claimed[0].ID).Scan(&status))
	require.Equal(t, "publishing", status)
}

// A report for a row that no longer exists must be distinguishable from one
// that settled something. Collapsing the two is how a lost reset link becomes
// invisible: the watcher would log "abandoned" and move on, with nothing in the
// database and nothing alarming in the log.
func TestMarkDead_ReportsFalseWhenTheRowIsAlreadyGone(t *testing.T) {
	ctx := context.Background()
	_, _, repo := testOutbox(t)

	settled, err := repo.MarkDead(ctx, 999_999, "delivery_exhausted")
	require.NoError(t, err)
	require.False(t, settled)
}
