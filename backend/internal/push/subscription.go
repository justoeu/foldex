package push

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/authctx"
)

// MaxSubscriptionsPerUser bounds persisted channels and each notification fanout.
const MaxSubscriptionsPerUser = 16

var (
	ErrInvalidSubscription = errors.New("push: invalid subscription")
	ErrSubscriptionLimit   = errors.New("push: subscription limit reached")
)

// Subscription mirrors a row of push_subscription (migration 000011).
type Subscription struct {
	ID         int64
	Endpoint   string
	P256dh     string
	Auth       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// Repository persists Web Push subscriptions.
type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// Save upserts by endpoint. The browser may re-subscribe with the same
// endpoint after a re-permission flow but with new p256dh/auth — keeping the
// row and just refreshing the keys avoids subscription bloat.
func (r *Repository) Save(ctx context.Context, uid authctx.UserID, endpoint, p256dh, auth string) (Subscription, error) {
	if endpoint == "" || p256dh == "" || auth == "" {
		return Subscription{}, ErrInvalidSubscription
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Subscription{}, fmt.Errorf("save push subscription begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := reserveSubscriptionSlot(ctx, tx, uid, endpoint); err != nil {
		return Subscription{}, err
	}

	var s Subscription
	err = tx.QueryRow(ctx, `
        INSERT INTO push_subscription (user_id, endpoint, p256dh, auth, last_used_at)
        VALUES ($1, $2, $3, $4, NULL)
        ON CONFLICT (endpoint) DO UPDATE
            SET p256dh  = EXCLUDED.p256dh,
                auth    = EXCLUDED.auth,
                user_id = EXCLUDED.user_id
        RETURNING id, endpoint, p256dh, auth, created_at, last_used_at
    `, int64(uid), endpoint, p256dh, auth).Scan(
		&s.ID, &s.Endpoint, &s.P256dh, &s.Auth, &s.CreatedAt, &s.LastUsedAt,
	)
	if err != nil {
		return Subscription{}, fmt.Errorf("save push subscription: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Subscription{}, fmt.Errorf("save push subscription commit: %w", err)
	}
	return s, nil
}

func reserveSubscriptionSlot(ctx context.Context, tx pgx.Tx, uid authctx.UserID, endpoint string) error {
	var lockedUser int64
	if err := tx.QueryRow(ctx,
		`SELECT id FROM app_user WHERE id = $1 FOR NO KEY UPDATE`, int64(uid)).Scan(&lockedUser); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidSubscription
		}
		return fmt.Errorf("save push subscription lock owner: %w", err)
	}

	var existing, matching int
	if err := tx.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE endpoint = $2)
		FROM push_subscription
		WHERE user_id = $1
	`, int64(uid), endpoint).Scan(&existing, &matching); err != nil {
		return fmt.Errorf("save push subscription count: %w", err)
	}
	if matching == 0 && existing >= MaxSubscriptionsPerUser {
		return ErrSubscriptionLimit
	}
	return nil
}

// List returns uid's live subscriptions — the fan-out target for a
// notification about that user's link.
//
// push_subscription.endpoint stays GLOBALLY unique (migration 000017 §8): an
// endpoint is a physical browser channel, not user data. Two users sharing one
// browser profile produce the same endpoint, and Save re-points user_id to
// whoever subscribed last, which is correct — the previous owner is no longer
// logged in there.
// The owner's status is re-checked HERE, at delivery, and not only where the
// change-check sweep claims work. The claim filters disabled owners so their
// links stop costing a fetch, but an account disabled BETWEEN the claim and the
// send would still get its notification — the claim's snapshot says nothing
// about the present. A push subscription is a browser channel that outlives the
// session revocation and the API-token kill that disabling performs, so it is
// the one credential-shaped thing left that has to be re-read at use.
func (r *Repository) List(ctx context.Context, uid authctx.UserID) ([]Subscription, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT s.id, s.endpoint, s.p256dh, s.auth, s.created_at, s.last_used_at
        FROM push_subscription s
        JOIN app_user u ON u.id = s.user_id AND u.status = 'active'
        WHERE s.user_id = $1
        ORDER BY s.id ASC
		LIMIT $2
    `, int64(uid), MaxSubscriptionsPerUser)
	if err != nil {
		return nil, fmt.Errorf("list push subscriptions: %w", err)
	}
	defer rows.Close()
	out := make([]Subscription, 0)
	for rows.Next() {
		var s Subscription
		if err := rows.Scan(&s.ID, &s.Endpoint, &s.P256dh, &s.Auth, &s.CreatedAt, &s.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteGone removes subscriptions that returned RFC 8030's permanent 404/410
// response. Ownership prevents a concurrent re-subscribe from deleting a row
// that moved to another account after the sender listed it.
func (r *Repository) DeleteGone(ctx context.Context, uid authctx.UserID, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx, `
        DELETE FROM push_subscription
        WHERE user_id = $1 AND id = ANY($2::bigint[])
    `, int64(uid), ids)
	if err != nil {
		return fmt.Errorf("delete gone push subscriptions: %w", err)
	}
	return nil
}

// DeleteByEndpointForUser is the user-facing unsubscribe. Silent no-op when the
// endpoint belongs to someone else — the caller returns 204 either way, so this
// cannot be used to probe whether an endpoint exists on another account.
func (r *Repository) DeleteByEndpointForUser(ctx context.Context, uid authctx.UserID, endpoint string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM push_subscription WHERE user_id = $1 AND endpoint = $2`, int64(uid), endpoint)
	if err != nil {
		return fmt.Errorf("delete push subscription: %w", err)
	}
	return nil
}

// MarkUsed bumps last_used_at for successful deliveries in one write.
func (r *Repository) MarkUsed(ctx context.Context, uid authctx.UserID, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx, `
        UPDATE push_subscription
        SET last_used_at = now()
        WHERE user_id = $1 AND id = ANY($2::bigint[])
    `, int64(uid), ids)
	if err != nil {
		return fmt.Errorf("mark push subscriptions used: %w", err)
	}
	return nil
}
