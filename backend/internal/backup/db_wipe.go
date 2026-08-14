package backup

import (
	"context"

	"github.com/jackc/pgx/v5"

	"foldex/internal/entityrefs"
	"foldex/internal/notemedia"
	"foldex/internal/pkg/authctx"
)

// ────────────────────────────────────────────────────────────────────────────
// Wipe mode.

// wipeUser deletes every content row owned by uid, and nothing else.
//
// It replaces the pre-000017 wipeAll, which did
// `TRUNCATE click_log, link_tag, note, link, folder, tag RESTART IDENTITY
// CASCADE`. That is no longer survivable for two independent reasons:
//
//  1. TRUNCATE is table-wide. In a multi-tenant install one user restoring a
//     backup would delete every other user's data.
//  2. RESTART IDENTITY resets sequences that are now SHARED across tenants.
//
// The loss of RESTART IDENTITY is why wipe mode no longer preserves original
// ids — see restoreMapped's comment and docs/SDD-AUTH-RBAC.md §10.3.
func wipeUser(ctx context.Context, tx pgx.Tx, uid authctx.UserID) (Counts, error) {
	var c Counts
	u := int64(uid)

	// Counts first, for the report. click_log/link_tag are polymorphic and
	// carry no user_id, so they are reached through a semi-join on the owner's
	// link/note rows — the same shape the DELETEs use below.
	if err := tx.QueryRow(ctx, `
        SELECT count(*) FROM click_log
        WHERE (entity_kind = 'link' AND entity_id IN (SELECT id FROM link WHERE user_id = $1))
           OR (entity_kind = 'note' AND entity_id IN (SELECT id FROM note WHERE user_id = $1))
    `, u).Scan(&c.ClickLogs); err != nil {
		return c, err
	}
	if err := tx.QueryRow(ctx, `
        SELECT count(*) FROM link_tag
        WHERE (entity_kind = 'link' AND entity_id IN (SELECT id FROM link WHERE user_id = $1))
           OR (entity_kind = 'note' AND entity_id IN (SELECT id FROM note WHERE user_id = $1))
    `, u).Scan(&c.LinkTags); err != nil {
		return c, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM link WHERE user_id = $1`, u).Scan(&c.Links); err != nil {
		return c, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM note WHERE user_id = $1`, u).Scan(&c.Notes); err != nil {
		return c, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM folder WHERE user_id = $1`, u).Scan(&c.Folders); err != nil {
		return c, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM tag WHERE user_id = $1`, u).Scan(&c.Tags); err != nil {
		return c, err
	}

	if err := notemedia.ReleaseOwnerRefs(ctx, tx, uid); err != nil {
		return c, err
	}
	if err := entityrefs.PurgeOwner(ctx, tx, uid); err != nil {
		return c, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM note WHERE user_id = $1`, u); err != nil {
		return c, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM link WHERE user_id = $1`, u); err != nil {
		return c, err
	}
	// Flatten the self-FK before deleting: folder_parent_same_user_fkey is
	// ON DELETE SET NULL, so a nested tree would otherwise need delete order to
	// match depth. Nulling first makes the delete order irrelevant.
	if _, err := tx.Exec(ctx, `UPDATE folder SET parent_id = NULL WHERE user_id = $1`, u); err != nil {
		return c, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM folder WHERE user_id = $1`, u); err != nil {
		return c, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM tag WHERE user_id = $1`, u); err != nil {
		return c, err
	}

	// app_setting is NOT touched. Before 000017 it held the master password and
	// was wiped so a restore reproduced the snapshot's settings exactly; that
	// hash is now a per-user column on app_user and is deliberately outside the
	// backup (ADR-30). The table now holds only instance-wide config, which one
	// user's restore has no business clearing.
	return c, nil
}
