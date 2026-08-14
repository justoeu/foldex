package links

import (
	"context"

	"github.com/jackc/pgx/v5"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/slug"
)

func resolveUpdateSlug(ctx context.Context, tx pgx.Tx, uid authctx.UserID, table string, id int64, explicit *string, title *string) (string, error) {
	return slug.ResolveUpdate(ctx, tx, uid, table, id, explicit, title, "link")
}
