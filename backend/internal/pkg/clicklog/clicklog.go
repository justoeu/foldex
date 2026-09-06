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
func Record(ctx context.Context, tx pgx.Tx, kind string, entityID, ownerID int64) error {
	if clickctx.Allow(ctx, kind, entityID) {
		_, err := tx.Exec(ctx,
			`INSERT INTO click_log (entity_kind, entity_id, user_id) VALUES ($1, $2, $3)`,
			kind, entityID, ownerID)
		if err != nil {
			return fmt.Errorf("insert click_log: %w", err)
		}
	}
	return nil
}
