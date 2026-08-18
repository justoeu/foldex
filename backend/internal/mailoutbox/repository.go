package mailoutbox

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Outgoing is one claimed row. The params stay encrypted here: a transport that
// only forwards bytes (the AMQP relay) never needs to open them, and the fewer
// places that hold a decrypted reset link the better.
type Outgoing struct {
	ID         int64
	Template   string
	Recipient  string
	Locale     string
	Attempts   int
	ClaimToken string
	Ciphertext []byte
	Nonce      []byte
}

// Repository owns the queue's state transitions.
type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// Claim takes up to n due rows and marks them in flight.
//
// FOR UPDATE SKIP LOCKED is what lets two relays run against one database
// without either blocking on the other or handing the same message to both —
// the same pattern the change-check worker uses to claim due links.
//
// attempts is incremented HERE, before delivery is attempted, not after it
// fails. A relay that dies mid-send has already spent the attempt, which is the
// conservative direction: the alternative lets a message that reliably kills
// its worker retry forever.
func (r *Repository) Claim(ctx context.Context, n int) ([]Outgoing, error) {
	// The claim is wrapped in a CTE so the batch can be ORDERED on the way out.
	// `UPDATE ... RETURNING` promises no row order, so a single worker draining
	// a batch would deliver it in whatever order the executor produced — and
	// two messages queued a millisecond apart would arrive reversed often
	// enough to matter, both for a user reading their inbox and for any test
	// that queues a marker to know the queue is empty.
	rows, err := r.pool.Query(ctx, `
		WITH claimed AS (
			UPDATE mail_outbox SET
				status      = 'publishing',
				claim_token = gen_random_uuid(),
				claimed_at  = now(),
				attempts    = attempts + 1
			WHERE id IN (
				SELECT id FROM mail_outbox
				WHERE status = 'pending' AND next_attempt_at <= now()
				ORDER BY next_attempt_at, id
				LIMIT $1
				FOR UPDATE SKIP LOCKED
			)
			RETURNING id, template, recipient, locale, attempts, claim_token::text,
			          payload_ciphertext, payload_nonce
		)
		SELECT * FROM claimed ORDER BY id`, n)
	if err != nil {
		return nil, fmt.Errorf("mailoutbox: claim: %w", err)
	}
	defer rows.Close()

	var out []Outgoing
	for rows.Next() {
		var m Outgoing
		if err := rows.Scan(&m.ID, &m.Template, &m.Recipient, &m.Locale, &m.Attempts,
			&m.ClaimToken, &m.Ciphertext, &m.Nonce); err != nil {
			return nil, fmt.Errorf("mailoutbox: scan claim: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mailoutbox: claim rows: %w", err)
	}
	return out, nil
}

// MarkPublished settles a delivered message.
//
// The CAS on claim_token is not ceremony: a relay that stalled past the claim
// TTL has had its row requeued and possibly re-delivered by another relay, and
// without the token check the straggler would overwrite that result and report
// success for work someone else did.
func (r *Repository) MarkPublished(ctx context.Context, id int64, claimToken string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE mail_outbox
		SET status = 'published', published_at = now(), claim_token = NULL, last_error = NULL
		WHERE id = $1 AND claim_token = $2::uuid AND status = 'publishing'`, id, claimToken)
	if err != nil {
		return fmt.Errorf("mailoutbox: mark published: %w", err)
	}
	return nil
}

// MarkFailed reschedules a message, or gives up once it has spent its budget.
//
// reason is a normalized token, never the transport's own error text: an MTA
// rejection routinely quotes the envelope back, and the envelope of these
// messages is a recipient address.
//
// permanent settles the row immediately regardless of the remaining budget.
// A template that no longer exists or a payload that will not decrypt cannot
// start working on the fourth try; retrying only delays the operator noticing.
func (r *Repository) MarkFailed(ctx context.Context, id int64, claimToken, reason string,
	retryIn time.Duration, maxAttempts int, permanent bool) error {

	_, err := r.pool.Exec(ctx, `
		UPDATE mail_outbox SET
			status          = CASE WHEN $6 OR attempts >= $5 THEN 'failed' ELSE 'pending' END,
			next_attempt_at = now() + $4::interval,
			last_error      = $3,
			claim_token     = NULL,
			claimed_at      = NULL
		WHERE id = $1 AND claim_token = $2::uuid AND status = 'publishing'`,
		id, claimToken, reason, intervalArg(retryIn), maxAttempts, permanent)
	if err != nil {
		return fmt.Errorf("mailoutbox: mark failed: %w", err)
	}
	return nil
}

// RequeueStuck returns rows abandoned in flight to the pending state.
//
// A relay killed between the claim and the result leaves its row in
// 'publishing' forever, and the whole point of a durable outbox is that a
// restart does not lose a reset link. The attempt it already spent is not
// refunded, so a message that keeps killing its worker still converges on
// 'failed' instead of cycling.
func (r *Repository) RequeueStuck(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE mail_outbox SET
			status      = 'pending',
			claim_token = NULL,
			claimed_at  = NULL,
			last_error  = 'claim_expired'
		WHERE status = 'publishing' AND claimed_at < now() - $1::interval`, intervalArg(olderThan))
	if err != nil {
		return 0, fmt.Errorf("mailoutbox: requeue stuck: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Purge drops settled rows. Published messages are transient bookkeeping;
// failed ones are operational evidence and keep the longer window.
func (r *Repository) Purge(ctx context.Context, publishedTTL, failedTTL time.Duration) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM mail_outbox
		WHERE (status = 'published' AND created_at < now() - $1::interval)
		   OR (status = 'failed'    AND created_at < now() - $2::interval)`,
		intervalArg(publishedTTL), intervalArg(failedTTL))
	if err != nil {
		return 0, fmt.Errorf("mailoutbox: purge: %w", err)
	}
	return tag.RowsAffected(), nil
}

// intervalArg renders a duration for Postgres. Seconds rather than the Go
// string form: `2h45m0s` is not an interval literal Postgres parses.
func intervalArg(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%d seconds", int64(d.Seconds()))
}
