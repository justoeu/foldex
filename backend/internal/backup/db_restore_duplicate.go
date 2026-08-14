package backup

import (
	"context"

	"github.com/jackc/pgx/v5"

	"foldex/internal/pkg/authctx"
)

// ────────────────────────────────────────────────────────────────────────────
// Duplicate mode.

func restoreDuplicate(ctx context.Context, tx pgx.Tx, uid authctx.UserID, snap *Snapshot) (Counts, []string, idMapping, error) {
	return restoreDuplicateStaged(ctx, tx, uid, snap)
}
