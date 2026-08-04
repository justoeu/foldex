package push

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/httperr"

	"foldex/internal/pkg/authctx"
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

// Repository persists Web Push subscriptions. Single-user model — no user
// id; revisit when multi-user lands.
type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// Save upserts by endpoint. The browser may re-subscribe with the same
// endpoint after a re-permission flow but with new p256dh/auth — keeping the
// row and just refreshing the keys avoids subscription bloat.
func (r *Repository) Save(ctx context.Context, uid authctx.UserID, endpoint, p256dh, auth string) (Subscription, error) {
	if endpoint == "" || p256dh == "" || auth == "" {
		return Subscription{}, httperr.New(400, "invalid_subscription", "endpoint, p256dh and auth are required")
	}
	var s Subscription
	err := r.pool.QueryRow(ctx, `
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
	return s, nil
}

// List returns uid's live subscriptions — the fan-out target for a
// notification about that user's link.
//
// push_subscription.endpoint stays GLOBALLY unique (migration 000017 §8): an
// endpoint is a physical browser channel, not user data. Two users sharing one
// browser profile produce the same endpoint, and Save re-points user_id to
// whoever subscribed last, which is correct — the previous owner is no longer
// logged in there.
func (r *Repository) List(ctx context.Context, uid authctx.UserID) ([]Subscription, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT id, endpoint, p256dh, auth, created_at, last_used_at
        FROM push_subscription
        WHERE user_id = $1
        ORDER BY id ASC
    `, int64(uid))
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

// DeleteByEndpoint is invoked by the sender when the push service returns
// 404/410 — the convention for "this endpoint is gone, stop sending". No-op
// when the row doesn't exist (idempotent).
// DeleteByEndpoint is deliberately NOT owner-scoped: it is called ONLY from the
// sender's 404/410 handling (RFC 8030 §7.3), where the push service itself has
// told us the channel is dead. The endpoint is globally unique, so removing it
// by endpoint alone is correct regardless of who currently owns the row.
//
// The user-facing DELETE /api/push/subscriptions must NOT use this — it takes
// the endpoint from the request body, so an unscoped delete would let anyone who
// learns another user's endpoint silence their notifications. Use
// DeleteByEndpointForUser there.
func (r *Repository) DeleteByEndpoint(ctx context.Context, endpoint string) error {
	//nolint:tenantscope // sender-only; see doc comment above
	_, err := r.pool.Exec(ctx, `DELETE FROM push_subscription WHERE endpoint = $1`, endpoint)
	if err != nil {
		return fmt.Errorf("delete push subscription: %w", err)
	}
	return nil
}

// MarkUsed bumps last_used_at after a successful Notify. Used for
// observability — old `last_used_at` values are candidates for pruning when
// the user dropped the foldex tab/extension years ago.
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

func (r *Repository) MarkUsed(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx, `UPDATE push_subscription SET last_used_at = now() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("mark used: %w", err)
	}
	return nil
}
