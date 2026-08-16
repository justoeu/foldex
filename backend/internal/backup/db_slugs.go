package backup

import (
	"context"

	"github.com/jackc/pgx/v5"

	slugpkg "foldex/internal/pkg/slug"
)

// loadTakenNoteSlugs is deliberately global: /n/{slug} is public and has no
// tenant identity with which to disambiguate equal note slugs.
func loadTakenNoteSlugs(ctx context.Context, tx pgx.Tx, bases []string, limit int) ([]string, error) {
	return slugpkg.LoadTakenNotes(ctx, tx, bases, limit)
}
