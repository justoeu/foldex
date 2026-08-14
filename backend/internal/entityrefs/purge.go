// Package entityrefs owns app-level cleanup for the polymorphic link_tag and
// click_log tables, whose entity FK was removed by migration 000014.
package entityrefs

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"foldex/internal/pkg/authctx"
)

func PurgeOne(ctx context.Context, tx pgx.Tx, kind string, id int64) error {
	if _, err := entityTable(kind); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM link_tag WHERE entity_kind = $1 AND entity_id = $2`, kind, id); err != nil {
		return fmt.Errorf("delete %s link_tag: %w", kind, err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM click_log WHERE entity_kind = $1 AND entity_id = $2`, kind, id); err != nil {
		return fmt.Errorf("delete %s click_log: %w", kind, err)
	}
	return nil
}

// PurgeOwnerSet removes relations for the requested entities that belong to
// uid. IDs owned by another account are ignored even if they appear in ids.
func PurgeOwnerSet(ctx context.Context, tx pgx.Tx, uid authctx.UserID, kind string, ids []int64) error {
	table, err := entityTable(kind)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	query := fmt.Sprintf(`
        WITH targets AS (
            SELECT id FROM %s
            WHERE user_id = $1 AND id = ANY($2::bigint[])
        ), deleted_tags AS (
            DELETE FROM link_tag refs
            USING targets
            WHERE refs.entity_kind = $3 AND refs.entity_id = targets.id
        )
        DELETE FROM click_log refs
        USING targets
        WHERE refs.entity_kind = $3 AND refs.entity_id = targets.id
    `, table)
	if _, err := tx.Exec(ctx, query, int64(uid), ids, kind); err != nil {
		return fmt.Errorf("delete owner %s set references: %w", kind, err)
	}
	return nil
}

func entityTable(kind string) (string, error) {
	switch kind {
	case "link":
		return "link", nil
	case "note":
		return "note", nil
	default:
		return "", fmt.Errorf("unsupported entity kind %q", kind)
	}
}

// PurgeFolderSubtree removes relations for entities selected by the
// owner-scoped _cascade_subtree temp table materialized by folders.Repository.
func PurgeFolderSubtree(ctx context.Context, tx pgx.Tx, uid authctx.UserID) error {
	for _, table := range []string{"link_tag", "click_log"} {
		query := fmt.Sprintf(`
            DELETE FROM %s
            WHERE (entity_kind = 'link' AND entity_id IN (
                SELECT l.id FROM link l
                WHERE l.user_id = $1 AND l.folder_id IN (SELECT id FROM _cascade_subtree)
            )) OR (entity_kind = 'note' AND entity_id IN (
                SELECT n.id FROM note n
                WHERE n.user_id = $1 AND n.folder_id IN (SELECT id FROM _cascade_subtree)
            ))
        `, table)
		if _, err := tx.Exec(ctx, query, int64(uid)); err != nil {
			return fmt.Errorf("delete subtree %s: %w", table, err)
		}
	}
	return nil
}

// PurgeOwner removes all polymorphic relations belonging to uid before an
// account-scoped backup wipe. The entity rows remain until the caller deletes
// them in the same transaction.
func PurgeOwner(ctx context.Context, tx pgx.Tx, uid authctx.UserID) error {
	for _, table := range []string{"link_tag", "click_log"} {
		query := fmt.Sprintf(`
            DELETE FROM %s
            WHERE (entity_kind = 'link' AND entity_id IN (
                SELECT id FROM link WHERE user_id = $1
            )) OR (entity_kind = 'note' AND entity_id IN (
                SELECT id FROM note WHERE user_id = $1
            ))
        `, table)
		if _, err := tx.Exec(ctx, query, int64(uid)); err != nil {
			return fmt.Errorf("delete owner %s: %w", table, err)
		}
	}
	return nil
}
