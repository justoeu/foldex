package tags

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Querier is satisfied by *pgxpool.Pool (and pgx.Tx for tests).
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// SetEntityTags replaces the tag set for a polymorphic link_tag row
// (entity_kind ∈ {link, note}) inside an open transaction.
func SetEntityTags(ctx context.Context, tx pgx.Tx, kind string, entityID int64, tagIDs []int64) error {
	if _, err := tx.Exec(ctx, `DELETE FROM link_tag WHERE entity_kind = $1 AND entity_id = $2`, kind, entityID); err != nil {
		return fmt.Errorf("clear %s tags: %w", kind, err)
	}
	if len(tagIDs) == 0 {
		return nil
	}
	rows := make([][]any, 0, len(tagIDs))
	for _, tid := range tagIDs {
		rows = append(rows, []any{kind, entityID, tid})
	}
	_, err := tx.CopyFrom(ctx,
		pgx.Identifier{"link_tag"},
		[]string{"entity_kind", "entity_id", "tag_id"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("insert %s tags: %w", kind, err)
	}
	return nil
}

// TagsForEntities batches chip lookup for one entity_kind. Link and note ids
// share the numeric space — never pass mixed kinds in one call.
func TagsForEntities(ctx context.Context, q Querier, kind string, ids []int64) (map[int64][]Chip, error) {
	out := map[int64][]Chip{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
        SELECT lt.entity_id, t.id, t.name, t.color, t.icon
        FROM link_tag lt
        JOIN tag t ON t.id = lt.tag_id
        WHERE lt.entity_kind = $1 AND lt.entity_id = ANY($2)
        ORDER BY t.name ASC
    `, kind, ids)
	if err != nil {
		return nil, fmt.Errorf("tags for %s: %w", kind, err)
	}
	defer rows.Close()
	for rows.Next() {
		var entityID int64
		var t Chip
		if err := rows.Scan(&entityID, &t.ID, &t.Name, &t.Color, &t.Icon); err != nil {
			return nil, err
		}
		out[entityID] = append(out[entityID], t)
	}
	return out, rows.Err()
}

// TagsForLinkAndNote loads chips for both entity kinds in one round-trip
// (N1-NEX-014). Results are keyed by entity_kind then entity_id so overlapping
// numeric ids never cross-contaminate.
func TagsForLinkAndNote(ctx context.Context, q Querier, linkIDs, noteIDs []int64) (map[string]map[int64][]Chip, error) {
	out := map[string]map[int64][]Chip{
		"link": {},
		"note": {},
	}
	if len(linkIDs) == 0 && len(noteIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
        SELECT lt.entity_kind, lt.entity_id, t.id, t.name, t.color, t.icon
        FROM link_tag lt
        JOIN tag t ON t.id = lt.tag_id
        WHERE (lt.entity_kind = 'link' AND lt.entity_id = ANY($1))
           OR (lt.entity_kind = 'note' AND lt.entity_id = ANY($2))
        ORDER BY t.name ASC
    `, linkIDs, noteIDs)
	if err != nil {
		return nil, fmt.Errorf("tags for link+note: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var entityID int64
		var t Chip
		if err := rows.Scan(&kind, &entityID, &t.ID, &t.Name, &t.Color, &t.Icon); err != nil {
			return nil, err
		}
		if out[kind] == nil {
			out[kind] = map[int64][]Chip{}
		}
		out[kind][entityID] = append(out[kind][entityID], t)
	}
	return out, rows.Err()
}
