// Package clicklog is the single production writer for public click_log rows.
//
// Link redirect and note render differ only at the HTTP edge (URL vs HTML).
// The INSERT itself is one primitive so coalescing (INV-182) cannot drift
// between the two twins.
package clicklog

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"foldex/internal/pkg/clickctx"
)

// Record writes one click_log row unless clickctx.Allow suppresses it.
// Suppression never fails the caller: the public path still redirects/renders.
//
// The ranking projection is UPSERTed in the same gate: a suppressed click
// must not move last_clicked_at, and a recorded click must not leave stats
// behind click_log (INV-056 — click_log is the source of truth).
func Record(ctx context.Context, tx pgx.Tx, kind string, entityID, ownerID int64) error {
	if clickctx.Allow(ctx, kind, entityID) {
		_, err := tx.Exec(ctx,
			`INSERT INTO click_log (entity_kind, entity_id, user_id) VALUES ($1, $2, $3)`,
			kind, entityID, ownerID)
		if err != nil {
			return fmt.Errorf("insert click_log: %w", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO entity_click_stats (user_id, entity_kind, entity_id, click_count, last_clicked_at)
			VALUES ($3, $1, $2, 1, now())
			ON CONFLICT (user_id, entity_kind, entity_id)
			DO UPDATE SET
				click_count = entity_click_stats.click_count + 1,
				last_clicked_at = now()`,
			kind, entityID, ownerID)
		if err != nil {
			return fmt.Errorf("upsert entity_click_stats: %w", err)
		}
	}
	return nil
}

// RefreshOwner rebuilds the ranking projection from click_log for one owner.
// Bulk writers (import, restore) call this after inserting historical rows
// that never went through Record.
func RefreshOwner(ctx context.Context, tx pgx.Tx, ownerID int64) error {
	if _, err := tx.Exec(ctx, `
		DELETE FROM entity_click_stats WHERE user_id = $1`, ownerID); err != nil {
		return fmt.Errorf("clear entity_click_stats: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO entity_click_stats (user_id, entity_kind, entity_id, click_count, last_clicked_at)
		SELECT user_id, entity_kind, entity_id, count(*)::bigint, max(clicked_at)
		FROM click_log
		WHERE user_id = $1
		GROUP BY user_id, entity_kind, entity_id`, ownerID); err != nil {
		return fmt.Errorf("refresh entity_click_stats: %w", err)
	}
	return nil
}
