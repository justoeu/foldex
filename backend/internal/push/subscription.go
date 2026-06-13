package push

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/httperr"
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
func (r *Repository) Save(ctx context.Context, endpoint, p256dh, auth string) (Subscription, error) {
	if endpoint == "" || p256dh == "" || auth == "" {
		return Subscription{}, httperr.New(400, "invalid_subscription", "endpoint, p256dh and auth are required")
	}
	var s Subscription
	err := r.pool.QueryRow(ctx, `
        INSERT INTO push_subscription (endpoint, p256dh, auth, last_used_at)
        VALUES ($1, $2, $3, NULL)
        ON CONFLICT (endpoint) DO UPDATE
            SET p256dh = EXCLUDED.p256dh,
                auth   = EXCLUDED.auth
        RETURNING id, endpoint, p256dh, auth, created_at, last_used_at
    `, endpoint, p256dh, auth).Scan(
		&s.ID, &s.Endpoint, &s.P256dh, &s.Auth, &s.CreatedAt, &s.LastUsedAt,
	)
	if err != nil {
		return Subscription{}, fmt.Errorf("save push subscription: %w", err)
	}
	return s, nil
}

// List returns all live subscriptions. Used by the sender as the fan-out
// target — single-user means "everyone" is fine; revisit when scoping by
// user becomes necessary.
func (r *Repository) List(ctx context.Context) ([]Subscription, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT id, endpoint, p256dh, auth, created_at, last_used_at
        FROM push_subscription
        ORDER BY id ASC
    `)
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
func (r *Repository) DeleteByEndpoint(ctx context.Context, endpoint string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM push_subscription WHERE endpoint = $1`, endpoint)
	if err != nil {
		return fmt.Errorf("delete push subscription: %w", err)
	}
	return nil
}

// MarkUsed bumps last_used_at after a successful Notify. Used for
// observability — old `last_used_at` values are candidates for pruning when
// the user dropped the foldex tab/extension years ago.
func (r *Repository) MarkUsed(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx, `UPDATE push_subscription SET last_used_at = now() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("mark used: %w", err)
	}
	return nil
}
