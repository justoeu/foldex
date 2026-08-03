package notes

import (
	"context"

	"github.com/jackc/pgx/v5"

	"foldex/internal/pkg/slug"
)

func resolveUpdateSlug(ctx context.Context, tx pgx.Tx, table string, id int64, explicit *string, title *string) (string, error) {
	return slug.ResolveUpdate(ctx, tx, table, id, explicit, title, "note")
}
