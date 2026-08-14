package backup

import (
	"context"

	"github.com/jackc/pgx/v5"

	"foldex/internal/pkg/authctx"
)

// ────────────────────────────────────────────────────────────────────────────
// Skip mode.

func restoreSkip(ctx context.Context, tx pgx.Tx, uid authctx.UserID, snap *Snapshot) (Counts, Counts, idMapping, error) {
	return restoreSkipStaged(ctx, tx, uid, snap)
}
