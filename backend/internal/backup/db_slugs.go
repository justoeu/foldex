package backup

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	slugpkg "foldex/internal/pkg/slug"
)

// loadTakenNoteSlugs is deliberately global: /n/{slug} is public and has no
// tenant identity with which to disambiguate equal note slugs.
func loadTakenNoteSlugs(ctx context.Context, tx pgx.Tx, bases []string, limit int) ([]string, error) {
	if len(bases) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		WITH requested(base) AS (
			SELECT DISTINCT unnest($1::text[])
		), matches AS (
			(SELECT n.slug FROM requested r JOIN note n ON n.slug = r.base LIMIT $3)
			UNION
			(SELECT n.slug
			 FROM note n
			 JOIN requested r ON r.base = regexp_replace(n.slug, '-[0-9]+$', '')
			 WHERE CASE
			     WHEN substring(n.slug FROM '-([0-9]+)$') ~ '^[0-9]+$'
			     THEN substring(n.slug FROM '-([0-9]+)$')::numeric >= 2
			      AND substring(n.slug FROM '-([0-9]+)$')::numeric < $2::numeric
			     ELSE false
			 END
			 LIMIT $3)
		)
		SELECT slug FROM matches LIMIT $3`, bases, slugpkg.MaxUniqueAttempts, limit+1)
	if err != nil {
		return nil, fmt.Errorf("load taken note slugs: %w", err)
	}
	defer rows.Close()
	taken := make([]string, 0)
	for rows.Next() {
		var candidate string
		if err := rows.Scan(&candidate); err != nil {
			return nil, fmt.Errorf("scan taken note slug: %w", err)
		}
		taken = append(taken, candidate)
		if len(taken) > limit {
			return nil, fmt.Errorf("load taken note slugs: more than %d relevant collisions", limit)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load taken note slugs: %w", err)
	}
	return taken, nil
}
