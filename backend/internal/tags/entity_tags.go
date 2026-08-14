package tags

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/domainerr"
)

// Querier is satisfied by *pgxpool.Pool (and pgx.Tx for tests).
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// SetEntityTags replaces the tag set for a polymorphic link_tag row
// (entity_kind ∈ {link, note}) inside an open transaction.
//
// This is the ONE place where cross-tenant leakage cannot be caught by a
// foreign key. link_tag lost its FK to link(id) in migration 000014 when it was
// polymorphized, and tag_id's FK to tag(id) carries no user_id to compose with,
// so migration 000017's composite-FK net does not cover it. Ownership of every
// incoming tag id is therefore verified here, in the same transaction, before
// any row is written. TestCrossUser_CannotAttachAnotherUsersTag locks it.
func SetEntityTags(ctx context.Context, tx pgx.Tx, uid authctx.UserID, kind string, entityID int64, tagIDs []int64) error {
	if _, err := tx.Exec(ctx, `DELETE FROM link_tag WHERE entity_kind = $1 AND entity_id = $2`, kind, entityID); err != nil {
		return fmt.Errorf("clear %s tags: %w", kind, err)
	}
	if len(tagIDs) == 0 {
		return nil
	}
	if err := assertTagsOwned(ctx, tx, uid, tagIDs); err != nil {
		return err
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

// SetEntityTagsWithPending creates inline tag definitions and replaces the
// entity's complete tag set inside the caller's transaction.
func SetEntityTagsWithPending(ctx context.Context, tx pgx.Tx, uid authctx.UserID, kind string, entityID int64, tagIDs []int64, pending []CreateInput) error {
	resolved := append([]int64(nil), tagIDs...)
	if len(pending) > 0 {
		rows := make([][]any, 0, len(pending))
		names := make([]string, 0, len(pending))
		for i := range pending {
			in := pending[i]
			in.Normalize()
			if err := in.Validate(); err != nil {
				return domainerr.InvalidInput(err.Error())
			}
			rows = append(rows, []any{int64(uid), in.Name, in.Color, in.Icon})
			names = append(names, in.Name)
		}
		if _, err := tx.CopyFrom(ctx,
			pgx.Identifier{"tag"},
			[]string{"user_id", "name", "color", "icon"},
			pgx.CopyFromRows(rows),
		); err != nil {
			return createError(err)
		}
		created, err := tx.Query(ctx, `SELECT id FROM tag WHERE user_id = $1 AND name = ANY($2)`, int64(uid), names)
		if err != nil {
			return fmt.Errorf("resolve pending tags: %w", err)
		}
		defer created.Close()
		for created.Next() {
			var id int64
			if err := created.Scan(&id); err != nil {
				return fmt.Errorf("resolve pending tag: %w", err)
			}
			resolved = append(resolved, id)
		}
		if err := created.Err(); err != nil {
			return fmt.Errorf("resolve pending tags: %w", err)
		}
	}
	return SetEntityTags(ctx, tx, uid, kind, entityID, resolved)
}

// assertTagsOwned fails unless every id belongs to uid. It reports the generic
// 400 invalid_input rather than naming which id was foreign: replying "tag 7 is
// not yours" would confirm that tag 7 exists on some other account.
func assertTagsOwned(ctx context.Context, q Querier, uid authctx.UserID, tagIDs []int64) error {
	rows, err := q.Query(ctx, `SELECT count(*) FROM tag WHERE user_id = $1 AND id = ANY($2)`,
		int64(uid), tagIDs)
	if err != nil {
		return fmt.Errorf("verify tag ownership: %w", err)
	}
	defer rows.Close()
	var owned int
	if rows.Next() {
		if err := rows.Scan(&owned); err != nil {
			return fmt.Errorf("verify tag ownership: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("verify tag ownership: %w", err)
	}
	// Compare against the DISTINCT count: a payload repeating one owned id must
	// not pass a check that a payload of that id plus a foreign one would fail.
	if owned != distinctCount(tagIDs) {
		return domainerr.InvalidInput("unknown tag id")
	}
	return nil
}

func distinctCount(ids []int64) int {
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		seen[id] = struct{}{}
	}
	return len(seen)
}

// TagsForEntities batches chip lookup for one entity_kind. Link and note ids
// share the numeric space — never pass mixed kinds in one call.
//
// The tag join is owner-filtered as belt and suspenders: callers already pass
// owner-scoped entity ids, but this closes the hole if one ever does not.
func TagsForEntities(ctx context.Context, q Querier, uid authctx.UserID, kind string, ids []int64) (map[int64][]Chip, error) {
	out := map[int64][]Chip{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
        SELECT lt.entity_id, t.id, t.name, t.color, t.icon
        FROM link_tag lt
        JOIN tag t ON t.id = lt.tag_id
        WHERE lt.entity_kind = $1 AND lt.entity_id = ANY($2) AND t.user_id = $3
        ORDER BY t.name ASC
    `, kind, ids, int64(uid))
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
func TagsForLinkAndNote(ctx context.Context, q Querier, uid authctx.UserID, linkIDs, noteIDs []int64) (map[string]map[int64][]Chip, error) {
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
        WHERE ((lt.entity_kind = 'link' AND lt.entity_id = ANY($1))
            OR (lt.entity_kind = 'note' AND lt.entity_id = ANY($2)))
          AND t.user_id = $3
        ORDER BY t.name ASC
    `, linkIDs, noteIDs, int64(uid))
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
