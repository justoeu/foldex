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
	return loadTaken(ctx, q, "link", bases, limit)
}

// LoadTakenNotes is the note equivalent of LoadTaken. Note slugs are global
// because their public route has no tenant identity to disambiguate them.
func LoadTakenNotes(ctx context.Context, q Queryer, bases []string, limit int) ([]string, error) {
	return loadTaken(ctx, q, "note", bases, limit)
}

func loadTaken(ctx context.Context, q Queryer, table string, bases []string, limit int) ([]string, error) {
	if len(bases) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		return nil, fmt.Errorf("load taken slugs: invalid limit %d", limit)
	}
	exactBases, suffixBases := lookupBases(bases)
	rows, err := q.Query(ctx, fmt.Sprintf(`
        WITH requested_exact(base) AS (
            SELECT DISTINCT unnest($1::text[])
        ), requested_suffix(base) AS (
            SELECT DISTINCT unnest($4::text[])
        ), matches AS (
            (SELECT entity.slug
             FROM requested_exact r
             JOIN %s entity ON entity.slug = r.base
             LIMIT $3)

            UNION

            (SELECT entity.slug
             FROM %s entity
             JOIN requested_suffix r
               ON r.base = regexp_replace(entity.slug, '-[0-9]+$', '')
             WHERE CASE
                 WHEN substring(entity.slug FROM '-([0-9]+)$') ~ '^[0-9]+$'
                 THEN substring(entity.slug FROM '-([0-9]+)$')::numeric >= 2
                   AND substring(entity.slug FROM '-([0-9]+)$')::numeric < $2::numeric
                 ELSE false
             END
             LIMIT $3)
        )
        SELECT slug FROM matches LIMIT $3
    `, table, table), exactBases, MaxUniqueAttempts, limit+1, suffixBases)
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

func lookupBases(bases []string) ([]string, []string) {
	exactSeen := make(map[string]struct{}, len(bases))
	suffixSeen := make(map[string]struct{}, len(bases)*3)
	exact := make([]string, 0, len(bases))
	suffixes := make([]string, 0, len(bases)*3)
	for _, base := range bases {
		first := candidateForAttempt(base, 1)
		if _, exists := exactSeen[first]; !exists {
			exactSeen[first] = struct{}{}
			exact = append(exact, first)
		}
		for attempt := 2; attempt < MaxUniqueAttempts; {
			stem, _ := candidateParts(base, attempt)
			if _, exists := suffixSeen[stem]; !exists {
				suffixSeen[stem] = struct{}{}
				suffixes = append(suffixes, stem)
			}
			if attempt == 2 {
				attempt = 10
			} else {
				attempt *= 10
			}
		}
	}
	return exact, suffixes
}
