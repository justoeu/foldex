package slug

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/domainerr"
)

// TitleScanner is satisfied by pgx.Tx / pgxpool.Pool for reading title during
// slug regeneration.
type TitleScanner interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// UpdateSources are the caller-supplied candidates for a regenerated slug:
// an explicit slug wins outright; otherwise the incoming title, when present,
// or the live row title feeds FromTitleOrFallback with the entity prefix.
type UpdateSources struct {
	Explicit *string
	Title    *string
	Prefix   string
}

// ResolveUpdate returns the slug to write on Update. When explicit is non-nil
// it wins; otherwise title (if provided) or the live row title is slugified
// with FromTitleOrFallback(prefix, id).
func ResolveUpdate(ctx context.Context, q TitleScanner, uid authctx.UserID, table string, id int64, src UpdateSources) (string, error) {
	if src.Explicit != nil {
		return *src.Explicit, nil
	}
	currentTitle := ""
	if src.Title != nil {
		currentTitle = strings.TrimSpace(*src.Title)
	} else {
		sql := fmt.Sprintf(`SELECT title FROM %s WHERE user_id = $1 AND id = $2`, table)
		if err := q.QueryRow(ctx, sql, int64(uid), id).Scan(&currentTitle); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return "", domainerr.ErrNotFound
			}
			return "", fmt.Errorf("read title for slug regen: %w", err)
		}
	}
	return FromTitleOrFallback(currentTitle, src.Prefix, id), nil
}
