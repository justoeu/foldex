package slug

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Queryer is the transaction-sized query surface needed by LoadTaken.
type Queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// LoadTaken preloads globally occupied candidates for the requested bases in
// one query. The result cap keeps a collision-heavy database from turning the
// preload itself into unbounded memory growth.
func LoadTaken(ctx context.Context, q Queryer, bases []string, limit int) ([]string, error) {
	if len(bases) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		return nil, fmt.Errorf("load taken slugs: invalid limit %d", limit)
	}
	rows, err := q.Query(ctx, `
        WITH requested(base) AS (
            SELECT DISTINCT unnest($1::text[])
        ), matches AS (
            (SELECT l.slug
             FROM requested r
             JOIN link l ON l.slug = r.base
             LIMIT $3)

            UNION

            (SELECT l.slug
             FROM link l
             JOIN requested r
               ON r.base = regexp_replace(l.slug, '-[0-9]+$', '')
             WHERE CASE
                 WHEN substring(l.slug FROM '-([0-9]+)$') ~ '^[0-9]+$'
                 THEN substring(l.slug FROM '-([0-9]+)$')::numeric >= 2
                  AND substring(l.slug FROM '-([0-9]+)$')::numeric < $2::numeric
                 ELSE false
             END
             LIMIT $3)
        )
        SELECT slug FROM matches LIMIT $3
    `, bases, MaxUniqueAttempts, limit+1)
	if err != nil {
		return nil, fmt.Errorf("load taken slugs: %w", err)
	}
	defer rows.Close()

	taken := make([]string, 0)
	for rows.Next() {
		var candidate string
		if err := rows.Scan(&candidate); err != nil {
			return nil, fmt.Errorf("scan taken slug: %w", err)
		}
		taken = append(taken, candidate)
		if len(taken) > limit {
			return nil, fmt.Errorf("load taken slugs: more than %d relevant collisions", limit)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load taken slugs: %w", err)
	}
	return taken, nil
}
